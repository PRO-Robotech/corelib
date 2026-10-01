// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/pgtest"
)

const (
	notice   = notifyv1.NotificationClass_NOTICE
	security = notifyv1.NotificationClass_SECURITY
)

// NTF1-B03: в покое — шифротекст (выгрузка таблицы целиком не несёт посеянной
// строки), в ответе Claim — открытый текст.
func TestNTF1B03_SecretAtRestIsCiphertextAndClaimOpensIt(t *testing.T) {
	s := newStand(t, standOpts{})
	seed := "seed-" + ids.NewID("itm")
	id := s.putOne(t, secretDesc(), "user@example.invalid", secretValues(seed))

	conn, err := s.pool.Acquire(context.Background())
	require.NoError(t, err)
	var dump bytes.Buffer
	_, err = conn.Conn().PgConn().CopyTo(context.Background(), &dump, `COPY probe_notification_outbox TO STDOUT`)
	conn.Release()
	require.NoError(t, err)
	require.Contains(t, dump.String(), id, "выгрузка не та: строки в ней нет")
	require.NotContains(t, dump.String(), seed)

	got := s.claim(t, 10)
	require.Len(t, got, 1)
	require.Equal(t, seed, got[0].GetAttrs()["code"])
	require.Equal(t, "probe", got[0].GetAttrs()["subject_name"])
}

// NTF1-B04: шифротекст первой строки в колонке второй не открывается —
// вторая закрыта INVALID(sealed_mismatch), секрет стёрт, в ответ не попала.
// Близнец — первая строка в том же ответе открыта.
func TestNTF1B04_CiphertextMovedToAnotherRowDoesNotOpen(t *testing.T) {
	s := newStand(t, standOpts{})
	first := s.putOne(t, secretDesc(), "a@example.invalid", secretValues("first-secret"))
	second := s.putOne(t, secretDesc(), "b@example.invalid", secretValues("second-secret"))
	s.exec(t, `UPDATE probe_notification_outbox SET secret_attrs = (SELECT secret_attrs FROM probe_notification_outbox WHERE id = $1) WHERE id = $2`, first, second)

	got := s.claim(t, 10)
	require.Equal(t, []string{first}, idsOf(got))
	require.Equal(t, "first-secret", got[0].GetAttrs()["code"])
	r := s.row(t, second)
	require.Equal(t, "invalid", r.State)
	require.Equal(t, "sealed_mismatch", r.Reason)
	require.False(t, r.HasSecret)
	require.True(t, r.HasOutcomeAt)
	require.Equal(t, 1.0, outcomes(t, s.reg, "notice", "invalid", "sealed_mismatch"))
}

// УК19: смена шаблона строки при прежнем шифротексте — sealed_mismatch.
// Близнец — та же строка без смены открывается (B03).
func TestUK19_TemplateChangedUnderTheCiphertextIsSealedMismatch(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, secretDesc(), "a@example.invalid", secretValues("x"))
	s.exec(t, `UPDATE probe_notification_outbox SET template = 'probe-hello' WHERE id = $1`, id)
	require.Empty(t, s.claim(t, 10))
	r := s.row(t, id)
	require.Equal(t, "invalid", r.State)
	require.Equal(t, "sealed_mismatch", r.Reason)
	require.False(t, r.HasSecret)
}

// NTF1-B06: строка прежнего ключа открывается кольцом «K2, прежний K1»; новые
// строки запечатаны K2. NTF1-B07: кольцо «только K2» — key_unavailable.
func TestNTF1B06B07_KeyRotationAndRemoval(t *testing.T) {
	k1 := ring(t, key(1, 0xA1))
	rotated := ring(t, key(2, 0xB2), key(1, 0xA1))

	t.Run("B06", func(t *testing.T) {
		s := newStand(t, standOpts{putRing: k1, serverRing: rotated})
		id := s.putOne(t, secretDesc(), "a@example.invalid", secretValues("old-key"))
		got := s.claim(t, 10)
		require.Len(t, got, 1)
		require.Equal(t, id, got[0].GetId())
		require.Equal(t, "old-key", got[0].GetAttrs()["code"])

		s2 := fixtureSealed(t, s.pool, true, rotated)
		require.NoError(t, s2.put(t, secretDesc(), "b@example.invalid", secretValues("new-key")))
		var first []byte
		require.NoError(t, s.pool.QueryRow(context.Background(),
			`SELECT substring(secret_attrs FROM 1 FOR 1) FROM probe_notification_outbox WHERE id <> $1`, id).Scan(&first))
		require.Equal(t, []byte{2}, first, "новая строка запечатана активным K2")
	})
	t.Run("B07", func(t *testing.T) {
		s := newStand(t, standOpts{putRing: k1, serverRing: ring(t, key(2, 0xB2))})
		id := s.putOne(t, secretDesc(), "a@example.invalid", secretValues("old-key"))
		require.Empty(t, s.claim(t, 10))
		r := s.row(t, id)
		require.Equal(t, "invalid", r.State)
		require.Equal(t, "key_unavailable", r.Reason)
		require.False(t, r.HasSecret)
		require.Equal(t, 1.0, outcomes(t, s.reg, "notice", "invalid", "key_unavailable"))
	})
}

// NTF1-B10: два параллельных Claim(60) над 100 строками — объединение без
// повторов, у каждой токен и остаток аренды.
func TestNTF1B10_ParallelClaimsGetDisjointRows(t *testing.T) {
	s := newStand(t, standOpts{})
	for i := 0; i < 100; i++ {
		s.putOne(t, helloDesc(), fmt.Sprintf("u%d@example.invalid", i), hello())
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		all  []*notifyv1.ClaimedNotification
		errs []error
	)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := s.server.Claim(context.Background(), &notifyv1.ClaimRequest{Max: 60, Classes: []notifyv1.NotificationClass{notice}})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			all = append(all, resp.GetNotifications()...)
		}()
	}
	wg.Wait()
	require.Empty(t, errs)
	seen := map[string]bool{}
	for _, n := range all {
		require.False(t, seen[n.GetId()], "строка %s выдана дважды", n.GetId())
		seen[n.GetId()] = true
		require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, n.GetLeaseToken())
		require.Positive(t, n.GetLeaseRemaining().AsDuration())
		require.Positive(t, n.GetExpiresIn().AsDuration())
		require.Equal(t, uint32(1), n.GetSchemaRev())
		require.Equal(t, notice, n.GetClass())
		require.NotEmpty(t, n.GetAddress())
	}
	require.Len(t, seen, 100)
}

// Ответ Claim — в порядке постановки (enqueued_at, затем id), enqueued_at —
// полной точности базы.
func TestClaimAnswersInEnqueueOrderWithFullPrecision(t *testing.T) {
	s := newStand(t, standOpts{})
	var want []string
	for i := 0; i < 5; i++ {
		want = append(want, s.putOne(t, helloDesc(), fmt.Sprintf("u%d@example.invalid", i), hello()))
	}
	got := s.claim(t, 10)
	var order []string
	for _, n := range got {
		order = append(order, n.GetId())
		var enq time.Time
		require.NoError(t, s.pool.QueryRow(context.Background(),
			`SELECT enqueued_at FROM probe_notification_outbox WHERE id = $1`, n.GetId()).Scan(&enq))
		require.True(t, n.GetEnqueuedAt().AsTime().Equal(enq), "enqueued_at усечён: %v против %v", n.GetEnqueuedAt().AsTime(), enq)
	}
	require.Equal(t, want, order)
}

// NTF1-B11: истёкшую строку Claim не выдаёт. Близнец — та же строка до срока
// выдаётся.
func TestNTF1B11_ExpiredRowIsNotClaimed(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(), "a@example.invalid", hello())
	s.expire(t, id)
	require.Empty(t, s.claim(t, 10))

	s.exec(t, `UPDATE probe_notification_outbox SET expires_at = now() + interval '1 hour' WHERE id = $1`, id)
	require.Equal(t, []string{id}, idsOf(s.claim(t, 10)))
}

// NTF1-B12: Ack SENT действующим токеном — sent, секрет стёрт, отметка
// закрытия, счётчики исходов и доставленных +1.
func TestNTF1B12_AckSentWithALiveLease(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, secretDesc(), "a@example.invalid", secretValues("x"))
	got := s.claim(t, 10)
	require.Len(t, got, 1)
	require.NoError(t, s.ack(id, got[0].GetLeaseToken(), notifyv1.OutcomeKind_SENT, 0, 0))
	r := s.row(t, id)
	require.Equal(t, "sent", r.State)
	require.Empty(t, r.Reason)
	require.False(t, r.HasSecret)
	require.True(t, r.HasOutcomeAt)
	require.Empty(t, r.LeaseToken)
	require.Equal(t, got[0].GetLeaseToken(), r.OutcomeToken)
	require.Equal(t, 1.0, outcomes(t, s.reg, "notice", "sent", ""))
	require.Equal(t, 1.0, gathered(t, s.reg, "kacho_notification_feed_delivered_total", map[string]string{"module": "probe"}))
}

// NTF1-B13: Ack с утраченной арендой (аренда истекла, строка выдана с T2) —
// LEASE_LOST, строка не изменилась. УК73 (близнец): токен по форме, но чужой —
// LEASE_LOST, а не INTERNAL.
func TestNTF1B13_AckWithALostLease(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(), "a@example.invalid", hello())
	t1 := s.claim(t, 10)[0].GetLeaseToken()
	s.endLease(t, id)
	again := s.claim(t, 10)
	require.Len(t, again, 1)
	require.NotEqual(t, t1, again[0].GetLeaseToken())
	before := s.row(t, id)

	for _, tok := range []string{t1, "00000000-0000-4000-8000-000000000000"} {
		err := s.ack(id, tok, notifyv1.OutcomeKind_SENT, 0, 0)
		require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
		reason, meta := errorInfoReason(t, err)
		require.Equal(t, "LEASE_LOST", reason)
		require.Equal(t, id, meta["resource_id"])
		require.Equal(t, before, s.row(t, id))
	}
	require.Equal(t, 0.0, outcomes(t, s.reg, "notice", "sent", ""))
}

// NTF1-B17: не-pending строка с секретом невыразима — 23514 с именем
// ограничения; тот же UPDATE со стиранием принят.
func TestNTF1B17_ClosedRowWithASecretIsRefusedByTheDatabase(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, secretDesc(), "a@example.invalid", secretValues("x"))
	_, err := s.pool.Exec(context.Background(),
		`UPDATE probe_notification_outbox SET state = 'sent', outcome_at = now() WHERE id = $1`, id)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "%v", err)
	require.Equal(t, "23514", pgErr.Code)
	require.Equal(t, "closed_carries_no_secret", pgErr.ConstraintName)

	s.exec(t, `UPDATE probe_notification_outbox SET state = 'sent', outcome_at = now(), secret_attrs = NULL WHERE id = $1`, id)
	require.Equal(t, "sent", s.row(t, id).State)
}

// NTF1-B18: Claim не удерживает соединение пула. Пул 4, 16 Claim и 16
// глаголов источника параллельно — все глаголы в срок; после ответов занятых
// соединений 0.
func TestNTF1B18_ClaimDoesNotHoldAPoolConnection(t *testing.T) {
	cfg, err := pgxpool.ParseConfig(pgtest.NewDB(t))
	require.NoError(t, err)
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	s := newStand(t, standOpts{pool: pool})
	for i := 0; i < 40; i++ {
		s.putOne(t, secretDesc(), fmt.Sprintf("u%d@example.invalid", i), secretValues("x"))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := s.server.Claim(ctx, &notifyv1.ClaimRequest{Max: 3, Classes: []notifyv1.NotificationClass{notice}})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := s.pool.Exec(ctx, `INSERT INTO probe_items (id) VALUES ($1)`, ids.NewID("itm"))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, int32(0), pool.Stat().AcquiredConns(), "после ответов Claim соединения пула заняты")
	require.Equal(t, 16, s.count(t, `SELECT count(*) FROM probe_items`)-40)
}

// NTF1-B21: границы max — ни одна строка не арендована; близнец — max=1 и
// max=500 на границах.
func TestNTF1B21_ClaimMaxBoundsLeaseNothing(t *testing.T) {
	s := newStand(t, standOpts{})
	for i := 0; i < 3; i++ {
		s.putOne(t, helloDesc(), fmt.Sprintf("u%d@example.invalid", i), hello())
	}
	for _, m := range []uint32{0, 501} {
		_, err := s.server.Claim(context.Background(), &notifyv1.ClaimRequest{Max: m, Classes: []notifyv1.NotificationClass{notice}})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	require.Zero(t, s.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE lease_token IS NOT NULL`))
	require.Len(t, s.claim(t, 1), 1)
	require.Len(t, s.claim(t, 500), 2)
}

// NTF1-B22, B23: id не по форме — INVALID_RESOURCE_ID; неизвестный — NOT_FOUND
// с текстом и metadata.resource_id; строка B12 не изменилась.
func TestNTF1B22B23_AckWithAMalformedOrUnknownID(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(), "a@example.invalid", hello())
	tok := s.claim(t, 10)[0].GetLeaseToken()
	before := s.row(t, id)

	err := s.ack("not-an-id", tok, notifyv1.OutcomeKind_SENT, 0, 0)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "invalid notification id 'not-an-id'", status.Convert(err).Message())

	unknown := ids.NewHyphenID(ids.PrefixNotificationHyphen)
	err = s.ack(unknown, tok, notifyv1.OutcomeKind_SENT, 0, 0)
	require.Equal(t, codes.NotFound, status.Code(err))
	require.Equal(t, "Notification "+unknown+" not found", status.Convert(err).Message())
	reason, meta := errorInfoReason(t, err)
	require.Equal(t, "RESOURCE_NOT_FOUND", reason)
	require.Equal(t, unknown, meta["resource_id"])
	require.Equal(t, before, s.row(t, id))
}

// NTF1-B24: Claim отдаёт только классы набора; строка другого класса остаётся
// pending без аренды.
func TestNTF1B24_ClaimGivesOnlyTheRequestedClasses(t *testing.T) {
	s := newStand(t, standOpts{})
	sec := s.putOne(t, securityDesc(), "a@example.invalid", hello())
	ntc := s.putOne(t, helloDesc(), "b@example.invalid", hello())
	got := s.claim(t, 10, security)
	require.Equal(t, []string{sec}, idsOf(got))
	require.Equal(t, security, got[0].GetClass())
	r := s.row(t, ntc)
	require.Equal(t, "pending", r.State)
	require.Empty(t, r.LeaseToken)
	require.False(t, r.Claimed)
}

// NTF1-B26 (а), (б): повтор того же Ack — успех без изменения строки; тот же
// токен с другим исходом — OUTCOME_ALREADY_RECORDED. Счётчик исходов растёт
// только на изменившей строку записи (CX1-26 (д)).
func TestNTF1B26_RepeatedAckAfterARecordedOutcome(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(), "a@example.invalid", hello())
	tok := s.claim(t, 10)[0].GetLeaseToken()
	require.NoError(t, s.ack(id, tok, notifyv1.OutcomeKind_SENT, 0, 0))
	first := s.row(t, id)

	require.NoError(t, s.ack(id, tok, notifyv1.OutcomeKind_SENT, 0, 0), "(а) повтор — успех")
	require.Equal(t, first, s.row(t, id), "(а) outcome_at прежний")

	err := s.ack(id, tok, notifyv1.OutcomeKind_DEFER, notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE, time.Minute)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, "Notification "+id+" outcome is already recorded", status.Convert(err).Message())
	reason, _ := errorInfoReason(t, err)
	require.Equal(t, "OUTCOME_ALREADY_RECORDED", reason)
	require.Equal(t, first, s.row(t, id))
	require.Equal(t, 1.0, outcomes(t, s.reg, "notice", "sent", ""), "повтор не увеличил счётчик")
	require.Equal(t, 1.0, gathered(t, s.reg, "kacho_notification_feed_delivered_total", map[string]string{"module": "probe"}))
}

// З9, З23: Ack DEFER — строка pending без аренды, причина последней отсрочки и
// «не раньше»; Claim до not_before её не выдаёт.
func TestAckDeferKeepsTheRowPendingUntilNotBefore(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(), "a@example.invalid", hello())
	tok := s.claim(t, 10)[0].GetLeaseToken()
	require.NoError(t, s.ack(id, tok, notifyv1.OutcomeKind_DEFER, notifyv1.OutcomeReason_GRANT_SKEW, 10*time.Minute))
	r := s.row(t, id)
	require.Equal(t, "pending", r.State)
	require.Empty(t, r.LeaseToken)
	require.Equal(t, "defer", r.RecordedKind)
	require.Equal(t, "grant_skew", r.RecordedReason)
	require.NotEmpty(t, r.NotBefore)
	var last string
	var gap float64
	require.NoError(t, s.pool.QueryRow(context.Background(), `
		SELECT last_defer_reason, extract(epoch FROM not_before - now())
		  FROM probe_notification_outbox WHERE id = $1`, id).Scan(&last, &gap))
	require.Equal(t, "grant_skew", last)
	require.InDelta(t, 600, gap, 5)
	require.Empty(t, s.claim(t, 10), "Claim выдал отсроченную строку")
	require.Equal(t, 1.0, gathered(t, s.reg, "kacho_notification_feed_defers_total",
		map[string]string{"module": "probe", "reason": "grant_skew"}))

	s.exec(t, `UPDATE probe_notification_outbox SET not_before = now() - interval '1 second' WHERE id = $1`, id)
	require.Equal(t, []string{id}, idsOf(s.claim(t, 10)))
}

// УК36, CX1-26: повтор DEFER после выдачи T2 — успех; DEFER другой причины тем
// же T1 — OUTCOME_ALREADY_RECORDED; T2 записывает свой исход.
func TestUK36_RepeatedDeferAfterTheRowWasReclaimed(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(), "a@example.invalid", hello())
	t1 := s.claim(t, 10)[0].GetLeaseToken()
	require.NoError(t, s.ack(id, t1, notifyv1.OutcomeKind_DEFER, notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE, time.Second))
	s.exec(t, `UPDATE probe_notification_outbox SET not_before = now() - interval '1 second' WHERE id = $1`, id)
	t2 := s.claim(t, 10)[0].GetLeaseToken()
	before := s.row(t, id)

	require.NoError(t, s.ack(id, t1, notifyv1.OutcomeKind_DEFER, notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE, time.Minute),
		"повтор того же DEFER — успех: defer_for в равенство не входит")
	require.Equal(t, before, s.row(t, id))

	err := s.ack(id, t1, notifyv1.OutcomeKind_DEFER, notifyv1.OutcomeReason_GRANT_SKEW, time.Second)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	reason, _ := errorInfoReason(t, err)
	require.Equal(t, "OUTCOME_ALREADY_RECORDED", reason)
	require.Equal(t, before, s.row(t, id))

	require.NoError(t, s.ack(id, t2, notifyv1.OutcomeKind_SENT, 0, 0))
	require.Equal(t, "sent", s.row(t, id).State)
}

// УК71: остаток аренды и срока — длительности, посчитанные часами базы, за
// вычетом времени ответа по монотонным часам процесса.
func TestUK71_ClaimAnswersRemainingDurationsMinusResponseTime(t *testing.T) {
	const spent = 7 * time.Second
	s := newStand(t, standOpts{clock: fixedClock(spent)})
	d := helloDesc()
	d.TTL = time.Hour
	s.putOne(t, d, "a@example.invalid", hello())
	got := s.claim(t, 10)
	require.Len(t, got, 1)
	lease := got[0].GetLeaseRemaining().AsDuration()
	require.LessOrEqual(t, lease, 5*time.Minute-spent)
	require.Greater(t, lease, 5*time.Minute-spent-5*time.Second)
	exp := got[0].GetExpiresIn().AsDuration()
	require.LessOrEqual(t, exp, time.Hour-spent)
	require.Greater(t, exp, time.Hour-spent-30*time.Second)
}
