// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listnarrow

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AllowedOnObject — ОДИНОЧНАЯ дверь того же механизма: «можно ли этому вызывающему
// ЭТОТ объект хотя бы по одному из названных отношений».
//
// Живёт здесь, а не отдельным пакетом, потому что делит с сужателем ВСЁ, кроме
// формы вопроса: тот же клиент, тот же срок вызова, то же отображение отказа соседа,
// та же полярность на безымянном вызывающем и на непровязанной модели. Вынести её
// значило бы завести второй экземпляр этих решений — и разойтись они смогли бы
// молча, ровно как разошлись четыре копии сужателя.
//
// Потребитель сегодня ОДИН (storage, привязка и отвязка тома: право видеть и менять
// привязки вытекает из права на МАШИНУ, а не на каждый том). Это названо прямо,
// чтобы следующий читатель не принял одиночность за недосмотр и не начал строить
// вокруг неё обобщение.
//
// Отношения передаются ЯВНО и не берутся из предиката страницы: предмет вопроса
// другой — не «попадает ли строка в выдачу», а «вправе ли вызывающий распорядиться
// объектом», и совпадение этих наборов было бы случайным.
//
// Fail-closed по всем линиям: нет личности → отказ по личности; нет модели → отказ
// по посадке; сосед не ответил → недоступность, никогда «да».
func AllowedOnObject(
	ctx context.Context,
	n *Narrower,
	resourceType, action string,
	relations []string,
	id string,
) (bool, error) {
	subject, err := gate(ctx, n)
	if err != nil {
		return false, err
	}
	if n.cfg.Breakglass {
		n.breakglassPass(nil, subject, resourceType, action)
		return true, nil
	}
	if resourceType == "" || action == "" || id == "" || len(relations) == 0 {
		return false, status.Error(codes.Internal,
			"object gate: resourceType, action, id and at least one relation required")
	}

	checks := make([]Check, 0, len(relations))
	for _, relation := range relations {
		checks = append(checks, Check{
			Subject:          subject,
			ResourceType:     resourceType,
			ResourceID:       id,
			Action:           action,
			RequiredRelation: relation,
		})
	}
	verdicts, cerr := n.askOnce(ctx, checks)
	if cerr != nil {
		return false, objectGateErr(cerr, n)
	}
	// Ответ не той длины выровнять с вопросами нельзя, и решение, принятое по
	// смещённому ответу, неверно так, что вызывающий этого не обнаружит.
	if len(verdicts) != len(checks) {
		return false, status.Errorf(codes.Unavailable,
			"object gate: AuthorizeService.BatchCheck returned %d responses for %d checks",
			len(verdicts), len(checks))
	}
	for _, allowed := range verdicts {
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

func objectGateErr(err error, n *Narrower) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Errorf(codes.Unavailable,
			"object gate: AuthorizeService.BatchCheck deadline exceeded (per-call %s)", n.cfg.Timeout)
	}
	if s, ok := status.FromError(err); ok && s.Code() != codes.OK && s.Code() != codes.Unknown {
		return status.Errorf(codes.Unavailable, "object gate: AuthorizeService.BatchCheck %s: %s",
			s.Code(), s.Message())
	}
	return status.Errorf(codes.Unavailable, "object gate: AuthorizeService.BatchCheck: %v", err)
}
