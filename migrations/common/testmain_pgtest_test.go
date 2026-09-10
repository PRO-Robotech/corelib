// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package common_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres instead of one per test.
//
// The template stays empty on purpose: these tests apply the migration under test
// themselves and then assert on what it did, so a pre-migrated database would be
// the wrong starting point. Each test still gets its own database — see
// pkg/pgtest for why a clone is the same isolation a separate container gave.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{Name: "opsmig"}))
}
