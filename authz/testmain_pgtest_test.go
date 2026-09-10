// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain hands this package one Postgres instead of one per test.
//
// No schema is applied: the ListenInvalidator tests carry no tables, they only
// need a database to LISTEN on and NOTIFY into. That is exactly why a clone is
// enough isolation here — NOTIFY is delivered only within the database it was
// issued in, so two tests on the same server still cannot hear each other.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{Name: "authz"}))
}
