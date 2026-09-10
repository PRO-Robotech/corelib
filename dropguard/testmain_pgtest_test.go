// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres for the live-gate proof.
//
// There is NO Migrate: the proof brings its own three-file chain and walks it, so the
// template stays empty and each call takes its own empty database.
//
// The container starts LAZILY — the first NewEmptyDB does it. Every other test in this
// package decides things without a database and keeps costing nothing.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{Name: "dropguardlive"}))
}
