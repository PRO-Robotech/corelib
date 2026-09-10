// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/pgtest"
)

func TestTransactor_InTx_CommitsOnSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (testcontainers); skipped with -short")
	}
	ctx := context.Background()
	dsn := pgtest.NewDB(t)

	pool, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	txtor := NewTransactor(pool)

	// Транзакция без ошибки — должна закоммититься.
	err = txtor.InTx(ctx, func(_ pgx.Tx) error {
		return nil
	})
	require.NoError(t, err)
}
