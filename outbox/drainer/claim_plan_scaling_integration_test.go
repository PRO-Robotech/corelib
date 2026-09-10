// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer

// Integration test: the partition-head-only CLAIM must stay CONSTANT-cost in
// backlog depth.
//
// # Root cause this locks (throughput inversion of kaname fga_outbox)
//
//	The partition-head-only claim (Config.PartitionColumn) reads
//
//	    SELECT t.id FROM <t> t
//	     WHERE t.sent_at IS NULL AND t.attempt_count < $1
//	       AND NOT EXISTS (SELECT 1 FROM <t> p
//	                        WHERE p.sent_at IS NULL AND p.attempt_count < $1
//	                          AND p.id < t.id AND p.<part> = t.<part>)
//	     ORDER BY t.attempt_count, t.id
//	     FOR UPDATE OF t SKIP LOCKED
//	     LIMIT $2
//
//	The service migration supplied the index for the correlated NOT EXISTS
//	(`((<part>), id) WHERE sent_at IS NULL`) but NOT one for the OUTER ordered
//	scan `ORDER BY (attempt_count, id)` over `WHERE sent_at IS NULL AND
//	attempt_count < $1`. With no index able to produce that order, Postgres never
//	reaches the nested-loop anti-join the partition index was built for: it
//	seq-scans the WHOLE pending backlog, sorts it, and hash-anti-joins it against
//	a second full seq-scan of the same backlog. The partition index is then dead
//	weight and the claim degrades to O(backlog).
//
//	Measured on a live stand (kind, Postgres 16, real fga_outbox rows):
//
//	    backlog   claim (no ordering index)   claim (with ordering index)
//	      5 000               11.7 ms                     0.81 ms
//	     10 000               28.2 ms                     0.66 ms
//	     20 000               61.6 ms                     0.72 ms
//	     40 000              105.7 ms                     0.75 ms
//	     80 000              327.0 ms                     0.82 ms
//
//	That shape is the throughput INVERSION mechanism, not merely slowness: the
//	deeper the queue, the slower each claim, the slower the drain, the deeper the
//	queue. A producer only marginally faster than the consumer therefore diverges
//	without bound instead of settling at a small standing lag. On the measured
//	e2e run a ~150 rows/s producer against a ~147 rows/s consumer built a 70+
//	second materialization lag — long enough for a first repository create to
//	answer 404 for 64 s while the gateway scope-extractor could not yet resolve
//	it.
//
// # What is asserted (behaviour, not structure)
//
//	The claim's EXECUTION does not scale with backlog depth: rows examined at 8x
//	the depth stay within a small constant factor, and no plan node seq-scans the
//	outbox table. Asserting the plan rather than wall-clock keeps the test
//	deterministic (no timing flake) while still failing on exactly the regression
//	that hurt: a claim whose work grows with the queue it is meant to drain.
//
// Run: go test ./pkg/outbox/drainer/... -run ClaimPlan -race

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// claimPlanTable is the canonical outbox table under test, created with EXACTLY
// the index set kaname ships for kaname.fga_outbox (migrations 0002 + 0063 +
// 0067). Kept in sync with services/iam/internal/migrations — the iam side owns
// the migration, this mirror lets the drainer prove the claim plan the index set
// produces.
//
// The set is exactly TWO partial indexes, and the SECOND half of that statement is
// as load-bearing as the first: every partial index on `sent_at IS NULL` in an
// order OTHER than the claim's is a decoy the planner will reach for under the
// empty-queue statistics a queue table carries into a burst (see
// Test_ClaimPlan_StaleStatistics_DoesNotCollapse).
const claimPlanTable = `
CREATE TABLE outbox (
    id            bigserial    PRIMARY KEY,
    event_type    text         NOT NULL,
    payload       jsonb        NOT NULL,
    created_at    timestamptz  NOT NULL DEFAULT now(),
    sent_at       timestamptz,
    last_error    text,
    attempt_count integer      NOT NULL DEFAULT 0,
    tuple_key     text
);

-- migration 0067: correlated NOT EXISTS (partition-head predecessor lookup) over
-- the ordering key — the full tuple identity (user, relation, object).
CREATE INDEX outbox_tuple_head_idx
    ON outbox (tuple_key, id) WHERE sent_at IS NULL;

-- migration 0063: OUTER ordered scan ORDER BY (attempt_count, id). Without it the
-- planner cannot use outbox_tuple_head_idx at all and the claim degrades to a
-- double seq-scan of the whole pending backlog.
CREATE INDEX outbox_claim_order_idx
    ON outbox (attempt_count, id) WHERE sent_at IS NULL;

-- migration 0068 removed a third one, (created_at) WHERE sent_at IS NULL, and it
-- must NOT come back: it is not read by anything, and it is the access path the
-- planner reaches for under empty-queue statistics, forcing the Sort that costs the
-- claim its early stop. See Test_ClaimPlan_StaleStatistics_DoesNotCollapse.
`

// Test_ClaimPlan_DoesNotScaleWithBacklogDepth pins the claim to constant cost in
// queue depth. RED before the ordering index exists: the plan seq-scans the whole
// pending backlog twice and the rows examined grow ~linearly with it.
func Test_ClaimPlan_DoesNotScaleWithBacklogDepth(t *testing.T) {
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}

	const (
		shallowBacklog = 5_000
		deepBacklog    = 40_000 // 8x deeper
		// rowsPerPartition mirrors the live stand: kaname.fga_outbox carried
		// ~25-40 rows per object during the burst, so only ~1 row in 30 is a
		// claimable partition head. A claim must still find its 16 heads without
		// reading the backlog.
		rowsPerPartition = 30
		maxAttempts      = 10
		claimLimit       = 16
		// The claim may read a small multiple more rows at 8x depth (planner
		// noise, page boundaries) but MUST NOT track the backlog. 8x depth with a
		// linear claim would be ~8x the rows; 3x is far below that and far above
		// any legitimate constant-cost variance.
		maxRowsGrowthFactor = 3.0
	)

	ctx := context.Background()
	pool := setupClaimPlanPG(ctx, t)

	claimSQL := buildClaimQuery("outbox", "tuple_key")

	seedBacklog(ctx, t, pool, 0, shallowBacklog, rowsPerPartition)
	analyzeOutbox(ctx, t, pool)
	shallowRows, _ := explainClaim(ctx, t, pool, claimSQL, maxAttempts, claimLimit)

	seedBacklog(ctx, t, pool, shallowBacklog, deepBacklog, rowsPerPartition)
	analyzeOutbox(ctx, t, pool)
	deepRows, deepPlan := explainClaim(ctx, t, pool, claimSQL, maxAttempts, claimLimit)

	t.Logf("rows examined: shallow(%d backlog)=%d  deep(%d backlog)=%d",
		shallowBacklog, shallowRows, deepBacklog, deepRows)

	require.NotContains(t, deepPlan, "Seq Scan",
		"claim seq-scans the outbox at depth — the partition index cannot be used "+
			"without an index producing ORDER BY (attempt_count, id) over the pending "+
			"rows, so the claim degrades to O(backlog) and the drain rate falls as the "+
			"queue it must drain grows.\nplan:\n%s", deepPlan)

	require.Positive(t, shallowRows, "explain reported no rows examined")
	growth := float64(deepRows) / float64(shallowRows)
	require.LessOrEqual(t, growth, maxRowsGrowthFactor,
		"claim cost tracks backlog depth (%.1fx more rows examined for 8x deeper queue): "+
			"the drain slows down exactly as the backlog grows, so a producer marginally "+
			"faster than the consumer diverges without bound.\nplan:\n%s", growth, deepPlan)
}

// Test_ClaimPlan_StaleStatistics_DoesNotCollapse pins the claim to an
// INDEX-ORDERED outer scan even when the planner's statistics say the queue is
// empty — the state every queue table is in at the START of a burst.
//
// # Why this is a distinct failure from the one above
//
//	The test above measures the plan the planner picks when it KNOWS the backlog.
//	But a queue table is empty almost all of the time, so its last ANALYZE almost
//	always ran on an empty queue and the estimate it carries INTO a burst is
//	`rows=1`. On a one-row estimate a Sort looks free, so any partial index on
//	`sent_at IS NULL` in an order OTHER than the claim's is enough for the planner
//	to choose "unordered pending scan → Sort → Limit". That plan cannot stop early:
//	the Sort must consume the WHOLE pending set, so the anti-join runs once per
//	pending row and the claim becomes O(backlog^2)-ish.
//
//	Measured on the live stand (Postgres 16, kaname.fga_outbox, 8 600 pending)
//	with the extra `(created_at) WHERE sent_at IS NULL` index present: 6 990 ms for
//	ONE claim. With it removed and nothing else changed: 29 ms. The drainer was
//	observed applying exactly one 16-row batch every ~11.5 s for 46 s — 1.4 rows/s
//	— and then jumping to ~600 rows/s the moment autovacuum finally re-analyzed.
//	Revokes queued behind that stall took 30 s on average (49 s worst) to reach
//	внешний потребитель, против 15-секундного бюджета сквозной пробы.
//
// # What is asserted, and why the plan is not executed
//
//	The chosen PLAN SHAPE, via EXPLAIN without ANALYZE. The property under test is
//	"which plan does the planner pick when its statistics say the queue is empty",
//	which is settled at planning time — and the collapsed plan is precisely the one
//	that must not be executed: measured here it ran for over ten minutes on 40 000
//	pending rows before it was killed.
//
//	A `Sort` node is the exact, complete witness. The claim's outer scan must
//	deliver ORDER BY (attempt_count, id); only an index on that order can, so ANY
//	other access path — the (created_at) decoy, a seq scan — forces a Sort. And a
//	Sort must consume its whole input before the LIMIT sees a row, which is what
//	turns "read 16 heads" into "read the entire backlog, once per candidate". So
//	Sort present ⟺ early stop lost ⟺ O(backlog) claim.
//
// RED if a decoy partial index over `sent_at IS NULL` is (re)introduced on this
// table; GREEN with the two-index set kaname ships.
func Test_ClaimPlan_StaleStatistics_DoesNotCollapse(t *testing.T) {
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}

	const (
		drainedHistory   = 40_000 // already-applied rows: the bulk of a live outbox
		backlog          = 40_000
		rowsPerPartition = 30
		maxAttempts      = 10
		claimLimit       = 16
	)

	ctx := context.Background()
	pool := setupClaimPlanPG(ctx, t)

	// The defining condition, in the shape a live outbox actually has it: the table
	// is mostly DRAINED history, and the last ANALYZE ran while the queue was empty
	// — so the statistics say "sent_at is essentially never NULL". Then a burst
	// arrives and nothing re-analyzes before the claim runs.
	seedDrained(ctx, t, pool, 0, drainedHistory, rowsPerPartition)
	analyzeOutbox(ctx, t, pool)
	seedBacklog(ctx, t, pool, drainedHistory, drainedHistory+backlog, rowsPerPartition)

	claimSQL := buildClaimQuery("outbox", "tuple_key")
	plan := explainClaimPlanOnly(ctx, t, pool, claimSQL, maxAttempts, claimLimit)
	t.Logf("stale-statistics claim plan over %d pending rows:\n%s", backlog, plan)

	require.NotContains(t, plan, "Sort",
		"the claim sorts the pending backlog instead of reading it in index order. Under the "+
			"empty-queue statistics a queue table carries into a burst a Sort is estimated "+
			"free, and it destroys the LIMIT's early stop — the claim then reads the WHOLE "+
			"queue it is trying to drain, once per candidate row. Remove any partial index "+
			"over `sent_at IS NULL` whose order is not the claim's ORDER BY "+
			"(attempt_count, id).\nplan:\n%s", plan)
	require.NotContains(t, plan, "Seq Scan",
		"claim seq-scans the outbox under stale statistics.\nplan:\n%s", plan)
}

// setupClaimPlanPG gives this test its own EMPTY database on the package's
// container and creates the canonical outbox table in it. Own setup (not the
// drainer_test helper) because this file is an internal test — it needs
// buildClaimQuery, which is unexported — and because the table under test here is
// claimPlanTable, not the package template's fga_outbox: these two tests seed the
// backlog and control when it is ANALYZEd, which is itself the property they
// measure.
func setupClaimPlanPG(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(ctx, pgtest.NewEmptyDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)

	_, err = pool.Exec(ctx, claimPlanTable)
	require.NoError(t, err, "create outbox table")

	return pool
}

// seedBacklog inserts unsent rows [from, to) spread over partitions of
// rowsPerPartition rows each. Statistics are NOT refreshed here — the caller
// decides, because "how fresh are the planner's statistics" is itself one of the
// properties under test (see Test_ClaimPlan_StaleStatistics_DoesNotCollapse).
//
// The partition key is materialised on the row exactly as kaname's trigger
// does it (`user || ' ' || relation || ' ' || object`), so the seeded shape has a
// realistic tuple/object ratio: many subjects per object, few rows per tuple.
func seedBacklog(ctx context.Context, t *testing.T, pool *pgxpool.Pool, from, to, rowsPerPartition int) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO outbox (event_type, payload, tuple_key)
		SELECT CASE WHEN i % 5 = 0 THEN 'fga.tuple.delete' ELSE 'fga.tuple.write' END,
		       jsonb_build_object(
		           'user',     'user:usr' || (i % 20)::text,
		           'relation', 'v_get',
		           'object',   'vpc_network:net' || (i / $3)::text),
		       'user:usr' || (i % 20)::text || ' v_get vpc_network:net' || (i / $3)::text
		FROM generate_series($1, $2 - 1) AS i`,
		from, to, rowsPerPartition)
	require.NoError(t, err, "seed backlog rows [%d,%d)", from, to)
}

// seedDrained inserts rows [from, to) that are ALREADY APPLIED (sent_at set) —
// the drained history that makes up almost all of a live outbox table. Its purpose
// is statistical: analyzing a table in this state records "sent_at is essentially
// never NULL", which is the estimate the planner then carries into a burst.
func seedDrained(ctx context.Context, t *testing.T, pool *pgxpool.Pool, from, to, rowsPerPartition int) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO outbox (event_type, payload, tuple_key, sent_at)
		SELECT 'fga.tuple.write',
		       jsonb_build_object(
		           'user',     'user:old' || (i % 20)::text,
		           'relation', 'v_get',
		           'object',   'vpc_network:old' || (i / $3)::text),
		       'user:old' || (i % 20)::text || ' v_get vpc_network:old' || (i / $3)::text,
		       now()
		FROM generate_series($1, $2 - 1) AS i`,
		from, to, rowsPerPartition)
	require.NoError(t, err, "seed drained rows [%d,%d)", from, to)
}

// analyzeOutbox refreshes planner statistics — what a healthy autovacuum does
// DURING a burst (kaname pins the per-table analyze threshold in migration 0064
// precisely to make that happen).
func analyzeOutbox(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `ANALYZE outbox`)
	require.NoError(t, err)
}

// explainClaim runs the real claim statement under EXPLAIN (ANALYZE) inside a
// rolled-back transaction — the claim is an UPDATE, so the rollback keeps
// attempt_count and row locks from leaking into the next measurement. Returns
// the total rows examined across all plan nodes plus the rendered plan.
func explainClaim(ctx context.Context, t *testing.T, pool *pgxpool.Pool, claimSQL string, maxAttempts, limit int) (int, string) {
	t.Helper()
	rows, plan := explainClaimWith(ctx, t, pool, "EXPLAIN (ANALYZE, FORMAT JSON) ", claimSQL, maxAttempts, limit)
	return rows, plan
}

// explainClaimPlanOnly renders the plan the planner CHOOSES without executing it.
// Used where the regression under test is the plan shape itself and running the bad
// plan is prohibitively slow (see Test_ClaimPlan_StaleStatistics_DoesNotCollapse).
func explainClaimPlanOnly(ctx context.Context, t *testing.T, pool *pgxpool.Pool, claimSQL string, maxAttempts, limit int) string {
	t.Helper()
	_, plan := explainClaimWith(ctx, t, pool, "EXPLAIN (FORMAT JSON) ", claimSQL, maxAttempts, limit)
	return plan
}

func explainClaimWith(ctx context.Context, t *testing.T, pool *pgxpool.Pool, prefix, claimSQL string, maxAttempts, limit int) (int, string) {
	t.Helper()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	var raw []byte
	err = tx.QueryRow(ctx, prefix+claimSQL, maxAttempts, limit).Scan(&raw)
	require.NoError(t, err, "explain claim")

	var explained []struct {
		Plan map[string]any `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &explained))
	require.NotEmpty(t, explained)

	var (
		rows  int
		nodes []string
	)
	walkPlan(explained[0].Plan, &rows, &nodes)
	return rows, strings.Join(nodes, "\n")
}

// walkPlan sums "Actual Rows" over every node of an EXPLAIN JSON plan tree and
// renders one line per node. Rows examined (not wall-clock) is the deterministic
// witness of "does this claim read the whole backlog". Under a plan-only EXPLAIN
// the actual counters are absent and render as zero — there the node TYPES are the
// signal.
func walkPlan(node map[string]any, rows *int, nodes *[]string) {
	if node == nil {
		return
	}
	nodeType, _ := node["Node Type"].(string)
	actual, _ := node["Actual Rows"].(float64)
	loops, _ := node["Actual Loops"].(float64)
	if loops < 1 {
		loops = 1
	}
	*rows += int(actual * loops)
	*nodes = append(*nodes, fmt.Sprintf("  %-20s actual_rows=%d loops=%d", nodeType, int(actual), int(loops)))

	children, _ := node["Plans"].([]any)
	for _, c := range children {
		child, ok := c.(map[string]any)
		if !ok {
			continue
		}
		walkPlan(child, rows, nodes)
	}
}
