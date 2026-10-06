// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	coreerrors "github.com/PRO-Robotech/corelib/errors"
)

// Машинные признаки отказов Ack (Р8).
const (
	// ReasonLeaseLost — аренда утрачена: токен не тот, аренда истекла, исход
	// записан другим токеном, уборщиком или Supersede.
	ReasonLeaseLost = "LEASE_LOST"
	// ReasonOutcomeAlreadyRecorded — тем же токеном записан другой исход.
	ReasonOutcomeAlreadyRecorded = "OUTCOME_ALREADY_RECORDED"
)

// uuidForm — каноническая запись UUID: так её отдаёт Claim.
var uuidForm = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Ack записывает исход арендованной строки (замысел NTF-1 З9) — тонкий
// вызывающий ядра: границы — validateAck до SQL; запись и классификация нуля
// строк — recordOutcome в своей транзакции; следствия записи — только в
// ветке, изменившей строку.
func (s *Server) Ack(ctx context.Context, req *notifyv1.AckRequest) (*notifyv1.AckResponse, error) {
	a := ackFromWire(req)
	if v := validateAck(a); v != nil {
		if v.badID {
			return nil, coreerrors.ReasonInvalidResourceID.Errf(coreerrors.PeerRef{
				Service: s.module, ResourceType: "notification",
			}, "invalid notification id '%s'", a.id)
		}
		return nil, fieldError(v.field, v.rule)
	}
	res, err := s.e.ack(ctx, a)
	if err != nil {
		return nil, s.internalError(ctx, "ack", err)
	}
	switch res.verdict {
	case ackRecorded, ackRepeated:
		return &notifyv1.AckResponse{}, nil
	case ackNotFound:
		return nil, coreerrors.ReasonResourceNotFound.Errf(coreerrors.PeerRef{
			Service: s.module, ResourceType: "notification", ResourceID: a.id,
		}, "Notification %s not found", a.id)
	case ackAlreadyRecorded:
		return nil, s.reasonError(ReasonOutcomeAlreadyRecorded, a.id,
			fmt.Sprintf("Notification %s outcome is already recorded", a.id))
	case ackLeaseLost:
	}
	return nil, s.reasonError(ReasonLeaseLost, a.id, fmt.Sprintf("Notification %s lease is lost", a.id))
}

// ackFromWire — запись исхода словами ленты. Вид без значения — пустой (его
// отвергает validateAck); неизвестный номер вида — его написание контрактом
// («99»), которого нет в ackReasons.
func ackFromWire(req *notifyv1.AckRequest) ackInput {
	a := ackInput{id: req.GetId(), token: req.GetLeaseToken()}
	o := req.GetOutcome()
	if o.GetKind() != notifyv1.OutcomeKind_OUTCOME_UNSPECIFIED {
		a.outcome.Kind = Kind(strings.ToLower(o.GetKind().String()))
	}
	if o.GetReason() != notifyv1.OutcomeReason_OUTCOME_REASON_UNSPECIFIED {
		a.outcome.Reason = Reason(strings.ToLower(o.GetReason().String()))
	}
	if d := req.GetDeferFor(); d != nil {
		a.deferSet = true
		a.deferMalformed = d.CheckValid() != nil
		a.deferFor = d.AsDuration()
	}
	return a
}
