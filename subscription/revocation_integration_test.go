// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

// revocation_integration_test.go — kacho#1022 на стороне ВЛАДЕЛЬЦА.
//
// # Какую половину закрывает эта проба и какую не закрывает
//
// Страж проектной оси спрашивался ОДИН раз — при открытии. Дальше поток жил
// сам: отзыв доступа к проекту не спрашивал никто, и события продолжали идти
// ровно до тех пор, пока их не переставало пропускать пообъектное сужение. А оно
// становится отрицательным не в момент отзыва привязки, а когда реконсайлер iam
// снимет пообъектные кортежи, — то есть окно между «право отозвано» и «строки
// перестали уходить» задавалось чужим конвейером и числом названо не было.
//
// Здесь утверждается ИСХОД: после отзыва проектного доступа из потока не уходит
// ни одного события. Именно исход, а не факт вызова: проба «стража позвали»
// осталась бы зелёной на страже, чей ответ никуда не идёт.
//
// Закрытие самого СОЕДИНЕНИЯ — предмет того, кто его открыл (для сегодняшнего
// единственного потребителя это край, kacho#1022, `gateway/internal/watcher` и
// `gateway/internal/subscriptionstream`).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/listnarrow"
)

// revocablePeer — приёмная сторона модели прав, чей вердикт МЕНЯЕТСЯ по ходу
// потока. Это и есть предмет: отзыв случается ПОСЛЕ открытия.
//
// Дублируется здесь, а не берётся из `narrowtest`, по одной причине: тамошний
// дублёр читается пробой после прогона и менять его вердикт на живом потоке —
// гонка. Форма ответа взята оттуда дословно.
type revocablePeer struct {
	mu sync.Mutex
	// denyType — тип объекта, по которому вердикт отрицателен. Отрицание сужено
	// ТИПОМ, чтобы проба различала две разные проверки: страж оси спрашивает про
	// `project`, пообъектное сужение — про вид ресурса. Отними доступ ко всему —
	// и проба зеленела бы на сужении, ничего не сказав о страже.
	denyType string
	// failType — тип объекта, по которому сосед не отвечает вовсе.
	failType string
	calls    map[string]int
}

func newRevocablePeer() *revocablePeer { return &revocablePeer{calls: map[string]int{}} }

func (p *revocablePeer) revokeProject() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.denyType = "project"
}

func (p *revocablePeer) breakProject() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failType = "project"
}

func (p *revocablePeer) callsOn(objectType string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[objectType]
}

func (p *revocablePeer) BatchCheck(
	_ context.Context, checks []listnarrow.Check,
) ([]bool, error) {
	p.mu.Lock()
	deny, fail := p.denyType, p.failType
	for _, c := range checks {
		p.calls[c.ResourceType]++
	}
	p.mu.Unlock()

	out := make([]bool, 0, len(checks))
	for _, c := range checks {
		if fail != "" && c.ResourceType == fail {
			return nil, errors.New("модель не ответила")
		}
		out = append(out, c.ResourceType != deny)
	}
	return out, nil
}

func revocableStand(t *testing.T, peer *revocablePeer) *stand {
	t.Helper()
	return newStand(t, standOpts{
		narrower: listnarrow.New(peer, listnarrow.Config{
			Relations: map[string][]string{"": {"v_get"}},
			// Окно вердиктов снято НАМЕРЕННО: предмет пробы — окно ОТЗЫВА, и
			// оставь здесь умолчание в пять секунд, проба мерила бы срок
			// жизни кэша, а не то, спрашивают ли право заново.
			CacheTTL: time.Nanosecond,
		}),
	})
}

// TestRevokedProjectStopsTheStreamMidFlight — НЕСУЩЕЕ утверждение половины
// владельца, вместе со своим положительным контролем.
//
// Контроль стоит В ТОЙ ЖЕ пробе и ПЕРЕД отзывом: без него «после отзыва событий
// нет» зеленело бы на потоке, который не отдаёт ничего никогда.
func TestRevokedProjectStopsTheStreamMidFlight(t *testing.T) {
	peer := newRevocablePeer()
	s := revocableStand(t, peer)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		ProjectId: "prj-a",
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: право есть — событие приходит.
	if got := recvEvents(t, sb, 1); got[0].GetResourceId() != "net00000000000000001" {
		t.Fatalf("до отзыва пришло событие %q", got[0].GetResourceId())
	}

	peer.revokeProject()
	s.emit(t, "Network", "net00000000000000002", "CREATED", "prj-a")

	select {
	case ev, ok := <-sb.events:
		if ok {
			t.Fatalf("после отзыва проектного доступа поток отдал событие %q — "+
				"страж оси спрошен один раз при открытии и больше никогда", ev.GetResourceId())
		}
		assertProjectAbsentRefusal(t, <-sb.fail)
	case err := <-sb.fail:
		assertProjectAbsentRefusal(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("после отзыва поток не отдал ни события, ни отказа — исход не наступил вовсе")
	}
}

// assertProjectAbsentRefusal — форма отказа обязана совпасть с формой отсутствия
// проекта ДОСЛОВНО: различимый текст выдал бы существование чужого проекта.
func assertProjectAbsentRefusal(t *testing.T, err error) {
	t.Helper()
	st := status.Convert(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("поток закрылся кодом %v (%q), ожидался NotFound", st.Code(), st.Message())
	}
	if st.Message() != "Project prj-a not found" {
		t.Fatalf("текст отказа %q, ожидался дословно «Project prj-a not found»", st.Message())
	}
}

// TestUnansweredModelStopsTheStreamMidFlight — FAIL-CLOSED на живом потоке.
//
// Модель не ответила — это НЕ «право есть». Тот же ответ, что у неотвеченного
// чтения журнала: недоступность, никогда молчаливое продолжение.
func TestUnansweredModelStopsTheStreamMidFlight(t *testing.T) {
	peer := newRevocablePeer()
	s := revocableStand(t, peer)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		ProjectId: "prj-a",
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	recvEvents(t, sb, 1)

	peer.breakProject()
	s.emit(t, "Network", "net00000000000000002", "CREATED", "prj-a")

	select {
	case ev, ok := <-sb.events:
		if ok {
			t.Fatalf("модель не ответила, а поток отдал событие %q — неполученный ответ засчитан за «да»",
				ev.GetResourceId())
		}
		requireUnavailable(t, <-sb.fail)
	case err := <-sb.fail:
		requireUnavailable(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("неотвеченная модель не дала ни события, ни отказа")
	}
}

func requireUnavailable(t *testing.T, err error) {
	t.Helper()
	if code := status.Code(err); code != codes.Unavailable {
		t.Fatalf("поток закрылся кодом %v, ожидался Unavailable", code)
	}
}

// TestProjectGateIsAskedAgainOnlyWhereThereIsAProjectAxis — граница предмета.
//
// Поток без проектной оси сторожить нечем: страж объявлен для оси, а не для
// потока. Проба стоит затем, чтобы повторный опрос не завёлся там, где у него
// нет вопроса, и не начал звать соседа впустую на каждой порции.
func TestProjectGateIsAskedAgainOnlyWhereThereIsAProjectAxis(t *testing.T) {
	peer := newRevocablePeer()
	s := revocableStand(t, peer)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	recvEvents(t, sb, 1)

	if n := peer.callsOn("project"); n != 0 {
		t.Fatalf("страж проектной оси спрошен %d раз на потоке без этой оси", n)
	}
	if n := peer.callsOn("vpc_network"); n == 0 {
		t.Fatal("пообъектное сужение не спрошено вовсе — проба зеленела бы на мёртвом потоке")
	}
}

// TestRevokedGroupDerivedAccessStopsTheStreamToo — доступ, полученный ЧЕРЕЗ
// ГРУППУ, снимается тем же переспросом стража.
//
// # Зачем проба, если механизм тот же
//
// Потому что по ИМЕНИ такой отзыв не закрывается ничем: строка журнала смены
// субъекта называет группу, а субъекта `group:…` в реестре открытых потоков не
// бывает — он учитывается по тому, кто предъявил удостоверение. Половина
// «закрыть по имени» на этом входе не работает НИКОГДА, и без этой пробы
// оставалось бы неизвестным, работает ли вторая.
//
// Модель разрешает доступ вызывающего членством в группе наравне с прямой
// выдачей, поэтому предмет вопроса стража — право ВЫЗЫВАЮЩЕГО, а не то, чем оно
// получено. Стенд снимает право у вызывающего: с точки зрения потока «сняли
// группу» и «сняли прямую выдачу» — одно наблюдение.
func TestRevokedGroupDerivedAccessStopsTheStreamToo(t *testing.T) {
	peer := newRevocablePeer()
	s := revocableStand(t, peer)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		ProjectId: "prj-a",
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	// Положительный контроль: право (полученное как угодно) есть — событие идёт.
	recvEvents(t, sb, 1)

	// Право снято. По имени этот отзыв закрыть было бы нечем.
	peer.revokeProject()
	s.emit(t, "Network", "net00000000000000002", "CREATED", "prj-a")

	select {
	case ev, ok := <-sb.events:
		if ok {
			t.Fatalf("после снятия права поток отдал событие %q — отзыв, который нельзя закрыть "+
				"по имени, не закрывается и переспросом стража: у него нет читателя вовсе",
				ev.GetResourceId())
		}
		assertProjectAbsentRefusal(t, <-sb.fail)
	case err := <-sb.fail:
		assertProjectAbsentRefusal(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("исход не наступил вовсе")
	}
}
