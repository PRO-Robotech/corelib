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
	limits []feed.Limit
	pause  string    // окно, после которого Put спит: '<scope>/<window_seconds>'
	clock  time.Time // часы базы пробы; нулевое — настоящие
}

// lockEnv — база одного ключа: истёкшая строка с вкладом в окна лимитов,
// счётчики окон подняты до 3.
type lockEnv struct {
	pool, noSort *pgxpool.Pool
	f            *fixture
	d            feed.TemplateDesc
	v            feed.Values
	recipient    string
	before       map[string]int
}

// lockSetup — истёкшая строка с вкладом в окна лимитов на ключах initiator и
// recipient; второй пул ходит при enable_sort = off.
func lockSetup(t *testing.T, c lockCase, initiator, recipient string) *lockEnv {
	t.Helper()
	dsn := pgtest.NewDB(t)
	if !c.clock.IsZero() {
		pinClock(t, dsn, c.clock)
	}
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

	e := &lockEnv{pool: pool, noSort: noSort, f: fixtureOn(t, pool, true), d: helloDesc(c.limits...), v: hello(), recipient: recipient}
	e.v.Initiator = initiator
	require.NoError(t, e.f.put(t, e.d, recipient, e.v))
	_, err = pool.Exec(context.Background(), `UPDATE probe_notification_outbox SET expires_at = now() - interval '1 second'`)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `UPDATE probe_notification_window SET count = 3`)
	require.NoError(t, err)
	e.before = windowCounts(t, pool)
	return e
}

// unlockedOrder — порядок, в котором возврат без шага l взял бы строки окон в
// этой базе сейчас: тот же оператор тем же пулом (enable_sort = off) с
// RETURNING, транзакция откатывается. Порядок — хеш-порядок группировки d, и
// он зависит от значений ключа окна, в том числе от window_start, то есть от
// часа базы: при фиксированных ключах проба то строит условие гонки, то нет.
func (e *lockEnv) unlockedOrder(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	tx, err := e.noSort.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, noLockStepRefund+` RETURNING w.scope || '/' || w.window_seconds`, feed.SweepBatch, refundWords())
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var w string
		require.NoError(t, rows.Scan(&w))
		out = append(out, w)
	}
	require.NoError(t, rows.Err())
	require.Len(t, out, len(e.before), "возврат без шага l обязан взять каждое окно истёкшей строки")
	return out
}

// race — Put той же пары ключей держит окно c.pause и спит; в паузе стартует
// возврат вклада пулом enable_sort = off. Возвращает итоговые счётчики окон и
// ошибки Put и возврата.
func (e *lockEnv) race(t *testing.T, c lockCase, refund func(ctx context.Context, db *pgxpool.Pool) error) (after map[string]int, putErr, refundErr error) {
	t.Helper()
	f := e.f
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		jtx, err := journaltx.Begin(f.ctx, e.pool, journaltx.NewOptions(true))
		if err != nil {
			putErr = err
			return
		}
		defer func() { _ = jtx.Rollback(context.Background()) }()
		if _, err := jtx.Exec(f.ctx, `SELECT set_config('probe.pause_after', $1, true)`, c.pause); err != nil {
			putErr = err
			return
		}
		if putErr = feed.Put(f.ctx, jtx, e.d, e.recipient, e.v); putErr != nil {
			return
		}
		putErr = jtx.Commit(f.ctx)
	}()
	require.Eventually(t, func() bool {
		var n int
		_ = e.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event = 'PgSleep'`).Scan(&n)
		return n == 1
	}, 10*time.Second, 20*time.Millisecond, "Put не дошёл до паузы")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	refundErr = refund(ctx, e.noSort)
	wg.Wait()
	return windowCounts(t, e.pool), putErr, refundErr
}

// raceOutcome — исход гонки на ключе, где возврат без шага l взял бы окна
// против порядка Put.
type raceOutcome struct {
	initiator         string
	putErr, refundErr error
	before, after     map[string]int
}

// candidateKeys — сколько пар ключей перебирает проба, ища пары, на которых
// возврат без шага l берёт окна против порядка Put. Доля таких пар в окне
// часа замерена от 45% до 76% (группировка тех же пяти колонок при
// enable_sort = off, 200 ключей, окна 11, 12 и 21 часа UTC 2026-10-05,
// postgres:16.15), так что 32 кандидата без единой нужной пары — ~4e-9.
const candidateKeys = 32

// adversarialRaces перебирает пары ключей (инициатор, адресат) и проводит
// гонку только на тех, где возврат без шага l взял бы первым окно, отличное от
// c.pause, — там, где Put держит одно окно и ждёт второе, а возврат держит
// второе и ждёт первое. На прочих ключах условие гонки не строится: и
// инъекция, и настоящий уборщик там зелены при любом порядке, и такой зелёный
// ничего не судит. Набирает want исходов; ни одного — отказ пробы.
func adversarialRaces(t *testing.T, c lockCase, refund func(ctx context.Context, db *pgxpool.Pool) error, want int) []raceOutcome {
	t.Helper()
	var out []raceOutcome
	inOrder, shifted := 0, 0
	seen := 0
	for i := 0; i < candidateKeys && len(out) < want; i++ {
		seen++
		ini := fmt.Sprintf("usr-%02d", i)
		e := lockSetup(t, c, ini, fmt.Sprintf("u%02d@example.invalid", i))
		if order := e.unlockedOrder(t); order[0] == c.pause {
			inOrder++
			continue
		}
		after, putErr, refundErr := e.race(t, c, refund)
		if len(after) != len(e.before) {
			// Put гонки попал в следующее окно часов: строк, за которые
			// спорят, больше нет — прогон ключа не судит ничего.
			shifted++
			continue
		}
		out = append(out, raceOutcome{initiator: ini, putErr: putErr, refundErr: refundErr, before: e.before, after: after})
	}
	t.Logf("ключей осмотрено %d из %d: против порядка Put %d, по порядку Put %d, окно сменилось %d",
		seen, candidateKeys, len(out)+shifted, inOrder, shifted)
	require.NotEmpty(t, out, "ни на одном из %d ключей возврат без шага l не берёт окна против порядка Put — условие гонки не построено", seen)
	return out
}

// pinClock ставит базе пробы часы, идущие от at: now() без схемы разрешается в
// probe_clock.now() — at плюс время, прошедшее с постановки. Окно лимита
// (date_bin от now()) и с ним хеш группы возврата берутся от этих часов, а не
// от часа прогона. Соединения, открытые после, получают путь поиска базы.
func pinClock(t *testing.T, dsn string, at time.Time) {
	t.Helper()
	conn, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Exec(context.Background(), fmt.Sprintf(`
CREATE SCHEMA probe_clock;
DO $do$ BEGIN
  EXECUTE format('CREATE FUNCTION probe_clock.now() RETURNS timestamptz LANGUAGE sql STABLE AS %%L',
    format('SELECT %%L::timestamptz + (pg_catalog.transaction_timestamp() - %%L::timestamptz)', %s, pg_catalog.now()));
  EXECUTE format('ALTER DATABASE %%I SET search_path = probe_clock, pg_catalog, public', current_database());
END $do$;`, "'"+at.UTC().Format(time.RFC3339Nano)+"'"))
	require.NoError(t, err)
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

// racesPerProbe — сколько ключей против порядка Put судит каждая проба.
const racesPerProbe = 3

// pinnedHour — часы базы прогона 37305233709 (окно 11:00 UTC 2026-10-05): при
// прежних шести фиксированных ключах возврат без шага l брал на всех шести
// окно инициатора первым, и инъекция не дала 40P01 ни на одном.
var pinnedHour = time.Date(2026, 10, 5, 11, 30, 0, 0, time.UTC)

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
// уборщик стартует в паузе при enable_sort = off. Ключи — те, где возврат без
// шага l взял бы окно адресата первым. 40P01 нет ни на одном ключе, окна
// согласованы.
func TestUK83_PutAndSweeperTakeWindowsInOneOrder(t *testing.T) {
	putAndSweeperInOneOrder(t, time.Time{})
}

// УК83 при часах прогона 37305233709.
func TestUK83_PutAndSweeperTakeWindowsInOneOrderInTheWindowOfRun37305233709(t *testing.T) {
	putAndSweeperInOneOrder(t, pinnedHour)
}

func putAndSweeperInOneOrder(t *testing.T, clock time.Time) {
	t.Helper()
	c := lockCase{limits: initiatorAndRecipient(), pause: "initiator/3600", clock: clock}
	for _, r := range adversarialRaces(t, c, feedRefund, racesPerProbe) {
		require.NoError(t, r.putErr, "ключ %s", r.initiator)
		require.NoError(t, r.refundErr, "ключ %s", r.initiator)
		consistent(t, r.before, r.after)
	}
}

// УК83 (инъекция): возврат без шага l — 40P01 на КАЖДОМ ключе, где он берёт
// окна против порядка Put; близнец — строка с вкладом в одно окно: та же
// инъекция без 40P01.
func TestUK83_InjectionRefundWithoutTheLockStepDeadlocks(t *testing.T) {
	injectionWithoutTheLockStep(t, time.Time{})
}

// УК83 (инъекция) при часах прогона 37305233709: окно, в котором прежние
// фиксированные ключи не строили условие гонки.
func TestUK83_InjectionDeadlocksInTheWindowOfRun37305233709(t *testing.T) {
	injectionWithoutTheLockStep(t, pinnedHour)
}

func injectionWithoutTheLockStep(t *testing.T, clock time.Time) {
	t.Helper()
	c := lockCase{limits: initiatorAndRecipient(), pause: "initiator/3600", clock: clock}
	races := adversarialRaces(t, c, injectedRefund, racesPerProbe)
	deadlocks := 0
	for _, r := range races {
		t.Logf("ключ %s: Put %v, возврат %v", r.initiator, r.putErr, r.refundErr)
		if isDeadlock(r.putErr) || isDeadlock(r.refundErr) {
			deadlocks++
		}
	}
	require.Equal(t, len(races), deadlocks, "инъекция без шага l дала 40P01 на %d ключах из %d, где возврат берёт окна против порядка Put — проба слепа", deadlocks, len(races))
	t.Logf("40P01 на %d ключах из %d", deadlocks, len(races))

	one := lockCase{limits: []feed.Limit{{Scope: feed.ScopeRecipient, WindowSeconds: 3600, Max: 100}}, pause: "recipient/3600", clock: clock}
	e := lockSetup(t, one, "", rcpt)
	_, putErr, refundErr := e.race(t, one, injectedRefund)
	require.False(t, isDeadlock(putErr) || isDeadlock(refundErr), "одно окно: %v / %v", putErr, refundErr)
}

// УК86 (близнец УК83): одна область, две длины окна — 1800 и 86400. Put идёт по
// возрастанию window_seconds, уборщик — тем же порядком: 40P01 нет. Ключи
// адресата — те, где возврат без шага l взял бы окно 86400 первым.
func TestUK86_OneScopeTwoWindowLengthsDoNotDeadlock(t *testing.T) {
	c := lockCase{
		limits: []feed.Limit{
			{Scope: feed.ScopeRecipient, WindowSeconds: 1800, Max: 100},
			{Scope: feed.ScopeRecipient, WindowSeconds: 86400, Max: 100},
		},
		pause: "recipient/1800",
	}
	for _, r := range adversarialRaces(t, c, feedRefund, racesPerProbe) {
		require.NoError(t, r.putErr)
		require.NoError(t, r.refundErr)
		consistent(t, r.before, r.after)
		require.True(t, strings.Contains(fmt.Sprint(r.after), "recipient/86400"))
	}
}
