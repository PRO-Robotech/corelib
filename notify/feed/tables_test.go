// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// Владелец, судящий отказ сервера по имени таблицы (перепись проверок схемы),
// узнаёт таблицу ленты своей службы через TablesOf, не собирая имени сам.
// Источник имён — та же DDL, что ставит миграция ленты: каждая таблица,
// которую создаёт schema.DDL, узнаётся; близнецы — чужая служба, имя со
// схемой, соседняя таблица владельца — нет.
func TestTablesOfRecognisesEveryTableTheFeedSchemaCreates(t *testing.T) {
	for _, svc := range []string{"probe", "kaname.kaname"} {
		tables, err := feed.TablesOf(svc)
		require.NoError(t, err)

		up, _, err := schema.DDL(svc, schema.Current())
		require.NoError(t, err)
		created := createdTables(t, up)
		require.Len(t, created, 3, "DDL ленты %s создаёт три таблицы", svc)
		for _, c := range created {
			require.True(t, tables.Has(c.table), "%s: таблица %q из DDL ленты не узнана", svc, c.table)
		}

		other, err := feed.TablesOf("other")
		require.NoError(t, err)
		for _, c := range created {
			require.False(t, other.Has(c.table), "чужая служба узнала %q", c.table)
			require.False(t, tables.Has(c.schema+"."+c.table), "имя со схемой %q", c.table)
		}
		require.False(t, tables.Has("notification_grants"))
		require.False(t, tables.Has(""))
	}
}

// Негодный префикс — отказ при построении, а не набор, молча не узнающий
// ничего; нулевое значение не узнаёт ни одной таблицы.
func TestTablesOfRefusesAPrefixThatIsNotAServiceName(t *testing.T) {
	for _, bad := range []string{"", "Kaname", "a.b.c", "kaname;drop"} {
		_, err := feed.TablesOf(bad)
		require.Error(t, err, "%q", bad)
	}
	good, err := feed.TablesOf("probe")
	require.NoError(t, err)
	up, _, err := schema.DDL("probe", schema.Current())
	require.NoError(t, err)
	var zero feed.Tables
	for _, c := range createdTables(t, up) {
		require.True(t, good.Has(c.table))
		require.False(t, zero.Has(c.table), "нулевое значение узнало %q", c.table)
	}
}

// createdTable — таблица, которую ставит оператор CREATE TABLE DDL ленты: имя
// так, как сервер отдаёт его в отказе (без схемы и кавычек), и схема.
type createdTable struct{ schema, table string }

var createTable = regexp.MustCompile(`CREATE TABLE (?:"([a-z0-9_]+)"\.)?"([a-z0-9_]+)"`)

func createdTables(t *testing.T, ddl string) []createdTable {
	t.Helper()
	var out []createdTable
	for _, m := range createTable.FindAllStringSubmatch(ddl, -1) {
		out = append(out, createdTable{schema: m[1], table: m[2]})
	}
	require.NotEmpty(t, out, "в DDL ленты нет ни одного CREATE TABLE — обход пуст")
	return out
}
