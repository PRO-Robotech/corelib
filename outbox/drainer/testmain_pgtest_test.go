// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres instead of one per test.
//
// The template carries fgaOutboxSchema, which is what the thirty-odd drainer
// integration tests want; the two claim-plan tests build their own table on an
// EMPTY database instead (setupClaimPlanPG), because they seed 80 000 rows and
// measure the plan the planner picks over them — a pre-made table is no help and
// its statistics would be the wrong starting point.
//
// This TestMain serves BOTH test packages compiled into this binary — the
// external drainer_test and the internal drainer — since a binary may define it
// only once. It lives here because fgaOutboxSchema does.
//
// The concurrency proofs are unaffected: SKIP LOCKED, partition-head ordering and
// the two-replica races contend inside ONE test, on real rows of that test's own
// database. NOTIFY likewise does not cross databases, so tests listening on the
// same channel name no longer hear each other only because they no longer share a
// database — which is the same guarantee separate containers gave.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "drainer",
		Migrate: pgtest.SQL(fgaOutboxSchema),
	}))
}
