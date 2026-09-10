// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package outbox_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres instead of one per test.
//
// outboxSchema is the same fixed table for every test here, so it is applied once
// into a template and each test gets its own clone. The properties under test —
// Emit's atomicity with the enclosing transaction, and the NOTIFY its trigger
// fires — are per-database: a rollback rolls back the caller's own rows, and a
// NOTIFY reaches only sessions listening in the database it was issued in.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "outbox",
		Migrate: pgtest.SQL(outboxSchema),
	}))
}
