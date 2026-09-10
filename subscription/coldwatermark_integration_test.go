// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

// coldwatermark_integration_test.go — ноль перестал быть перегруженным (kacho#1386).
//
// # Предмет
//
// Граница устоявшегося начинает жизнь нулём, и первое наблюдение подтверждает
// её НЕ ВСЕГДА: писатель, державший журнал в момент наблюдения, переносит
// подтверждение на следующий проход. До него граница равна нулю — и ноль этот
// означает «позиции ещё нет», а не «позиция ноль».
//
// Подписчик, пришедший БЕЗ позиции («отдавай с текущего места»), садится на
// границу. Сев на неподтверждённый ноль, он садится в НАЧАЛО журнала: ему
// уезжает вся накопленная история, а служебное сообщение при этом объявляет его
// догнавшим (`caught_up` сравнивает курсор с той же нулевой границей). Строки
// журнала дренаж не удаляет — он лишь помечает отправленное, — поэтому хвост
// бывает длинным, а кэш решений о доступе обесценивается на всё время догона.
//
// # Почему проба интеграционная
//
// Холодное наблюдение производится не подставным значением, а НАСТОЯЩИМ
// писателем, держащим блокировку журнала: граница строится на `pg_locks`, и
// вход, которого нет в базе, её состояния не воспроизводит.

import (
	"context"
	"sync"
	"testing"
	"time"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/pagetoken"
)

// TestSubscriptionWithoutPositionNeverSeatsOnAnUnconfirmedZero — несущий случай.
//
// Журнал НЕПУСТ, писатель держит его в момент открытия потока. Подписчик без
// позиции не вправе сесть в начало журнала и не вправе получить историю.
func TestSubscriptionWithoutPositionNeverSeatsOnAnUnconfirmedZero(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// История: три зафиксированные строки, которых подписчик «с текущего
	// места» видеть не должен.
	s.exec(t, `INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
	           VALUES ('Network','net00000000000000001','CREATED','{"projectId":"prj-a"}'::jsonb),
	                  ('Network','net00000000000000002','CREATED','{"projectId":"prj-a"}'::jsonb),
	                  ('Network','net00000000000000003','CREATED','{"projectId":"prj-a"}'::jsonb)`)

	// Писатель держит журнал: блокировка взята, фиксации нет. Ровно в этом
	// состоянии первое наблюдение границы не подтверждается.
	writerConn := mustConnect(t, ctx, s.dsn)
	tx, err := writerConn.Begin(ctx)
	mustNoErr(t, err)
	insertInTx(t, ctx, tx, "net00000000000000004")

	// Открытие идёт в своей горутине: починенный сервер ЖДЁТ подтверждения
	// границы, и ждать он обязан именно этого писателя.
	var (
		wg  sync.WaitGroup
		sb  *sub
		mu  sync.Mutex
		got bool
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		opened := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{})
		mu.Lock()
		sb, got = opened, true
		mu.Unlock()
	}()

	// Дать серверу дойти до наблюдения границы, затем отпустить писателя.
	time.Sleep(700 * time.Millisecond)
	mustNoErr(t, tx.Commit(ctx))
	wg.Wait()

	mu.Lock()
	opened, ok := sb, got
	mu.Unlock()
	if !ok {
		t.Fatal("поток не открылся")
	}

	pos, decoded := pagetoken.DecodeSubscriptionPosition(opened.opened.GetPosition())
	if !decoded {
		t.Fatalf("позиция %q не разбирается", opened.opened.GetPosition())
	}
	if pos.Settled == 0 {
		t.Errorf("подписчик без позиции посажен на НОЛЬ при непустом журнале — "+
			"неподтверждённая граница усвоена как позиция, и дальше уезжает вся история "+
			"(caught_up при этом объявлен %v)", opened.opened.GetCaughtUp())
	}

	// Наблюдаемое следствие того же дефекта: история не имеет права уехать
	// подписчику, попросившему «с текущего места».
	requireQuiet(t, opened)
}

// TestEmptyJournalSeatsAtZeroWithoutWaiting — ЗАКОННЫЙ БЛИЗНЕЦ.
//
// У пустого журнала ноль — настоящая позиция, а не отсутствие таковой: строк,
// которые можно было бы пропустить, нет. Такой подписчик обязан сесть сразу и
// получить всё, что появится ПОСЛЕ него.
//
// Без этой стороны починка зеленела бы на сервере, который не открывает поток
// никогда, — то есть на средстве, вылечившем расход отказом в обслуживании.
func TestEmptyJournalSeatsAtZeroWithoutWaiting(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	done := make(chan *sub, 1)
	go func() { done <- s.open(t, ctx, &subscriptionv1.SubscriptionRequest{}) }()

	var sb *sub
	select {
	case sb = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("поток на ПУСТОМ журнале не открылся — подтверждать нечего, ждать нечего")
	}

	pos, decoded := pagetoken.DecodeSubscriptionPosition(sb.opened.GetPosition())
	if !decoded || pos.Settled != 0 {
		t.Fatalf("позиция пустого журнала %v (разобрана %v), ожидался ноль", pos.Settled, decoded)
	}

	s.exec(t, `INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
	           VALUES ('Network','net00000000000000009','CREATED','{"projectId":"prj-a"}'::jsonb)`)
	evs := recvEvents(t, sb, 1)
	if evs[0].GetResourceId() != "net00000000000000009" {
		t.Errorf("пришёл предмет %q, ожидался net00000000000000009", evs[0].GetResourceId())
	}
}
