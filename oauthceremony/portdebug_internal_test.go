// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// portdebug_internal_test.go — подробности (Debug) отказа порта называют вызов
// и не оставляют висящего разделителя.
//
// Разборы отказа порта (fromPort, closedPortFailure, contractBreach) приписывают
// к подробностям имя вызова. Подробность, приехавшая от порта, бывает пустой:
// часовой пакета её не несёт вовсе, а чужая ошибка вправе иметь пустой текст.
// Склейка «вызов: подробность» дала бы тогда `"<вызов>: "` — строку, которая
// обещает продолжение и не несёт его, и журнал, отбирающий по точному тексту,
// видел бы два написания одного отказа.
//
// Файл внутренний: у разборов нет экспортированного адреса, а путь через
// церемонию дошёл бы не до каждой их ветви.
package oauthceremony

import (
	"context"
	"errors"
	"testing"
)

// silentErr — чужая ошибка с пустым текстом, обёртывающая cause: форма, в
// которой подробность порта пуста, хотя отказ вида «срок» или «отмена».
type silentErr struct{ cause error }

func (e silentErr) Error() string { return "" }
func (e silentErr) Unwrap() error { return e.cause }

// TestPortDebugNamesTheCallWithoutADanglingSeparator — каждая ветвь каждого
// разбора: пустая подробность даёт ровно имя вызова, непустая — «вызов:
// подробность». Непустой близнец отличается от пустого ровно текстом
// подробности, и без него проба была бы зелёной у разбора, выбрасывающего
// подробность всегда.
func TestPortDebugNamesTheCallWithoutADanglingSeparator(t *testing.T) {
	const op = "Ports.Probe.Call"
	enriched := failf(CodeGrantNotFound, nil, "", "", "row 7")
	cases := []struct {
		name string
		got  *ProtocolError
		want string
	}{
		{"fromPort: часовой пакета", fromPort(op, ErrGrantNotFound), op},
		{"fromPort: наш отказ с подробностью", fromPort(op, enriched), op + ": row 7"},
		{"fromPort: срок с пустым текстом", fromPort(op, silentErr{context.DeadlineExceeded}), op},
		{"fromPort: срок", fromPort(op, context.DeadlineExceeded), op + ": " + context.DeadlineExceeded.Error()},
		{"fromPort: отмена с пустым текстом", fromPort(op, silentErr{context.Canceled}), op},
		{"fromPort: отмена", fromPort(op, context.Canceled), op + ": " + context.Canceled.Error()},
		{"fromPort: чужая ошибка с пустым текстом", fromPort(op, errors.New("")), op},
		{"fromPort: чужая ошибка", fromPort(op, errors.New("disk full")), op + ": disk full"},
		{"closedPortFailure: срок с пустым текстом", closedPortFailure(op, silentErr{context.DeadlineExceeded}), op},
		{"closedPortFailure: срок", closedPortFailure(op, context.DeadlineExceeded), op + ": " + context.DeadlineExceeded.Error()},
		{"closedPortFailure: отмена с пустым текстом", closedPortFailure(op, silentErr{context.Canceled}), op},
		{"closedPortFailure: отмена", closedPortFailure(op, context.Canceled), op + ": " + context.Canceled.Error()},
		{"closedPortFailure: чужая ошибка с пустым текстом", closedPortFailure(op, errors.New("")), op},
		{"closedPortFailure: чужая ошибка", closedPortFailure(op, errors.New("disk full")), op + ": disk full"},
		{"contractBreach: без причины", contractBreach(op, ""), op},
		{"contractBreach: с причиной", contractBreach(op, "zero rows"), op + ": zero rows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Debug != tc.want {
				t.Errorf("подробности %q, ожидалось %q", tc.got.Debug, tc.want)
			}
		})
	}
	t.Logf("ветвей разбора осмотрено: %d", len(cases))
}

// TestPortDebugKeepsTheCaseOfTheBranch — близнец по случаю: пустая подробность
// не меняет вердикта ветви. Без этой половины разбор, отвечающий на пустой
// текст «ошибкой сервера», прошёл бы пробу подробностей.
func TestPortDebugKeepsTheCaseOfTheBranch(t *testing.T) {
	const op = "Ports.Probe.Call"
	cases := []struct {
		got  *ProtocolError
		want FailureCode
	}{
		{fromPort(op, ErrGrantNotFound), CodeGrantNotFound},
		{fromPort(op, silentErr{context.DeadlineExceeded}), CodePortDeadline},
		{fromPort(op, silentErr{context.Canceled}), CodePortCanceled},
		{fromPort(op, errors.New("")), CodeServerError},
		{closedPortFailure(op, silentErr{context.DeadlineExceeded}), CodePortDeadline},
		{closedPortFailure(op, silentErr{context.Canceled}), CodePortCanceled},
		{closedPortFailure(op, errors.New("")), CodeServerError},
	}
	for i, tc := range cases {
		if tc.got.Code != tc.want {
			t.Errorf("ветвь %d: случай %v, ожидался %v", i, tc.got.Code, tc.want)
		}
	}
}
