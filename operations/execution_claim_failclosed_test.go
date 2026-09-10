// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

// Предмет: подтверждение живости операции перед исполнением — это ПРОВЕРКА, а
// не подсказка.
//
// Перед запуском доменной функции worker атомарно подтверждает, что строка ещё
// не разрешена (иначе исполнение создало бы ресурс поверх уже разрешённой
// ошибкой операции: клиент видит отказ, ресурс молча существует, повтор ловит
// «уже есть» на то, чего «нет»). Если подтвердить НЕ УДАЛОСЬ, это не «живая» и
// не «терминальная» — это «не знаю», и продолжать по «не знаю» на пути, который
// создаёт ресурсы, нельзя: отказ от исполнения безопасен (строка остаётся
// незавершённой, её доберёт реконсайлер), исполнение — нет.
//
// Отдельно проверяется цена: несколько попыток подтверждения, чтобы одиночный
// сетевой сбой не отменял работу, и наблюдаемость обоих исходов — «ноль отказов
// за всю жизнь контроля» обязано быть отличимо от «контроль не работал».

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/corelib/operations"
)

// flakyClaimRepo — Repo с executionClaimer, у которого подтверждение живости
// отказывает первые failFirst раз, а затем отвечает штатно. Моделирует
// НЕ-дедлайновый транзиентный сбой (обрыв соединения, рестарт/переключение БД):
// такой отказ приходит сразу, а контекст исполнения остаётся живым — именно в
// этой асимметрии и живёт дефект.
type flakyClaimRepo struct {
	mu         sync.Mutex
	live       bool
	failFirst  int
	failAlways bool
	claimCalls int
	done       map[string]*anypb.Any
	errored    map[string]*rpcstatus.Status
}

func newFlakyClaimRepo(live bool) *flakyClaimRepo {
	return &flakyClaimRepo{live: live, done: map[string]*anypb.Any{}, errored: map[string]*rpcstatus.Status{}}
}

func (r *flakyClaimRepo) Create(context.Context, operations.Operation) error { return nil }
func (r *flakyClaimRepo) CreateWithPrincipal(context.Context, operations.Operation, operations.Principal) error {
	return nil
}

func (r *flakyClaimRepo) Get(context.Context, string) (*operations.Operation, error) {
	return nil, operations.ErrNotFound
}

func (r *flakyClaimRepo) List(context.Context, operations.ListFilter) ([]operations.Operation, string, error) {
	return nil, "", nil
}

func (r *flakyClaimRepo) MarkDone(_ context.Context, id string, resp *anypb.Any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done[id] = resp
	return nil
}

func (r *flakyClaimRepo) MarkError(_ context.Context, id string, st *rpcstatus.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errored[id] = st
	return nil
}

func (r *flakyClaimRepo) Cancel(context.Context, string) error { return nil }

func (r *flakyClaimRepo) ClaimForExecution(_ context.Context, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimCalls++
	if r.failAlways || r.claimCalls <= r.failFirst {
		return false, errors.New("claim transient: connection reset by peer")
	}
	return r.live, nil
}

func (r *flakyClaimRepo) claimCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claimCalls
}

func (r *flakyClaimRepo) terminalCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.done) + len(r.errored)
}

// TestWorker_DoesNotRunFn_WhenLivenessCannotBeConfirmed — несущая проба:
// подтверждение не удаётся ни разу, и доменная функция НЕ исполняется.
//
// Утверждение наблюдаемое: не «вернулась ошибка», а «функция, создающая ресурс,
// не была вызвана» и «терминальная запись не появилась» — строка остаётся
// незавершённой и достаётся реконсайлеру.
func TestWorker_DoesNotRunFn_WhenLivenessCannotBeConfirmed(t *testing.T) {
	repo := newFlakyClaimRepo(true)
	repo.failAlways = true
	rec := operations.NewMemRecorder()
	w := fastTerminalWorker(t, rec)

	var fnCalled atomic.Bool
	resp := mustAny(t, wrapperspb.String("must-not-happen"))
	operations.RunWithWorker(w, context.Background(), repo, "op-claim-unconfirmed", func(context.Context) (*anypb.Any, error) {
		fnCalled.Store(true)
		return resp, nil
	})

	waitFor(t, 5*time.Second, func() bool { return rec.ExecutionClaimFailures() >= 1 })
	// Дать worker'у время исполнить fn, если бы он её не пропустил.
	time.Sleep(200 * time.Millisecond)

	assert.False(t, fnCalled.Load(),
		"доменная функция исполнена без подтверждения живости: ресурс может быть создан поверх "+
			"уже разрешённой операции — клиент видит отказ, ресурс молча существует")
	assert.Equal(t, 0, repo.terminalCount(),
		"неподтверждённая операция не должна получать терминальную запись — её добирает реконсайлер")
	assert.GreaterOrEqual(t, repo.claimCount(), 2,
		"подтверждение обязано быть повторено: одиночный сетевой сбой не должен отменять работу")
	assert.GreaterOrEqual(t, rec.ExecutionClaimFailures(), float64(1),
		"отказ от исполнения обязан быть наблюдаем: «ноль отказов за всю жизнь контроля» должно "+
			"быть отличимо от «контроль не работал»")
}

// TestWorker_RunsFn_WhenClaimRecoversAfterTransientFailure — законная половина:
// одиночный транзиентный сбой подтверждения не отменяет работу. Без неё «фикс»
// свёлся бы к отказу при любом чихе сети.
func TestWorker_RunsFn_WhenClaimRecoversAfterTransientFailure(t *testing.T) {
	repo := newFlakyClaimRepo(true)
	repo.failFirst = 2 // первые две попытки срываются, третья отвечает
	rec := operations.NewMemRecorder()
	w := fastTerminalWorker(t, rec)

	var fnCalled atomic.Bool
	resp := mustAny(t, wrapperspb.String("recovered"))
	operations.RunWithWorker(w, context.Background(), repo, "op-claim-recovers", func(context.Context) (*anypb.Any, error) {
		fnCalled.Store(true)
		return resp, nil
	})

	waitFor(t, 5*time.Second, func() bool { return repo.terminalCount() >= 1 })
	assert.True(t, fnCalled.Load(),
		"после успешного повтора подтверждения работа обязана исполниться")
	assert.GreaterOrEqual(t, rec.ExecutionClaimRetries(), float64(2),
		"повторы подтверждения обязаны быть наблюдаемы")
	assert.Equal(t, float64(0), rec.ExecutionClaimFailures(),
		"восстановившееся подтверждение — не отказ контроля")
}

// TestWorker_TerminalWriteOnAlreadyResolvedRowIsObservable — второй безмолвный
// участок того же пути: если работа всё же выполнилась, а строка к тому моменту
// уже разрешена, терминальная запись не проходит по сравнению-и-замене и
// проглатывается как штатный исход. Ровно этот случай и означает «ресурс создан
// поверх разрешённой операции», поэтому он обязан быть посчитан.
func TestWorker_TerminalWriteOnAlreadyResolvedRowIsObservable(t *testing.T) {
	repo := newAlreadyDoneRepo()
	rec := operations.NewMemRecorder()
	w := fastTerminalWorker(t, rec)

	resp := mustAny(t, wrapperspb.String("late"))
	operations.RunWithWorker(w, context.Background(), repo, "op-late-terminal", func(context.Context) (*anypb.Any, error) {
		return resp, nil
	})

	waitFor(t, 5*time.Second, func() bool { return rec.TerminalWriteAlreadyResolved("MarkDone") >= 1 })
	assert.GreaterOrEqual(t, rec.TerminalWriteAlreadyResolved("MarkDone"), float64(1),
		"дозапись терминала в уже разрешённую строку обязана быть посчитана: иначе единственный "+
			"сигнал о ресурсе, созданном поверх разрешённой операции, невидим")
}

// alreadyDoneRepo — строка живая на подтверждении, но к моменту терминальной
// записи уже разрешена (гонка с отменой/реконсайлером).
type alreadyDoneRepo struct{ mu sync.Mutex }

func newAlreadyDoneRepo() *alreadyDoneRepo { return &alreadyDoneRepo{} }

func (r *alreadyDoneRepo) Create(context.Context, operations.Operation) error { return nil }
func (r *alreadyDoneRepo) CreateWithPrincipal(context.Context, operations.Operation, operations.Principal) error {
	return nil
}

func (r *alreadyDoneRepo) Get(context.Context, string) (*operations.Operation, error) {
	return nil, operations.ErrNotFound
}

func (r *alreadyDoneRepo) List(context.Context, operations.ListFilter) ([]operations.Operation, string, error) {
	return nil, "", nil
}

func (r *alreadyDoneRepo) MarkDone(context.Context, string, *anypb.Any) error {
	return operations.ErrAlreadyDone
}

func (r *alreadyDoneRepo) MarkError(context.Context, string, *rpcstatus.Status) error {
	return operations.ErrAlreadyDone
}

func (r *alreadyDoneRepo) Cancel(context.Context, string) error { return nil }

func (r *alreadyDoneRepo) ClaimForExecution(context.Context, string) (bool, error) {
	return true, nil
}
