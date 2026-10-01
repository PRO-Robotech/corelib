// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
)

// claimSQL — аренда пачки одним оператором (З8, CX1-25): отбор под замком
// строки с SKIP LOCKED и перевод аренды в том же операторе. Остатки аренды и
// срока считает база (обе стороны — её часы, УК71), в микросекундах.
func claimSQL(svc string) string {
	return fmt.Sprintf(`WITH c AS (
  SELECT id FROM %[1]s
   WHERE state = 'pending' AND class = ANY($1) AND expires_at > now()
     AND (not_before IS NULL OR not_before <= now())
     AND (lease_until IS NULL OR lease_until <= now())
   ORDER BY enqueued_at, id
   LIMIT $2
     FOR UPDATE SKIP LOCKED)
UPDATE %[1]s t
   SET lease_token = gen_random_uuid(), lease_until = now() + make_interval(secs => $3),
       first_claimed_at = coalesce(t.first_claimed_at, now())
  FROM c
 WHERE t.id = c.id
RETURNING t.id, t.lease_token::text, t.template, t.schema_rev, t.class, t.recipient_address, t.attrs, t.secret_attrs,
          t.enqueued_at,
          (extract(epoch FROM t.lease_until - now()) * 1000000)::bigint,
          (extract(epoch FROM t.expires_at - now()) * 1000000)::bigint`, tablename.Of(svc, tablename.Outbox))
}

// sealedCloseSQL — закрытие арендованной строки, чей секрет не открылся (З8):
// условный перевод её же токеном аренды, секрет стирается.
func sealedCloseSQL(svc string) string {
	return fmt.Sprintf(`UPDATE %s
   SET state = 'invalid', outcome_reason = $3, outcome_at = now(), secret_attrs = NULL,
       lease_token = NULL, lease_until = NULL
 WHERE id = $1 AND lease_token = $2::uuid AND state = 'pending'`, tablename.Of(svc, tablename.Outbox))
}

// leased — строка, арендованная оператором Claim.
type leased struct {
	id, token, template, class, address string
	rev                                 uint32
	attrs, secret                       []byte
	enqueued                            time.Time
	leaseLeft, expiresLeft              time.Duration
}

// Claim арендует пачку строк (З8). Границы входа — до первого оператора SQL;
// аренда — один оператор, соединение возвращается пулу до расшифровки;
// расшифровка — после коммита, строка с неоткрывшимся секретом закрывается
// INVALID и в ответ не попадает.
func (s *Server) Claim(ctx context.Context, req *notifyv1.ClaimRequest) (*notifyv1.ClaimResponse, error) {
	classes, err := claimClasses(req)
	if err != nil {
		return nil, err
	}
	got, err := s.lease(ctx, classes, int64(req.GetMax()))
	if err != nil {
		return nil, s.internalError(ctx, "claim", err)
	}
	// Отсчёт времени ответа — от возврата оператора: расшифровка и закрытие
	// неоткрывшихся строк идут после него.
	elapsed := s.clock.Start()

	sort.Slice(got, func(i, j int) bool {
		if !got[i].enqueued.Equal(got[j].enqueued) {
			return got[i].enqueued.Before(got[j].enqueued)
		}
		return got[i].id < got[j].id
	})
	out := make([]*notifyv1.ClaimedNotification, 0, len(got))
	kept := make([]leased, 0, len(got))
	for _, l := range got {
		attrs, reason, err := s.open(l)
		if err != nil {
			return nil, s.internalError(ctx, "claim attrs", err)
		}
		if reason != ReasonNone {
			s.closeSealed(ctx, l, reason)
			continue
		}
		out = append(out, &notifyv1.ClaimedNotification{
			Id:         l.id,
			LeaseToken: l.token,
			Template:   l.template,
			SchemaRev:  l.rev,
			Class:      wireClass(Class(l.class)),
			Recipient:  &notifyv1.ClaimedNotification_Address{Address: l.address},
			Attrs:      attrs,
			EnqueuedAt: timestamppb.New(l.enqueued),
		})
		kept = append(kept, l)
	}
	// Остатки — за вычетом времени, прошедшего в процессе до ответа (УК71).
	spent := elapsed()
	for i, n := range out {
		n.LeaseRemaining = durationpb.New(max(kept[i].leaseLeft-spent, 0))
		n.ExpiresIn = durationpb.New(max(kept[i].expiresLeft-spent, 0))
	}
	return &notifyv1.ClaimResponse{Notifications: out}, nil
}

// lease — оператор аренды под своим сроком; строки прочитаны и соединение
// возвращено пулу до возврата.
func (s *Server) lease(ctx context.Context, classes []string, limit int64) ([]leased, error) {
	ctx, cancel := context.WithTimeout(ctx, storeCallTimeout)
	defer cancel()
	rows, err := s.db.Query(ctx, claimSQL(s.service), classes, limit, LeaseTTL.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var got []leased
	for rows.Next() {
		var l leased
		var leaseUS, expiresUS int64
		if err := rows.Scan(&l.id, &l.token, &l.template, &l.rev, &l.class, &l.address, &l.attrs, &l.secret,
			&l.enqueued, &leaseUS, &expiresUS); err != nil {
			return nil, err
		}
		l.leaseLeft = time.Duration(leaseUS) * time.Microsecond
		l.expiresLeft = time.Duration(expiresUS) * time.Microsecond
		got = append(got, l)
	}
	return got, rows.Err()
}

// claimClasses — границы Claim (NTF1-B21, B24): max в [1..MaxClaim], набор
// классов непуст и из перечня. Возвращает набор словами колонки class.
func claimClasses(req *notifyv1.ClaimRequest) ([]string, error) {
	switch m := req.GetMax(); {
	case m == 0:
		return nil, fieldError("max", "required")
	case m > MaxClaim:
		return nil, fieldError("max", fmt.Sprintf("must be ≤ %d", MaxClaim))
	}
	if len(req.GetClasses()) == 0 {
		return nil, fieldError("classes", "required")
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range req.GetClasses() {
		word, ok := classWord(c)
		if !ok {
			return nil, fieldError("classes", c.String()+" is not a class")
		}
		if !seen[word] {
			seen[word] = true
			out = append(out, word)
		}
	}
	return out, nil
}

func classWord(c notifyv1.NotificationClass) (string, bool) {
	switch c {
	case notifyv1.NotificationClass_SECURITY:
		return string(ClassSecurity), true
	case notifyv1.NotificationClass_NOTICE:
		return string(ClassNotice), true
	case notifyv1.NotificationClass_NOTIFICATION_CLASS_UNSPECIFIED:
		return "", false
	}
	return "", false
}

func wireClass(c Class) notifyv1.NotificationClass {
	switch c {
	case ClassSecurity:
		return notifyv1.NotificationClass_SECURITY
	case ClassNotice:
		return notifyv1.NotificationClass_NOTICE
	}
	return notifyv1.NotificationClass_NOTIFICATION_CLASS_UNSPECIFIED
}

// open — значения атрибутов строки: открытые плюс секретные, расшифрованные
// кольцом. Неоткрывшийся секрет — причина закрытия строки, а не ошибка.
// Ошибка — только порча открытой части (jsonb не карта строк).
func (s *Server) open(l leased) (map[string]string, Reason, error) {
	attrs := map[string]string{}
	if err := json.Unmarshal(l.attrs, &attrs); err != nil {
		return nil, ReasonNone, fmt.Errorf("строка %s: attrs: %w", l.id, err)
	}
	if l.secret == nil {
		return attrs, ReasonNone, nil
	}
	pt, err := s.ring.Open(s.service, l.id, l.template, l.secret)
	switch {
	case errors.Is(err, ErrKeyUnavailable):
		return nil, ReasonKeyUnavailable, nil
	case err != nil:
		return nil, ReasonSealedMismatch, nil
	}
	secret := map[string]string{}
	if err := json.Unmarshal(pt, &secret); err != nil {
		return nil, ReasonSealedMismatch, nil
	}
	for k, v := range secret {
		attrs[k] = v
	}
	return attrs, ReasonNone, nil
}

// closeSealed закрывает строку с неоткрывшимся секретом её токеном аренды.
// Отказ закрытия строку не выдаёт: она остаётся под арендой и после её конца
// выдаётся и закрывается снова.
func (s *Server) closeSealed(ctx context.Context, l leased, reason Reason) {
	ctx, cancel := context.WithTimeout(ctx, storeCallTimeout)
	defer cancel()
	tag, err := s.db.Exec(ctx, sealedCloseSQL(s.service), l.id, l.token, string(reason))
	if err != nil {
		s.log.WarnContext(ctx, "notification feed row with an unopenable secret was not closed",
			slog.String("module", s.module), slog.String("id", l.id), slog.String("reason", string(reason)),
			slog.String("err", err.Error()))
		return
	}
	if tag.RowsAffected() == 1 {
		s.metrics.observeOutcome(Class(l.class), Outcome{Kind: KindInvalid, Reason: reason})
	}
}
