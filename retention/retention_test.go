// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// retention_test.go — ПЕТЛЯ УБОРКИ: что она обещает и чего не делает.
//
// Пакет заведён как ЕДИНСТВЕННЫЙ экземпляр расписания уборки для всех владельцев
// платформы, и до этих проб у него не было ни одной собственной: его поведение
// проверялось косвенно, через уборщика таблицы операций. Косвенная проверка
// молчит ровно там, где предмет принадлежит петле, а не предикату, — на потолке
// партий, на изоляции отказов между предметами и на жизненном цикле.
//
// Пробы намеренно НЕ ходят в базу: предмет здесь — расписание, а не оператор
// SQL. Уборщик подставляется функцией, и это законно, потому что порт объявлен
// функцией by construction.
package retention_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/retention"
)

// fixedSweep — уборщик, снимающий заданное число строк за партию.
func fixedSweep(perBatch int64, full bool) retention.SweepFunc {
	return func(context.Context, time.Duration, int) (int64, bool, error) {
		return perBatch, full, nil
	}
}

// countingSweep — уборщик, считающий свои вызовы.
//
// Счётчик АТОМАРНЫЙ: петля зовёт уборщика из своей горутины, а проба читает
// счётчик из своей. Обычное поле дало бы гонку в самой пробе — то есть красное,
// не относящееся к продукту.
func countingSweep(calls *atomic.Int64, perBatch int64, full bool) retention.SweepFunc {
	return func(context.Context, time.Duration, int) (int64, bool, error) {
		calls.Add(1)
		return perBatch, full, nil
	}
}

func newSweeper(t *testing.T, cfg retention.Config, subjects ...retention.Subject) *retention.Sweeper {
	t.Helper()
	s, err := retention.New(cfg, subjects, nil)
	if err != nil {
		t.Fatalf("сборка уборщика: %v", err)
	}
	return s
}

// ─── СБОРКА: негодные величины отвергаются, а не исполняются вхолостую ───────

func TestNewRejectsUnusableSchedules(t *testing.T) {
	ok := retention.Subject{Name: "t", Sweep: fixedSweep(0, false)}
	base := retention.DefaultConfig()

	zeroBatch := base
	zeroBatch.Batch = 0
	hugeBatch := base
	hugeBatch.Batch = retention.MaxBatch + 1
	zeroPasses := base
	zeroPasses.MaxBatchesPerPass = 0
	zeroInterval := base
	zeroInterval.Interval = 0

	for _, tc := range []struct {
		name string
		cfg  retention.Config
		subj []retention.Subject
	}{
		{"нулевая партия — петля исполняется и не убирает ничего", zeroBatch, []retention.Subject{ok}},
		{"партия выше потолка — оператор держит строки горячего пути", hugeBatch, []retention.Subject{ok}},
		{"нулевой потолок проходов — то же вхолостую", zeroPasses, []retention.Subject{ok}},
		{"нулевой интервал", zeroInterval, []retention.Subject{ok}},
		{"пустой реестр — петля без предметов", base, nil},
		{"предмет без имени — его ноль нечем назвать", base, []retention.Subject{{Sweep: fixedSweep(0, false)}}},
		{"предмет без уборщика — объявление без предмета", base, []retention.Subject{{Name: "t"}}},
		{"один предмет дважды — два места об одном пороге", base, []retention.Subject{ok, ok}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := retention.New(tc.cfg, tc.subj, nil); err == nil {
				t.Fatal("негодная сборка обязана быть отвергнута: уборка, исполняющаяся " +
					"вхолостую, выглядит работающей, будучи мёртвой")
			}
		})
	}
}

func TestNewAcceptsTheDefaultSchedule(t *testing.T) {
	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ к отрицанию выше: без него отвержения зеленели бы
	// на сборке, отвергающей вообще всё.
	if _, err := retention.New(retention.DefaultConfig(),
		[]retention.Subject{{Name: "t", Sweep: fixedSweep(0, false)}}, nil); err != nil {
		t.Fatalf("величины по умолчанию обязаны проходить собственную проверку: %v", err)
	}
}

// ─── ПРОХОД: догон, потолок, ранний выход ───────────────────────────────────

func TestPassRepeatsWhileTheBatchGoesOutFull(t *testing.T) {
	// Признак «партия ушла полной» и есть догон: без повтора скорость уборки
	// была бы «партия за тик», и при более высоком темпе записи она не догнала
	// бы НИКОГДА, оставаясь зелёной по всякой проверке «вызвался ли».
	var calls atomic.Int64
	cfg := retention.DefaultConfig()
	cfg.MaxBatchesPerPass = 4
	s := newSweeper(t, cfg, retention.Subject{
		Name: "t", Sweep: countingSweep(&calls, int64(cfg.Batch), true),
	})

	res := s.Pass(context.Background())

	if calls.Load() != 4 {
		t.Fatalf("партий за проход %d, а потолок %d — проход обязан упираться в потолок, "+
			"а не в первую партию", calls.Load(), cfg.MaxBatchesPerPass)
	}
	if got, want := res.Removed["t"], int64(4*cfg.Batch); got != want {
		t.Errorf("снято %d, ожидалось %d", got, want)
	}
}

func TestPassStopsOnTheFirstPartialBatch(t *testing.T) {
	// Зеркало предыдущей: неполная партия означает «снимать больше нечего», и
	// проход обязан на ней закончиться. Без этой стороны потолок читался бы как
	// «всегда делать столько проходов», то есть как холостая нагрузка на базу.
	var calls atomic.Int64
	cfg := retention.DefaultConfig()
	cfg.MaxBatchesPerPass = 4
	s := newSweeper(t, cfg, retention.Subject{
		Name: "t", Sweep: countingSweep(&calls, 1, false),
	})

	s.Pass(context.Background())

	if calls.Load() != 1 {
		t.Fatalf("партий %d — неполная партия означает «нечего снимать», проход обязан "+
			"остановиться на ней", calls.Load())
	}
}

func TestPassReportsEverySubjectEvenWhenNothingWasRemoved(t *testing.T) {
	// Ноль по предмету означает либо «убирать нечего», либо «уборка не доходит
	// до этой записи реестра». Ключ, заведённый ДО первой партии, и есть то, что
	// эти состояния различает.
	s := newSweeper(t, retention.DefaultConfig(),
		retention.Subject{Name: "a", Sweep: fixedSweep(0, false)},
		retention.Subject{Name: "b", Sweep: fixedSweep(0, false)})

	res := s.Pass(context.Background())

	for _, name := range []string{"a", "b"} {
		if _, ok := res.Removed[name]; !ok {
			t.Errorf("предмета %q нет в отчёте прохода — «нечего убирать» стало "+
				"неотличимо от «уборка не доходит»", name)
		}
	}
}

func TestPassIsolatesAFailingSubjectFromTheRest(t *testing.T) {
	// Реестр — перечень НЕЗАВИСИМЫХ предметов, а не транзакция: отказ по одному
	// не отменяет остальных. Иначе одна отказавшая таблица останавливала бы
	// уборку всей платформы.
	boom := errors.New("отказ уборщика")
	s := newSweeper(t, retention.DefaultConfig(),
		retention.Subject{Name: "плохой", Sweep: func(context.Context, time.Duration, int) (int64, bool, error) {
			return 0, false, boom
		}},
		retention.Subject{Name: "хороший", Sweep: fixedSweep(7, false)})

	res := s.Pass(context.Background())

	if res.Errs["плохой"] == nil {
		t.Error("отказ обязан попасть в отчёт по своему предмету")
	}
	if got := res.Removed["хороший"]; got != 7 {
		t.Errorf("исправный предмет убрал %d вместо 7 — отказ соседа его остановил", got)
	}
	if res.Err() == nil {
		t.Error("объединённый отказ прохода обязан быть непустым")
	}
}

func TestPassErrIsNilWhenNoSubjectFailed(t *testing.T) {
	// Положительный контроль к предыдущей.
	s := newSweeper(t, retention.DefaultConfig(),
		retention.Subject{Name: "t", Sweep: fixedSweep(1, false)})
	if err := s.Pass(context.Background()).Err(); err != nil {
		t.Fatalf("проход без отказов обязан отвечать nil, получено %v", err)
	}
}

func TestStatsAccumulateAndNameEverySubject(t *testing.T) {
	s := newSweeper(t, retention.DefaultConfig(),
		retention.Subject{Name: "a", Sweep: fixedSweep(3, false)},
		retention.Subject{Name: "b", Sweep: fixedSweep(0, false)})

	s.Pass(context.Background())
	s.Pass(context.Background())

	st := s.Stats()
	if st.Passes != 2 {
		t.Errorf("проходов %d, ожидалось 2 — ноль здесь отличает «убирать нечего» "+
			"от «петля не идёт вовсе»", st.Passes)
	}
	if st.Removed["a"] != 6 {
		t.Errorf("накоплено по «a» %d, ожидалось 6", st.Removed["a"])
	}
	if _, ok := st.Removed["b"]; !ok {
		t.Error("предмет с нулём обязан быть в накопителе поимённо")
	}
}

// ─── ЖИЗНЕННЫЙ ЦИКЛ ─────────────────────────────────────────────────────────

func TestStartRunsTheFirstPassImmediately(t *testing.T) {
	// Первый проход встречает всё, что накопилось за жизнь стенда; откладывать
	// его на интервал значило бы держать накопленное ещё и это время.
	var calls atomic.Int64
	cfg := retention.DefaultConfig()
	cfg.Interval = time.Hour // тикер заведомо не сработает за время пробы
	s := newSweeper(t, cfg, retention.Subject{
		Name: "t", Sweep: countingSweep(&calls, 0, false),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	deadline := time.After(2 * time.Second)
	for calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("за две секунды первый проход не случился — уборка ждёт интервала, " +
				"вместо того чтобы встретить накопленное сразу")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if !s.Wait(2 * time.Second) {
		t.Error("петля не завершилась после отмены контекста")
	}
}

func TestStartTwiceDoesNotPanicOnShutdown(t *testing.T) {
	// РЕГРЕССИЯ. Вторая петля закрывала тот же канал завершения, что и первая, и
	// падала паникой «close of closed channel» — не при сборке, а НА ОСТАНОВЕ,
	// то есть роняла процесс целиком в момент, когда он и так завершается.
	//
	// Ошибка провязки при этом остаётся ошибкой: второй вызов ничего не
	// поднимает и говорит об этом в журнал. Паника здесь — заведомо худший
	// исход, чем лишний вызов: она превращает опечатку композиционного корня в
	// отказ сервиса.
	s := newSweeper(t, retention.DefaultConfig(), retention.Subject{
		Name: "t", Sweep: fixedSweep(0, false),
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	s.Start(ctx)
	cancel()

	if !s.Wait(2 * time.Second) {
		t.Fatal("петля не завершилась после отмены контекста")
	}
}

func TestWaitReportsTimeoutWhenTheLoopWasNeverStarted(t *testing.T) {
	// Названо пробой, а не комментарием: ожидание неподнятой петли не
	// завершается никогда, и вызывающий обязан узнать это из ответа, а не из
	// зависшего останова.
	s := newSweeper(t, retention.DefaultConfig(), retention.Subject{
		Name: "t", Sweep: fixedSweep(0, false),
	})
	if s.Wait(50 * time.Millisecond) {
		t.Fatal("неподнятая петля не может «дождаться» завершения")
	}
}
