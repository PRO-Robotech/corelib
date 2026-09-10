// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operationspb

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	operationpb "github.com/PRO-Robotech/corelib/api/kacho/cloud/operation"
	"github.com/PRO-Robotech/corelib/operations"
)

// Handler — арендаторская реализация `OperationService` для ЛЮБОГО домена.
//
// # Что здесь энфорсится и почему это не может жить у вызывающего
//
// Владелец операции — принципал, её создавший. Без предиката владения caller,
// узнав чужой id, прочитал бы чужой ресурс (`Operation.response` несёт его
// целиком) или отменил бы чужую in-flight мутацию. Предикат уходит в SQL `WHERE`
// (`GetOwned`/`CancelOwned`), а чужой и несуществующий id отдают ОДИНАКОВЫЙ
// `NotFound`: «есть, но не твоя» обязано быть неотличимо от «нет такой».
//
// Правильный вызов складывается из трёх шагов, и пропуск любого возвращает
// исходную дыру: получить ownership-апгрейд репозитория, вывести ключ владения
// ИМЕННО из ctx, и на отсутствие ключа отказать, а не продолжить. Тот же довод,
// что у `operations.ListForCaller`, и та же форма.
//
// Ключ выводится `OwnerFromContext`, а НЕ `PrincipalFromContext`: последний на
// контексте без принципала отдаёт системную личность `{system, bootstrap}`,
// которая совпадает с предикатом владения на КАЖДОЙ системно записанной строке.
// То есть анонимный запрос стал бы владельцем всего.
//
// Admin-обхода предиката здесь НЕТ, и держится это НЕ посадкой.
//
// Прежняя редакция обосновывала отсутствие обхода тем, что `OperationService`
// выставляется только на публичном слушателе. Замер опроверг: geo, storage и
// registry регистрируют обработчик на ОБОИХ слушателях — шесть мест, по два на
// сервис. Довод, верный для одного сервиса, был схлопнут в общий и стал ложным
// для трёх; в файле, который читают семь доменов, это приглашение снять
// предикат на «доверенном» внутреннем (`security.md` §Hardening #5).
//
// Настоящее основание — предикат в SQL: ключ владения выводится из ctx и уходит
// в `WHERE`, поэтому исход не зависит от того, каким слушателем пришёл запрос.
// Прежняя ветка «если админ — снять предикат» читала флаг из КЛИЕНТСКОГО
// заголовка, ретранслированного краем, то есть была не admin-функцией, а живым
// обходом владения.
type Handler struct {
	operationpb.UnimplementedOperationServiceServer

	repo operations.Repo
}

// NewHandler создаёт обработчик. В проде repo — pgRepo, реализующий
// `operations.OwnedOperationRepo`; если не реализует (ошибка провязки),
// ownership-вызовы отвечают INTERNAL — fail-closed, а не молчаливый обход.
func NewHandler(repo operations.Repo) *Handler {
	return &Handler{repo: repo}
}

// Get возвращает операцию по id — только её владельцу.
func (h *Handler) Get(ctx context.Context, req *operationpb.GetOperationRequest) (*operationpb.Operation, error) {
	if req.GetOperationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id required")
	}
	owned, ok := operations.AsOwned(h.repo)
	if !ok {
		return nil, status.Error(codes.Internal, "operation get failed")
	}
	owner, hasOwner := operations.OwnerFromContext(ctx)
	if !hasOwner {
		return nil, notFound(req.GetOperationId())
	}
	op, err := owned.GetOwned(ctx, req.GetOperationId(), owner)
	if err != nil {
		if errors.Is(err, operations.ErrNotFound) {
			return nil, notFound(req.GetOperationId())
		}
		return nil, status.Error(codes.Internal, "operation get failed")
	}
	return ToProto(op), nil
}

// Cancel отменяет операцию — только её владельцу и АТОМАРНО.
//
// Отмена идёт одним `CancelOwned`: предикат владения и смена состояния — один
// оператор, терминальное состояние приходит в RETURNING, поэтому отдельного
// перечитывания после отмены не нужно.
//
// Одна из семи сведённых сюда копий была написана иначе: «прочитать своё
// (`GetOwned`) → отменить НЕСУЖЕННО → перечитать несуженно». Это форма
// check-then-act, запрещённая ban #10, и она снята — но сказать про неё «между
// проверкой и мутацией не держалось ничего» было бы НЕВЕРНО, и здесь это
// говорится прямо, чтобы следующий читатель не переизобрёл довод. Держалась
// НЕИЗМЕНЯЕМОСТЬ: колонки принципала не меняются, поэтому решение о владении не
// устаревало между двумя операторами, и чужую операцию отменить было нельзя ни
// при какой гонке. Снято здесь не окно эксплуатации, а ФОРМА: инвариант,
// выраженный software-проверкой вместо оператора БД, переживает первое же
// изменение того, что делало его верным, — и переживает молча.
func (h *Handler) Cancel(ctx context.Context, req *operationpb.CancelOperationRequest) (*operationpb.Operation, error) {
	if req.GetOperationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id required")
	}
	owned, ok := operations.AsOwned(h.repo)
	if !ok {
		return nil, status.Error(codes.Internal, "operation cancel failed")
	}
	owner, hasOwner := operations.OwnerFromContext(ctx)
	if !hasOwner {
		return nil, notFound(req.GetOperationId())
	}
	op, err := owned.CancelOwned(ctx, req.GetOperationId(), owner)
	if err != nil {
		if errors.Is(err, operations.ErrNotFound) {
			return nil, notFound(req.GetOperationId())
		}
		if errors.Is(err, operations.ErrAlreadyDone) {
			return nil, status.Errorf(codes.FailedPrecondition, "operation %s already completed", req.GetOperationId())
		}
		return nil, status.Error(codes.Internal, "operation cancel failed")
	}
	return ToProto(op), nil
}

// notFound — отказ «нет такой операции» для ВСЕХ семи доменов.
//
// Текст здесь НЕ ЗАПИСАН: он приходит из общего производителя
// `operations.NotFoundStatus`, которого зовёт ещё и край
// (`gateway/internal/opsproxy`) на своей ветке «префикс известен, backend не
// подключён». Обе стороны отвечают на одном адресе, поэтому их тексты обязаны
// совпадать побайтово — иначе по различию отличают «нет доступа» от «не
// существует» (`security.md` §Hardening #6). Двумя записями это держалось до
// #1370 и не удержалось: они разошлись регистром одной буквы.
func notFound(id string) error {
	return operations.NotFoundStatus(id)
}
