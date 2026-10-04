// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

// Пробы реализации формы fanout сверх RED полосы X2-F: откат миграции
// CREATE OR REPLACE возвращает прежнее определение; id строки ленты — форма
// каталога ids; якорь проектного вида обязателен; имя вне формы DNS-метки у
// снятия — ключа name нет (как у читателя журнала, subscription.deletedName).

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/ids"
)

// downOf — часть Down миграции goose.
func downOf(t *testing.T, content string) string {
	t.Helper()
	_, down, ok := strings.Cut(content, "-- +goose Down")
	if !ok {
		t.Fatalf("в миграции нет раздела -- +goose Down:\n%s", content)
	}
	return down
}

// «Версии тела»: вид Snapshot добавлен — init пишет CREATE OR REPLACE; после
// его Up вид Snapshot даёт строку ленты, после его Down — отказ «вне таблицы»
// (прежнее определение вернулось, триггер тот же). Близнец — Volume, который
// есть в обоих определениях, даёт строку при любом.
func TestFanoutReplaceMigrationDownRestoresThePreviousBody(t *testing.T) {
	d := newFanoutDB(t)
	snap := jrow{kind: "Snapshot", id: "snp-1", change: "created", project: "prj-1"}
	vol := jrow{kind: "Volume", id: "vol-1", change: "created", project: "prj-1"}
	require.Error(t, d.write(t, on("true"), snap), "до замены вида Snapshot в таблице нет")

	fanoutDecl(d.tr, strings.Replace(fanoutJournal, "  notification: {name_form: none, scope: cluster}\n",
		"  notification: {name_form: none, scope: cluster}\n  Snapshot: {name_form: dns, scope: project}\n", 1))
	o := testOptions()
	o.now = func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	requireInitOK(t, initFanout(d.tr, o))
	files := fanoutFiles(t, d.tr)
	require.Len(t, files, 2)
	second := d.tr.read(migrationPath(files[1]))

	_, err := d.pool.Exec(d.ctx, upOf(t, second))
	require.NoError(t, err, "Up замены")
	require.NoError(t, d.write(t, on("true"), snap), "после замены вид Snapshot в таблице")
	require.NoError(t, d.write(t, on("true"), vol))
	require.Equal(t, 2, d.feedCount(t))

	_, err = d.pool.Exec(d.ctx, downOf(t, second))
	require.NoError(t, err, "Down замены")
	err = d.write(t, on("true"), snap)
	require.Error(t, err, "откат замены вернул прежнее определение")
	require.Contains(t, err.Error(), "Snapshot")
	require.NoError(t, d.write(t, on("true"), vol), "близнец: триггер на месте после отката")
	require.Equal(t, 3, d.feedCount(t))
	require.Equal(t, 1, d.count(t, `SELECT count(*) FROM pg_trigger WHERE tgname = 'notify_feed_resource_event'`))
}

// id строки ленты, выпущенный телом, — форма каталога ids (ntf-<17
// Crockford>), на каждую строку свой.
func TestFanoutFeedRowIDIsOfTheIDsCatalogForm(t *testing.T) {
	d := newFanoutDB(t)
	ev := jrow{kind: "Volume", id: "vol-1", change: "created", project: "prj-1"}
	require.NoError(t, d.write(t, on("true"), ev, ev, ev, ev))
	seen := map[string]bool{}
	for _, r := range d.feedRows(t) {
		require.True(t, ids.IsValidHyphen(r.id, ids.PrefixNotificationHyphen), "id %q вне формы ids", r.id)
		require.False(t, seen[r.id])
		seen[r.id] = true
	}
	require.Len(t, seen, 4)
}

// Проектный вид без якоря — отказ, строки журнала нет; близнец — с якорем.
func TestFanoutProjectKindWithoutAnchorIsRefused(t *testing.T) {
	d := newFanoutDB(t)
	err := d.write(t, on("true"), jrow{kind: "Volume", id: "vol-1", change: "created", project: ""})
	require.Error(t, err)
	require.Contains(t, err.Error(), "project anchor")
	require.Equal(t, 0, d.journalCount(t, "Volume"))
	require.NoError(t, d.write(t, on("true"), jrow{kind: "Volume", id: "vol-1", change: "created", project: "prj-1"}))
}

// Снятие вида с именем, чьё имя вне формы DNS-метки, — строка ленты без
// ключа name; близнец — имя формы DNS-метки.
func TestFanoutDeletedNameOutsideTheFormIsOmitted(t *testing.T) {
	d := newFanoutDB(t)
	require.NoError(t, d.write(t, on("true"), jrow{kind: "Volume", id: "vol-1", change: "deleted", project: "prj-1",
		payload: map[string]any{"name": "Data_7"}}))
	require.NoError(t, d.write(t, on("true"), jrow{kind: "Volume", id: "vol-2", change: "deleted", project: "prj-1",
		payload: map[string]any{"name": "data-7"}}))
	rows := d.feedRows(t)
	require.Len(t, rows, 2)
	byID := map[string]feedRow{}
	for _, r := range rows {
		byID[r.attrs["resource_id"].(string)] = r
	}
	require.NotContains(t, byID["vol-1"].attrs, "name")
	require.Equal(t, "data-7", byID["vol-2"].attrs["name"])
}
