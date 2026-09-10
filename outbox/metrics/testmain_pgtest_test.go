// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package metrics_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres instead of one per test.
//
// The register-outbox DDL these tests read gauges from is identical for every
// one of them, so it is applied once into a template and each test gets its own
// clone. Nothing here depends on being alone with the SERVER — the gauges are
// scanned from a named table in the caller's own database — so a clone is the
// same isolation a separate container gave; see pkg/pgtest.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "outboxmetrics",
		Migrate: pgtest.SQL(schemaDDL),
	}))
}
