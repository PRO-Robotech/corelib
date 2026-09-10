// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

// continuouswriting_integration_test.go — поток открывается ПОД СПЛОШНОЙ
// ЗАПИСЬЮ, а неоткрытый поток не бывает немым (kacho#1386).
//
// # Предмет
//
// Подписчика без позиции сажают только на ПОДТВЕРЖДЁННУЮ границу — иначе он
// садится в начало журнала и вычитывает всю накопленную историю. Ожидание
// подтверждения обязано наступать: если оно не наступает под нагрузкой, расход
// вылечен отказом в обслуживании, и цена ошибки перенесена с «лишняя история» на
// «потока нет вовсе».
//
// # Почему нагрузка — часть предмета, а не декорация
//
// Граница строится на `pg_locks`, и «журнал занят» — состояние, которое
// производит только настоящий писатель. Под РЕДКОЙ записью пустой миг наступает
// сам и скрывает дефект: ожидание снимается тем же проходом, каким берётся
// новое, и разница видна лишь тогда, когда пустого мига не бывает.
//
// Режим подачи назван числами: 8 писателей, транзакция 250 мс, старт вразбежку.
// Он взят из возражения приёмки — на нём поток не открывался за 40 с, — и здесь
// он закреплён, чтобы починка доказывалась ТЕМ ЖЕ опытом.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
	"github.com/PRO-Robotech/corelib/pagetoken"
)

func pgxConnect(ctx context.Context, dsn string) (*pgx.Conn, error) { return pgx.Connect(ctx, dsn) }

// journalLoad — сплошная занятость журнала: writers горутин, каждая держит
// транзакцию hold и начинает со сдвигом, чтобы пустого мига не оставалось.
type journalLoad struct {
	stop chan struct{}
	wg   sync.WaitGroup
}

func startJournalLoad(t testing.TB, dsn string, writers int, hold time.Duration) *journalLoad {
	t.Helper()
	l := &journalLoad{stop: make(chan struct{})}
	for i := 0; i < writers; i++ {
		l.wg.Add(1)
		go func(n int) {
			defer l.wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn, err := pgxConnect(ctx, dsn)
			if err != nil {
				return
			}
			defer func() {
				closeCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
				_ = conn.Close(closeCtx)
				c()
			}()
			// Сдвиг старта: без него все писатели фиксируются одновременно и
			// журнал регулярно пустеет — то есть подача перестаёт быть сплошной.
			select {
			case <-time.After(time.Duration(n) * hold / time.Duration(writers)):
			case <-l.stop:
				return
			}
			for seq := 0; ; seq++ {
				select {
				case <-l.stop:
					return
				default:
				}
				tx, err := conn.Begin(ctx)
				if err != nil {
					return
				}
				var got int64
				id := fmt.Sprintf("ld%d%015d", n, seq)
				if err := tx.QueryRow(ctx, probeInsertSQL, id).Scan(&got); err != nil {
					_ = tx.Rollback(ctx)
					return
				}
				select {
				case <-time.After(hold):
				case <-l.stop:
					_ = tx.Rollback(ctx)
					return
				}
				if err := tx.Commit(ctx); err != nil {
					return
				}
			}
		}(i)
	}
	t.Cleanup(l.halt)
	return l
}

func (l *journalLoad) halt() {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
	l.wg.Wait()
}

// TestStreamOpensUnderContinuousWriting — НЕСУЩИЙ СЛУЧАЙ возражения приёмки.
//
// Журнал занят непрерывно. Подписчик без позиции обязан сесть — на границу,
// подтверждённую доистёкшими писателями, а не на ноль и не «никогда».
func TestStreamOpensUnderContinuousWriting(t *testing.T) {
	s := newStand(t, standOpts{budget: 120 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// История, которой подписчик «с текущего места» видеть не должен.
	s.exec(t, `INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
	           VALUES ('Network','net00000000000000001','CREATED','{"projectId":"prj-a"}'::jsonb)`)

	startJournalLoad(t, s.dsn, 8, 250*time.Millisecond)
	// Дать подаче выйти на режим: к моменту открытия журнал обязан быть занят,
	// иначе проба измеряет ненагруженный случай и дефекта не видит.
	time.Sleep(1 * time.Second)

	done := make(chan *sub, 1)
	go func() { done <- s.open(t, ctx, &subscriptionv1.SubscriptionRequest{}) }()

	var sb *sub
	select {
	case sb = <-done:
	case <-time.After(40 * time.Second):
		t.Fatal("поток не открылся за 40 с под сплошной записью: " +
			"подтверждённая граница есть и растёт, а подписчика на неё не сажают — " +
			"расход вылечен отказом в обслуживании, и притом немым")
	}

	pos, decoded := pagetoken.DecodeSubscriptionPosition(sb.opened.GetPosition())
	if !decoded {
		t.Fatalf("позиция %q не разбирается", sb.opened.GetPosition())
	}
	if pos.Settled == 0 {
		t.Errorf("подписчик без позиции посажен на НОЛЬ при непустом журнале "+
			"(caught_up при этом объявлен %v)", sb.opened.GetCaughtUp())
	}
}
