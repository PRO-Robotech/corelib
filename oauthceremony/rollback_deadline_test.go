// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// rollback_deadline_test.go — откат единицы работы после срока операции.
//
// Движок откатывает единицу работы, когда запись в ней отказала. Отказ записи
// часто и есть истёкший срок операции (Config.OperationTimeout): тогда откат
// приходит в мост уже ПОСЛЕ этого срока. Откат, унаследовавший срок операции,
// получил бы мёртвый контекст и не исполнился бы — транзакция службы осталась
// бы открытой до разрыва соединения. Поэтому откат получает контекст,
// отвязанный от отмены операции, со своим сроком не дальше Config.PortTimeout.
//
// Закрепление — наоборот: закрепить работу операции после её срока значило бы
// отдать успех тому, кто уже получил отказ по сроку. Закрепление срок операции
// сохраняет.
//
// Операция истекает детерминированно: открытие единицы работы ждёт, пока
// умрёт контекст операции (своего срока у открытия нет — см. BeginTX), и лишь
// затем отвечает успехом. После этого у записи два исхода, и это ровно один
// факт, которым отличаются проба и её близнец:
//
//   - запись отказала по мёртвому контексту, как отказала бы база, — движок
//     откатывает (проба отката);
//   - запись исполнилась, не глядя на контекст, как оператор, уже принятый
//     базой, — движок закрепляет (близнец: закрепление получает мёртвый
//     контекст).
//
// Класс, а не экземпляр: единицу работы открывают оба пути выдачи — обмен
// кода и оборот токена обновления, — и судятся оба.
package oauthceremony_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// unitCall — один вызов порта единицы работы так, как его увидела служба:
// метод, отказ контекста в миг вызова, был ли у контекста срок и сколько от
// него оставалось.
type unitCall struct {
	method    string
	ctxErr    error
	limited   bool
	remaining time.Duration
}

// lateUnitOfWork — единица работы, чьё открытие исполняется дольше срока
// операции: оно ждёт смерти контекста операции и отвечает успехом. Закрепление
// и откат отвечают, как база: на мёртвом контексте — его отказом.
type lateUnitOfWork struct {
	mu    sync.Mutex
	calls []unitCall
}

func (u *lateUnitOfWork) record(method string, ctx context.Context) error {
	call := unitCall{method: method, ctxErr: ctx.Err()}
	if deadline, limited := ctx.Deadline(); limited {
		call.limited = true
		call.remaining = time.Until(deadline)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, call)
	return call.ctxErr
}

func (u *lateUnitOfWork) Begin(ctx context.Context) (context.Context, error) {
	<-ctx.Done()
	_ = u.record("Begin", ctx)
	return ctx, nil
}

func (u *lateUnitOfWork) Commit(ctx context.Context) error {
	return u.record("Commit", ctx)
}

func (u *lateUnitOfWork) Rollback(ctx context.Context) error {
	return u.record("Rollback", ctx)
}

// callsOf — вызовы метода method в порядке прихода.
func (u *lateUnitOfWork) callsOf(method string) []unitCall {
	u.mu.Lock()
	defer u.mu.Unlock()

	var calls []unitCall
	for _, call := range u.calls {
		if call.method == method {
			calls = append(calls, call)
		}
	}
	return calls
}

var _ oauthceremony.UnitOfWork = (*lateUnitOfWork)(nil)

// deadlineHonouringAccessVault — хранилище токенов доступа, чья запись на
// мёртвом контексте отказывает его отказом, как отказала бы база. Прочее —
// из inner.
type deadlineHonouringAccessVault struct {
	oauthceremony.AccessTokenVault
}

func (v deadlineHonouringAccessVault) StoreAccessToken(ctx context.Context, signature string, grant oauthceremony.GrantRecord) (oauthceremony.StoreOutcome, error) {
	if err := ctx.Err(); err != nil {
		return oauthceremony.StoreOutcome{}, err
	}
	return v.AccessTokenVault.StoreAccessToken(ctx, signature, grant)
}

// TestRollbackAfterTheOperationDeadlineGetsALiveContext — откат, пришедший
// после срока операции, получает живой контекст со сроком не дальше
// Config.PortTimeout; закрепление на истёкшей операции — мёртвый.
func TestRollbackAfterTheOperationDeadlineGetsALiveContext(t *testing.T) {
	const (
		portTimeout      = 100 * time.Millisecond
		operationTimeout = 150 * time.Millisecond
	)
	type path struct {
		name string
		// exchange проходит выдачу над ceremony — готовит предмет над
		// prepared, у которой единицы работы нет.
		exchange func(t *testing.T, prepared, ceremony *oauthceremony.Ceremony) error
	}
	paths := []path{
		{
			name: "обмен кода",
			exchange: func(t *testing.T, prepared, ceremony *oauthceremony.Ceremony) error {
				t.Helper()
				code, _ := issueCode(t, prepared)
				_, err := ceremony.Exchange(context.Background(), codeExchange(code))
				return err
			},
		},
		{
			name: "оборот токена обновления",
			exchange: func(t *testing.T, prepared, ceremony *oauthceremony.Ceremony) error {
				t.Helper()
				tokens := exchangeCode(t, prepared)
				_, err := ceremony.Exchange(context.Background(), refreshRequest(tokens.RefreshToken))
				return err
			},
		},
	}

	var judged int
	for _, p := range paths {
		for _, writeFails := range []bool{true, false} {
			judged++
			name := p.name + "/запись отказала после срока операции"
			if !writeFails {
				name = p.name + "/близнец: запись исполнилась после срока операции"
			}
			t.Run(name, func(t *testing.T) {
				store := newMemoryPorts()
				registerTestClient(t, store)
				prepared := newTestCeremony(t, store.ports())

				unit := &lateUnitOfWork{}
				ports := store.ports()
				ports.Transaction = unit
				if writeFails {
					ports.AccessTokens = deadlineHonouringAccessVault{AccessTokenVault: store}
				}
				ceremony := newTestCeremony(t, ports, func(cfg *oauthceremony.Config) {
					cfg.PortTimeout = portTimeout
					cfg.OperationTimeout = operationTimeout
				})

				err := p.exchange(t, prepared, ceremony)
				if len(unit.callsOf("Begin")) == 0 {
					t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: единица работы не открыта (отказ операции %v) — судить нечего", err)
				}

				if !writeFails {
					commits := unit.callsOf("Commit")
					if len(commits) == 0 {
						t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: закрепления не было (отказ операции %v)", err)
					}
					for i, call := range commits {
						if !errors.Is(call.ctxErr, context.DeadlineExceeded) {
							t.Errorf("закрепление №%d на истёкшей операции получило контекст с отказом %v, ожидался %v",
								i+1, call.ctxErr, context.DeadlineExceeded)
						}
					}
					return
				}

				rollbacks := unit.callsOf("Rollback")
				if len(rollbacks) == 0 {
					t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: отката не было (отказ операции %v)", err)
				}
				if err == nil {
					t.Error("операция, запись которой отказала, ответила успехом")
				}
				for i, call := range rollbacks {
					if call.ctxErr != nil {
						t.Errorf("откат №%d получил мёртвый контекст: %v", i+1, call.ctxErr)
					}
					if !call.limited {
						t.Errorf("откат №%d получил контекст без срока", i+1)
					}
					if call.remaining <= 0 || call.remaining > portTimeout {
						t.Errorf("откату №%d оставалось %v, ожидалось больше нуля и не больше %v",
							i+1, call.remaining, portTimeout)
					}
				}
			})
		}
	}
	t.Logf("перепись: путей %d · случаев %d", len(paths), judged)
	if len(paths) == 0 || judged != 2*len(paths) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: судимо %d", judged)
	}
}
