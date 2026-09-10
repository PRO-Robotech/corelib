// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"google.golang.org/protobuf/types/known/anypb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/listnarrow/narrowtest"
	"github.com/PRO-Robotech/corelib/pagetoken"
	"github.com/PRO-Robotech/corelib/subscription"
)

// TestConjunctionOfThreeAxesNarrows — WATCH-1-01 вместе со своим положительным
// контролем WATCH-1-02.
//
// Отрицание без контроля зеленело бы на сервере, который не отдаёт ничего
// никогда, поэтому обе половины стоят в одной пробе и на ОДНОМ наполнении.
func TestConjunctionOfThreeAxesNarrows(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Два вида, два проекта, три идентификатора.
	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	s.emit(t, "Network", "net00000000000000002", "CREATED", "prj-b")
	s.emit(t, "Subnet", "sub00000000000000001", "CREATED", "prj-a")
	target := s.emit(t, "Network", "net00000000000000003", "UPDATED", "prj-a")
	_ = target

	t.Run("три оси сужают вместе", func(t *testing.T) {
		sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
			Kinds:     []string{"vpc_network"},
			ProjectId: "prj-a",
			Ids:       []string{"net00000000000000003"},
			Start: &subscriptionv1.SubscriptionRequest_Anchor{
				Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
			},
		})
		if len(sb.opened.GetHonoredFilters()) != 3 {
			t.Fatalf("честно отобранных осей %v, ожидалось три", sb.opened.GetHonoredFilters())
		}
		got := recvEvents(t, sb, 1)
		if got[0].GetResourceId() != "net00000000000000003" {
			t.Fatalf("пришло событие %q", got[0].GetResourceId())
		}
		// Событие, удовлетворяющее ДВУМ осям из трёх, не приходит: следом за
		// первым в потоке больше ничего нет.
		requireQuiet(t, sb)
	})

	t.Run("ни одной оси — приходят все", func(t *testing.T) {
		sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Anchor{
				Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
			},
		})
		if len(sb.opened.GetHonoredFilters()) != 0 {
			t.Fatalf("оси не задавались, а перечень честных = %v", sb.opened.GetHonoredFilters())
		}
		got := recvEvents(t, sb, 4)
		var prev string
		for i, ev := range got {
			if ev.GetPosition() == "" {
				t.Fatalf("событие %d без позиции", i)
			}
			if i > 0 && ev.GetPosition() == prev {
				t.Fatalf("позиция события %d повторяет предыдущую", i)
			}
			prev = ev.GetPosition()
		}
	})
}

// TestOpenedSaysWhetherTheJournalWasExhausted — WATCH-1-13: «событий не было»
// отличимо от «я подключился позже» ПО СЛОВУ СЕРВЕРА, а не арифметикой клиента
// над непрозрачным токеном.
func TestOpenedSaysWhetherTheJournalWasExhausted(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	s.emit(t, "Network", "net00000000000000002", "CREATED", "prj-a")

	t.Run("с текущего конца — журнал исчерпан", func(t *testing.T) {
		sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{})
		if !sb.opened.GetCaughtUp() {
			t.Fatal("подписка с хвоста объявлена недогнавшей")
		}
		if !sb.opened.GetRetainsEverything() {
			t.Fatal("владелец объявил удержание целиком, а служебное сообщение молчит")
		}
		requireQuiet(t, sb)
	})

	t.Run("с начала журнала — ещё догоняю", func(t *testing.T) {
		sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Anchor{
				Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
			},
		})
		if sb.opened.GetCaughtUp() {
			t.Fatal("подписка с начала непустого журнала объявлена догнавшей")
		}
	})
}

// TestThreeSubscribersAllGetEveryEvent — предикат готовности №3, первая половина:
// события не теряются при нескольких подписчиках, и порядок в пределах предмета
// цел.
func TestThreeSubscribersAllGetEveryEvent(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const subscribers = 3
	const events = 12

	streams := make([]*sub, subscribers)
	for i := range streams {
		sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{})
		if !sb.opened.GetCaughtUp() {
			t.Fatalf("подписчик %d открылся на пустом журнале недогнавшим", i)
		}
		streams[i] = sb
	}

	for i := 0; i < events; i++ {
		s.emit(t, "Network", "net0000000000000000"+string(rune('a'+i)), "CREATED", "prj-a")
	}

	var wg sync.WaitGroup
	seen := make([][]string, subscribers)
	for i := range streams {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for _, ev := range recvEvents(t, streams[i], events) {
				seen[i] = append(seen[i], ev.GetResourceId())
			}
		}(i)
	}
	wg.Wait()

	for i := 1; i < subscribers; i++ {
		if strings.Join(seen[i], ",") != strings.Join(seen[0], ",") {
			t.Fatalf("подписчик %d получил другую последовательность:\n%v\n%v", i, seen[i], seen[0])
		}
	}
	if len(seen[0]) != events {
		t.Fatalf("получено %d событий из %d", len(seen[0]), events)
	}
}

// TestResumeAfterBreakLosesNothingAndRepeatsNothing — предикат готовности №3,
// вторая половина (WATCH-1-08/09): курсор переживает обрыв, поток не теряет и не
// задваивает.
func TestResumeAfterBreakLosesNothingAndRepeatsNothing(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	s.emit(t, "Network", "net00000000000000002", "CREATED", "prj-a")

	first, cancelFirst := context.WithCancel(ctx)
	sb := s.open(t, first, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	got := recvEvents(t, sb, 2)
	resume := got[1].GetPosition()
	// ОБРЫВ: клиент уходит, соединение сервера закрывается.
	cancelFirst()

	// Пока клиента нет, владелец пишет ещё два события.
	s.emit(t, "Network", "net00000000000000003", "CREATED", "prj-a")
	s.emit(t, "Network", "net00000000000000004", "CREATED", "prj-a")

	sb2 := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Position{Position: resume},
	})
	if sb2.opened.GetPosition() != resume {
		t.Fatalf("сервер начал не с возвращённой позиции: %q против %q",
			sb2.opened.GetPosition(), resume)
	}
	after := recvEvents(t, sb2, 2)
	if after[0].GetResourceId() != "net00000000000000003" ||
		after[1].GetResourceId() != "net00000000000000004" {
		t.Fatalf("возобновление отдало %q, %q — ожидалось третье и четвёртое",
			after[0].GetResourceId(), after[1].GetResourceId())
	}
	// Уже покрытое позицией не приходит повторно.
	requireQuiet(t, sb2)
}

// TestStreamCeilingRefusesInsteadOfQueueing — предикат готовности №4 с
// ПОЛОЖИТЕЛЬНЫМ КОНТРОЛЕМ: в пределах потолка подписка открывается, на
// превышении приходит ОТКАЗ, а не зависание.
func TestStreamCeilingRefusesInsteadOfQueueing(t *testing.T) {
	s := newStand(t, standOpts{maxStreams: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Положительный контроль: два потока в пределах потолка открываются.
	for i := 0; i < 2; i++ {
		if sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{}); sb.opened == nil {
			t.Fatalf("поток %d не открылся в пределах потолка", i)
		}
	}

	// Третий обязан получить отказ, а не встать в очередь. Срок вдвое короче
	// срока пробы: зависание обязано проявиться отказом ПО СРОКУ, а не тем, что
	// проба досидит до собственного предела.
	over, cancelOver := context.WithTimeout(ctx, 5*time.Second)
	defer cancelOver()
	strm, err := s.client.Subscribe(over, &subscriptionv1.SubscriptionRequest{})
	if err == nil {
		_, err = strm.Recv()
	}
	if err == nil {
		t.Fatal("третий поток открылся при потолке два")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.ResourceExhausted {
		t.Fatalf("превышение потолка ответило %s (%v), ожидался ResourceExhausted", st.Code(), err)
	}
}

// TestUnnamedCallerAndUnwiredNarrowerAreRefusedBeforeTheBackend — порядок
// отказов: обе полосы обязаны наступать ДО обращения к базе, иначе отказ
// начинает зависеть от её доступности.
func TestUnnamedCallerAndUnwiredNarrowerAreRefusedBeforeTheBackend(t *testing.T) {
	t.Run("вызывающий не назван", func(t *testing.T) {
		s := newStand(t, standOpts{caller: context.Background()})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		strm, err := s.client.Subscribe(ctx, &subscriptionv1.SubscriptionRequest{})
		if err == nil {
			_, err = strm.Recv()
		}
		st, _ := status.FromError(err)
		if st.Code() != codes.PermissionDenied {
			t.Fatalf("безымянный вызывающий получил %s (%v)", st.Code(), err)
		}
	})

	t.Run("сужатель не сужает — подъём отказан", func(t *testing.T) {
		// Отказ ПОДЪЁМА, а не запроса: сервер, поднявшийся с непровязанной
		// моделью, отдавал бы весь журнал молча.
		_, err := subscription.NewServer(subscription.Config{
			Journal:      probeJournal(),
			DSN:          "postgres://ignored/ignored",
			Narrower:     narrowtest.Unwired(),
			ProjectGate:  probeProjectGate(),
			MaxStreams:   1,
			StreamBudget: time.Minute,
			IdlePoll:     time.Second,
		})
		if err == nil {
			t.Fatal("сервер поднялся с сужателем, который не сужает")
		}
		if !strings.Contains(err.Error(), "Narrower") {
			t.Fatalf("отказ не называет сужателя: %v", err)
		}
	})
}

// TestEveryDeliveredRowIsAuthorized — сужение идёт НА КАЖДУЮ ОТДАВАЕМУЮ СТРОКУ,
// а не один раз при открытии.
func TestEveryDeliveredRowIsAuthorized(t *testing.T) {
	s := newStand(t, standOpts{narrower: narrowtest.Allowing("net00000000000000002")})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")
	s.emit(t, "Network", "net00000000000000002", "CREATED", "prj-a")
	s.emit(t, "Network", "net00000000000000003", "CREATED", "prj-a")

	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	got := recvEvents(t, sb, 1)
	if got[0].GetResourceId() != "net00000000000000002" {
		t.Fatalf("отдано %q, а вызывающему разрешена только вторая сеть", got[0].GetResourceId())
	}
	requireQuiet(t, sb)
}

// TestModelRefusalIsNotSilence — отказ модели прав НЕ означает «событий нет»:
// поток закрывается отказом, а не молчит.
func TestModelRefusalIsNotSilence(t *testing.T) {
	s := newStand(t, standOpts{narrower: narrowtest.Failing(status.Error(codes.Unavailable, "модель недоступна"))})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")

	strm, err := s.client.Subscribe(ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	if err != nil {
		t.Fatalf("подписка не открылась: %v", err)
	}
	if _, err := strm.Recv(); err != nil {
		t.Fatalf("служебное сообщение не пришло: %v", err)
	}
	if _, err := strm.Recv(); err == nil {
		t.Fatal("поток отдал событие, хотя модель прав не ответила")
	} else if st, _ := status.FromError(err); st.Code() == codes.OK {
		t.Fatal("поток закрылся успехом при неответившей модели")
	}
}

// TestPositionNoLongerResumableIsAnExplicitRefusal — WATCH-1-11: явный отказ с
// названной возобновимой позицией, а не молчаливое начало с ближайшего
// удержанного места.
func TestPositionNoLongerResumableIsAnExplicitRefusal(t *testing.T) {
	// Колонка срока идёт ПАРОЙ к объявлению чистки: с задачи #1666 объявить одно
	// без другого нельзя — «журнал чистится» и «по чему судить возраст» суть одно
	// решение, и половина его не выражается.
	j := sweepingJournal()
	s := newStand(t, standOpts{journal: &j})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i := 0; i < 5; i++ {
		s.emit(t, "Network", "net0000000000000000"+string(rune('a'+i)), "CREATED", "prj-a")
	}
	// Владелец чистит журнал: первые три строки сняты.
	s.exec(t, "DELETE FROM probe_outbox WHERE sequence_no <= 3")

	lost := pagetoken.EncodeSubscriptionPosition(pagetoken.SubscriptionPosition{Settled: 1})
	strm, err := s.client.Subscribe(ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Position{Position: lost},
	})
	if err == nil {
		_, err = strm.Recv()
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.OutOfRange {
		t.Fatalf("утраченная позиция ответила %s (%v), ожидался OutOfRange", st.Code(), err)
	}
	if !hasResumablePosition(st) {
		t.Fatalf("отказ не называет возобновимую позицию машинно: %v", st.Proto())
	}

	// Положительный контроль: позиция ВНУТРИ удерживаемого открывает поток
	// обычным порядком, и служебное сообщение называет нижнюю границу.
	inside := pagetoken.EncodeSubscriptionPosition(pagetoken.SubscriptionPosition{Settled: 3})
	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Position{Position: inside},
	})
	if sb.opened.GetRetainsEverything() {
		t.Fatal("чистящий владелец объявил, что удерживает всё")
	}
	if sb.opened.GetEarliestResumablePosition() == "" {
		t.Fatal("чистящий владелец не назвал нижнюю возобновимую позицию")
	}
}

// TestProjectTheCallerCannotSeeIsRefusedAsAbsent — WATCH-1-05: отказ полосы
// доступа, неотличимый от «такого проекта нет».
func TestProjectTheCallerCannotSeeIsRefusedAsAbsent(t *testing.T) {
	s := newStand(t, standOpts{narrower: narrowtest.DenyingAll()})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	strm, err := s.client.Subscribe(ctx, &subscriptionv1.SubscriptionRequest{ProjectId: "prj-secret"})
	if err == nil {
		_, err = strm.Recv()
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("недоступный проект ответил %s (%v), ожидался NotFound", st.Code(), err)
	}
	if st.Message() != "Project prj-secret not found" {
		t.Fatalf("текст отказа %q не совпадает с формой отсутствия владельца", st.Message())
	}
}

// TestStateThatCannotBeSerializedIsNamed — WATCH-1-34: ошибка сборки состояния НЕ
// даёт подписчику пустой нагрузки.
func TestStateThatCannotBeSerializedIsNamed(t *testing.T) {
	j := probeJournal()
	j.Mapping.State = func(subscription.Row) (*anypb.Any, subscription.StateAbsence, error) {
		return nil, subscription.StateAbsenceUnnamed, errState
	}
	s := newStand(t, standOpts{journal: &j})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.emit(t, "Network", "net00000000000000001", "CREATED", "prj-a")

	sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
		Start: &subscriptionv1.SubscriptionRequest_Anchor{
			Anchor: subscriptionv1.SubscriptionAnchor_BEGINNING,
		},
	})
	got := recvEvents(t, sb, 1)
	if got[0].GetState() != nil {
		t.Fatal("подписчику отдана нагрузка, хотя состояние собрать не удалось")
	}
	un := got[0].GetStateUnavailable()
	if un == nil {
		t.Fatal("событие не несёт признака «состояния не будет»")
	}
	if un.GetReason() != subscriptionv1.SubscriptionEvent_StateUnavailable_NOT_SERIALIZABLE {
		t.Fatalf("причина названа как %s", un.GetReason())
	}
	if got[0].GetChange() == subscriptionv1.SubscriptionEvent_CHANGE_UNSPECIFIED {
		t.Fatal("род изменения не назван, хотя он известен из словаря владельца")
	}
}

// errState — отказ сборки состояния, поданный отображением намеренно.
var errState = errors.New("состояние не собирается")

// hasResumablePosition — назван ли машинно признак отказа и возобновимая позиция.
func hasResumablePosition(st *status.Status) bool {
	for _, d := range st.Details() {
		info, ok := d.(*errdetails.ErrorInfo)
		if !ok {
			continue
		}
		if info.GetReason() != "SUBSCRIPTION_POSITION_LOST" {
			continue
		}
		if _, ok := pagetoken.DecodeSubscriptionPosition(
			info.GetMetadata()["earliest_resumable_position"]); ok {
			return true
		}
	}
	return false
}
