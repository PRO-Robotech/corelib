// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	coreerrors "github.com/PRO-Robotech/corelib/errors"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/validate"
)

// Машинные признаки отказов Ack (Р8).
const (
	// ReasonLeaseLost — аренда утрачена: токен не тот, аренда истекла, исход
	// записан другим токеном или уборщиком.
	ReasonLeaseLost = "LEASE_LOST"
	// ReasonOutcomeAlreadyRecorded — тем же токеном записан другой исход.
	ReasonOutcomeAlreadyRecorded = "OUTCOME_ALREADY_RECORDED"
)

// ackTerminalSQL — один записывающий оператор терминального исхода (З9,
// CX1-26 (а)): строка закрывается, секрет стирается, аренда снимается,
// записанная пара и токен сохраняются для классификации повтора.
func ackTerminalSQL(svc string) string {
	return fmt.Sprintf(`UPDATE %s
   SET state = $3, outcome_reason = nullif($4, ''), outcome_at = now(), secret_attrs = NULL,
       recorded_kind = $3, recorded_reason = nullif($4, ''), outcome_token = $2::uuid,
       lease_token = NULL, lease_until = NULL
 WHERE id = $1 AND lease_token = $2::uuid AND lease_until > now() AND state = 'pending'
RETURNING class`, tablename.Of(svc, tablename.Outbox))
}

// ackDeferSQL — один записывающий оператор DEFER (З9, З23): строка остаётся
// pending без аренды, «не раньше» — now() + defer_for.
func ackDeferSQL(svc string) string {
	return fmt.Sprintf(`UPDATE %s
   SET last_defer_reason = $3, not_before = now() + make_interval(secs => $4),
       recorded_kind = 'defer', recorded_reason = $3, outcome_token = $2::uuid,
       lease_token = NULL, lease_until = NULL
 WHERE id = $1 AND lease_token = $2::uuid AND lease_until > now() AND state = 'pending'
RETURNING class`, tablename.Of(svc, tablename.Outbox))
}

// ackRecordedSQL — одно чтение при нуле строк записи (CX1-26 (б)).
func ackRecordedSQL(svc string) string {
	return fmt.Sprintf(`SELECT coalesce(outcome_token::text, ''), coalesce(recorded_kind, ''), coalesce(recorded_reason, '')
  FROM %s WHERE id = $1`, tablename.Of(svc, tablename.Outbox))
}

// uuidForm — каноническая запись UUID: так её отдаёт Claim.
var uuidForm = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ackRequest — Ack, разобранный до SQL.
type ackRequest struct {
	id, token string
	outcome   Outcome
	deferFor  time.Duration
}

// Ack записывает исход арендованной строки (З9). Границы — до SQL; запись —
// один условный оператор; ноль строк — одно чтение и классификация повтора в
// объявленном порядке; следствия записи — только в ветке, изменившей строку.
func (s *Server) Ack(ctx context.Context, req *notifyv1.AckRequest) (*notifyv1.AckResponse, error) {
	a, err := s.parseAck(req)
	if err != nil {
		return nil, err
	}
	var row pgx.Row
	if a.outcome.Kind == KindDefer {
		row = s.db.QueryRow(ctx, ackDeferSQL(s.service), a.id, a.token, string(a.outcome.Reason), a.deferFor.Seconds())
	} else {
		row = s.db.QueryRow(ctx, ackTerminalSQL(s.service), a.id, a.token, string(a.outcome.Kind), string(a.outcome.Reason))
	}
	var class string
	switch err := row.Scan(&class); {
	case err == nil:
		s.metrics.observeOutcome(Class(class), a.outcome)
		return &notifyv1.AckResponse{}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, s.internalError(ctx, "ack", err)
	}
	return s.classifyRepeat(ctx, a)
}

// classifyRepeat — ноль строк записи: строки нет — NOT_FOUND; тот же токен и
// та же пара — успех без изменения; тот же токен и иная пара —
// OUTCOME_ALREADY_RECORDED; иначе — LEASE_LOST. Срок аренды в классификацию
// не входит (повтор после конца аренды — успех).
func (s *Server) classifyRepeat(ctx context.Context, a ackRequest) (*notifyv1.AckResponse, error) {
	var token, kind, reason string
	err := s.db.QueryRow(ctx, ackRecordedSQL(s.service), a.id).Scan(&token, &kind, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, coreerrors.ReasonResourceNotFound.Errf(coreerrors.PeerRef{
			Service: s.module, ResourceType: "notification", ResourceID: a.id,
		}, "Notification %s not found", a.id)
	}
	if err != nil {
		return nil, s.internalError(ctx, "ack recorded", err)
	}
	if token != "" && strings.EqualFold(token, a.token) {
		if Kind(kind) == a.outcome.Kind && Reason(reason) == a.outcome.Reason {
			return &notifyv1.AckResponse{}, nil
		}
		return nil, s.reasonError(ReasonOutcomeAlreadyRecorded, a.id,
			fmt.Sprintf("Notification %s outcome is already recorded", a.id))
	}
	return nil, s.reasonError(ReasonLeaseLost, a.id, fmt.Sprintf("Notification %s lease is lost", a.id))
}

// parseAck — границы Ack в объявленном порядке: id, токен, исход, причина,
// defer_for (NTF1-B14, B22, УК72, УК73, CX1-26 (г)).
func (s *Server) parseAck(req *notifyv1.AckRequest) (ackRequest, error) {
	a := ackRequest{id: req.GetId(), token: req.GetLeaseToken()}
	if a.id == "" {
		return a, fieldError("id", "required")
	}
	if err := validate.ResourceID("notification", ids.PrefixNotificationHyphen, a.id); err != nil {
		return a, coreerrors.ReasonInvalidResourceID.Errf(coreerrors.PeerRef{
			Service: s.module, ResourceType: "notification",
		}, "invalid notification id '%s'", a.id)
	}
	if a.token == "" {
		return a, fieldError("lease_token", "required")
	}
	if !uuidForm.MatchString(a.token) {
		return a, fieldError("lease_token", "must be a UUID")
	}
	o := req.GetOutcome()
	if o.GetKind() == notifyv1.OutcomeKind_OUTCOME_UNSPECIFIED {
		return a, fieldError("outcome", "required")
	}
	if o.GetKind() == notifyv1.OutcomeKind_EXPIRED {
		return a, fieldError("outcome.kind", "EXPIRED is set by the source only")
	}
	if _, known := notifyv1.OutcomeKind_name[int32(o.GetKind())]; !known {
		return a, fieldError("outcome.kind", fmt.Sprintf("%d is not an outcome kind", o.GetKind()))
	}
	a.outcome.Kind = Kind(strings.ToLower(o.GetKind().String()))
	if o.GetReason() != notifyv1.OutcomeReason_OUTCOME_REASON_UNSPECIFIED {
		a.outcome.Reason = Reason(strings.ToLower(o.GetReason().String()))
	}
	if !slices.Contains(ackReasons()[a.outcome.Kind], a.outcome.Reason) {
		return a, fieldError("outcome.reason",
			fmt.Sprintf("%s is not allowed with %s", o.GetReason(), o.GetKind()))
	}
	d := req.GetDeferFor()
	if a.outcome.Kind != KindDefer {
		if d != nil {
			return a, fieldError("defer_for", "must not be set unless outcome.kind is DEFER")
		}
		return a, nil
	}
	if d == nil {
		return a, fieldError("defer_for", "required")
	}
	if d.CheckValid() != nil || d.AsDuration() < MinDefer || d.AsDuration() > MaxDefer {
		return a, fieldError("defer_for", "must be in [1s..15m]")
	}
	a.deferFor = d.AsDuration()
	return a, nil
}
