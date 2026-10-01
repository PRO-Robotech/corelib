// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/notify/feed"
)

// untouchedDB — хранилище, которого проба не разрешает касаться: каждая
// граница входа решается до первого оператора SQL.
type untouchedDB struct{ calls atomic.Int32 }

func (d *untouchedDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	d.calls.Add(1)
	return pgconn.CommandTag{}, context.Canceled
}

func (d *untouchedDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	d.calls.Add(1)
	return nil, context.Canceled
}

func (d *untouchedDB) QueryRow(context.Context, string, ...any) pgx.Row {
	d.calls.Add(1)
	return errRow{}
}

type errRow struct{}

func (errRow) Scan(...any) error { return context.Canceled }

func enabled(t *testing.T, on bool) feed.Enabled {
	t.Helper()
	word := "false"
	if on {
		word = "true"
	}
	e, err := feed.ParseEnabled("KACHO_PROBE_NOTIFICATIONS_ENABLED", func(string) (string, bool) { return word, true })
	require.NoError(t, err)
	return e
}

func validatingServer(t *testing.T) (*feed.Server, *untouchedDB) {
	t.Helper()
	db := &untouchedDB{}
	s, err := feed.NewServer(feed.ServerConfig{
		Module: "probe", Service: "probe", Enabled: enabled(t, true), DB: db,
		Keyring: ring(t, key(1, 0xA1)), Metrics: prometheus.NewRegistry(),
	})
	require.NoError(t, err)
	return s, db
}

// refusal — код, текст и поле нарушения отказа.
func refusal(t *testing.T, err error) (codes.Code, string, string) {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok, "не статус: %v", err)
	field := ""
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok && len(br.GetFieldViolations()) > 0 {
			field = br.GetFieldViolations()[0].GetField()
		}
	}
	return st.Code(), st.Message(), field
}

func errorInfoReason(t *testing.T, err error) (string, map[string]string) {
	t.Helper()
	for _, d := range status.Convert(err).Details() {
		if ei, ok := d.(*errdetails.ErrorInfo); ok {
			return ei.GetReason(), ei.GetMetadata()
		}
	}
	return "", nil
}

// NTF1-B21, B24: границы Claim — до первого оператора SQL, без усечения.
func TestNTF1B21B24_ClaimBoundsAreRefusedBeforeSQL(t *testing.T) {
	notice := []notifyv1.NotificationClass{notifyv1.NotificationClass_NOTICE}
	for _, tc := range []struct {
		name  string
		req   *notifyv1.ClaimRequest
		text  string
		field string
	}{
		{"max=0", &notifyv1.ClaimRequest{Max: 0, Classes: notice}, "max: required", "max"},
		{"max=501", &notifyv1.ClaimRequest{Max: 501, Classes: notice}, "max: must be ≤ 500", "max"},
		{"classes={}", &notifyv1.ClaimRequest{Max: 10}, "classes: required", "classes"},
		{"UNSPECIFIED", &notifyv1.ClaimRequest{Max: 10, Classes: []notifyv1.NotificationClass{
			notifyv1.NotificationClass_NOTICE, notifyv1.NotificationClass_NOTIFICATION_CLASS_UNSPECIFIED}},
			"classes: NOTIFICATION_CLASS_UNSPECIFIED is not a class", "classes"},
		{"вне перечня", &notifyv1.ClaimRequest{Max: 10, Classes: []notifyv1.NotificationClass{9}},
			"classes: 9 is not a class", "classes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := validatingServer(t)
			_, err := s.Claim(context.Background(), tc.req)
			code, text, field := refusal(t, err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Equal(t, tc.text, text)
			require.Equal(t, tc.field, field)
			require.Zero(t, db.calls.Load(), "граница входа дошла до базы")
		})
	}
}

const tokenT = "6f1c2a52-8a51-4f4e-9d39-0e7e7e3c2a10"

func sent() *notifyv1.Outcome { return &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_SENT} }

// NTF1-B14, B22, УК72, УК73, CX1-26 (г): границы Ack — до первого оператора
// SQL, с именем поля.
func TestNTF1AckBoundsAreRefusedBeforeSQL(t *testing.T) {
	const id = "ntf-0123456789abcdefg"
	deferOf := func(r notifyv1.OutcomeReason) *notifyv1.Outcome {
		return &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_DEFER, Reason: r}
	}
	for _, tc := range []struct {
		name  string
		req   *notifyv1.AckRequest
		text  string
		field string
	}{
		{"id пуст", &notifyv1.AckRequest{LeaseToken: tokenT, Outcome: sent()}, "id: required", "id"},
		{"B22 id не по форме", &notifyv1.AckRequest{Id: "not-an-id", LeaseToken: tokenT, Outcome: sent()},
			"invalid notification id 'not-an-id'", ""},
		{"токен пуст", &notifyv1.AckRequest{Id: id, Outcome: sent()}, "lease_token: required", "lease_token"},
		{"УК73 токен x", &notifyv1.AckRequest{Id: id, LeaseToken: "x", Outcome: sent()},
			"lease_token: must be a UUID", "lease_token"},
		{"B14 исхода нет", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT}, "outcome: required", "outcome"},
		{"B14 UNSPECIFIED", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT, Outcome: &notifyv1.Outcome{}},
			"outcome: required", "outcome"},
		{"EXPIRED", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_EXPIRED, Reason: notifyv1.OutcomeReason_UNCLAIMED}},
			"outcome.kind: EXPIRED is set by the source only", "outcome.kind"},
		{"вид вне перечня", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT, Outcome: &notifyv1.Outcome{Kind: 42}},
			"outcome.kind: 42 is not an outcome kind", "outcome.kind"},
		{"SENT с причиной", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_SENT, Reason: notifyv1.OutcomeReason_REVOKED}},
			"outcome.reason: REVOKED is not allowed with SENT", "outcome.reason"},
		{"DENIED без причины", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_DENIED}},
			"outcome.reason: OUTCOME_REASON_UNSPECIFIED is not allowed with DENIED", "outcome.reason"},
		{"INVALID sealed_mismatch ставит сервер ленты", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: &notifyv1.Outcome{Kind: notifyv1.OutcomeKind_INVALID, Reason: notifyv1.OutcomeReason_SEALED_MISMATCH}},
			"outcome.reason: SEALED_MISMATCH is not allowed with INVALID", "outcome.reason"},
		{"DEFER unclaimed ставит уборщик", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: deferOf(notifyv1.OutcomeReason_UNCLAIMED), DeferFor: durationpb.New(time.Minute)},
			"outcome.reason: UNCLAIMED is not allowed with DEFER", "outcome.reason"},
		{"УК72 defer_for при SENT", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT, Outcome: sent(),
			DeferFor: durationpb.New(time.Minute)}, "defer_for: must not be set unless outcome.kind is DEFER", "defer_for"},
		{"DEFER без defer_for", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: deferOf(notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE)}, "defer_for: required", "defer_for"},
		{"defer_for < 1s", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: deferOf(notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE), DeferFor: durationpb.New(999 * time.Millisecond)},
			"defer_for: must be in [1s..15m]", "defer_for"},
		{"defer_for > 15m", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome: deferOf(notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE), DeferFor: durationpb.New(15*time.Minute + time.Second)},
			"defer_for: must be in [1s..15m]", "defer_for"},
		{"defer_for не по форме", &notifyv1.AckRequest{Id: id, LeaseToken: tokenT,
			Outcome:  deferOf(notifyv1.OutcomeReason_PLATFORM_UNAVAILABLE),
			DeferFor: &durationpb.Duration{Seconds: 60, Nanos: -1}},
			"defer_for: must be in [1s..15m]", "defer_for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := validatingServer(t)
			_, err := s.Ack(context.Background(), tc.req)
			code, text, field := refusal(t, err)
			require.Equal(t, codes.InvalidArgument, code)
			require.Equal(t, tc.text, text)
			require.Equal(t, tc.field, field)
			require.Zero(t, db.calls.Load(), "граница входа дошла до базы")
		})
	}
	s, _ := validatingServer(t)
	_, err := s.Ack(context.Background(), &notifyv1.AckRequest{Id: "not-an-id", LeaseToken: tokenT, Outcome: sent()})
	reason, _ := errorInfoReason(t, err)
	require.Equal(t, "INVALID_RESOURCE_ID", reason)
}

// Р9, NTF1-N07: сервер ленты не поднимается при выключенном флаге и без
// обязательных зависимостей; привязка — объект notification_feed:<модуль>.
func TestServerConfigIsJudgedAtConstruction(t *testing.T) {
	base := func() feed.ServerConfig {
		return feed.ServerConfig{
			Module: "probe", Service: "probe", Enabled: enabled(t, true), DB: &untouchedDB{},
			Keyring: ring(t, key(1, 1)), Metrics: prometheus.NewRegistry(),
		}
	}
	for name, mut := range map[string]func(*feed.ServerConfig){
		"флаг выключен":    func(c *feed.ServerConfig) { c.Enabled = enabled(t, false) },
		"флаг не разобран": func(c *feed.ServerConfig) { c.Enabled = feed.Enabled{} },
		"модуль":           func(c *feed.ServerConfig) { c.Module = "Probe" },
		"служба":           func(c *feed.ServerConfig) { c.Service = "a-b" },
		"хранилище":        func(c *feed.ServerConfig) { c.DB = nil },
		"кольцо":           func(c *feed.ServerConfig) { c.Keyring = nil },
		"метрики":          func(c *feed.ServerConfig) { c.Metrics = nil },
	} {
		c := base()
		mut(&c)
		_, err := feed.NewServer(c)
		require.Error(t, err, name)
	}
	s, err := feed.NewServer(base())
	require.NoError(t, err)
	b := s.Bound()
	require.Equal(t, "notification_feed", string(b.Type))
	require.Equal(t, "probe", b.ID)
}
