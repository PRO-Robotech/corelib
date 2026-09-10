// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription

// watermark_established_internal_test.go — «позиции ещё нет» отличимо от
// «позиция ноль» (kacho#1386).
//
// Граница устоявшегося начинает жизнь нулём и подтверждается НЕ ВСЕГДА первым
// же наблюдением: писатель, державший журнал в момент наблюдения, переносит
// подтверждение на следующий проход. Пока подтверждения нет, ноль означает
// отсутствие позиции — а тот, кто садится на границу, обязан различать эти два
// значения, иначе он садится в начало журнала.
//
// Проба разбирает ПЕРЕХОД состояния, а не запрос к базе: вход подаётся так же,
// как его подаёт наблюдение, и потому воспроизводим без Postgres. Что то же
// самое состояние производит НАСТОЯЩИЙ писатель, утверждает интеграционная
// проба (`coldwatermark_integration_test.go`).

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// countingWarnHandler считает записи уровня WARN. Жалоба на удержание — часть
// объявленного размена («не потерять» против «доставить сейчас»), поэтому её
// наличие утверждается, а не предполагается.
type countingWarnHandler struct{ warns *int }

func (h countingWarnHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h countingWarnHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		*h.warns++
	}
	return nil
}
func (h countingWarnHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h countingWarnHandler) WithGroup(string) slog.Handler      { return h }

func newProbeWatermark() *Watermark {
	return &Watermark{log: slog.Default(), now: time.Now}
}

// TestWatermarkEstablishedSeparatesNoPositionFromPositionZero — обе стороны.
func TestWatermarkEstablishedSeparatesNoPositionFromPositionZero(t *testing.T) {
	t.Run("непустой журнал под писателем — позиции ЕЩЁ НЕТ", func(t *testing.T) {
		h := newProbeWatermark()
		h.observe(5000, 1, []string{"3/17"})
		if h.established {
			t.Error("наблюдение объявлено состоявшимся, хотя писатель держит журнал: " +
				"ноль будет усвоен как позиция, и подписчик сядет в начало журнала")
		}
		if h.settled != 0 {
			t.Errorf("граница %d, ожидался 0 — подтверждать было нечем", h.settled)
		}
	})

	t.Run("писатель доистёк — наблюдение подтверждено", func(t *testing.T) {
		h := newProbeWatermark()
		h.observe(5000, 1, []string{"3/17"})
		h.observe(5000, 1, nil)
		if !h.established {
			t.Error("наблюдение не состоялось после ухода писателя")
		}
		if h.settled != 5000 {
			t.Errorf("граница %d, ожидалась 5000", h.settled)
		}
	})

	t.Run("непустой журнал без писателей — состоялось сразу", func(t *testing.T) {
		h := newProbeWatermark()
		h.observe(42, 1, nil)
		if !h.established || h.settled != 42 {
			t.Errorf("состоялось=%v граница=%d, ожидалось true/42", h.established, h.settled)
		}
	})

	// ЗАКОННЫЙ БЛИЗНЕЦ. У пустого журнала ноль — настоящая позиция: строк,
	// которые можно было бы пропустить, нет. Объяви его «ещё не позицией» — и
	// подписчик на пустом журнале не сядет НИКОГДА, то есть расход вылечился бы
	// отказом в обслуживании.
	t.Run("пустой журнал — ноль ЕСТЬ позиция", func(t *testing.T) {
		h := newProbeWatermark()
		h.observe(0, 0, nil)
		if !h.established {
			t.Error("пустой журнал объявлен неподтверждённым — подтверждать в нём нечего")
		}
		if h.settled != 0 {
			t.Errorf("граница %d, ожидался 0", h.settled)
		}
	})

	// Тот же близнец под писателем: номер ещё не выдан, максимума нет. Ждать
	// нечего — строка, которую он пишет, получит номер выше нуля и уедет
	// подписчику как событие ПОСЛЕ подписки.
	t.Run("пустой журнал под писателем — ноль ЕСТЬ позиция", func(t *testing.T) {
		h := newProbeWatermark()
		h.observe(0, 0, []string{"4/21"})
		if !h.established {
			t.Error("пустой журнал под писателем объявлен неподтверждённым — " +
				"его первая строка придёт событием, а не историей")
		}
	})

	// Состоявшееся наблюдение НЕ отзывается новым писателем: граница уже
	// известна, и следующий вопрос задаётся с неё, а не с нуля.
	t.Run("подтверждение не отзывается новым писателем", func(t *testing.T) {
		h := newProbeWatermark()
		h.observe(42, 1, nil)
		h.observe(90, 1, []string{"5/9"})
		if !h.established {
			t.Error("подтверждённое наблюдение отозвано появлением писателя")
		}
		if h.settled != 42 {
			t.Errorf("граница %d, ожидалась 42 — она не вправе двигаться до подтверждения", h.settled)
		}
	})
}

// TestResolveCursorSeatsFromNowOnTheSettledBoundary — подписчик без позиции
// садится на границу, и садиться ему дают только на подтверждённую.
//
// Две другие ветви выбора курсора подтверждением не связаны, и это утверждается
// здесь же: «с начала» и «с названной позиции» не спрашивают границу вовсе.
func TestResolveCursorSeatsFromNowOnTheSettledBoundary(t *testing.T) {
	s := &Server{}
	h := newProbeWatermark()
	h.observe(77, 1, nil)

	got, err := s.resolveCursor(Start{}, h, 0)
	if err != nil {
		t.Fatalf("выбор курсора: %v", err)
	}
	if got != 77 {
		t.Errorf("курсор %d, ожидался 77 — подписчик без позиции садится на границу", got)
	}

	got, err = s.resolveCursor(Start{FromBeginning: true}, h, 3)
	if err != nil {
		t.Fatalf("выбор курсора с начала: %v", err)
	}
	if got != 3 {
		t.Errorf("курсор %d, ожидался 3 — «с начала» садится на пол удержанного", got)
	}
}

// TestWatermarkSettlesUnderContinuousWriting — ПОДТВЕРЖДЕНИЕ НАСТУПАЕТ ПОД
// НЕПРЕРЫВНОЙ ЗАПИСЬЮ.
//
// Признак дефекта: граница РАСТЁТ (каждый проход снимает прошлое ожидание), а
// наблюдение так и не объявляется состоявшимся — потому что состоявшимся
// считалось «ожидания не осталось», а новое ожидание берётся тем же проходом,
// что снимает прошлое. Под сплошной записью пустого мига не бывает никогда, и
// подписчик без позиции не садится НИКОГДА: расход лечится отказом в
// обслуживании, причём немым.
//
// Подтверждает границу шаг 1, а не отсутствие очереди: снятое ожидание значит,
// что писатели номера `pendingSeq` доистекли, — и это верно независимо от того,
// взято ли следом ожидание для номера ВЫШЕ.
func TestWatermarkSettlesUnderContinuousWriting(t *testing.T) {
	var warns int
	h := newProbeWatermark()
	h.log = slog.New(countingWarnHandler{warns: &warns})
	clock := time.Unix(0, 0)
	h.now = func() time.Time { return clock }

	const passes = 50
	for i := 1; i <= passes; i++ {
		// Каждый проход: журнал вырос, держит его НОВЫЙ писатель. Прошлый
		// доистёк — значит прошлое наблюдение подтверждено.
		//
		// Часы двигаются так же, как у близнеца с УДЕРЖАНИЕМ, и на ту же
		// величину: различие между пробами — ровно личность писателя, а не
		// длительность прогона.
		clock = clock.Add(2 * time.Second)
		h.observe(int64(i), 1, []string{fmt.Sprintf("%d/%d", i, i)})
	}
	// Горизонт ДВИЖЕТСЯ, и жаловаться не на что: жалоба заведена на удержание,
	// а не на занятость журнала. Пожалуйся она здесь — оператор получал бы её
	// на штатной записи, перестал бы читать, и настоящее удержание уехало бы
	// вместе с шумом.
	if warns != 0 {
		t.Errorf("движущийся горизонт дал %d жалоб(ы) на удержание: "+
			"штатная запись — не застрявший писатель", warns)
	}
	if !h.established {
		t.Errorf("за %d проходов наблюдение не состоялось ни разу при границе %d: "+
			"подтверждённая граница ЕСТЬ и известна, а подписчика на неё не сажают",
			passes, h.settled)
	}
	if h.settled != passes-1 {
		t.Errorf("граница %d, ожидалась %d — подтверждается номер прошлого прохода",
			h.settled, passes-1)
	}
}

// TestHeldJournalNeitherSettlesNorGoesQuiet — ЗАКОННЫЙ БЛИЗНЕЦ предыдущей.
//
// Тот же поток проходов, но журнал держит ОДИН И ТОТ ЖЕ писатель. Здесь
// подтверждать нечем по существу: невыпущенный номер этого писателя может
// оказаться ниже наблюдаемого максимума, и посадка на максимум потеряла бы его.
// Наблюдение обязано остаться несостоявшимся — иначе починка предыдущей пробы
// была бы «объявить состоявшимся всё подряд».
//
// И ровно поэтому здесь обязана звучать жалоба: удержание — единственный
// случай, когда поток честно ждёт, и немым это ожидание быть не вправе.
func TestHeldJournalNeitherSettlesNorGoesQuiet(t *testing.T) {
	var warns int
	h := newProbeWatermark()
	h.log = slog.New(countingWarnHandler{warns: &warns})
	clock := time.Unix(0, 0)
	h.now = func() time.Time { return clock }

	for i := 1; i <= 50; i++ {
		clock = clock.Add(2 * time.Second)
		h.observe(int64(i), 1, []string{"7/7"})
	}
	if h.established {
		t.Error("наблюдение объявлено состоявшимся, хотя журнал держит " +
			"незавершённый писатель: его номер может быть ниже наблюдаемого максимума")
	}
	if h.settled != 0 {
		t.Errorf("граница %d, ожидался 0 — подтверждать было нечем", h.settled)
	}
	if warns == 0 {
		t.Error("удержание длиннее stallWarnAfter не дало ни одной жалобы: " +
			"поток ждёт молча, и «писатель завис» неотличимо от «событий нет»")
	}
}
