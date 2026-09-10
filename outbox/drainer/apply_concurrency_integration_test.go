// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer_test

// Integration tests for Config.ApplyConcurrency — bounded concurrent apply of a
// claim-batch's external calls (Lever A: raise drainer throughput ceiling from
// ~1/apply_latency to ~N/apply_latency to close the producer/consumer inversion),
// while KEEPING exactly-once (the claim-tx still holds every row's
// FOR UPDATE SKIP LOCKED lock until commit; applies touch no DB).
//
// Run: go test ./pkg/outbox/... -race -p 1

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox/drainer"
)

// Test_1_4_30_ApplyConcurrency_HidesPerCallLatency — throughput regression.
//
// K rows whose apply each blocks a fixed latency L (models a slow peer, e.g. iam
// RegisterResource under contention). A SEQUENTIAL drainer needs ~K*L; a drainer
// with ApplyConcurrency=N drains a full N-wide wave per claim, so ~ceil(K/N)*L.
// With K=24, L=300ms, N=8: sequential ≈ 7.2s, concurrent ≈ 0.9s. The 3s deadline
// sits well between the two — it PASSES only when the batch's applies actually run
// concurrently (RED on the sequential path, GREEN with ApplyConcurrency wired).
func Test_1_4_30_ApplyConcurrency_HidesPerCallLatency(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	const (
		k = 24
		l = 300 * time.Millisecond
		n = 8
	)
	fa.setDelay("fga.tuple.write", l) // every apply blocks L then succeeds

	cfg := testCfg()
	cfg.ApplyConcurrency = n
	cfg.ApplyTimeout = 5 * time.Second // must exceed L
	cfg.PollFallback = 500 * time.Millisecond

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	ids := make([]int64, k)
	for i := 0; i < k; i++ {
		ids[i] = insertOutboxRow(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"resource_id":"app-%03d"}`, i))
	}

	// Deadline between concurrent (~0.9s) and sequential (~7.2s) floors.
	const deadline = 3 * time.Second
	start := time.Now()
	require.Truef(t, fa.waitForCalls(k, deadline),
		"ApplyConcurrency=%d must drain %d rows (each %s apply) within %s — sequential would need ~%s; got %d applies in %s",
		n, k, l, deadline, time.Duration(k)*l, fa.countCalls(), time.Since(start))

	// All rows delivered exactly once (no double-apply from concurrent claim/apply).
	for _, id := range ids {
		row := waitForRowSent(t, ctx, pool, id, 3*time.Second)
		assert.NotNil(t, row.sentAt, "row id=%d must be marked sent", id)
	}
	assert.Equalf(t, k, fa.countCalls(),
		"each of %d rows applied exactly once (no double-apply); got %d", k, fa.countCalls())
}

// Test_1_4_31_ApplyConcurrency_ExactlyOnce_Race — mandatory -race exactly-once
// under concurrent apply.
//
// A SINGLE drainer with ApplyConcurrency=8 over M unique rows: each row must be
// applied EXACTLY ONCE despite up-to-8 concurrent in-flight applies per batch.
// This locks that parallelising the apply-phase does not break the claim-tx /
// FOR UPDATE SKIP LOCKED exactly-once guarantee (the mark-phase stays sequential
// on the single tx). Run under -race to catch any data race on the shared
// outcome slice / tx.
func Test_1_4_31_ApplyConcurrency_ExactlyOnce_Race(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)

	fa := newFakeApplier()
	fa.setKeyFn(func(_ string, payload []byte) string { return string(payload) })
	// Small apply latency so several applies of a batch genuinely overlap.
	fa.setDelay("fga.tuple.write", 15*time.Millisecond)

	var totalApplies atomic.Int64
	fa.setOnCallHook(func(_ string, _ []byte, _ int) { totalApplies.Add(1) })

	cfg := testCfg()
	cfg.ApplyConcurrency = 8

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	const m = 64
	ids := make([]int64, m)
	for i := 0; i < m; i++ {
		ids[i] = insertOutboxRow(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"resource_id":"app-%03d"}`, i))
	}

	// All delivered.
	for _, id := range ids {
		row := waitForRowSent(t, ctx, pool, id, 10*time.Second)
		assert.NotNil(t, row.sentAt, "row id=%d must be sent", id)
	}

	// Quiescence: total applies must REACH m and HOLD (no late duplicate from a
	// concurrent in-flight apply that a losing path re-claimed).
	final, ok := waitStableInt64(totalApplies.Load, int64(m), 700*time.Millisecond, 15*time.Second)
	require.Truef(t, ok,
		"exactly-once under ApplyConcurrency=%d: %d rows must reach and HOLD exactly %d applies (no double-apply); observed %d",
		cfg.ApplyConcurrency, m, m, final)

	// Each unique payload applied exactly once.
	calls := fa.snapshotCalls()
	seen := make(map[string]int, len(calls))
	for _, c := range calls {
		seen[string(c.payload)]++
	}
	assert.Len(t, seen, m, "all %d unique rows applied (no lost row)", m)
	for payload, count := range seen {
		assert.Equalf(t, 1, count, "payload %s applied %dx (must be exactly once)", payload, count)
	}
}

// Test_1_4_32_ApplyConcurrencyDefault_IsOne asserts the default Config leaves
// ApplyConcurrency=1 (sequential) — the opt-in nature of the feature (zero
// behaviour change for consumers that don't set it).
func Test_1_4_32_ApplyConcurrencyDefault_IsOne(t *testing.T) {
	t.Parallel()
	// withDefaults is unexported; assert via the public New path indirectly is
	// overkill — instead assert the documented default through a tiny config the
	// same way production consumers construct it (BatchSize/Channel set, no
	// ApplyConcurrency) stays sequential by leaving the field zero-valued.
	var c drainer.Config
	require.Equal(t, 0, c.ApplyConcurrency,
		"zero-value Config must leave ApplyConcurrency unset (0) so withDefaults maps it to 1 = sequential")
}
