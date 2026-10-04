// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

// Пробы функции базы resource-event формы fanout (полоса X2-F NTF-3; приёмка
// ac1f9fc9…, NTF3-65, NTF3-68, NTF3-160 (б), УК3-49; замысел З10 п.1–5,
// CX3C-07) на Postgres 16 (testcontainers через pgtest): журнал модуля svc
// формы NTF-3 (колонка инициатора из настройки транзакции, время — DEFAULT
// now()), лента svc миграцией схемы v1, функция и триггер — миграцией,
// которую пишет notifygen init. Порядок несущий: база и фикстура журнала и
// ленты строятся ДО обращения к испытуемому; их провал — «проба не
// исполнилась», а не красный.

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/feed/schema"
	"github.com/PRO-Robotech/corelib/notify/spec"
	"github.com/PRO-Robotech/corelib/pgtest"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{Name: "notifygen"}))
}

// svcJournalDDL — журнал модуля svc в форме NTF-3 (замысел §6).
const svcJournalDDL = `
CREATE TABLE svc_journal (
    sequence_no   bigserial   PRIMARY KEY,
    resource_kind text        NOT NULL,
    resource_id   text        NOT NULL,
    event_type    text        NOT NULL,
    payload       jsonb       NOT NULL DEFAULT '{}',
    project_id    text        NOT NULL DEFAULT '',
    initiator     text        NOT NULL DEFAULT NULLIF(current_setting('kacho_journal.initiator', true), ''),
    created_at    timestamptz NOT NULL DEFAULT now()
);
`

const probeInitiator = "user:usr-a1"

// upOf — часть Up миграции goose.
func upOf(t *testing.T, content string) string {
	t.Helper()
	_, rest, ok := strings.Cut(content, "-- +goose Up")
	if !ok {
		t.Fatalf("в миграции нет раздела -- +goose Up:\n%s", content)
	}
	up, _, _ := strings.Cut(rest, "-- +goose Down")
	return up
}

// fanoutDB — база пробы: журнал и лента svc (фикстура), затем испытуемый —
// миграция функции resource-event, которую пишет init.
type fanoutDB struct {
	pool *pgxpool.Pool
	tr   *tree
	ctx  context.Context
}

func newFanoutDB(t *testing.T) *fanoutDB {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.NewEmptyDB(t))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: пул: %v", err)
	}
	pgtest.ClosePoolAtEnd(t, pool)
	up, _, err := schema.DDL("svc", schema.V1)
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: схема ленты v1: %v", err)
	}
	if _, err := pool.Exec(ctx, svcJournalDDL+up); err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: журнал и лента svc: %v", err)
	}
	tr := fanoutTree(t)

	// Испытуемый.
	requireInitOK(t, initFanout(tr, testOptions()))
	files := fanoutFiles(t, tr)
	require.NotEmpty(t, files, "init не написал миграции функции resource-event")
	for _, f := range files {
		_, err := pool.Exec(ctx, upOf(t, tr.read(migrationPath(f))))
		require.NoError(t, err, "миграция функции %s не применяется", f)
	}
	return &fanoutDB{pool: pool, tr: tr, ctx: ctx}
}

// jrow — строка журнала, которую пишет писатель модуля.
type jrow struct {
	kind, id, change, project string
	payload                   map[string]any
}

// write — транзакция писателя: настройки (nil — не выставляется), строки
// журнала, коммит. Ошибка любого шага — откат и отказ.
func (d *fanoutDB) write(t *testing.T, settings map[string]string, rows ...jrow) error {
	t.Helper()
	tx, err := d.pool.Begin(d.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(d.ctx) }() // после Commit — пустой ход
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := tx.Exec(d.ctx, `SELECT set_config($1, $2, true)`, k, settings[k]); err != nil {
			return err
		}
	}
	for _, r := range rows {
		p := r.payload
		if p == nil {
			p = map[string]any{}
		}
		b, err := json.Marshal(p)
		require.NoError(t, err)
		if _, err := tx.Exec(d.ctx, `INSERT INTO svc_journal (resource_kind, resource_id, event_type, payload, project_id)
			VALUES ($1, $2, $3, $4, $5)`, r.kind, r.id, r.change, b, r.project); err != nil {
			return err
		}
	}
	return tx.Commit(d.ctx)
}

func on(flag string) map[string]string {
	return map[string]string{"kacho_feed.enabled": flag, "kacho_journal.initiator": probeInitiator}
}

func (d *fanoutDB) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, d.pool.QueryRow(d.ctx, q, args...).Scan(&n))
	return n
}

func (d *fanoutDB) feedCount(t *testing.T) int {
	return d.count(t, `SELECT count(*) FROM svc_notification_outbox`)
}

func (d *fanoutDB) journalCount(t *testing.T, kind string) int {
	return d.count(t, `SELECT count(*) FROM svc_journal WHERE resource_kind = $1`, kind)
}

// feedRow — строка ленты, как её читает notify-sender.
type feedRow struct {
	id, template, class, recipient, state string
	schemaRev                             int
	attrs                                 map[string]any
	secretNull                            bool
	ttl                                   time.Duration
}

func (d *fanoutDB) feedRows(t *testing.T) []feedRow {
	t.Helper()
	rows, err := d.pool.Query(d.ctx, `SELECT id, template, schema_rev, class, recipient_address, attrs,
		secret_attrs IS NULL, state, EXTRACT(EPOCH FROM expires_at - enqueued_at)::bigint
		FROM svc_notification_outbox ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []feedRow
	for rows.Next() {
		var (
			r     feedRow
			attrs []byte
			ttl   int64
		)
		require.NoError(t, rows.Scan(&r.id, &r.template, &r.schemaRev, &r.class, &r.recipient, &attrs,
			&r.secretNull, &r.state, &ttl))
		require.NoError(t, json.Unmarshal(attrs, &r.attrs))
		r.ttl = time.Duration(ttl) * time.Second
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func attrKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// NTF3-65 (флаг false — 0 строк), З10 п.1 и п.5: при kacho_feed.enabled =
// false функция выходит без строки ленты и без строки сигнала; близнец —
// флаг true: одна строка ленты и одна строка сигнала. Отличие — значение
// флага.
func TestNTF3_X2F_FunctionFlagFalseWritesNoFeedRow(t *testing.T) {
	ev := jrow{kind: "Volume", id: "vol-1", change: "created", project: "prj-1"}

	off := newFanoutDB(t)
	require.NoError(t, off.write(t, on("false"), ev))
	require.Equal(t, 0, off.feedCount(t), "флаг false: строк ленты 0")
	require.Equal(t, 0, off.journalCount(t, spec.FeedSignalKey), "флаг false: строк сигнала 0")
	require.Equal(t, 1, off.journalCount(t, "Volume"))

	twin := newFanoutDB(t)
	require.NoError(t, twin.write(t, on("true"), ev))
	require.Equal(t, 1, twin.feedCount(t), "флаг true: строка ленты 1")
	require.Equal(t, 1, twin.journalCount(t, spec.FeedSignalKey), "флаг true: строка сигнала 1")
}

// З10 п.1: настройка флага не выставлена — отказ с именем настройки,
// подстановки нет; строка журнала откатывается вместе с отказом. Близнец —
// настройка выставлена.
func TestNTF3_X2F_FunctionRefusesWithoutTheFlagSetting(t *testing.T) {
	d := newFanoutDB(t)
	ev := jrow{kind: "Volume", id: "vol-1", change: "created", project: "prj-1"}
	err := d.write(t, map[string]string{"kacho_journal.initiator": probeInitiator}, ev)
	require.Error(t, err, "без настройки kacho_feed.enabled функция обязана отказать")
	require.Contains(t, err.Error(), "kacho_feed.enabled")
	require.Equal(t, 0, d.journalCount(t, "Volume"))
	require.Equal(t, 0, d.feedCount(t))

	require.NoError(t, d.write(t, on("true"), ev), "близнец: настройка выставлена")
	require.Equal(t, 1, d.feedCount(t))
}

// З10 п.2, CX3C-04 (в): вид вне таблицы, выведенной из объявления журнала, —
// отказ с именем вида, подстановки нет. Близнец — вид из таблицы.
func TestNTF3_X2F_FunctionRefusesAKindOutsideTheTable(t *testing.T) {
	d := newFanoutDB(t)
	err := d.write(t, on("true"), jrow{kind: "Bucket", id: "bkt-1", change: "created", project: "prj-1"})
	require.Error(t, err, "вид вне таблицы обязан дать отказ")
	require.Contains(t, err.Error(), "Bucket")
	require.Equal(t, 0, d.feedCount(t))

	require.NoError(t, d.write(t, on("true"), jrow{kind: "Volume", id: "vol-1", change: "created", project: "prj-1"}))
	require.Equal(t, 1, d.feedCount(t))
}

// NTF3-68 (половина функции базы), Р3, З10 п.3–4: строка ленты формы fanout
// — шаблон resource-event, schema_rev из revision.yaml, класс notice, ttl 72h,
// адресата и секрета нет, id формы строки ленты Go-писателя (ntf-…);
// атрибуты — ровно закрытый список Р3, значения из строки журнала: вид и род
// изменения переведены таблицей, scope — якорь вида, initiator —
// NEW.initiator, occurred_at — время строки журнала, усечённое до секунды,
// name — из нагрузки снятия вида с именем.
func TestNTF3_X2F_FeedRowCarriesTheClosedAttributeList(t *testing.T) {
	d := newFanoutDB(t)
	require.NoError(t, d.write(t, on("true"),
		jrow{kind: "Volume", id: "vol-7", change: "deleted", project: "prj-1", payload: map[string]any{"name": "data-7"}}))

	var occurred time.Time
	require.NoError(t, d.pool.QueryRow(d.ctx,
		`SELECT date_trunc('second', created_at) FROM svc_journal WHERE resource_kind = 'Volume'`).Scan(&occurred))
	rev, err := spec.ReadRevision([]byte(d.tr.read("svc/notifications/resource-event/revision.yaml")))
	require.NoError(t, err)

	rows := d.feedRows(t)
	require.Len(t, rows, 1)
	r := rows[0]
	require.Equal(t, "resource-event", r.template)
	require.Equal(t, rev.Number, r.schemaRev)
	require.Equal(t, "notice", r.class)
	require.Equal(t, "", r.recipient, "у fanout адресата нет")
	require.True(t, r.secretNull, "секрета нет")
	require.Equal(t, "pending", r.state)
	require.Equal(t, 72*time.Hour, r.ttl)
	require.True(t, strings.HasPrefix(r.id, ids.PrefixNotificationHyphen+"-"), "id строки ленты: %q", r.id)

	require.Equal(t, []string{"change", "initiator", "kind", "name", "occurred_at", "resource_id", "scope"}, attrKeys(r.attrs))
	require.Equal(t, "Volume", r.attrs["kind"])
	require.Equal(t, "vol-7", r.attrs["resource_id"])
	require.Equal(t, "project:prj-1", r.attrs["scope"])
	require.Equal(t, "DELETED", r.attrs["change"])
	require.Equal(t, probeInitiator, r.attrs["initiator"])
	require.Equal(t, "data-7", r.attrs["name"])
	s, ok := r.attrs["occurred_at"].(string)
	require.True(t, ok, "occurred_at — строка времени: %#v", r.attrs["occurred_at"])
	at, err := time.Parse(time.RFC3339Nano, s)
	require.NoError(t, err)
	require.True(t, occurred.Equal(at), "occurred_at %s, время строки журнала %s", at, occurred)
}

// УК3-49 (половина генератора), CX3J-01 (в), З10 п.3: снятие вида без имени
// (Repository, NameFormNone) даёт строку ленты БЕЗ ключа name — не пустая строка и не
// null; близнец — снятие вида с именем (Volume) при той же нагрузке — ключ
// name с именем. Отличие — вид. Go-половину (SendX опускает незаданный
// optional) держит TestNTF3_Z10_FanoutSendXHasNoRecipientAndOptionalName.
func TestNTF3_X2F_UK3_49_NamelessKindOmitsTheNameKey(t *testing.T) {
	payload := map[string]any{"name": "x-1"}

	d := newFanoutDB(t)
	require.NoError(t, d.write(t, on("true"), jrow{kind: "Repository", id: "rep-1", change: "deleted", project: "prj-1", payload: payload}))
	rows := d.feedRows(t)
	require.Len(t, rows, 1)
	require.NotContains(t, rows[0].attrs, "name", "вид без имени: ключа name нет")

	twin := newFanoutDB(t)
	require.NoError(t, twin.write(t, on("true"), jrow{kind: "Volume", id: "vol-1", change: "deleted", project: "prj-1", payload: payload}))
	rows = twin.feedRows(t)
	require.Len(t, rows, 1)
	require.Equal(t, "x-1", rows[0].attrs["name"], "вид с именем: ключ name с именем")
}

// З10 п.3: имя только у снятия — правка вида с именем не несёт ключа name.
func TestNTF3_X2F_NameOnlyOnDeleted(t *testing.T) {
	d := newFanoutDB(t)
	require.NoError(t, d.write(t, on("true"), jrow{kind: "Volume", id: "vol-1", change: "updated", project: "prj-1",
		payload: map[string]any{"name": "data-1"}}))
	rows := d.feedRows(t)
	require.Len(t, rows, 1)
	require.Equal(t, "UPDATED", rows[0].attrs["change"])
	require.NotContains(t, rows[0].attrs, "name")
}

// CX3C-07, NTF3-160 (б), З10 п.5: каждая строка журнала даёт свою строку ленты
// (две строки одной транзакции — две строки ленты с разными id); на каждую —
// строка сигнала notification с объектом-модулем, как у Go-писателя
// (feed.JournalSignal: ID — модуль); строка сигнала строки ленты не даёт
// (WHEN триггера).
func TestNTF3_X2F_OneFeedRowPerJournalRowAndTheSignalIsNotFanned(t *testing.T) {
	d := newFanoutDB(t)
	ev := jrow{kind: "Volume", id: "vol-1", change: "updated", project: "prj-1"}
	require.NoError(t, d.write(t, on("true"), ev, ev))

	rows := d.feedRows(t)
	require.Len(t, rows, 2, "строк ленты на строку журнала — 1")
	require.NotEqual(t, rows[0].id, rows[1].id)
	require.Equal(t, 2, d.journalCount(t, spec.FeedSignalKey), "строка сигнала на каждое событие")
	require.Equal(t, 2, d.count(t, `SELECT count(*) FROM svc_journal WHERE resource_kind = $1 AND resource_id = 'svc'`,
		spec.FeedSignalKey), "объект сигнала — модуль svc")
}
