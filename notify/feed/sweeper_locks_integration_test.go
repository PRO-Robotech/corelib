// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// pauseTrigger — фикстура пробы УК83 (М26): Put, чья транзакция выставила
// probe.pause_after = '<scope>/<window_seconds>', держит строку этого окна и
// спит, прежде чем взять следующее. Триггер живёт только в базе пробы.
const pauseTrigger = `
CREATE FUNCTION probe_pause_after_window() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF current_setting('probe.pause_after', true) = NEW.scope || '/' || NEW.window_seconds THEN
    PERFORM pg_sleep(2.5);
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER probe_pause_after_window AFTER INSERT OR UPDATE ON probe_notification_window
  FOR EACH ROW EXECUTE FUNCTION probe_pause_after_window();
`

// noLockStepRefund — инъекция УК83: оператор истечения без шага l. Строки окна
// блокирует сам UPDATE … FROM d в порядке соединения, а не в порядке ключа.
const noLockStepRefund = `
WITH x AS (
  UPDATE probe_notification_outbox SET state = 'expired',
    outcome_reason = coalesce(last_defer_reason, CASE WHEN first_claimed_at IS NULL THEN 'unclaimed' ELSE 'no_ack' END),
    outcome_token = NULL, recorded_kind = NULL, recorded_reason = NULL, secret_attrs = NULL, outcome_at = now()
  WHERE id IN (SELECT id FROM probe_notification_outbox
                WHERE state = 'pending' AND expires_at <= now() AND (lease_until IS NULL OR lease_until <= now())
                LIMIT $1 FOR UPDATE SKIP LOCKED)
  RETURNING id, outcome_reason),
d AS (
  SELECT c.template, c.scope, c.window_seconds, c.key, c.window_start, count(*) AS n
    FROM probe_notification_contrib c JOIN x ON c.notification_id = x.id
   WHERE x.outcome_reason = ANY($2)
   GROUP BY c.template, c.scope, c.window_seconds, c.key, c.window_start)
UPDATE probe_notification_window w SET count = w.count - d.n
  FROM d
 WHERE (w.template, w.scope, w.window_seconds, w.key, w.window_start)
     = (d.template, d.scope, d.window_seconds, d.key, d.window_start)`

type lockCase struct {
	limits    []feed.Limit
	initiator string
	pause     string // окно, после которого Put спит
}

// lockRace — истёкшая строка с вкладом в окна лимитов; Put той же пары ключей
// держит первое окно и спит; в паузе стартует возврат вклада при
// enable_sort = off. Возвращает ошибки Put и возврата и итоговые счётчики окон.
func lockRace(t *testing.T, c lockCase, refund func(ctx context.Context, db *pgxpool.Pool) error) (putErr, refundErr error, before, after map[string]int) {
	t.Helper()
	dsn := pgtest.NewDB(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	_, err = pool.Exec(context.Background(), pauseTrigger)
	require.NoError(t, err)

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["enable_sort"] = "off"
	noSort, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, noSort)

	f := fixtureOn(t, pool, true)
	d := helloDesc(c.limits...)
	v := hello()
	v.Initiator = c.initiator
	require.NoError(t, f.put(t, d, rcpt, v))
	_, err = pool.Exec(context.Background(), `UPDATE probe_notification_outbox SET expires_at = now() - interval '1 second'`)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `UPDATE probe_notification_window SET count = 3`)
	require.NoError(t, err)
	before = windowCounts(t, pool)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		jtx, err := journaltx.Begin(f.ctx, pool, journaltx.NewOptions(true))
		if err != nil {
			putErr = err
			return
		}
		defer func() { _ = jtx.Rollback(context.Background()) }()
		if _, err := jtx.Exec(f.ctx, `SELECT set_config('probe.pause_after', $1, true)`, c.pause); err != nil {
			putErr = err
			return
		}
		if putErr = feed.Put(f.ctx, jtx, d, rcpt, v); putErr != nil {
			return
		}
		putErr = jtx.Commit(f.ctx)
	}()
	require.Eventually(t, func() bool {
		var n int
		_ = pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event = 'PgSleep'`).Scan(&n)
		return n == 1
	}, 10*time.Second, 20*time.Millisecond, "Put не дошёл до паузы")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	refundErr = refund(ctx, noSort)
	wg.Wait()
	return putErr, refundErr, before, windowCounts(t, pool)
}

func windowCounts(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT scope || '/' || window_seconds || '/' || key, count FROM probe_notification_window`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		require.NoError(t, rows.Scan(&k, &n))
		out[k] = n
	}
	require.NoError(t, rows.Err())
	return out
}

func isDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

func feedRefund(ctx context.Context, db *pgxpool.Pool) error {
	sw, err := feed.NewSweeper(feed.SweeperConfig{Module: "probe", Service: "probe", DB: db, Metrics: prometheus.NewRegistry()})
	if err != nil {
		return err
	}
	return sw.Pass(ctx)
}

func injectedRefund(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, noLockStepRefund, feed.SweepBatch, refundWords())
	return err
}

func refundWords() []string {
	var out []string
	for _, r := range feed.RefundReasons() {
		out = append(out, string(r))
	}
	return out
}

// initiators — ключи инициатора; среди них есть дающие хеш-порядок «адресат
// первым» (М26).
var initiators = []string{"usr-a", "usr-k", "usr-q", "usr-z", "aaaa", "zzzz"}

func initiatorAndRecipient() []feed.Limit {
	return []feed.Limit{
		{Scope: feed.ScopeInitiator, WindowSeconds: 3600, Max: 100},
		{Scope: feed.ScopeRecipient, WindowSeconds: 3600, Max: 100},
	}
}

// consistent — окна согласованы: Put внёс +1 в каждое окно, возврат снял −1
// с каждого окна истёкшей строки (её вклад — во все окна лимитов).
func consistent(t *testing.T, before, after map[string]int) {
	t.Helper()
	require.Equal(t, len(before), len(after), "окна: %v → %v", before, after)
	for k, n := range before {
		require.Equal(t, n, after[k], "окно %s: Put +1 и возврат −1 обязаны сойтись (%v → %v)", k, before, after)
	}
}

// УК83, М26: Put держит окно инициатора и после паузы берёт окно адресата;
// уборщик стартует в паузе при enable_sort = off. 40P01 нет ни на одном ключе,
// окна согласованы.
func TestUK83_PutAndSweeperTakeWindowsInOneOrder(t *testing.T) {
	for _, ini := range initiators {
		t.Run(ini, func(t *testing.T) {
			putErr, refundErr, before, after := lockRace(t,
				lockCase{limits: initiatorAndRecipient(), initiator: ini, pause: "initiator/3600"}, feedRefund)
			require.NoError(t, putErr)
			require.NoError(t, refundErr)
			consistent(t, before, after)
		})
	}
}

// УК83 (инъекция): возврат без шага l — 40P01 хотя бы на одном ключе;
// близнец — строка с вкладом в одно окно: та же инъекция без 40P01.
func TestUK83_InjectionRefundWithoutTheLockStepDeadlocks(t *testing.T) {
	deadlocks := 0
	for _, ini := range initiators {
		putErr, refundErr, _, _ := lockRace(t,
			lockCase{limits: initiatorAndRecipient(), initiator: ini, pause: "initiator/3600"}, injectedRefund)
		if isDeadlock(putErr) || isDeadlock(refundErr) {
			deadlocks++
		}
		t.Logf("ключ %s: Put %v, возврат %v", ini, putErr, refundErr)
	}
	require.Positive(t, deadlocks, "инъекция без шага l не дала 40P01 ни на одном ключе — проба слепа")
	t.Logf("40P01 на %d ключах из %d", deadlocks, len(initiators))

	putErr, refundErr, _, _ := lockRace(t, lockCase{
		limits: []feed.Limit{{Scope: feed.ScopeRecipient, WindowSeconds: 3600, Max: 100}},
		pause:  "recipient/3600",
	}, injectedRefund)
	require.False(t, isDeadlock(putErr) || isDeadlock(refundErr), "одно окно: %v / %v", putErr, refundErr)
}

// УК86 (близнец УК83): одна область, две длины окна — 1800 и 86400. Put идёт по
// возрастанию window_seconds, уборщик — тем же порядком: 40P01 нет.
func TestUK86_OneScopeTwoWindowLengthsDoNotDeadlock(t *testing.T) {
	putErr, refundErr, before, after := lockRace(t, lockCase{
		limits: []feed.Limit{
			{Scope: feed.ScopeRecipient, WindowSeconds: 1800, Max: 100},
			{Scope: feed.ScopeRecipient, WindowSeconds: 86400, Max: 100},
		},
		pause: "recipient/1800",
	}, feedRefund)
	require.NoError(t, putErr)
	require.NoError(t, refundErr)
	consistent(t, before, after)
	require.True(t, strings.Contains(fmt.Sprint(after), "recipient/86400"))
}
