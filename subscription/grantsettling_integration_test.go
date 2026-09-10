// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/listnarrow/narrowtest"
)

// Предмет файла — ОКНО МАТЕРИАЛИЗАЦИИ ГРАНТА, задача продукта #2264.
//
// Строка журнала и строка ресурса коммитятся ОДНОЙ транзакцией, а кортеж
// владения кладётся ПОСЛЕ фиксации (`fgaregister.DeliverAfterCommit` у каждого
// владельца). Пробуждение потока приходит НА ФИКСАЦИИ — то есть в самый ранний
// возможный миг, когда строка уже видна, а грант ещё нет.
//
// Пообъектный вопрос в этот миг получает «нет» ЗАКОННО, и до этой ветки такой
// ответ был ОКОНЧАТЕЛЬНЫМ: курсор идёт по ПРОЧИТАННОЙ строке, поэтому строка
// уезжала под него и открытый поток не отдавал её больше НИКОГДА. Клиент при
// этом не может отличить «изменений не было» от «изменение было и не доехало»:
// у него нет ни пропуска в нумерации, ни ошибки.
//
// Наблюдалось прогоном console-e2e 34152034441: перечитывание с той же позиции
// предмет ПРИНЕСЛО, а открытый поток — нет, при нуле ошибок связи и одном
// открытии потока.

// gatedPeer — приёмная сторона, чей вердикт МЕНЯЕТСЯ по ходу пробы.
//
// Дублёр `narrowtest.Peer` для этого негоден by construction: его вердикты
// объявляются при сборке, а предмет здесь — переход «нет» → «да», случившийся
// ПОСЛЕ того, как поток уже спросил.
//
// Он не снисходительнее продукта: отвечает В ПОРЯДКЕ вопросов и той же длины,
// как требует контракт порта, и ведёт перепись вопросов по идентификатору —
// именно она даёт пробе одно-фактную точку переключения («грант положили ровно
// после того, как о предмете спросили»), а не задержку по часам.
type gatedPeer struct {
	mu      sync.Mutex
	denied  map[string]bool
	asked   map[string]int
	batches int
}

func newGatedPeer(deny ...string) *gatedPeer {
	d := make(map[string]bool, len(deny))
	for _, id := range deny {
		d[id] = true
	}
	return &gatedPeer{denied: d, asked: make(map[string]int)}
}

func (p *gatedPeer) BatchCheck(_ context.Context, checks []listnarrow.Check) ([]bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.batches++
	out := make([]bool, 0, len(checks))
	for _, c := range checks {
		p.asked[c.ResourceID]++
		out = append(out, !p.denied[c.ResourceID])
	}
	return out, nil
}

// timesAsked — сколько раз сосед был спрошен об идентификаторе.
func (p *gatedPeer) timesAsked(id string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked[id]
}

// grant — кортеж владения ЛЁГ: с этого мига предмет видим.
func (p *gatedPeer) grant(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.denied, id)
}

func gatedNarrower(p *gatedPeer) *listnarrow.Narrower {
	return listnarrow.New(p, listnarrow.Config{Relations: map[string][]string{"": {"v_get"}}})
}

// awaitAsked ждёт, пока сосед не будет спрошен о предмете — то есть пока поток
// не ВЫНЕСЕТ по нему суждение. Ждать по часам здесь нельзя: предмет пробы —
// порядок «спросили → грант лёг», а не длительность.
func awaitAsked(t testing.TB, p *gatedPeer, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if p.timesAsked(id) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("поток не спросил модель о предмете %q за 20 с — суждения не было вовсе, "+
		"и проба ничего не утверждает о его исходе", id)
}

// TestOpenStreamKeepsARowWhoseGrantMaterializesLate — ПРЕДМЕТ задачи #2264.
//
// Открытый поток вынес суждение о строке в окне материализации гранта и получил
// законное «нет». Грант лёг сразу после этого. Событие обязано доехать по ТОМУ
// ЖЕ открытому потоку — ровно один раз и со своей позицией.
func TestOpenStreamKeepsARowWhoseGrantMaterializesLate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	const late = "net-late-grant"

	peer := newGatedPeer(late)
	st := newStand(t, standOpts{narrower: gatedNarrower(peer)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sb := st.open(t, ctx, &subscriptionv1.SubscriptionRequest{ProjectId: "prj-1"})

	st.emit(t, "Network", late, "CREATED", "prj-1")
	awaitAsked(t, peer, late)
	peer.grant(late)

	got := recvEvents(t, sb, 1)
	if got[0].GetResourceId() != late {
		t.Fatalf("поток отдал предмет %q, ожидался %q", got[0].GetResourceId(), late)
	}
	if got[0].GetPosition() == "" {
		t.Fatal("событие пришло без позиции: возобновиться с него нечем")
	}
	// Повтора не бывает: удержание не есть перечитывание уже отданного.
	requireQuiet(t, sb)
}

// TestWithheldRowIsSkippedOnceItsWindowIsSpent — ЗАКОННЫЙ БЛИЗНЕЦ.
//
// Предмет, который вызывающему не разрешат НИКОГДА, обязан остаться невидимым, а
// поток — не залипнуть на нём: соседнее событие доезжает. Без этой половины
// починка первой пробы зеленела бы на сервере, который просто отдаёт всё.
func TestWithheldRowIsSkippedOnceItsWindowIsSpent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	const (
		never   = "net-never-granted"
		visible = "net-visible"
	)

	peer := newGatedPeer(never)
	st := newStand(t, standOpts{narrower: gatedNarrower(peer)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sb := st.open(t, ctx, &subscriptionv1.SubscriptionRequest{ProjectId: "prj-1"})

	st.emit(t, "Network", never, "CREATED", "prj-1")
	st.emit(t, "Network", visible, "CREATED", "prj-1")

	got := recvEvents(t, sb, 1)
	if got[0].GetResourceId() != visible {
		t.Fatalf("поток отдал предмет %q, а вызывающему разрешён только %q", got[0].GetResourceId(), visible)
	}
	requireQuiet(t, sb)
	if peer.timesAsked(never) == 0 {
		t.Fatal("о неразрешённом предмете не спросили ни разу — проба зеленеет не по своему предмету")
	}
}

// TestGrantWindowDoesNotReorderTheStream — удержание СОХРАНЯЕТ ПОРЯДОК.
//
// Позиция едет клиенту заголовком возобновления, и он возобновляется с
// ПОСЛЕДНЕЙ полученной. Отдать событие с меньшей позицией после большей значило
// бы отбросить клиента назад и заставить его применить уже применённое.
func TestGrantWindowDoesNotReorderTheStream(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	const (
		late  = "net-order-late"
		after = "net-order-after"
	)

	peer := newGatedPeer(late)
	st := newStand(t, standOpts{narrower: gatedNarrower(peer)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sb := st.open(t, ctx, &subscriptionv1.SubscriptionRequest{ProjectId: "prj-1"})

	st.emit(t, "Network", late, "CREATED", "prj-1")
	awaitAsked(t, peer, late)
	st.emit(t, "Network", after, "CREATED", "prj-1")
	peer.grant(late)

	got := recvEvents(t, sb, 2)
	if got[0].GetResourceId() != late || got[1].GetResourceId() != after {
		t.Fatalf("порядок нарушен: пришли %q, %q — ожидались %q, %q",
			got[0].GetResourceId(), got[1].GetResourceId(), late, after)
	}
	if got[0].GetPosition() >= got[1].GetPosition() {
		t.Fatalf("позиции не возрастают: %q затем %q", got[0].GetPosition(), got[1].GetPosition())
	}
	requireQuiet(t, sb)
}

// TestNarrowerStaysWiredUnderTheGrantWindow — предпосылка соседних проб.
//
// Дублёр здесь СВОЙ, а не `narrowtest`; проба утверждает, что собранный им
// сужатель действительно сужает — иначе три пробы выше зеленели бы на сервере,
// который отказал бы в подписке вовсе.
func TestNarrowerStaysWiredUnderTheGrantWindow(t *testing.T) {
	if !gatedNarrower(newGatedPeer()).Narrows() {
		t.Fatal("сужатель пробы не сужает — сервер отверг бы подписку, и предмет проб не наступил бы")
	}
	if narrowtest.AllowingAll().Narrows() != true {
		t.Fatal("контроль: сужатель дерева обязан сужать")
	}
}

// censusLog — журнал процесса, у которого проба СПРАШИВАЕТ напечатанное.
//
// Перепись — наблюдаемое: расхождение «прочитано против отдано» обязано быть
// видно тому, кто разбирает пропуск, не имея этой пробы под рукой. Утверждать
// про неё чтением кода значило бы не утверждать ничего.
type censusLog struct {
	mu      sync.Mutex
	records []map[string]int64
}

func (c *censusLog) Enabled(context.Context, slog.Level) bool { return true }
func (c *censusLog) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *censusLog) WithGroup(string) slog.Handler            { return c }

func (c *censusLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "subscription: stream delivery census" {
		return nil
	}
	got := make(map[string]int64, 3)
	r.Attrs(func(a slog.Attr) bool {
		if a.Value.Kind() == slog.KindInt64 {
			got[a.Key] = a.Value.Int64()
		}
		return true
	})
	c.mu.Lock()
	c.records = append(c.records, got)
	c.mu.Unlock()
	return nil
}

// await ждёт переписи потока: она печатается при ЗАКРЫТИИ, а закрытие идёт в
// чужой горутине.
func (c *censusLog) await(t testing.TB) map[string]int64 {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.records)
		var last map[string]int64
		if n > 0 {
			last = c.records[n-1]
		}
		c.mu.Unlock()
		if last != nil {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("перепись доставки не напечатана: расхождение «прочитано против отдано» " +
		"осталось немым, и пропуск виден только пробой браузера")
	return nil
}

// TestDeliveryCensusNamesBothReadAndSentRows — ВТОРАЯ ПОЛОВИНА предиката #2264.
//
// Отсрочка снимает потерю в окне материализации гранта и НЕ снимает её за
// окном: строка, чей грант так и не лёг, будет снята — законно неотличимо от
// строки, которую вызывающему не разрешат никогда. Поэтому остаток обязан быть
// НАЗВАН ЧИСЛОМ, а не умолчан: «прочитано» без «отдано» пропуска не показывает,
// «отдано» без «прочитано» — тоже.
func TestDeliveryCensusNamesBothReadAndSentRows(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	const (
		never   = "net-census-never"
		visible = "net-census-visible"
	)

	cap := &censusLog{}
	peer := newGatedPeer(never)
	st := newStand(t, standOpts{
		narrower: gatedNarrower(peer),
		logger:   slog.New(cap),
	})

	ctx, cancel := context.WithCancel(context.Background())
	sb := st.open(t, ctx, &subscriptionv1.SubscriptionRequest{ProjectId: "prj-1"})

	st.emit(t, "Network", never, "CREATED", "prj-1")
	st.emit(t, "Network", visible, "CREATED", "prj-1")
	recvEvents(t, sb, 1)
	cancel()

	got := cap.await(t)
	// РОВНО две, а не «хотя бы»: удержанная партия перечитывается каждым
	// проходом, и счёт по числу прочитанных строк назвал бы одну строку столько
	// раз, сколько её спрашивали, — то есть показал бы расхождение, которого нет.
	if got["rows_read"] != 2 {
		t.Fatalf("перепись назвала rows_read=%d, а журнал нёс ровно две строки: "+
			"перечитывание удержанной партии не должно попадать в счёт", got["rows_read"])
	}
	if got["events_sent"] != 1 {
		t.Fatalf("перепись назвала events_sent=%d, а вызывающему разрешена одна строка", got["events_sent"])
	}
	if got["rows_withheld"] != 1 {
		t.Fatalf("перепись назвала rows_withheld=%d, а снята была одна строка: "+
			"расхождение «прочитано против отдано» не объяснено", got["rows_withheld"])
	}
}

// TestDeliveryCensusIsPrintedOnARefusedStreamToo — перепись на КАЖДОМ исходе.
//
// Поток, закончившийся отказом, уносит своё расхождение вместе с собой, если
// перепись печатается только на чистом конце, — а разбирают как раз такие
// потоки. Здесь отказ производит модель прав, отвечающая на переспрос стража
// оси «нет».
func TestDeliveryCensusIsPrintedOnARefusedStreamToo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration")
	}
	cap := &censusLog{}
	peer := newGatedPeer()
	st := newStand(t, standOpts{
		narrower: gatedNarrower(peer),
		logger:   slog.New(cap),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sb := st.open(t, ctx, &subscriptionv1.SubscriptionRequest{ProjectId: "prj-1"})

	// Право на ось отзывается — переспрос стража перед порцией закроет поток
	// отказом (см. [Server.regate]).
	peer.mu.Lock()
	peer.denied["prj-1"] = true
	peer.mu.Unlock()
	st.emit(t, "Network", "net-refused-census", "CREATED", "prj-1")

	select {
	case <-sb.fail:
	case <-time.After(20 * time.Second):
		t.Fatal("поток не закрылся на отозванном праве — предмет пробы не наступил")
	}
	got := cap.await(t)
	if _, ok := got["rows_read"]; !ok {
		t.Fatal("перепись отказавшего потока не назвала rows_read")
	}
	if _, ok := got["events_sent"]; !ok {
		t.Fatal("перепись отказавшего потока не назвала events_sent")
	}
}
