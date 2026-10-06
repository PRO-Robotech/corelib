// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/corelib/subscription"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/notify/feed/schema"
	"github.com/PRO-Robotech/corelib/notify/form"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/pgtest"
	"github.com/PRO-Robotech/corelib/subscription"
)

// TestMain выдаёт пакету один Postgres: лента фикстурного источника probe —
// миграцией ДЕЙСТВУЮЩЕЙ версии, которую строит schema (та же, что пишет
// notifygen init на чистом дереве службы), журнал
// подписки формы NTF-3 (колонка инициатора из настройки транзакции) и таблица
// фикстурного ресурса — строка, записанная до постановки в той же транзакции.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name: "feed",
		Migrate: func(ctx context.Context, dsn string) error {
			up, _, err := schema.DDL("probe", schema.Current())
			if err != nil {
				return err
			}
			return pgtest.SQL(up, probeJournalSchema)(ctx, dsn)
		},
	}))
}

const probeJournalSchema = `
CREATE TABLE probe_outbox (
    sequence_no   bigserial   PRIMARY KEY,
    resource_kind text        NOT NULL,
    resource_id   text        NOT NULL,
    event_type    text        NOT NULL,
    payload       jsonb       NOT NULL,
    initiator     text        NOT NULL DEFAULT NULLIF(current_setting('kacho_journal.initiator', true), ''),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE probe_items (id text PRIMARY KEY);
`

func probeJournal() subscription.Journal {
	return subscription.Journal{
		Storage: subscription.Storage{
			Table: "probe_outbox", PositionColumn: "sequence_no", KindColumn: "resource_kind",
			IDColumn: "resource_id", ChangeColumn: "event_type", PayloadColumn: "payload",
			Project: subscription.ProjectAbsent, Retention: subscription.RetainsEverything,
			InitiatorColumn: "initiator", OccurredAtColumn: "created_at",
		},
		Channel: "probe_outbox",
		Mapping: subscription.Mapping{
			Kinds: map[string]subscription.Kind{
				"notification": {ObjectType: "notification_feed", Action: "read",
					NameForm: subscription.NameFormNone, Scope: subscription.ScopeCluster},
			},
			Changes: map[string]subscriptionv1.SubscriptionEvent_Change{"UPDATED": subscriptionv1.SubscriptionEvent_UPDATED},
			// Состояние ленты словом state_unavailable (NTF-2 З17): событие
			// будит notify, а строки он забирает Claim.
			State: func(subscription.Row) (*anypb.Any, subscription.StateAbsence, error) {
				return nil, subscription.StateNotProduced, nil
			},
		},
	}
}

// randomSealer — запечатывание фикстуры: шифротекст не несёт открытого
// текста. Настоящее кольцо — Keyring (З11); пробы сервера ленты берут его.
type randomSealer struct{}

func (randomSealer) Seal(service, id, template string, plaintext []byte) ([]byte, error) {
	b := make([]byte, 32+len(plaintext))
	_, err := rand.Read(b)
	return b, err
}

type fixture struct {
	pool *pgxpool.Pool
	reg  *prometheus.Registry
	src  *feed.Source
	ctx  context.Context
}

func newFixture(t *testing.T, enabled bool) *fixture {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), pgtest.NewDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	return fixtureOn(t, pool, enabled)
}

func fixtureOn(t *testing.T, pool *pgxpool.Pool, enabled bool) *fixture {
	t.Helper()
	return fixtureSealed(t, pool, enabled, randomSealer{})
}

// fixtureSealed — источник probe с заданным запечатыванием (кольцо З11 у проб
// сервера ленты).
func fixtureSealed(t *testing.T, pool *pgxpool.Pool, enabled bool, sealer feed.Sealer) *fixture {
	t.Helper()
	word := "false"
	if enabled {
		word = "true"
	}
	en, err := feed.ParseEnabled("KACHO_PROBE_NOTIFICATIONS_ENABLED",
		func(string) (string, bool) { return word, true })
	require.NoError(t, err)
	sig, err := feed.JournalSignal(probeJournal(), "probe", "UPDATED")
	require.NoError(t, err)
	reg := prometheus.NewRegistry()
	src, err := feed.NewSource(feed.Config{
		Module: "probe", Service: "probe", Enabled: en,
		Signal: sig, Sealer: sealer, Metrics: reg,
	})
	require.NoError(t, err)
	ctx := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)})
	return &fixture{pool: pool, reg: reg, src: src, ctx: src.Bind(ctx)}
}

func (f *fixture) begin(t *testing.T) *journaltx.Tx {
	t.Helper()
	tx, err := journaltx.Begin(f.ctx, f.pool, journaltx.NewOptions(f.src.Enabled()))
	require.NoError(t, err)
	// Проба, упавшая при открытой транзакции, возвращает соединение пулу;
	// после Commit откат — no-op.
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// seed пишет строку фикстурного ресурса в транзакцию до постановки.
func (f *fixture) seed(t *testing.T, tx *journaltx.Tx) string {
	t.Helper()
	id := ids.NewID("itm")
	_, err := tx.Exec(f.ctx, `INSERT INTO probe_items (id) VALUES ($1)`, id)
	require.NoError(t, err)
	return id
}

func (f *fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.QueryRow(context.Background(), q, args...).Scan(&n))
	return n
}

func (f *fixture) rows(t *testing.T) int {
	return f.count(t, `SELECT count(*) FROM probe_notification_outbox`)
}

func (f *fixture) signals(t *testing.T) int {
	return f.count(t, `SELECT count(*) FROM probe_outbox WHERE resource_kind = 'notification' AND resource_id = 'probe'`)
}

func (f *fixture) windows(t *testing.T) int {
	return f.count(t, `SELECT count(*) FROM probe_notification_window`)
}

func (f *fixture) windowSum(t *testing.T) int {
	return f.count(t, `SELECT coalesce(sum(count), 0)::int FROM probe_notification_window`)
}

// put — одна постановка в своей транзакции со строкой фикстурного ресурса.
// Сторож Put транзакцию не портит: она коммитится при любом исходе Put, и
// строка ресурса после этого есть (NTF1-B27, B28, B29).
func (f *fixture) put(t *testing.T, d feed.TemplateDesc, to string, v feed.Values) error {
	t.Helper()
	tx := f.begin(t)
	item := f.seed(t, tx)
	putErr := feed.Put(f.ctx, tx, d, to, v)
	require.NoError(t, tx.Commit(f.ctx), "транзакция после Put обязана коммититься (исход Put: %v)", putErr)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM probe_items WHERE id = $1`, item))
	return putErr
}

func attrs(kv ...any) feed.Values {
	m := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return feed.Values{Attrs: m}
}

func helloDesc(limits ...feed.Limit) feed.TemplateDesc {
	return feed.TemplateDesc{
		Name: "probe-hello", Class: feed.ClassNotice, SchemaRev: 1, TTL: 72 * time.Hour, Recipient: feed.RecipientAddress,
		Limits: limits,
		Attrs:  []feed.AttrDesc{{Name: "name", Kind: form.KindText, Presence: feed.PresenceRequired, Subject: true}},
	}
}

func hello() feed.Values { return attrs("name", "probe") }

func perHour(max int32) feed.Limit {
	return feed.Limit{Scope: feed.ScopeRecipient, WindowSeconds: 3600, Max: max}
}

func linkDesc() feed.TemplateDesc {
	return feed.TemplateDesc{
		Name: "probe-link", Class: feed.ClassNotice, SchemaRev: 1, TTL: time.Hour, Recipient: feed.RecipientAddress,
		Attrs: []feed.AttrDesc{
			{Name: "subject_name", Kind: form.KindText, Presence: feed.PresenceRequired, Subject: true},
			{Name: "target", Kind: form.KindPath, Presence: feed.PresenceRequired},
			{Name: "token", Kind: form.KindToken, Presence: feed.PresenceRequired},
		},
	}
}

func linkTwin() map[string]any {
	return map[string]any{"subject_name": "probe", "target": "/", "token": "0123456789abcdefghijklmnopqrstuv"}
}

func allDesc() feed.TemplateDesc {
	return feed.TemplateDesc{
		Name: "probe-all", Class: feed.ClassNotice, SchemaRev: 1, TTL: time.Hour, Recipient: feed.RecipientAddress,
		Attrs: []feed.AttrDesc{
			{Name: "code", Kind: form.KindSecret, Presence: feed.PresenceRequired},
			{Name: "issued_at", Kind: form.KindTimestamp, Presence: feed.PresenceRequired},
			{Name: "note", Kind: form.KindText, Presence: feed.PresenceRequired},
			{Name: "subject_name", Kind: form.KindText, Presence: feed.PresenceRequired, Subject: true},
			{Name: "target", Kind: form.KindPath, Presence: feed.PresenceRequired},
			{Name: "token", Kind: form.KindToken, Presence: feed.PresenceRequired},
		},
	}
}

func allTwin() map[string]any {
	return map[string]any{
		"subject_name": "probe", "note": "n", "code": "123456", "target": "/",
		"token": "0123456789abcdefghijklmnopqrstuv", "issued_at": time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
	}
}

func optDesc() feed.TemplateDesc {
	return feed.TemplateDesc{
		Name: "probe-opt", Class: feed.ClassNotice, SchemaRev: 1, TTL: time.Hour, Recipient: feed.RecipientAddress,
		Attrs: []feed.AttrDesc{
			{Name: "inviter", Kind: form.KindText, Presence: feed.PresenceOptional},
			{Name: "subject_name", Kind: form.KindText, Presence: feed.PresenceRequired, Subject: true},
			{Name: "target", Kind: form.KindPath, Presence: feed.PresenceOptional},
		},
	}
}

// requireNamed — ошибка сторожа называет атрибут и не несёт значения.
func requireNamed(t *testing.T, err error, guard error, name string, values ...string) {
	t.Helper()
	require.ErrorIs(t, err, guard)
	require.Contains(t, err.Error(), name)
	for _, v := range values {
		if v != "" && !strings.Contains(name, v) {
			require.NotContains(t, err.Error(), v, "ошибка несёт значение")
		}
	}
}

// metricValue читает одну серию счётчика или датчика без testutil (он тянул
// бы в go.mod новую зависимость ради пробы).
func metricValue(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	close(ch)
	m, ok := <-ch
	require.True(t, ok, "серии нет")
	var d dto.Metric
	require.NoError(t, m.Write(&d))
	switch {
	case d.Counter != nil:
		return d.GetCounter().GetValue()
	case d.Gauge != nil:
		return d.GetGauge().GetValue()
	}
	t.Fatalf("серия не счётчик и не датчик: %s", fmt.Sprint(&d))
	return 0
}
