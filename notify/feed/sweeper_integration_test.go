// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/retention"
)

func (s *stand) sweeper(t *testing.T) *feed.Sweeper {
	t.Helper()
	return sweeperOn(t, s.pool, s.reg)
}

func sweeperOn(t *testing.T, db feed.DB, reg prometheus.Registerer) *feed.Sweeper {
	t.Helper()
	sw, err := feed.NewSweeper(feed.SweeperConfig{Module: "probe", Service: "probe", DB: db, Metrics: reg})
	require.NoError(t, err)
	return sw
}

func (s *stand) sweep(t *testing.T) {
	t.Helper()
	require.NoError(t, s.sweeper(t).Pass(context.Background()))
}

func (s *stand) windowCount(t *testing.T, key string) int {
	t.Helper()
	return s.count(t, `SELECT coalesce(sum(count), 0)::int FROM probe_notification_window WHERE key = $1`, key)
}

// setWindow — счётчик окна адресата до прохода (посев вместо постановок).
func (s *stand) setWindow(t *testing.T, key string, n int) {
	t.Helper()
	s.exec(t, `UPDATE probe_notification_window SET count = $2 WHERE key = $1`, key, n)
}

// deferred — строка выдана, получила DEFER(reason) и снова готова к выдаче.
func (s *stand) deferred(t *testing.T, id string, reason notifyv1.OutcomeReason) {
	t.Helper()
	got := s.claim(t, 10)
	require.Len(t, got, 1)
	require.NoError(t, s.ack(id, got[0].GetLeaseToken(), notifyv1.OutcomeKind_DEFER, reason, time.Second))
	s.exec(t, `UPDATE probe_notification_outbox SET not_before = now() - interval '1 second' WHERE id = $1`, id)
}

const rcpt = "user@example.invalid"

// NTF1-B15, B16: истечение после DEFER(platform_unavailable) возвращает вклад;
// после DEFER(template_skew) — нет. Секрет стёрт, одной транзакцией.
func TestNTF1B15B16_ExpiryRefundsOnlyPlatformFault(t *testing.T) {
	for _, tc := range []struct {
		reason notifyv1.OutcomeReason
		word   string
		after  int
	}{
		{notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE, "platform_unavailable", 2},
		{notifyv1.OutcomeReason_TEMPLATE_SKEW, "template_skew", 3},
	} {
		t.Run(tc.word, func(t *testing.T) {
			s := newStand(t, standOpts{})
			id := s.putOne(t, secretDesc(perHour(5)), rcpt, secretValues("x"))
			s.setWindow(t, rcpt, 3)
			s.deferred(t, id, tc.reason)
			s.expire(t, id)
			s.sweep(t)
			r := s.row(t, id)
			require.Equal(t, "expired", r.State)
			require.Equal(t, tc.word, r.Reason)
			require.False(t, r.HasSecret)
			require.True(t, r.HasOutcomeAt)
			require.Empty(t, r.OutcomeToken, "исход уборщика не несёт токена (CX1-26 (г))")
			require.Empty(t, r.RecordedKind)
			require.Equal(t, tc.after, s.windowCount(t, rcpt))
			require.Equal(t, 1.0, outcomes(t, s.reg, "notice", "expired", tc.word))
		})
	}
}

// NTF1-B25: уборщик не закрывает строку под действующей арендой; Ack в
// пределах аренды — sent, вклад не возвращён; без Ack после конца аренды —
// EXPIRED(platform_unavailable) и запоздалый Ack — LEASE_LOST.
func TestNTF1B25_SweeperRespectsALiveLease(t *testing.T) {
	prep := func(t *testing.T) (*stand, string, string) {
		s := newStand(t, standOpts{})
		id := s.putOne(t, secretDesc(perHour(5)), rcpt, secretValues("x"))
		s.setWindow(t, rcpt, 3)
		s.deferred(t, id, notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE)
		got := s.claim(t, 10)
		require.Len(t, got, 1)
		s.expire(t, id)
		s.sweep(t)
		r := s.row(t, id)
		require.Equal(t, "pending", r.State)
		require.True(t, r.HasSecret)
		require.Equal(t, 3, s.windowCount(t, rcpt))
		return s, id, got[0].GetLeaseToken()
	}
	t.Run("Ack в пределах аренды", func(t *testing.T) {
		s, id, tok := prep(t)
		require.NoError(t, s.ack(id, tok, notifyv1.OutcomeKind_SENT, 0, 0))
		r := s.row(t, id)
		require.Equal(t, "sent", r.State)
		require.False(t, r.HasSecret)
		require.Equal(t, 3, s.windowCount(t, rcpt))
	})
	t.Run("без Ack", func(t *testing.T) {
		s, id, tok := prep(t)
		s.endLease(t, id)
		s.sweep(t)
		r := s.row(t, id)
		require.Equal(t, "expired", r.State)
		require.Equal(t, "platform_unavailable", r.Reason)
		require.Equal(t, 2, s.windowCount(t, rcpt))
		err := s.ack(id, tok, notifyv1.OutcomeKind_SENT, 0, 0)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		reason, _ := errorInfoReason(t, err)
		require.Equal(t, "LEASE_LOST", reason)
		require.Equal(t, r, s.row(t, id))
	})
}

// NTF1-B31: каждая строка получает терминальный исход с причиной. (а) не
// выдана — unclaimed, вклад возвращён; (б) выдана и брошена — no_ack, вклад не
// возвращён (УК66: аренда истекла, больше не выдана); (в) флаг выключен после
// постановки — уборщик работает. До срока строка pending. Близнец B15 —
// причина последней отсрочки.
func TestNTF1B31_EveryRowGetsATerminalOutcome(t *testing.T) {
	put := func(t *testing.T) (*stand, string) {
		s := newStand(t, standOpts{})
		d := secretDesc(perHour(5))
		d.TTL = 5 * time.Minute
		id := s.putOne(t, d, rcpt, secretValues("x"))
		s.setWindow(t, rcpt, 3)
		return s, id
	}
	pendingUntilDue := func(t *testing.T, s *stand, id string) {
		s.exec(t, `UPDATE probe_notification_outbox SET expires_at = now() + interval '1 second' WHERE id = $1`, id)
		s.sweep(t)
		require.Equal(t, "pending", s.row(t, id).State, "уборщик закрыл строку до срока")
		s.expire(t, id)
	}
	t.Run("(а) unclaimed", func(t *testing.T) {
		s, id := put(t)
		pendingUntilDue(t, s, id)
		s.sweep(t)
		r := s.row(t, id)
		require.Equal(t, "expired", r.State)
		require.Equal(t, "unclaimed", r.Reason)
		require.False(t, r.HasSecret)
		require.Equal(t, 2, s.windowCount(t, rcpt))
		require.Equal(t, 1.0, outcomes(t, s.reg, "notice", "expired", "unclaimed"))
	})
	t.Run("(б) no_ack", func(t *testing.T) {
		s, id := put(t)
		require.Len(t, s.claim(t, 10), 1)
		pendingUntilDue(t, s, id)
		s.endLease(t, id)
		s.sweep(t)
		r := s.row(t, id)
		require.Equal(t, "expired", r.State)
		require.Equal(t, "no_ack", r.Reason)
		require.False(t, r.HasSecret)
		require.Equal(t, 3, s.windowCount(t, rcpt))
		require.Equal(t, 1.0, outcomes(t, s.reg, "notice", "expired", "no_ack"))
	})
	t.Run("(в) флаг выключен после постановки", func(t *testing.T) {
		s, id := put(t)
		off := fixtureOn(t, s.pool, false)
		require.False(t, off.src.Enabled())
		pendingUntilDue(t, s, id)
		require.NoError(t, sweeperOn(t, s.pool, off.reg).Pass(context.Background()))
		r := s.row(t, id)
		require.Equal(t, "expired", r.State)
		require.Equal(t, "unclaimed", r.Reason)
		require.Equal(t, 2, s.windowCount(t, rcpt))
	})
	t.Run("близнец B15", func(t *testing.T) {
		s, id := put(t)
		s.deferred(t, id, notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE)
		pendingUntilDue(t, s, id)
		s.sweep(t)
		r := s.row(t, id)
		require.Equal(t, "platform_unavailable", r.Reason)
		require.Equal(t, 2, s.windowCount(t, rcpt))
	})
	t.Run("(предел) ttl 720h", func(t *testing.T) {
		s := newStand(t, standOpts{})
		d := helloDesc()
		d.TTL = feed.TTLMax
		id := s.putOne(t, d, rcpt, hello())
		require.Equal(t, 1, s.count(t, `SELECT count(*) FROM probe_notification_outbox
			WHERE id = $1 AND expires_at = enqueued_at + interval '720 hours'`, id))
	})
}

// УК66, CX1-55 (б): два уборщика двух реплик над одной строкой — строк
// EXPIRED 1, вклад возвращён ровно один раз.
func TestUK66_TwoSweepersRefundOnce(t *testing.T) {
	s := newStand(t, standOpts{})
	id := s.putOne(t, helloDesc(perHour(5)), rcpt, hello())
	s.setWindow(t, rcpt, 3)
	s.expire(t, id)
	a, b := s.sweeper(t), s.sweeper(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, sw := range []*feed.Sweeper{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = sw.Pass(context.Background())
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Equal(t, 1, s.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE state = 'expired'`))
	require.Equal(t, 2, s.windowCount(t, rcpt))
}

// УК79, CX1-62: две строки unclaimed одного адресата в одном окне — счётчик
// 5 → 3; близнец — строки в разных окнах, по −1.
func TestUK79_RefundIsSummedPerWindow(t *testing.T) {
	t.Run("одно окно", func(t *testing.T) {
		s := newStand(t, standOpts{})
		a := s.putOne(t, helloDesc(perHour(9)), rcpt, hello())
		b := s.putOne(t, helloDesc(perHour(9)), rcpt, hello())
		s.setWindow(t, rcpt, 5)
		s.expire(t, a)
		s.expire(t, b)
		s.sweep(t)
		require.Equal(t, 3, s.windowCount(t, rcpt))
	})
	t.Run("разные окна", func(t *testing.T) {
		s := newStand(t, standOpts{})
		a := s.putOne(t, helloDesc(perHour(9)), "a@example.invalid", hello())
		b := s.putOne(t, helloDesc(perHour(9)), "b@example.invalid", hello())
		s.setWindow(t, "a@example.invalid", 5)
		s.setWindow(t, "b@example.invalid", 5)
		s.expire(t, a)
		s.expire(t, b)
		s.sweep(t)
		require.Equal(t, 4, s.windowCount(t, "a@example.invalid"))
		require.Equal(t, 4, s.windowCount(t, "b@example.invalid"))
	})
}

// NTF1-B31: за проход закрывается каждая строка — пачки повторяются до нуля.
func TestSweeperPassClosesMoreThanOneBatch(t *testing.T) {
	s := newStand(t, standOpts{})
	s.putOne(t, helloDesc(), rcpt, hello())
	s.exec(t, `INSERT INTO probe_notification_outbox (id, template, schema_rev, class, recipient_address, attrs, state, expires_at)
		SELECT 'ntf-' || lpad(g::text, 17, '0'), 'probe-hello', 1, 'notice', 'x@example.invalid', '{}'::jsonb, 'pending', now() - interval '1 second'
		  FROM generate_series(1, $1::int) g`, feed.SweepBatch+7)
	s.sweep(t)
	require.Zero(t, s.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE state = 'pending' AND expires_at <= now()`))
	require.Equal(t, feed.SweepBatch+7, s.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE state = 'expired'`))
}

func subject(t *testing.T, subjects []retention.Subject, table string) retention.Subject {
	t.Helper()
	for _, sub := range subjects {
		if sub.Name == table {
			return sub
		}
	}
	t.Fatalf("предмета %s нет среди %d", table, len(subjects))
	return retention.Subject{}
}

// УК94: предметы уборки названы именами таблиц; пороги — экспортированные
// величины.
func TestUK94_RetentionSubjectsAreNamedByTheirTables(t *testing.T) {
	subs := feed.RetentionSubjects(&untouchedDB{}, "probe")
	require.Len(t, subs, 2)
	require.Equal(t, feed.ClosedRetention, subject(t, subs, `"probe_notification_outbox"`).Grace)
	require.Equal(t, feed.WindowRetention, subject(t, subs, `"probe_notification_window"`).Grace)
}

// УК76: по одной закрытой строке каждого терминального вида старше
// ClosedRetention снята вместе с contrib; pending старше срока — нет; закрытая
// моложе срока — нет.
func TestUK76_ClosedRowsAreReclaimedPendingNever(t *testing.T) {
	s := newStand(t, standOpts{})
	closed := map[string]string{
		"sent": "", "recipient_rejected": "", "denied": "revoked", "invalid": "attrs_invalid",
		"dropped": "recipient_net", "expired": "unclaimed",
	}
	old := map[string]string{}
	for state, reason := range closed {
		id := s.putOne(t, helloDesc(perHour(50)), state+"@example.invalid", hello())
		s.exec(t, `UPDATE probe_notification_outbox SET state = $2, outcome_reason = nullif($3, ''),
			outcome_at = now() - interval '169 hours' WHERE id = $1`, id, state, reason)
		old[state] = id
	}
	pendingOld := s.putOne(t, helloDesc(perHour(50)), "p@example.invalid", hello())
	s.exec(t, `UPDATE probe_notification_outbox SET enqueued_at = now() - interval '400 hours' WHERE id = $1`, pendingOld)
	young := s.putOne(t, helloDesc(perHour(50)), "y@example.invalid", hello())
	s.exec(t, `UPDATE probe_notification_outbox SET state = 'sent', outcome_at = now() - interval '167 hours' WHERE id = $1`, young)

	sub := subject(t, feed.RetentionSubjects(s.pool, "probe"), `"probe_notification_outbox"`)
	n, full, err := sub.Sweep(context.Background(), sub.Grace, 1000)
	require.NoError(t, err)
	require.False(t, full)
	require.Equal(t, int64(len(closed)), n)
	for state, id := range old {
		require.Zero(t, s.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE id = $1`, id), state)
		require.Zero(t, s.count(t, `SELECT count(*) FROM probe_notification_contrib WHERE notification_id = $1`, id), state)
	}
	require.Equal(t, 1, s.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE id = $1`, pendingOld))
	require.Equal(t, 1, s.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE id = $1`, young))
}

// УК77: окно, прошедшее больше WindowRetention назад, — снято; текущее — нет.
// УК83: строка окна, занятая писателем, уборку не задерживает.
func TestUK77_PastWindowsAreReclaimed(t *testing.T) {
	s := newStand(t, standOpts{})
	s.putOne(t, helloDesc(perHour(5)), rcpt, hello())
	s.exec(t, `INSERT INTO probe_notification_window (template, scope, window_seconds, key, window_start, count)
		VALUES ('probe-hello', 'recipient', 3600, 'old@example.invalid', now() - interval '26 hours', 1),
		       ('probe-hello', 'recipient', 3600, 'edge@example.invalid', now() - interval '24 hours', 1),
		       ('probe-hello', 'recipient', 3600, 'held@example.invalid', now() - interval '30 hours', 1)`)

	hold, err := s.pool.Begin(context.Background())
	require.NoError(t, err)
	defer func() { _ = hold.Rollback(context.Background()) }()
	_, err = hold.Exec(context.Background(), `SELECT 1 FROM probe_notification_window WHERE key = 'held@example.invalid' FOR UPDATE`)
	require.NoError(t, err)

	sub := subject(t, feed.RetentionSubjects(s.pool, "probe"), `"probe_notification_window"`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, _, err := sub.Sweep(ctx, sub.Grace, 1000)
	require.NoError(t, err, "уборка окна ждала занятую строку")
	require.Equal(t, int64(1), n)
	require.Zero(t, s.windowCount(t, "old@example.invalid"))
	require.Equal(t, 1, s.windowCount(t, "edge@example.invalid"), "окно кончилось 23 ч назад — моложе срока")
	require.Equal(t, 1, s.windowCount(t, "held@example.invalid"), "занятая строка пропущена")
	require.Equal(t, 1, s.windowCount(t, rcpt), "текущее окно")

	require.NoError(t, hold.Rollback(context.Background()))
	n, _, err = sub.Sweep(context.Background(), sub.Grace, 1000)
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "следующий проход снимает освободившуюся строку")
}

// УК82: партия закрытых строк выбирается частичным индексом без сортировки
// всех подходящих строк. Посев — форма, ради которой правило заведено:
// подходящих строк на порядки больше партии (накопленное за жизнь стенда), и
// pending рядом. Узел Incremental Sort сортирует только группы равного
// outcome_at, поданные индексом по порядку, — он сортировкой всех подходящих
// не является.
func TestUK82_ClosedBatchUsesThePartialIndex(t *testing.T) {
	s := newStand(t, standOpts{})
	s.exec(t, `INSERT INTO probe_notification_outbox (id, template, schema_rev, class, recipient_address, attrs, state, outcome_at, expires_at)
		SELECT 'ntf-' || lpad(g::text, 17, '0'), 'probe-hello', 1, 'notice', 'x@example.invalid', '{}'::jsonb,
		       CASE WHEN g % 5 = 0 THEN 'pending' ELSE 'sent' END,
		       CASE WHEN g % 5 = 0 THEN NULL ELSE now() - make_interval(hours => 200, secs => g) END,
		       now() + interval '1 hour'
		  FROM generate_series(1, 60000) g`)
	s.exec(t, `ANALYZE probe_notification_outbox`)
	rows, err := s.pool.Query(context.Background(), "EXPLAIN (COSTS OFF) "+feed.ClosedSweepStatement("probe"),
		feed.ClosedRetention.Seconds(), 1000)
	require.NoError(t, err)
	var lines []string
	for rows.Next() {
		var l string
		require.NoError(t, rows.Scan(&l))
		lines = append(lines, l)
	}
	require.NoError(t, rows.Err())
	plan := strings.Join(lines, "\n")
	t.Logf("план:\n%s", plan)
	nodes := planNodes(lines)
	require.Contains(t, nodes, "Index Scan using probe_notification_outbox_closed_idx on probe_notification_outbox probe_notification_outbox_1")
	for _, n := range nodes {
		require.NotEqual(t, "Sort", n, "узел сортировки всех подходящих строк")
	}
}

// planNodes — имена узлов плана EXPLAIN (COSTS OFF): первая строка и строки
// «->»; строки свойств узла (Sort Key, Index Cond) узлами не являются.
func planNodes(lines []string) []string {
	var out []string
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if i > 0 && !strings.HasPrefix(l, "->") {
			continue
		}
		out = append(out, strings.TrimSpace(strings.TrimPrefix(l, "->")))
	}
	return out
}

// NTF1-B20, УК75: наблюдаемость ленты. Источник без единой принятой строки
// отдаёт доставленных 0 и растущий возраст старейшей; строка без DEFER и после
// DEFER читаются без ошибки сканирования; отсроченные считаются только у
// строк с not_before в будущем.
func TestNTF1B20_FeedIsObservable(t *testing.T) {
	s := newStand(t, standOpts{})
	sw := s.sweeper(t)
	require.Equal(t, 0.0, gathered(t, s.reg, "kacho_notification_feed_delivered_total", map[string]string{"module": "probe"}))

	a := s.putOne(t, helloDesc(), "a@example.invalid", hello())
	s.putOne(t, helloDesc(), "b@example.invalid", hello())
	s.exec(t, `UPDATE probe_notification_outbox SET enqueued_at = now() - interval '90 seconds' WHERE id = $1`, a)
	require.NoError(t, sw.Pass(context.Background()))
	age := gathered(t, s.reg, "kacho_notification_feed_oldest_pending_seconds", map[string]string{"module": "probe", "class": "notice"})
	require.GreaterOrEqual(t, age, 90.0)
	require.Equal(t, 0.0, gathered(t, s.reg, "kacho_notification_feed_oldest_pending_seconds", map[string]string{"module": "probe", "class": "security"}))
	require.Equal(t, 0.0, gathered(t, s.reg, "kacho_notification_feed_deferred", map[string]string{"module": "probe", "class": "notice"}))

	s.exec(t, `UPDATE probe_notification_outbox SET enqueued_at = now() - interval '200 seconds' WHERE id = $1`, a)
	got := s.claim(t, 1)
	require.Equal(t, a, got[0].GetId())
	require.NoError(t, s.ack(a, got[0].GetLeaseToken(), notifyv1.OutcomeKind_DEFER, notifyv1.OutcomeReason_GRANT_SKEW, time.Minute))
	require.NoError(t, sw.Pass(context.Background()))
	require.GreaterOrEqual(t, gathered(t, s.reg, "kacho_notification_feed_oldest_pending_seconds",
		map[string]string{"module": "probe", "class": "notice"}), 200.0, "возраст растёт")
	require.Equal(t, 1.0, gathered(t, s.reg, "kacho_notification_feed_deferred", map[string]string{"module": "probe", "class": "notice"}))
	require.Equal(t, 0.0, gathered(t, s.reg, "kacho_notification_feed_delivered_total", map[string]string{"module": "probe"}))

	// Серии исходов заведены на каждую клетку заранее: «ноль» отличим от
	// «серии нет».
	require.Equal(t, 0.0, outcomes(t, s.reg, "security", "expired", "no_ack"))
	require.Equal(t, 0.0, outcomes(t, s.reg, "notice", "expired", "unclaimed"))
}

// З10, CX1-55 (а): StartSweeper поднимает три прохода — истечение, закрытые
// строки, прошедшие окна — и первый идёт сразу.
func TestStartSweeperRunsAllThreePasses(t *testing.T) {
	s := newStand(t, standOpts{})
	expiring := s.putOne(t, helloDesc(), "e@example.invalid", hello())
	s.expire(t, expiring)
	closed := s.putOne(t, helloDesc(), "c@example.invalid", hello())
	s.exec(t, `UPDATE probe_notification_outbox SET state = 'sent', outcome_at = now() - interval '200 hours' WHERE id = $1`, closed)
	s.exec(t, `INSERT INTO probe_notification_window (template, scope, window_seconds, key, window_start, count)
		VALUES ('probe-hello', 'recipient', 3600, 'old@example.invalid', now() - interval '30 hours', 1)`)

	ctx, cancel := context.WithCancel(context.Background())
	sw, err := feed.StartSweeper(ctx, feed.SweeperConfig{Module: "probe", Service: "probe", DB: s.pool, Metrics: s.reg})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return s.row(t, expiring).State == "expired" &&
			s.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE id = $1`, closed) == 0 &&
			s.windowCount(t, "old@example.invalid") == 0
	}, 20*time.Second, 100*time.Millisecond)
	cancel()
	require.True(t, sw.Wait(10*time.Second), "петли уборщика не остановились")
}
