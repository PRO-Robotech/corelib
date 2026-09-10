// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Пробы арендаторской полосы операции — ОДНИ на дерево.
//
// До сведения (#1369) те же свойства утверждались семью наборами проб, по одному
// на сервис; их объединение давало 39 имён при примерно семнадцати РАЗЛИЧНЫХ
// свойствах. Здесь перечислены свойства, а не имена: дублировать проверку семь
// раз значило бы доказывать одно и то же семь раз и всё равно упустить восьмой
// сервис — так и вышло с неатомарной отменой.
package operationspb

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	operationpb "github.com/PRO-Robotech/corelib/api/kacho/cloud/operation"
	"github.com/PRO-Robotech/corelib/operations"
)

// ─── дублёр репозитория ──────────────────────────────────────────────────────

// fakeRepo ЗАПИСЫВАЕТ вызовы: предмет части проб — не «что вернули», а «какой
// глагол позвали». Несуженные Get/Cancel обязаны не звучать вовсе на
// арендаторском пути, и доказать это можно только счётчиком.
type fakeRepo struct {
	owned bool // реализует ли ownership-апгрейд

	op  *operations.Operation
	err error

	unscopedGet    int
	unscopedCancel int
	getOwned       int
	cancelOwned    int
	lastOwner      operations.Owner
}

func (f *fakeRepo) Create(context.Context, operations.Operation) error { return nil }
func (f *fakeRepo) CreateWithPrincipal(context.Context, operations.Operation, operations.Principal) error {
	return nil
}
func (f *fakeRepo) Get(context.Context, string) (*operations.Operation, error) {
	f.unscopedGet++
	return f.op, f.err
}
func (f *fakeRepo) List(context.Context, operations.ListFilter) ([]operations.Operation, string, error) {
	return nil, "", nil
}
func (f *fakeRepo) MarkDone(context.Context, string, *anypb.Any) error      { return nil }
func (f *fakeRepo) MarkError(context.Context, string, *status.Status) error { return nil }
func (f *fakeRepo) Cancel(context.Context, string) error {
	f.unscopedCancel++
	return f.err
}

type ownedRepo struct{ *fakeRepo }

func (o ownedRepo) GetOwned(_ context.Context, _ string, owner operations.Owner) (*operations.Operation, error) {
	o.getOwned++
	o.lastOwner = owner
	return o.op, o.err
}

func (o ownedRepo) CancelOwned(_ context.Context, _ string, owner operations.Owner) (*operations.Operation, error) {
	o.cancelOwned++
	o.lastOwner = owner
	return o.op, o.err
}

func (o ownedRepo) ListOwned(context.Context, operations.ListFilter, operations.Owner) ([]operations.Operation, string, error) {
	return nil, "", nil
}

func sample() *operations.Operation {
	return &operations.Operation{
		ID:          "op-1",
		Description: "проба",
		CreatedAt:   time.Date(2026, 8, 27, 10, 0, 0, 123456000, time.UTC),
		ModifiedAt:  time.Date(2026, 8, 27, 10, 0, 5, 987654000, time.UTC),
		CreatedBy:   "usr-1",
		Done:        true,
		Principal:   operations.Principal{Type: "user", ID: "usr-1", DisplayName: "Пользователь"},
	}
}

func ownerCtx() context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: "usr-1", DisplayName: "Пользователь"})
}

func newOwned(op *operations.Operation, err error) (*Handler, *fakeRepo) {
	f := &fakeRepo{owned: true, op: op, err: err}
	return NewHandler(ownedRepo{f}), f
}

// ─── формат запроса ──────────────────────────────────────────────────────────

func TestGetRejectsEmptyID(t *testing.T) {
	h, _ := newOwned(sample(), nil)
	_, err := h.Get(ownerCtx(), &operationpb.GetOperationRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

func TestCancelRejectsEmptyID(t *testing.T) {
	h, _ := newOwned(sample(), nil)
	_, err := h.Cancel(ownerCtx(), &operationpb.CancelOperationRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

// ─── владение: анонимный не владеет ничем ────────────────────────────────────

// Предмет: `PrincipalFromContext` на пустом ctx отдаёт СИСТЕМНУЮ личность,
// которая совпадает с предикатом владения на каждой системно записанной строке.
// Если бы ключ выводился ею, анонимный запрос стал бы владельцем всего.
func TestAnonymousGetsNotFoundAndNeverReachesRepo(t *testing.T) {
	for _, verb := range []string{"get", "cancel"} {
		t.Run(verb, func(t *testing.T) {
			h, f := newOwned(sample(), nil)
			var err error
			if verb == "get" {
				_, err = h.Get(context.Background(), &operationpb.GetOperationRequest{OperationId: "op-1"})
			} else {
				_, err = h.Cancel(context.Background(), &operationpb.CancelOperationRequest{OperationId: "op-1"})
			}
			requireCode(t, err, codes.NotFound)
			requireMessage(t, err, "operation op-1 not found")
			if f.getOwned+f.cancelOwned+f.unscopedGet+f.unscopedCancel != 0 {
				t.Fatalf("анонимный запрос дошёл до репозитория: %+v", f)
			}
		})
	}
}

// ─── чужая операция неотличима от несуществующей ─────────────────────────────

// `security.md` §Hardening #6: различимый текст есть existence-oracle. Утверждаем
// СООБЩЕНИЕ, а не только код: рефактор, вернувший «operation not found for
// principal …», оставил бы код тем же и вернул бы оракул.
func TestForeignAndMissingAreByteIdentical(t *testing.T) {
	foreign, _ := newOwned(nil, operations.ErrNotFound)
	_, errForeign := foreign.Get(ownerCtx(), &operationpb.GetOperationRequest{OperationId: "op-9"})

	anon, _ := newOwned(sample(), nil)
	_, errAnon := anon.Get(context.Background(), &operationpb.GetOperationRequest{OperationId: "op-9"})

	if grpcstatus.Convert(errForeign).Message() != grpcstatus.Convert(errAnon).Message() {
		t.Fatalf("тексты отличимы — это existence-oracle:\n  чужая: %q\n  анонимный: %q",
			grpcstatus.Convert(errForeign).Message(), grpcstatus.Convert(errAnon).Message())
	}
	requireMessage(t, errForeign, "operation op-9 not found")
}

// ─── положительный путь ──────────────────────────────────────────────────────

func TestOwnerIsServedAndOwnerKeyComesFromContext(t *testing.T) {
	h, f := newOwned(sample(), nil)
	got, err := h.Get(ownerCtx(), &operationpb.GetOperationRequest{OperationId: "op-1"})
	if err != nil {
		t.Fatalf("владельцу отказано: %v", err)
	}
	if got.GetId() != "op-1" {
		t.Fatalf("вернулась не та операция: %q", got.GetId())
	}
	if f.getOwned != 1 || f.unscopedGet != 0 {
		t.Fatalf("чтение пошло не через ownership-порт: %+v", f)
	}
	if f.lastOwner.PrincipalID != "usr-1" || f.lastOwner.PrincipalType != "user" {
		t.Fatalf("ключ владения выведен не из ctx: %+v", f.lastOwner)
	}
}

// ─── отмена АТОМАРНА ─────────────────────────────────────────────────────────

// Несущая проба этой задачи. До сведения одна из копий отменяла так: прочитать
// своё (`GetOwned`) → отменить НЕСУЖЕННО (`Cancel`) → перечитать несуженно
// (`Get`). Предикат владения уходил из МУТАЦИИ совсем: несуженный глагол о
// владельце не знает. Сказать «между проверкой и мутацией не держалось ничего»
// было бы неверно — держалась неизменяемость колонок принципала, — но инвариант,
// выраженный software-проверкой вместо оператора БД, переживает первое же
// изменение того, что делало его верным (ban #10).
func TestCancelIsAtomicAndNeverTouchesUnscopedVerbs(t *testing.T) {
	h, f := newOwned(sample(), nil)
	if _, err := h.Cancel(ownerCtx(), &operationpb.CancelOperationRequest{OperationId: "op-1"}); err != nil {
		t.Fatalf("владельцу отказано в отмене: %v", err)
	}
	if f.cancelOwned != 1 {
		t.Fatalf("отмена пошла не через CancelOwned: %+v", f)
	}
	if f.unscopedCancel != 0 || f.unscopedGet != 0 {
		t.Fatalf("на арендаторском пути позван НЕСУЖЕННЫЙ глагол (check-then-act, ban #10): "+
			"Cancel=%d Get=%d", f.unscopedCancel, f.unscopedGet)
	}
	if f.getOwned != 0 {
		t.Fatalf("отмена сделала лишнее чтение до мутации — это и есть форма check-then-act: %+v", f)
	}
}

// ЧТО ЭТА ПРОБА УТВЕРЖДАЕТ — И ЧЕГО НЕ УТВЕРЖДАЕТ.
//
// Она закрепляет ОТОБРАЖЕНИЕ обработчика: успех `CancelOwned` на уже отменённой
// операции доезжает до вызывающего как операция с исходом отказа, а не как
// ошибка. Саму ИДЕМПОТЕНТНОСТЬ она не проверяет и проверить не может: дублёр
// состояния не моделирует, а идемпотентность живёт в SQL — ветвь
// `existing.Error.GetCode() == 1` в `pkg/operations/repo.go`. Убери её, и эта
// проба останется зелёной.
//
// Идемпотентность закреплена там, где она детерминирована и где есть настоящая
// БД: `pkg/operations/ownership_integration_test.go` —
// `TestOwnership_CancelOwned_Idempotent_ReCancel`. Имя ниже названо по тому, что
// проба делает, а не по тому, ради чего заведена: заголовок шире тела — это
// вакуумное утверждение, и оно зеленеет при откате того, что объявляет.
//
// До сведения один из семи доменов звал НЕСУЖЕННЫЙ `repo.Cancel`, а тот отдаёт
// `ErrAlreadyDone` на ЛЮБОЙ завершённой операции — включая уже отменённую.
// Поэтому повторная отмена там отвечала `FAILED_PRECONDITION`, а у шести
// остальных — успехом: их `CancelOwned` на уже отменённой возвращает саму
// операцию.
//
// Вместе со сведением снята проба того домена, утверждавшая СТАРЫЙ контракт
// (восемь конкурентных отмен: один успех, семь отказов). Снять её было верно —
// но снятие красной пробы есть способ, которым «единственная смена поведения»
// переживает обзор незамеченной, поэтому НОВЫЙ контракт закреплён здесь явно.
func TestCancelReturnsTheOperationWhenRepoAcceptsRecancel(t *testing.T) {
	cancelled := sample()
	cancelled.Error = &status.Status{Code: 1, Message: "operation cancelled"}

	h, f := newOwned(cancelled, nil)
	got, err := h.Cancel(ownerCtx(), &operationpb.CancelOperationRequest{OperationId: "op-1"})
	if err != nil {
		t.Fatalf("повторная отмена уже отменённой операции отказала: %v", err)
	}
	if got.GetId() != "op-1" {
		t.Fatalf("вернулась не та операция: %q", got.GetId())
	}
	if _, isErr := got.GetResult().(*operationpb.Operation_Error); !isErr {
		t.Fatal("операция вернулась без исхода отказа — клиент не увидит, что она отменена")
	}
	if f.cancelOwned != 1 {
		t.Fatalf("отмена пошла мимо ownership-порта: %+v", f)
	}
}

func TestCancelOfCompletedOperationIsFailedPrecondition(t *testing.T) {
	h, _ := newOwned(nil, operations.ErrAlreadyDone)
	_, err := h.Cancel(ownerCtx(), &operationpb.CancelOperationRequest{OperationId: "op-1"})
	requireCode(t, err, codes.FailedPrecondition)
	requireMessage(t, err, "operation op-1 already completed")
}

// ─── отказы не текут наружу ──────────────────────────────────────────────────

// `security.md` §Hardening #1: дефолтная ветка маппера отдаёт ФИКСИРОВАННЫЙ
// текст. Утверждаем сообщение, а не только код: эхо `err.Error()` выносит наружу
// текст драйвера с хостом, портом и именем БД.
func TestRepoFailureDoesNotLeak(t *testing.T) {
	leaky := errors.New("pgx: dial tcp 10.0.0.5:5432: connection refused (db=kacho_vpc user=kacho)")
	for _, tc := range []struct{ verb, want string }{
		{"get", "operation get failed"},
		{"cancel", "operation cancel failed"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			h, _ := newOwned(nil, leaky)
			var err error
			if tc.verb == "get" {
				_, err = h.Get(ownerCtx(), &operationpb.GetOperationRequest{OperationId: "op-1"})
			} else {
				_, err = h.Cancel(ownerCtx(), &operationpb.CancelOperationRequest{OperationId: "op-1"})
			}
			requireCode(t, err, codes.Internal)
			requireMessage(t, err, tc.want)
			if msg := grpcstatus.Convert(err).Message(); strings.Contains(msg, "10.0.0.5") || strings.Contains(msg, "pgx") {
				t.Fatalf("наружу утёк текст драйвера: %q", msg)
			}
		})
	}
}

// ─── провязка без ownership-порта fail-closed ────────────────────────────────

// Репозиторий без ownership-апгрейда — ошибка провязки. Откат на несуженный путь
// был бы молчаливым обходом владения, поэтому исход — отказ.
func TestRepoWithoutOwnershipUpgradeFailsClosed(t *testing.T) {
	h := NewHandler(&fakeRepo{op: sample()})
	_, errGet := h.Get(ownerCtx(), &operationpb.GetOperationRequest{OperationId: "op-1"})
	requireCode(t, errGet, codes.Internal)
	_, errCancel := h.Cancel(ownerCtx(), &operationpb.CancelOperationRequest{OperationId: "op-1"})
	requireCode(t, errCancel, codes.Internal)
}

// ─── преобразователь ─────────────────────────────────────────────────────────

// Вход намеренно НЕ круглый: сравнение с заранее усечённым образцом зеленеет и
// тогда, когда усечения нет, а вход случайно оказался круглым.
//
// Положительный контроль обязателен: без него «усечено» неотличимо от
// «обнулено». Эта мысль перенесена сюда из снятой пробы nlb, чей предмет уехал
// в общий слой, — снимая пробу вместе с предметом, её утверждение переносят, а
// не теряют.
func TestToProtoCarriesEveryFieldAndTruncatesToSeconds(t *testing.T) {
	got := ToProto(sample())
	if got.GetCreatedAt().GetNanos() != 0 || got.GetModifiedAt().GetNanos() != 0 {
		t.Fatalf("доли секунды утекли на провод: created=%d modified=%d",
			got.GetCreatedAt().GetNanos(), got.GetModifiedAt().GetNanos())
	}
	if got.GetCreatedAt().AsTime().Second() != 0 || got.GetModifiedAt().AsTime().Second() != 5 {
		t.Fatalf("секундная часть не сохранена — это обнуление, а не усечение: created=%v modified=%v",
			got.GetCreatedAt().AsTime(), got.GetModifiedAt().AsTime())
	}
	if got.GetPrincipalType() != "user" || got.GetPrincipalId() != "usr-1" || got.GetPrincipalDisplayName() != "Пользователь" {
		t.Fatalf("поля принципала потеряны: %+v", got)
	}
	if got.GetCreatedBy() != "usr-1" || !got.GetDone() || got.GetDescription() != "проба" {
		t.Fatalf("поля операции потеряны: %+v", got)
	}
}

// Ветки `Operation.result` — несущая часть контракта: клиент по ним отличает
// успех от отказа. Снятие ОБЕИХ веток оставляло суиту зелёной, пока эта проба не
// была написана: утверждали поля конверта, а не его исход.
func TestToProtoCarriesTheResultBranch(t *testing.T) {
	t.Run("отказ", func(t *testing.T) {
		op := sample()
		op.Error = &status.Status{Code: 1, Message: "operation cancelled"}
		got := ToProto(op)
		errRes, ok := got.GetResult().(*operationpb.Operation_Error)
		if !ok {
			t.Fatalf("исход отказа не доехал до контракта: %T", got.GetResult())
		}
		if errRes.Error.GetCode() != 1 || errRes.Error.GetMessage() != "operation cancelled" {
			t.Fatalf("отказ доехал искажённым: %+v", errRes.Error)
		}
	})

	t.Run("успех", func(t *testing.T) {
		payload, err := anypb.New(&operationpb.Operation{Id: "полезная нагрузка"})
		if err != nil {
			t.Fatalf("подготовка нагрузки: %v", err)
		}
		op := sample()
		op.Response = payload
		got := ToProto(op)
		res, ok := got.GetResult().(*operationpb.Operation_Response)
		if !ok {
			t.Fatalf("исход успеха не доехал до контракта: %T", got.GetResult())
		}
		if res.Response.GetTypeUrl() != payload.GetTypeUrl() {
			t.Fatalf("нагрузка доехала искажённой: %q", res.Response.GetTypeUrl())
		}
	})

	t.Run("отказ сильнее ответа", func(t *testing.T) {
		payload, _ := anypb.New(&operationpb.Operation{Id: "x"})
		op := sample()
		op.Error = &status.Status{Code: 2}
		op.Response = payload
		if _, ok := ToProto(op).GetResult().(*operationpb.Operation_Error); !ok {
			t.Fatal("при заполненных обоих исходах контракт обязан нести ОТКАЗ — " +
				"иначе клиент прочитает отказавшую операцию как успешную")
		}
	})
}

// Метаданные несут id создаваемого ресурса — по ним клиент узнаёт, что создалось.
func TestToProtoCarriesMetadata(t *testing.T) {
	payload, err := anypb.New(&operationpb.Operation{Id: "мета"})
	if err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	op := sample()
	op.Metadata = payload
	if got := ToProto(op).GetMetadata(); got.GetTypeUrl() != payload.GetTypeUrl() {
		t.Fatalf("метаданные потеряны: %+v", got)
	}
}

// Пустой момент времени означает «не задано» и на провод не идёт вовсе: иначе
// клиент читает `0001-01-01T00:00:00Z` как настоящую отметку.
func TestToProtoOmitsUnsetTimestamps(t *testing.T) {
	op := sample()
	op.ModifiedAt = time.Time{}
	got := ToProto(op)
	if got.GetModifiedAt() != nil {
		t.Fatalf("пустой момент уехал на провод как %v", got.GetModifiedAt().AsTime())
	}
	if got.GetCreatedAt() == nil {
		t.Fatal("заполненный момент пропал — охрана срабатывает не на том поле")
	}
}

func TestToProtoOfNilIsNil(t *testing.T) {
	if ToProto(nil) != nil {
		t.Fatal("пустое значение обязано давать пустое, а не панику или пустую структуру")
	}
}

// ─── помощники ───────────────────────────────────────────────────────────────

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := grpcstatus.Code(err); got != want {
		t.Fatalf("код %s, ожидался %s (ошибка: %v)", got, want, err)
	}
}

func requireMessage(t *testing.T, err error, want string) {
	t.Helper()
	if got := grpcstatus.Convert(err).Message(); got != want {
		t.Fatalf("текст %q, ожидался %q — тон отказа часть контракта", got, want)
	}
}
