// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// permission_denied_terminal_integration_test.go — a permission denial from the
// owner is TERMINAL, and the observable consequence is that it does not wedge its
// partition.
//
// Repeating an identical request the owner refused on authorization grounds cannot
// start succeeding: the decision is a function of (caller, relation, object), none
// of which a retry changes. Classifying it "transient" therefore does not buy a
// later success — it buys a row that is retried forever, never reaches MaxAttempts
// (markTransientFailure caps attempt_count one below the poison gate on purpose),
// and so stays permanently inside the claim query's blocking set. Every later row
// of that partition is then never claimed.
//
// The cost of that is not a delay, it is a security regression, and it has been
// observed: an nlb register queue in which no row was ever delivered — 198 rows,
// all refused on authorization grounds. Because a partition is keyed per resource,
// the row stuck at the head was a REGISTRATION and the rows behind it included the
// UNREGISTRATION: the resource was deleted while its grant stayed materialized.
// A grant that survives the deletion of what it grants is over-grant.
//
// Poisoning is the safe direction of failure by comparison: the write never
// happened (nothing was granted) and the partition unblocks, while the service's
// periodic redrive replays the poisoned row so a temporary cause still succeeds
// later. Under-granting is fail-closed; the wedge is fail-open.

// Test_PermissionDeniedHead_DoesNotWedgePartition is the behavioural lock. The
// head of a partition is refused on authorization grounds on every attempt; a
// later row of the SAME partition must still be delivered.
//
// It fails by TIMING OUT before the fix: the head is capped below the poison gate
// forever, so the successor is never claimed and its sent_at stays NULL.
func Test_PermissionDeniedHead_DoesNotWedgePartition(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)

	const obj = "vpc_network:permdenied000000001"
	payload := fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":%q}`, obj)

	fa := newFakeApplier()
	// Key on event_type (the default) so the head and its successor are told
	// apart: the head is refused on every attempt, the successor applies cleanly
	// — if it is ever claimed at all.
	fa.setDefaultErr(permissionDenied())
	fa.setErrorSeq("fga.tuple.delete", nil)

	// HEAD: refused on authorization grounds on every attempt. Smallest id.
	headID := insertOutboxRow(t, ctx, pool, "fga.tuple.write", payload)
	// SUCCESSOR: same partition (identical tuple triple), would apply cleanly.
	succID := insertOutboxRow(t, ctx, pool, "fga.tuple.delete", payload)

	cfg := testCfg()
	cfg.PartitionColumn = iamPartitionKey
	cfg.MaxAttempts = 3
	cfg.PollFallback = 200 * time.Millisecond

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	row := waitForRowSent(t, ctx, pool, succID, 45*time.Second)
	assert.NotNil(t, row.sentAt,
		"a partition head refused on authorization grounds must be marked terminally so it stops blocking: "+
			"otherwise the unregistration behind it never applies and the grant outlives the resource")

	// And the head itself must be OUT of the blocking set — poisoned at the gate,
	// not parked one attempt below it forever.
	var attempts int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attempt_count FROM kaname.fga_outbox WHERE id = $1`, headID).Scan(&attempts))
	assert.GreaterOrEqualf(t, attempts, cfg.MaxAttempts,
		"the refused head must reach the poison gate (MaxAttempts=%d), not be capped below it forever; got attempt_count=%d",
		cfg.MaxAttempts, attempts)
}

func permissionDenied() error {
	return status.Error(codes.PermissionDenied, "fga_writer relation missing on iam_fgaproxy:system")
}
