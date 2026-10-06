// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
)

// Правка Х3 NTF-4 (§1.1, Р17, §8 строка 80–81): Ack принимает терминальный
// SUPPRESSED(reason) — строка закрыта этим исходом, секрет стёрт, аренда
// снята, вклад строки в окна лимита источника НЕ возвращается ни Ack, ни
// уборщиком. По каждой из четырёх причин. Близнец с одним изменённым фактом
// (исход строки) — истечение после DEFER(platform_unavailable): вклад
// возвращён, значит фикстура возврат наблюдает.
func TestNTF4X3_AckSuppressedClosesTheRowAndKeepsTheSourceLimit(t *testing.T) {
	prep := func(t *testing.T) (*stand, string) {
		s := newStand(t, standOpts{})
		id := s.putOne(t, secretDesc(perHour(5)), rcpt, secretValues("x"))
		s.setWindow(t, rcpt, 3)
		require.Equal(t, 3, s.windowCount(t, rcpt))
		require.True(t, s.row(t, id).HasSecret, "фикстура: секрет у pending-строки")
		return s, id
	}

	t.Run("близнец EXPIRED(platform_unavailable)", func(t *testing.T) {
		s, id := prep(t)
		s.deferred(t, id, notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE)
		s.expire(t, id)
		s.sweep(t)
		require.Equal(t, "expired", s.row(t, id).State)
		require.Equal(t, 2, s.windowCount(t, rcpt), "близнец: истечение по вине платформы вклад возвращает")
	})

	for _, word := range suppressedReasonWords() {
		t.Run(word, func(t *testing.T) {
			s, id := prep(t)
			got := s.claim(t, 10)
			require.Len(t, got, 1)
			tok := got[0].GetLeaseToken()

			kind := contractKind(t, "SUPPRESSED")
			reason := contractReason(t, strings.ToUpper(word))
			require.Equal(t, 0.0, outcomes(t, s.reg, "notice", suppressedWord, word), "серия исхода объявлена при 0 до первого Ack")

			require.NoError(t, s.ack(id, tok, kind, reason, 0))
			r := s.row(t, id)
			require.Equal(t, suppressedWord, r.State)
			require.Equal(t, word, r.Reason)
			require.Equal(t, suppressedWord, r.RecordedKind)
			require.Equal(t, word, r.RecordedReason)
			require.Equal(t, tok, r.OutcomeToken)
			require.Empty(t, r.LeaseToken, "аренда снята")
			require.False(t, r.HasSecret, "секрет стёрт")
			require.True(t, r.HasOutcomeAt)
			require.Equal(t, 3, s.windowCount(t, rcpt), "Ack SUPPRESSED вклад не возвращает")
			require.Equal(t, 1.0, outcomes(t, s.reg, "notice", suppressedWord, word))
			require.Equal(t, 0.0, gathered(t, s.reg, "kacho_notification_feed_delivered_total", map[string]string{"module": "probe"}),
				"подавленное письмо не доставлено")

			s.expire(t, id)
			s.sweep(t)
			require.Equal(t, r, s.row(t, id), "уборщик закрытую строку не трогает")
			require.Equal(t, 3, s.windowCount(t, rcpt), "уборщик вклад SUPPRESSED не возвращает")
		})
	}
}

// Р17, DoD S2 п.2 NTF-4 (серверная сторона NTF4-110/111): правило повтора Ack
// NTF-1 распространяется на SUPPRESSED без исключений — причина входит в
// исход. Тот же токен и тот же SUPPRESSED(reason) — успех, строка не
// изменилась, счётчик не вырос; тот же токен и другая причина либо другой вид
// — OUTCOME_ALREADY_RECORDED; другой токен — LEASE_LOST. Обе очерёдности
// причин: soft_bounce → complaint (NTF4-110) и complaint → soft_bounce
// (NTF4-111).
func TestNTF4X3_RepeatedAckWithSuppressedFollowsTheOneRule(t *testing.T) {
	for _, tc := range []struct{ name, first, other string }{
		{"NTF4-110 soft_bounce, повтор с complaint", "soft_bounce", "complaint"},
		{"NTF4-111 complaint, повтор с soft_bounce", "complaint", "soft_bounce"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStand(t, standOpts{})
			id := s.putOne(t, helloDesc(), rcpt, hello())
			got := s.claim(t, 10)
			require.Len(t, got, 1)
			tok := got[0].GetLeaseToken()

			kind := contractKind(t, "SUPPRESSED")
			first := contractReason(t, strings.ToUpper(tc.first))
			other := contractReason(t, strings.ToUpper(tc.other))

			require.NoError(t, s.ack(id, tok, kind, first, 0), "первый Ack")
			recorded := s.row(t, id)
			require.Equal(t, suppressedWord, recorded.State)
			require.Equal(t, tc.first, recorded.Reason)

			require.NoError(t, s.ack(id, tok, kind, first, 0), "(а) повтор тем же исходом — успех")
			require.Equal(t, recorded, s.row(t, id), "(а) строка не изменилась")

			for _, again := range []struct {
				name   string
				kind   notifyv1.OutcomeKind
				reason notifyv1.OutcomeReason
			}{
				{"(б) другая причина", kind, other},
				{"(в) другой вид", notifyv1.OutcomeKind_SENT, 0},
			} {
				err := s.ack(id, tok, again.kind, again.reason, 0)
				require.Equal(t, codes.FailedPrecondition, status.Code(err), "%s: %v", again.name, err)
				require.Equal(t, "Notification "+id+" outcome is already recorded", status.Convert(err).Message(), again.name)
				reason, _ := errorInfoReason(t, err)
				require.Equal(t, "OUTCOME_ALREADY_RECORDED", reason, again.name)
				require.Equal(t, recorded, s.row(t, id), again.name)
			}

			err := s.ack(id, "00000000-0000-4000-8000-000000000000", kind, first, 0)
			require.Equal(t, codes.FailedPrecondition, status.Code(err), "(г) другой токен: %v", err)
			reason, _ := errorInfoReason(t, err)
			require.Equal(t, "LEASE_LOST", reason, "(г) другой токен")
			require.Equal(t, recorded, s.row(t, id))

			require.Equal(t, 1.0, outcomes(t, s.reg, "notice", suppressedWord, tc.first), "повторы счётчик не увеличили")
			require.Equal(t, 0.0, outcomes(t, s.reg, "notice", suppressedWord, tc.other))
		})
	}
}

// Х3: состояние suppressed и его причины выразимы в базе источника ровно
// закрытым перечнем Р17 — CHECK состояния и CHECK outcome_pair новой версии
// схемы. Положительный близнец — закрытие строки исходом NTF-1 тем же
// оператором (denied, revoked): форма оператора CHECK проходит.
func TestNTF4X3_SuppressedRowIsExpressibleOnlyWithItsReasons(t *testing.T) {
	s := newStand(t, standOpts{})
	put := func() string { return s.putOne(t, secretDesc(), "a@example.invalid", secretValues("x")) }
	closeAs := func(id, state string, reason any, eraseSecret bool) error {
		q := `UPDATE probe_notification_outbox SET state = $2, outcome_reason = $3, outcome_at = now()`
		if eraseSecret {
			q += `, secret_attrs = NULL`
		}
		_, err := s.pool.Exec(context.Background(), q+` WHERE id = $1`, id, state, reason)
		return err
	}
	refusedBy := func(t *testing.T, err error, constraint string) {
		t.Helper()
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), "ждали отказ CHECK %s, получили %v", constraint, err)
		require.Equal(t, "23514", pgErr.Code, "%v", err)
		require.Equal(t, constraint, pgErr.ConstraintName, "%v", err)
	}

	twin := put()
	require.NoError(t, closeAs(twin, "denied", "revoked", true), "близнец: закрытие исходом NTF-1")
	require.Equal(t, "denied", s.row(t, twin).State)

	for _, word := range suppressedReasonWords() {
		id := put()
		require.NoError(t, closeAs(id, suppressedWord, word, true), "suppressed(%s)", word)
		require.Equal(t, suppressedWord, s.row(t, id).State)
	}
	refusedBy(t, closeAs(put(), suppressedWord, nil, true), "outcome_pair")
	refusedBy(t, closeAs(put(), suppressedWord, "revoked", true), "outcome_pair")
	refusedBy(t, closeAs(put(), "expired", "hard_bounce", true), "outcome_pair")
	refusedBy(t, closeAs(put(), suppressedWord, "complaint", false), "closed_carries_no_secret")
}
