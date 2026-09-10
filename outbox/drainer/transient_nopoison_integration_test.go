// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer_test

// Integration tests for transient-class no-poison + the mandatory CAS-claim
// exactly-once -race test.
//
// They assert the transient-class no-poison guarantee: a long transient outage
// (> MaxAttempts consecutive failures) must NOT poison the intent. A naive
// `attempt_count++` on every transient claim would drive the row past the CAS
// gate (attempt_count < MaxAttempts) and lose the tuple permanently.
//
// Run: go test ./outbox/... -race -p 1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/outbox/drainer"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// Test_1_4_04_LongTransientOutage_NoPoison — a transient outage longer than
// MaxAttempts must not poison the intent.
//
// fake-applier returns Unavailable on the first N > MaxAttempts attempts
// (models IAM-down), then nil. The intent must NOT be poisoned: after IAM
// "returns" it is applied exactly once (sent_at NOT NULL, last_error NULL).
func Test_1_4_04_LongTransientOutage_NoPoison(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	cfg := testCfg()
	cfg.MaxAttempts = 5 // small, so "N > MaxAttempts" is cheap
	cfg.BackoffMin = 20 * time.Millisecond
	cfg.BackoffMax = 40 * time.Millisecond

	// N = 8 transient Unavailable (> MaxAttempts=5), then success.
	seq := make([]error, 0, 8)
	for i := 0; i < 8; i++ {
		seq = append(seq, status.Error(codes.Unavailable, "iam down"))
	}
	fa.setErrorSeq("fga.tuple.write", seq...)
	fa.setDefaultErr(nil) // after the sequence is exhausted → success

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	time.Sleep(150 * time.Millisecond)
	id := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"resource_kind":"apps_application","resource_id":"app-A","project_id":"prj-X"}`)

	// Through the entire outage + recovery the row must eventually be sent.
	row := waitForRowSent(t, ctx, pool, id, 30*time.Second)
	assert.NotNil(t, row.sentAt, "transient outage must NOT poison — row applied after recovery")
	assert.Nil(t, row.lastError, "last_error reset to NULL on final success")

	// Applied exactly once successfully (the 9th call, after 8 transient fails).
	calls := fa.snapshotCalls()
	require.GreaterOrEqual(t, len(calls), 9,
		"expected ≥9 apply attempts (8 transient + ≥1 success), got %d", len(calls))
}

// Test_1_4_06_ApplyConsumesFullApplyTimeout_TransientStillCapped_NoPoison —
// regression for the mark-context bug.
//
// When the peer hangs and the apply only returns AT the ApplyTimeout deadline
// (DeadlineExceeded — a transient class), the drainer must STILL record the
// transient outcome and keep attempt_count capped below MaxAttempts (the
// documented transient no-poison invariant). The bug: processRowInTx derived the
// DB-mark context with the SAME ApplyTimeout budget as the apply, started at the
// same instant — so an apply that consumed the whole budget left the mark's
// context already expired ("context already done"), markTransientFailure never
// landed its cap, and attempt_count (bumped by the claim UPDATE) climbed to
// MaxAttempts → the row false-poisoned and its owner-tuple was lost forever.
//
// This case (apply consuming the full ApplyTimeout) is exactly what the existing
// no-poison tests miss: they use a FAST-failing applier, so the mark context
// still has budget and the bug is invisible.
func Test_1_4_06_ApplyConsumesFullApplyTimeout_TransientStillCapped_NoPoison(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	cfg := testCfg()
	cfg.MaxAttempts = 3
	cfg.ApplyTimeout = 200 * time.Millisecond
	cfg.BackoffMin = 10 * time.Millisecond
	cfg.BackoffMax = 20 * time.Millisecond

	// Apply blocks until its (ApplyTimeout-bounded) ctx is cancelled, then returns
	// ctx.Err() (DeadlineExceeded) — deterministically consuming the FULL
	// ApplyTimeout on every attempt (models a hung IAM that only errors at the
	// deadline). Always transient; never succeeds.
	fa.setDelay("fga.tuple.write", 10*time.Second) // >> ApplyTimeout → blocks to deadline

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)
	id := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"resource_kind":"apps_application","resource_id":"app-slow","project_id":"prj-X"}`)

	// The drainer must attempt the row at least MaxAttempts times. With the bug it
	// stops here (poisoned after the cap fails to land); with the fix it keeps
	// re-claiming unbounded.
	require.Truef(t, fa.waitForCalls(cfg.MaxAttempts, 20*time.Second),
		"drainer must attempt the hung transient row ≥ MaxAttempts (got %d)", fa.countCalls())

	// Settle so the MaxAttempts-th claim's commit has landed and any poison would
	// be observable.
	time.Sleep(1 * time.Second)

	r := readOutboxRow(t, ctx, pool, id)
	assert.Nil(t, r.sentAt, "hung transient apply never succeeds → row not sent")
	assert.Lessf(t, r.attemptCount, cfg.MaxAttempts,
		"transient no-poison invariant: attempt_count must stay capped < MaxAttempts even when apply "+
			"consumes the FULL ApplyTimeout (got attempt_count=%d, MaxAttempts=%d) — a poisoned row "+
			"(attempt_count>=MaxAttempts) permanently loses the owner-tuple", r.attemptCount, cfg.MaxAttempts)

	// Positive proof of no-poison: the row stays claimable, so the drainer keeps
	// re-attempting it past MaxAttempts (unbounded transient retry). With the bug
	// the poison gate freezes the call count at MaxAttempts.
	assert.Greaterf(t, fa.countCalls(), cfg.MaxAttempts,
		"no-poison row must keep being re-claimed past MaxAttempts (got %d applies, MaxAttempts=%d)",
		fa.countCalls(), cfg.MaxAttempts)
}

// Test_1_4_05_PermanentError_PoisonAndSurface — a permanent error poisons and
// surfaces the row while normal rows keep draining.
//
// fake-applier returns a permanent error (ErrPermanent) → the row IS poisoned
// (attempt_count == MaxAttempts, sent_at NULL, last_error set), while a normal
// row drains (drainer not stuck on the poisoned one).
func Test_1_4_05_PermanentError_PoisonAndSurface(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()
	// Key by the parsed resource_id (NOT raw payload bytes — JSONB re-serialises
	// with normalised whitespace, so a raw-bytes key would never match).
	fa.setKeyFn(func(_ string, payload []byte) string {
		var v struct {
			ResourceID string `json:"resource_id"`
		}
		_ = json.Unmarshal(payload, &v)
		return v.ResourceID
	})
	fa.setErrorSeq("app-BAD",
		errors.Join(drainer.ErrPermanent, errors.New("malformed intent")))
	fa.setDefaultErr(nil) // app-OK → success

	cfg := testCfg()
	cfg.MaxAttempts = 5

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	time.Sleep(150 * time.Millisecond)
	idBad := insertOutboxRow(t, ctx, pool, "fga.tuple.write", `{"resource_id":"app-BAD"}`)
	time.Sleep(50 * time.Millisecond)
	idOK := insertOutboxRow(t, ctx, pool, "fga.tuple.write", `{"resource_id":"app-OK"}`)

	rowOK := waitForRowSent(t, ctx, pool, idOK, 5*time.Second)
	assert.NotNil(t, rowOK.sentAt, "OK row must drain (drainer not stuck on poisoned)")

	rowBad := waitForAttemptCount(t, ctx, pool, idBad, cfg.MaxAttempts, 5*time.Second)
	assert.Nil(t, rowBad.sentAt, "permanent → poisoned, NOT sent")
	assert.GreaterOrEqual(t, rowBad.attemptCount, cfg.MaxAttempts,
		"ErrPermanent forces attempt_count >= MaxAttempts (poison)")
	require.NotNil(t, rowBad.lastError)
	assert.Contains(t, *rowBad.lastError, "malformed intent")
}

// Test_1_4_21_CASClaimExactlyOnce_Race — mandatory -race exactly-once test.
//
// M pending rows, 3 concurrent drainer instances (2 replicas + extra) on the
// SAME database. The fake-applier counts Apply calls per-payload; each row must
// be applied EXACTLY ONCE (CAS-claim + FOR UPDATE SKIP LOCKED guarantee no
// double-apply, no lost row across replicas).
func Test_1_4_21_CASClaimExactlyOnce_Race(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool0, dsn := setupDrainerPG(t)

	fa := newFakeApplier()
	fa.setKeyFn(func(eventType string, payload []byte) string { return string(payload) })

	const replicas = 3
	pools := []*pgxpool.Pool{pool0}
	for i := 1; i < replicas; i++ {
		p, err := pgxpool.New(ctx, dsn)
		require.NoError(t, err)
		pgtest.ClosePoolAtEnd(t, p)
		pools = append(pools, p)
	}

	var totalApplies atomic.Int64
	dCtx, dCancel := context.WithCancel(ctx)
	defer dCancel()
	var wg sync.WaitGroup
	for i := 0; i < replicas; i++ {
		applier := func(ctx context.Context, eventType string, payload rawPayload) error {
			totalApplies.Add(1)
			return fa.Apply(ctx, eventType, []byte(payload))
		}
		d, err := drainer.New[rawPayload](pools[i], testCfg(), rawDecoder, applier, testLogger())
		require.NoError(t, err)
		wg.Add(1)
		go func() { defer wg.Done(); _ = d.Run(dCtx) }()
	}
	t.Cleanup(func() { dCancel(); wg.Wait() })

	time.Sleep(250 * time.Millisecond)

	const m = 40
	for i := 0; i < m; i++ {
		insertOutboxRow(t, ctx, pool0, "fga.tuple.write",
			fmt.Sprintf(`{"resource_id":"app-%03d"}`, i))
	}

	// Reach m applies, then hold a quiescence window: any (m+1)th apply from a
	// losing replica is caught the instant it lands (fail-fast), not missed by a
	// fixed settle-sleep.
	final, ok := waitStableInt64(totalApplies.Load, int64(m), 700*time.Millisecond, 15*time.Second)
	require.Truef(t, ok,
		"exactly-once: %d rows must reach and HOLD exactly %d applies across %d replicas (no double-apply); observed %d",
		m, m, replicas, final)

	calls := fa.snapshotCalls()
	seen := make(map[string]int, len(calls))
	for _, c := range calls {
		seen[string(c.payload)]++
	}
	assert.Len(t, seen, m, "all %d unique rows must be applied (no lost row)", m)
	for payload, count := range seen {
		assert.Equalf(t, 1, count, "payload %s applied %dx (must be exactly once)", payload, count)
	}
}
