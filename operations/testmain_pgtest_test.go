// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres instead of one per test.
//
// Fifty-four tests reach a database here, and each of them used to pay for a
// container start and a fresh CREATE TABLE before its first assertion — which is
// most of what the package cost. The `operations` table is the same for all of
// them, so it goes into the template once and every test gets its own clone.
//
// The CAS, SKIP LOCKED and claim proofs are unaffected: their contention is
// between goroutines INSIDE one test, over real rows of that test's own database.
// Nothing in this package reads server-wide state or needs a cluster to itself —
// the one test that needs a non-default session setting
// (idle_in_transaction_session_timeout) sets it on its own pool, from the DSN
// startPostgres returns.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "ops",
		Migrate: pgtest.SQL(createTable),
	}))
}
