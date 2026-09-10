// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer_test

// Integration lock for the ORDERING contract of Config.ApplyConcurrency as it
// applies to REGISTER-outboxes (compute/vpc/storage/nlb `fga_register_outbox`),
// whose target is iam's resource_mirror — a source_version-LWW UPSERT plus a
// version-gated hard DELETE.
//
// What is modelled (faithful to services/iam/.../resource_mirror/emitter.go)
//
//	register(v)     → INSERT when absent; else UPDATE only if stored < v
//	                  (`ON CONFLICT … WHERE resource_mirror.source_version <
//	                  EXCLUDED.source_version` — LWW, stale register is a no-op)
//	unregister(tomb)→ DELETE only if stored <= tomb; ABSENT ROW ⇒ PLAIN NO-OP.
//	                  The delete leaves NO TOMBSTONE behind.
//
// The asymmetry is the whole point: LWW guards the ON-CONFLICT-UPDATE branch
// only. Once the row is gone there is nothing left to compare against, so a
// reordered STALE register falls into the INSERT branch and RESURRECTS the row —
// permanently, because iam's reconciler is level-triggered off the mirror and
// treats the resurrected row as the current truth.
//
// Event-type mapping: the inline test schema (fgaOutboxSchema) has a
// CHECK on event_type, so `fga.tuple.write` stands for register and
// `fga.tuple.delete` for unregister. Only the ordering semantics matter here.
//
// Run: go test ./pkg/outbox/drainer/... -race -p 1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox/drainer"
)

// mirrorState models iam's kaname.resource_mirror: a per-object row carrying
// the producer's source_version, written LWW and deleted without a tombstone.
type mirrorState struct {
	mu      sync.Mutex
	version map[string]time.Time // object → stored source_version (row present)
	log     []string             // ordered "register:<obj>@<v>" / "unregister:<obj>@<v>"
}

func newMirrorState() *mirrorState {
	return &mirrorState{version: map[string]time.Time{}}
}

type mirrorEvent struct {
	Object  string    `json:"object"`
	Version time.Time `json:"version"`
}

// apply is a drainer.Applier[rawPayload] over the modelled mirror.
func (s *mirrorState) apply(_ context.Context, eventType string, payload rawPayload) error {
	var e mirrorEvent
	if err := json.Unmarshal(payload, &e); err != nil {
		return errors.Join(drainer.ErrPermanent, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch eventType {
	case "fga.tuple.write": // register
		stored, present := s.version[e.Object]
		if !present || stored.Before(e.Version) {
			// INSERT branch (absent) — unconditional; UPDATE branch — LWW-gated.
			s.version[e.Object] = e.Version
		}
		s.log = append(s.log, fmt.Sprintf("register:%s@%s", e.Object, e.Version.UTC().Format(time.RFC3339Nano)))
	case "fga.tuple.delete": // unregister
		if stored, present := s.version[e.Object]; present && !stored.After(e.Version) {
			delete(s.version, e.Object) // hard delete — NO tombstone retained
		}
		s.log = append(s.log, fmt.Sprintf("unregister:%s@%s", e.Object, e.Version.UTC().Format(time.RFC3339Nano)))
	default:
		return errors.Join(drainer.ErrPermanent, fmt.Errorf("unknown event_type %q", eventType))
	}
	return nil
}

func (s *mirrorState) isPresent(obj string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.version[obj]
	return ok
}

func (s *mirrorState) snapshotLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

// Test_1_4_45_RegisterOutbox_UnregisterThenStaleRegister — decision table pinning
// WHEN ApplyConcurrency>1 needs Config.PartitionColumn on a register-outbox.
//
// All cases seed the SAME reorder pressure as Test_1_4_40: a bumped register
// (attempt_count=5 after a transient iam outage) that precedes a fresh unregister
// (attempt_count=0) in id order, plus filler rows of OTHER objects so the claim's
// `ORDER BY (attempt_count, id)` splits the pair across claim batches. The
// producer's intent is register(t1) THEN unregister(t2>t1) ⇒ the resource was
// created and then deleted, so the mirror must end up ABSENT.
//
//   - WITHOUT PartitionColumn the unregister is claimed first; it no-ops on the
//     absent row (leaving no tombstone) and the stale register then INSERTs →
//     the mirror row for a DELETED resource is RESURRECTED and never reclaimed.
//     source_version-LWW does NOT prevent this: it only guards the
//     ON-CONFLICT-UPDATE branch, and this apply takes the INSERT branch.
//   - WITH PartitionColumn per-partition FIFO holds cross-batch, so register
//     applies before unregister and the mirror ends ABSENT (correct).
//
// This is the behavioural ground truth behind the Config.ApplyConcurrency
// ORDERING godoc: register-outboxes that carry BOTH register and unregister of
// one object are NOT order-insensitive and MUST set PartitionColumn.
func Test_1_4_45_RegisterOutbox_UnregisterThenStaleRegister(t *testing.T) {
	t.Parallel()

	const obj = "compute_instance:ins0000000000000001"
	t1 := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC) // register (create)
	t2 := t1.Add(time.Minute)                           // unregister (delete), strictly newer

	cases := []struct {
		name             string
		partitionColumn  string
		applyConcurrency int
		wantPresent      bool
		rationale        string
	}{
		{
			name:             "no PartitionColumn, concurrent apply: stale register resurrects the deleted row",
			partitionColumn:  "",
			applyConcurrency: 2,
			wantPresent:      true,
			rationale: "LWW guards only the ON-CONFLICT-UPDATE branch; the hard delete leaves no " +
				"tombstone, so a reordered stale register takes the INSERT branch and resurrects",
		},
		{
			// Backs the ORDERING godoc claim that reorder is driven by the claim's
			// `ORDER BY attempt_count, id`, NOT by concurrency: even a strictly
			// sequential drainer applies the fresh unregister before the bumped
			// register. ApplyConcurrency is a throughput knob, not the cause.
			name:             "no PartitionColumn, sequential apply: reorder happens at ApplyConcurrency=1 too",
			partitionColumn:  "",
			applyConcurrency: 1,
			wantPresent:      true,
			rationale:        "attempt_count-first claim ordering alone puts the fresh unregister ahead of the bumped register",
		},
		{
			name:             "PartitionColumn: per-partition FIFO keeps register before unregister",
			partitionColumn:  "payload->>'object'",
			applyConcurrency: 2,
			wantPresent:      false,
			rationale:        "claim never takes a successor ahead of a deliverable same-partition predecessor",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			pool, _ := setupDrainerPG(t)
			st := newMirrorState()

			// R: register(t1), already bumped to attempt_count=5 (transient history).
			// Inserted FIRST → smallest id (create precedes delete).
			rID := insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.write",
				fmt.Sprintf(`{"object":%q,"version":%q}`, obj, t1.Format(time.RFC3339Nano)), 5)
			// U: unregister(t2), fresh attempt_count=0. Inserted SECOND → id > rID.
			uID := insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.delete",
				fmt.Sprintf(`{"object":%q,"version":%q}`, obj, t2.Format(time.RFC3339Nano)), 0)
			// Filler rows of OTHER objects (attempt_count=0) so the attempt=5 register
			// and the attempt=0 unregister never share one 2-row claim batch.
			fresh := make([]int64, 0, 4)
			for i := 0; i < 4; i++ {
				fresh = append(fresh, insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.write",
					fmt.Sprintf(`{"object":"compute_instance:filler%02d","version":%q}`,
						i, t1.Format(time.RFC3339Nano)), 0))
			}

			cfg := testCfg()
			cfg.ApplyConcurrency = tc.applyConcurrency
			cfg.PartitionColumn = tc.partitionColumn
			cfg.MaxAttempts = 10
			cfg.PollFallback = 300 * time.Millisecond

			dCancel, done := startDrainerApplier(t, ctx, pool, cfg, st.apply)
			defer func() { dCancel(); <-done }()

			waitForListenerReady(t, ctx, pool, cfg.Channel)

			for _, id := range append([]int64{rID, uID}, fresh...) {
				waitForRowSent(t, ctx, pool, id, 30*time.Second)
			}

			require.Equalf(t, tc.wantPresent, st.isPresent(obj),
				"mirror row present=%v, want %v — %s; apply log=%v",
				st.isPresent(obj), tc.wantPresent, tc.rationale, st.snapshotLog())
		})
	}
}
