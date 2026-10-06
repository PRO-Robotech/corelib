// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
)

var errStore = errors.New("хранилище пробы отказало")

// deadlineDB — хранилище, записывающее срок контекста каждого оператора.
type deadlineDB struct {
	mu        sync.Mutex
	deadlines []time.Duration // остаток срока на входе оператора; −1 — срока нет
}

func (d *deadlineDB) note(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		d.deadlines = append(d.deadlines, time.Until(dl))
		return
	}
	d.deadlines = append(d.deadlines, -1)
}

func (d *deadlineDB) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	d.note(ctx)
	return pgconn.CommandTag{}, errStore
}

func (d *deadlineDB) Query(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
	d.note(ctx)
	return nil, errStore
}

func (d *deadlineDB) QueryRow(ctx context.Context, _ string, _ ...any) pgx.Row {
	d.note(ctx)
	return failedRow{}
}

func (d *deadlineDB) Begin(ctx context.Context) (pgx.Tx, error) {
	d.note(ctx)
	return nil, errStore
}

type failedRow struct{}

func (failedRow) Scan(...any) error { return errStore }

func (d *deadlineDB) requireBounded(t *testing.T, limit time.Duration, what string) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	require.NotEmpty(t, d.deadlines, "%s: ни одного оператора", what)
	for _, left := range d.deadlines {
		require.NotEqual(t, time.Duration(-1), left, "%s: оператор без срока", what)
		require.LessOrEqual(t, left, limit, "%s: срок оператора длиннее своего", what)
	}
	d.deadlines = nil
}

// arch-per-call-deadline: каждый оператор хранилища сервера ленты и уборщика
// идёт под своим сроком, и тогда, когда у вызывающего срока нет.
func TestStoreCallsRunUnderTheirOwnDeadline(t *testing.T) {
	db := &deadlineDB{}
	ring, err := NewKeyring(Key{ID: 1, Secret: make([]byte, KeySize)})
	require.NoError(t, err)
	en, err := ParseEnabled("X", func(string) (string, bool) { return "true", true })
	require.NoError(t, err)
	reg := prometheus.NewRegistry()
	srv, err := NewServer(ServerConfig{Module: "probe", Service: "probe", Enabled: en, DB: db, Keyring: ring, Metrics: reg, Observer: NopObserver})
	require.NoError(t, err)

	_, err = srv.Claim(context.Background(), &notifyv1.ClaimRequest{Max: 1, Classes: []notifyv1.NotificationClass{notifyv1.NotificationClass_NOTICE}})
	require.Error(t, err)
	db.requireBounded(t, storeCallTimeout, "Claim")

	_, err = srv.Ack(context.Background(), &notifyv1.AckRequest{Id: "ntf-0123456789abcdefg",
		LeaseToken: "6f1c2a52-8a51-4f4e-9d39-0e7e7e3c2a10", Outcome: &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_SENT}})
	require.Error(t, err)
	db.requireBounded(t, storeCallTimeout, "Ack")

	sw, err := NewSweeper(SweeperConfig{Module: "probe", Service: "probe", DB: db, Metrics: reg})
	require.NoError(t, err)
	require.Error(t, sw.Pass(context.Background()))
	db.requireBounded(t, sweepCallTimeout, "проход истечения")

	for _, s := range RetentionSubjects(db, "probe") {
		_, _, err := s.Sweep(context.Background(), s.Grace, 10)
		require.Error(t, err)
	}
	db.requireBounded(t, sweepCallTimeout, "уборка")

	require.Positive(t, storeCallTimeout)
	require.Less(t, storeCallTimeout, LeaseTTL)
	require.Less(t, sweepCallTimeout, SweepInterval)
}
