// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres instead of one per test.
//
// Three tests here each started their own container inline — the pool test, the commit
// test and the rollback test — so the package paid three container starts to make three
// assertions about a pool.
//
// There is NO Migrate because this package has no schema: the only table in it is the
// `items` that the rollback test creates for itself, and it wants that in a database
// nobody else is writing to. Each test therefore takes its own database and creates it
// there, exactly as it did when each had its own container.
//
// The settings the pool test reads — statement_timeout, idle_in_transaction_session_
// timeout — are SESSION settings applied by NewPool through its own connections, not
// cluster settings, so sharing a server with the other two tests cannot move them.
//
// The container starts lazily, so under -short — where all three skip — none is started.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{Name: "db"}))
}
