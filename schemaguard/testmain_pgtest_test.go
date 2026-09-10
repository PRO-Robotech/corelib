// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package schemaguard_test

import (
	"os"
	"testing"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain поднимает шаблон базы с журналом goose ОДИН раз; каждая проба
// получает его клон, поэтому записи версий одной пробы не видны другой.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "schemaguard",
		Migrate: pgtest.SQL(gooseTable),
	}))
}
