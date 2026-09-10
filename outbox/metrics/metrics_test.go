// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package metrics_test

// Unit + integration tests for the outbox metrics package.
//
// They verify that backlog_depth / oldest_pending_age / poisoned_total are
// exposed per outbox table.
//
// The metrics layer is split into:
//   - metrics.Recorder — a small interface (the prometheus client is wired by
//     the service at the composition root; corelib stays dependency-light and
//     testable via an in-memory recorder).
//   - metrics.MemRecorder — the in-memory test/default implementation.
//   - metrics.Collector — periodically scans an outbox table for backlog_depth
//     and oldest_pending_age_seconds and feeds them into the Recorder.
//
// The poisoned_total counter is incremented by the drainer when it poisons a
// row (verified end-to-end in the drainer integration tests); here we verify
// the Collector's DB-scan gauges + the Recorder contract.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox/metrics"
	"github.com/PRO-Robotech/corelib/pgtest"
)

const schemaDDL = `
CREATE SCHEMA IF NOT EXISTS kacho_apps;
CREATE TABLE kacho_apps.fga_register_outbox (
    id            bigserial    PRIMARY KEY,
    event_type    text         NOT NULL,
    resource_kind text         NOT NULL DEFAULT '',
    resource_id   text         NOT NULL DEFAULT '',
    payload       jsonb        NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz  NOT NULL DEFAULT now(),
    sent_at       timestamptz,
    last_error    text,
    attempt_count integer      NOT NULL DEFAULT 0
);

-- Очередь ВТОРОЙ формы: доставленность помечена не отметкой времени, а
-- состоянием. Это форма журнала аудита iam (таблица audit_outbox схемы kaname),
-- и она существовала ЗАДОЛГО до сканера: сканер читал ровно одну форму и на этой
-- падал бы с «колонки sent_at нет», то есть очередь была ненаблюдаема by
-- construction.
CREATE TABLE kacho_apps.status_shaped_outbox (
    id         text        PRIMARY KEY,
    event_type text        NOT NULL,
    status     text        NOT NULL DEFAULT 'pending',
    attempts   integer     NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
`

func setupPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// schemaDDL is already in this database: it was applied once to the package's
	// template (see TestMain) and this is a clone of it.
	pool, err := pgxpool.New(ctx, pgtest.NewDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	// Applying the schema through this pool used to leave it with a live
	// connection; keep handing back a warm one.
	require.NoError(t, pool.Ping(ctx))
	return pool
}

// Test_MemRecorder_Contract — the in-memory recorder records the three series
// keyed by table label.
func Test_MemRecorder_Contract(t *testing.T) {
	t.Parallel()
	const tbl = "kacho_apps.fga_register_outbox"
	rec := metrics.NewMemRecorder()

	rec.SetBacklogDepth(tbl, 3)
	rec.SetOldestPendingAgeSeconds(tbl, 12.5)
	rec.IncPoisoned(tbl)
	rec.IncPoisoned(tbl)

	assert.Equal(t, float64(3), rec.BacklogDepth(tbl))
	assert.Equal(t, 12.5, rec.OldestPendingAgeSeconds(tbl))
	assert.Equal(t, float64(2), rec.PoisonedTotal(tbl),
		"poisoned_total is a historic counter (monotonic)")
}

// Test_1_4_23_CollectorScan_BacklogAndOldest — Collector.Scan reports backlog
// depth and oldest-pending age over the outbox table.
//
// 3 pending intents + 1 already-sent + 1 poisoned-looking row → Collector.Scan
// reports backlog_depth==3 (only pending), oldest_pending_age_seconds>0.
func Test_1_4_23_CollectorScan_BacklogAndOldest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)
	const tbl = "kacho_apps.fga_register_outbox"

	// 3 pending (sent_at NULL), one created clearly in the past.
	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, created_at)
		 VALUES ('fga.register', now() - interval '30 seconds')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type) VALUES ('fga.register'), ('fga.register')`)
	require.NoError(t, err)
	// 1 already sent — must NOT count toward backlog.
	_, err = pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, sent_at) VALUES ('fga.register', now())`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{
		Table:       tbl,
		MaxAttempts: 10,
	})
	require.NoError(t, col.Scan(ctx))

	assert.Equal(t, float64(3), rec.BacklogDepth(tbl),
		"backlog_depth counts only pending (sent_at NULL) rows")
	assert.Greater(t, rec.OldestPendingAgeSeconds(tbl), float64(20),
		"oldest_pending_age_seconds reflects the oldest pending row (~30s)")
}

// Test_1_4_23_CollectorScan_PoisonedCount — Collector also reports the count of
// poisoned (attempt_count >= MaxAttempts AND sent_at NULL) rows so an operator
// can alert without waiting for a fresh poison-increment.
func Test_1_4_23_CollectorScan_PoisonedCount(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)
	const tbl = "kacho_apps.fga_register_outbox"

	// 1 poisoned: attempt_count == MaxAttempts, sent_at NULL.
	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, attempt_count, last_error)
		 VALUES ('fga.register', 10, 'permanent')`)
	require.NoError(t, err)
	// 1 pending (not poisoned).
	_, err = pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type) VALUES ('fga.register')`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{Table: tbl, MaxAttempts: 10})
	require.NoError(t, col.Scan(ctx))

	assert.Equal(t, float64(1), rec.PoisonedCount(tbl),
		"collector sets the poisoned-count gauge to the current count of poisoned rows")
	assert.Equal(t, float64(2), rec.BacklogDepth(tbl),
		"poisoned rows are still pending → counted in backlog (sent_at NULL)")
}

// ── ФОРМА ОЧЕРЕДИ ОБЪЯВЛЯЕТСЯ, А НЕ ПОДРАЗУМЕВАЕТСЯ ──────────────────────────

// TestIntegration_StatusShapedQueueIsScannable — сканер читает очередь, которая
// помечает доставленность СОСТОЯНИЕМ, а не отметкой времени.
//
// Почему это не «ещё одна конфигурация»: пока форма подразумевалась, очередь
// иной формы была ненаблюдаема НЕ потому, что кто-то забыл её провязать, а
// потому, что провязка упала бы на первом же скане — «колонки sent_at нет». То
// есть отсутствие наблюдаемости выглядело как её ненужность.
func TestIntegration_StatusShapedQueueIsScannable(t *testing.T) {
	pool := setupPG(t)
	ctx := context.Background()

	const table = "kacho_apps.status_shaped_outbox"
	_, err := pool.Exec(ctx, `INSERT INTO `+table+` (id, event_type, status, attempts, created_at) VALUES
		('e1', 'a.b', 'pending',   0, now() - interval '90 seconds'),
		('e2', 'a.b', 'in_flight', 2, now() - interval '30 seconds'),
		('e3', 'a.b', 'failed',    9, now() - interval '10 seconds'),
		('e4', 'a.b', 'sent',      1, now() - interval '600 seconds')`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{
		Table:       table,
		MaxAttempts: 5,
		Shape:       metrics.ShapeStatus,
	})
	require.NoError(t, col.Scan(ctx))

	// Недоставленными считаются ВСЕ, кроме `sent`: строка `failed` — это backlog,
	// а не «обработана». Зачесть её в доставленные значило бы объявить очередь
	// разгруженной ровно тогда, когда доставка перестала получаться.
	assert.Equal(t, 3.0, rec.BacklogDepth(table), "недоставленные: pending + in_flight + failed")

	// Голова считается по НЕДОСТАВЛЕННЫМ. Самая старая строка таблицы — `sent`
	// (600 c); если бы предикат её захватывал, возраст был бы ~600, и очередь
	// выглядела бы застрявшей вшестеро сильнее, чем есть.
	assert.InDelta(t, 90.0, rec.OldestPendingAgeSeconds(table), 15.0,
		"возраст старейшей НЕДОСТАВЛЕННОЙ строки, а не старейшей вообще")

	// Отравление считается по своей колонке: у этой формы она `attempts`.
	assert.Equal(t, 1.0, rec.PoisonedCount(table), "attempts >= 5 и не sent — одна строка")
}

// TestIntegration_DefaultShapeIsUnchanged — ПАРНЫЙ ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ.
//
// Без него проба выше зеленела бы и на сканере, который перестал понимать
// прежнюю форму: «новая форма читается» и «старая сломана» — совместимые
// утверждения, и различает их только этот прогон.
func TestIntegration_DefaultShapeIsUnchanged(t *testing.T) {
	pool := setupPG(t)
	ctx := context.Background()

	const table = "kacho_apps.fga_register_outbox"
	_, err := pool.Exec(ctx, `INSERT INTO `+table+` (event_type, attempt_count, created_at, sent_at) VALUES
		('a.b', 0, now() - interval '45 seconds', NULL),
		('a.b', 7, now() - interval '20 seconds', NULL),
		('a.b', 0, now() - interval '900 seconds', now())`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{
		Table: table, MaxAttempts: 5,
		// Shape НЕ задана — умолчание обязано остаться прежним.
	})
	require.NoError(t, col.Scan(ctx))

	assert.Equal(t, 2.0, rec.BacklogDepth(table))
	assert.InDelta(t, 45.0, rec.OldestPendingAgeSeconds(table), 15.0)
	assert.Equal(t, 1.0, rec.PoisonedCount(table))
}

// TestIntegration_ShapePredicatesPartitionTheTable — «недоставлено» и
// «доставлено» РАЗБИВАЮТ таблицу, а не пересекаются и не теряют строк.
//
// Без этой пробы комментарий у `queueShapeSQL` утверждал бы дополнительность
// предикатов, которую никто не проверял, — то есть был бы обещанием в шапке
// структуры. Форма, у которой они пересеклись бы, публиковала бы глубину больше
// числа строк; форма, у которой они не покрывают таблицу, — молча теряла бы
// строки из обеих величин, и очередь выглядела бы разгруженной.
//
// Спрашивается напрямую у БД теми же выражениями, что исполняет сканер: вторая
// их запись здесь разошлась бы с первой ровно там, где обе печатают «сошлось».
func TestIntegration_ShapePredicatesPartitionTheTable(t *testing.T) {
	pool := setupPG(t)
	ctx := context.Background()

	cases := []struct {
		name               string
		table              string
		pending, delivered string
		seed               string
	}{
		{
			name: "форма отметки времени", table: "kacho_apps.fga_register_outbox",
			pending: "sent_at IS NULL", delivered: "sent_at IS NOT NULL",
			seed: `INSERT INTO kacho_apps.fga_register_outbox (event_type, sent_at) VALUES
				('p.q', NULL), ('p.q', NULL), ('p.q', now())`,
		},
		{
			name: "форма состояния", table: "kacho_apps.status_shaped_outbox",
			pending: "status <> 'sent'", delivered: "status = 'sent'",
			seed: `INSERT INTO kacho_apps.status_shaped_outbox (id, event_type, status) VALUES
				('p1', 'p.q', 'pending'), ('p2', 'p.q', 'in_flight'),
				('p3', 'p.q', 'failed'),  ('p4', 'p.q', 'sent')`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, c.seed)
			require.NoError(t, err)

			var total, pending, delivered, both int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT
				  count(*),
				  count(*) FILTER (WHERE `+c.pending+`),
				  count(*) FILTER (WHERE `+c.delivered+`),
				  count(*) FILTER (WHERE (`+c.pending+`) AND (`+c.delivered+`))
				FROM `+c.table).Scan(&total, &pending, &delivered, &both))

			require.NotZero(t, total, "таблица пуста: на пустой разбиение выполняется "+
				"тождественно, и проба ничего бы не проверила")
			assert.Zero(t, both, "предикаты ПЕРЕСЕКАЮТСЯ: строка учтена и недоставленной, "+
				"и доставленной — глубина станет больше числа строк")
			assert.Equal(t, total, pending+delivered,
				"предикаты НЕ ПОКРЫВАЮТ таблицу: строки выпали из обеих величин, и очередь "+
					"выглядит разгруженной на величину потери")
		})
	}
}
