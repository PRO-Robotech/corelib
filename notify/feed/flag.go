// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Enabled — флаг источника KACHO_<SVC>_NOTIFICATIONS_ENABLED, разобранный
// корнем один раз (З12). Закрытый тип: строит его только ParseEnabled, и
// нулевое значение («не разобрано») NewSource отвергает — false из незаданного
// неотличим от выключенного модуля.
type Enabled struct {
	on, set bool
}

// On — значение флага.
func (e Enabled) On() bool { return e.on }

// Set — разобрано ли значение ParseEnabled.
func (e Enabled) Set() bool { return e.set }

// ParseEnabled разбирает флаг: ровно "true" или "false". strconv.ParseBool не
// годится — он принимает 1, t, TRUE. Незаданная переменная, пустая строка и
// иное написание — отказ старта с именем переменной (CX1-20, NTF1-N08).
func ParseEnabled(name string, lookup func(string) (string, bool)) (Enabled, error) {
	raw, ok := lookup(name)
	if !ok {
		return Enabled{}, fmt.Errorf("feed: переменная %s не задана — ожидается true или false", name)
	}
	switch raw {
	case "true":
		return Enabled{on: true, set: true}, nil
	case "false":
		return Enabled{on: false, set: true}, nil
	}
	return Enabled{}, fmt.Errorf("feed: переменная %s: значение вне {true, false}", name)
}

// DeliveryNotConfiguredReason — машинный признак отказа «доставка не настроена».
const DeliveryNotConfiguredReason = "NOTIFICATION_DELIVERY_NOT_CONFIGURED"

// DeliveryNotConfiguredStatus — ЕДИНЫЙ статус отказа глагола при выключенном
// флаге (NTF1-N06): FAILED_PRECONDITION, текст ErrDeliveryNotConfigured,
// ErrorInfo с признаком. Одинаков для любого адреса.
func DeliveryNotConfiguredStatus() *status.Status {
	st := status.New(codes.FailedPrecondition, ErrDeliveryNotConfigured.Error())
	withInfo, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: DeliveryNotConfiguredReason,
		Domain: "notify.kacho.cloud",
	})
	if err != nil {
		// Деталь не прикрепилась — код и текст важнее детали.
		return st
	}
	return withInfo
}
