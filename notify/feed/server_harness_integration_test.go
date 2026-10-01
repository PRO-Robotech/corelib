// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"

	notifyv1 "github.com/PRO-Robotech/corelib/api/corelib/notify"
	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// stand — источник probe с кольцом ключей и сервером ленты над одной базой.
// Ставит письма Put источника, забирает и отмечает их сервером — без notify.
type stand struct {
	*fixture
	ring   *feed.Keyring
	server *feed.Server
}

type standOpts struct {
	putRing    *feed.Keyring // кольцо постановки; nil — ring
	serverRing *feed.Keyring // кольцо сервера; nil — ring
	clock      feed.Clock
	pool       *pgxpool.Pool
}

func newStand(t *testing.T, o standOpts) *stand {
	t.Helper()
	r := ring(t, key(1, 0xA1))
	if o.putRing == nil {
		o.putRing = r
	}
	if o.serverRing == nil {
		o.serverRing = r
	}
	if o.pool == nil {
		pool, err := pgxpool.New(context.Background(), pgtest.NewDB(t))
		require.NoError(t, err)
		pgtest.ClosePoolAtEnd(t, pool)
		o.pool = pool
	}
	f := fixtureSealed(t, o.pool, true, o.putRing)
	srv, err := feed.NewServer(feed.ServerConfig{
		Module: "probe", Service: "probe", Enabled: enabled(t, true), DB: o.pool,
		Keyring: o.serverRing, Clock: o.clock, Metrics: f.reg,
	})
	require.NoError(t, err)
	return &stand{fixture: f, ring: r, server: srv}
}

// putOne ставит одно письмо и возвращает id строки.
func (s *stand) putOne(t *testing.T, d feed.TemplateDesc, to string, v feed.Values) string {
	t.Helper()
	before := s.ids(t)
	require.NoError(t, s.put(t, d, to, v))
	after := s.ids(t)
	for id := range after {
		if !before[id] {
			return id
		}
	}
	t.Fatal("строка не поставлена")
	return ""
}

func (s *stand) ids(t *testing.T) map[string]bool {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT id FROM probe_notification_outbox`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		out[id] = true
	}
	require.NoError(t, rows.Err())
	return out
}

func (s *stand) claim(t *testing.T, max uint32, classes ...notifyv1.NotificationClass) []*notifyv1.ClaimedNotification {
	t.Helper()
	if len(classes) == 0 {
		classes = []notifyv1.NotificationClass{notifyv1.NotificationClass_NOTICE}
	}
	resp, err := s.server.Claim(context.Background(), &notifyv1.ClaimRequest{Max: max, Classes: classes})
	require.NoError(t, err)
	return resp.GetNotifications()
}

func (s *stand) ack(id, token string, kind notifyv1.OutcomeKind, reason notifyv1.OutcomeReason, deferFor time.Duration) error {
	req := &notifyv1.AckRequest{Id: id, LeaseToken: token, Outcome: &notifyv1.Outcome{Kind: kind, Reason: reason}}
	if deferFor > 0 {
		req.DeferFor = durationpb.New(deferFor)
	}
	_, err := s.server.Ack(context.Background(), req)
	return err
}

func (s *stand) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(), q, args...)
	require.NoError(t, err)
}

// rowState — наблюдаемое строки ленты, по которому проба судит «строка не
// изменилась».
type rowState struct {
	State, Reason, RecordedKind, RecordedReason, OutcomeToken, LeaseToken string
	HasSecret, HasOutcomeAt, Claimed                                      bool
	OutcomeAt, NotBefore                                                  string
}

func (s *stand) row(t *testing.T, id string) rowState {
	t.Helper()
	var r rowState
	require.NoError(t, s.pool.QueryRow(context.Background(), `
		SELECT state, coalesce(outcome_reason, ''), coalesce(recorded_kind, ''), coalesce(recorded_reason, ''),
		       coalesce(outcome_token::text, ''), coalesce(lease_token::text, ''),
		       secret_attrs IS NOT NULL, outcome_at IS NOT NULL, first_claimed_at IS NOT NULL,
		       coalesce(outcome_at::text, ''), coalesce(not_before::text, '')
		  FROM probe_notification_outbox WHERE id = $1`, id).Scan(
		&r.State, &r.Reason, &r.RecordedKind, &r.RecordedReason, &r.OutcomeToken, &r.LeaseToken,
		&r.HasSecret, &r.HasOutcomeAt, &r.Claimed, &r.OutcomeAt, &r.NotBefore))
	return r
}

// endLease — аренда строки кончилась (посев вместо ожидания LeaseTTL).
func (s *stand) endLease(t *testing.T, id string) {
	t.Helper()
	s.exec(t, `UPDATE probe_notification_outbox SET lease_until = now() - interval '1 second' WHERE id = $1`, id)
}

// expire — срок строки прошёл (посев вместо ожидания ttl).
func (s *stand) expire(t *testing.T, id string) {
	t.Helper()
	s.exec(t, `UPDATE probe_notification_outbox SET expires_at = now() - interval '1 second' WHERE id = $1`, id)
}

func idsOf(ns []*notifyv1.ClaimedNotification) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.GetId())
	}
	sort.Strings(out)
	return out
}

// gathered — значение серии метрики с ровно этими метками; -1 — серии нет.
func gathered(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			if len(got) != len(labels) {
				continue
			}
			for k, v := range labels {
				if got[k] != v {
					continue next
				}
			}
			switch {
			case m.Counter != nil:
				return m.GetCounter().GetValue()
			case m.Gauge != nil:
				return m.GetGauge().GetValue()
			}
		}
	}
	return -1
}

func outcomes(t *testing.T, reg *prometheus.Registry, class, kind, reason string) float64 {
	t.Helper()
	return gathered(t, reg, "kacho_notification_feed_outcomes_total",
		map[string]string{"module": "probe", "class": class, "kind": kind, "reason": reason})
}

// secretDesc — шаблон probe-all (атрибут code — секрет) с лимитом на
// адресата.
func secretDesc(limits ...feed.Limit) feed.TemplateDesc {
	d := allDesc()
	d.Limits = limits
	return d
}

func secretValues(code string) feed.Values {
	v := allTwin()
	v["code"] = code
	return feed.Values{Attrs: v}
}

func securityDesc() feed.TemplateDesc {
	d := helloDesc()
	d.Name = "probe-sec"
	d.Class = feed.ClassSecurity
	return d
}

// fixedClock — монотонные часы пробы: между возвратом оператора и ответом
// «прошло» ровно d (УК71, УК80).
type fixedClock time.Duration

func (c fixedClock) Start() feed.Elapsed { return func() time.Duration { return time.Duration(c) } }
