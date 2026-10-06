// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package tablename_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
)

// УК89 (б): имя таблицы ленты — только Of; три вида — лента, окно, вклад.
func TestOfNamesTheThreeFeedTables(t *testing.T) {
	require.Equal(t, `"probe_notification_outbox"`, tablename.Of("probe", tablename.Outbox))
	require.Equal(t, `"probe_notification_window"`, tablename.Of("probe", tablename.Window))
	require.Equal(t, `"probe_notification_contrib"`, tablename.Of("probe", tablename.Contrib))
	require.Equal(t, `"kacho_vpc"."vpc_notification_window"`, tablename.Of("kacho_vpc.vpc", tablename.Window))
	require.Len(t, tablename.Kinds(), 3)
}

// Имя индекса — тоже от функции пакета: суффиксы литералом только здесь.
func TestIndexNameIsUnqualified(t *testing.T) {
	require.Equal(t, `"probe_notification_outbox_pending_idx"`, tablename.Index("probe", tablename.Outbox, tablename.Pending))
	require.Equal(t, `"vpc_notification_outbox_closed_idx"`, tablename.Index("kacho_vpc.vpc", tablename.Outbox, tablename.Closed))
}

// Префикс службы судится до того, как попадёт в оператор.
func TestValidRejectsANameThatIsNotAnIdentifier(t *testing.T) {
	for _, bad := range []string{"", "Probe", "pro-be", "1probe", "a.b.c", "probe;drop", ".probe", "probe."} {
		require.Error(t, tablename.Valid(bad), "%q", bad)
	}
	for _, good := range []string{"probe", "kaname", "kacho_vpc.vpc", "notify_probe"} {
		require.NoError(t, tablename.Valid(good), "%q", good)
	}
}

// Вид вне перечня — не паника на пути Put, а имя, которое сервер отвергает
// разбором любого оператора (пустой идентификатор в кавычках, SQLSTATE 42601):
// ни таблица, ни индекс под ним не создаются и не пишутся. Близнец — три вида
// перечня дают свои имена.
func TestKindOutsideTheListIsAnIdentifierPostgresRefusesNotAPanic(t *testing.T) {
	for _, k := range []tablename.Kind{0, tablename.Kind(len(tablename.Kinds()) + 1), -1} {
		require.NotPanics(t, func() {
			require.Equal(t, `""`, tablename.Of("probe", k), "вид %d", int(k))
			require.Equal(t, `""`, tablename.Index("probe", k, tablename.Pending), "вид %d", int(k))
		})
	}
	for _, k := range tablename.Kinds() {
		require.NotEqual(t, `""`, tablename.Of("probe", k))
	}
}

// Postgres молча усекает идентификатор длиннее 63 байт: два индекса ленты
// длинной службы совпали бы. Valid отвергает префикс, при котором самое
// длинное производное имя (таблица или индекс) не помещается; близнец —
// префикс ровно на границе.
func TestValidRefusesAPrefixWhoseDerivedNameWouldBeTruncated(t *testing.T) {
	longest := 0
	for _, k := range tablename.Kinds() {
		for _, r := range tablename.Roles() {
			name := strings.Trim(tablename.Index("s", k, r), `"`)
			if n := len(name) - 1; n > longest {
				longest = n
			}
		}
		if n := len(strings.Trim(tablename.Of("s", k), `"`)) - 1; n > longest {
			longest = n
		}
	}
	room := 63 - longest
	require.Positive(t, room)
	atLimit := "s" + strings.Repeat("v", room-1)
	require.NoError(t, tablename.Valid(atLimit))
	require.NoError(t, tablename.Valid("kacho_vpc."+atLimit))
	require.Error(t, tablename.Valid(atLimit+"v"))
	require.Error(t, tablename.Valid("kacho_vpc."+atLimit+"v"))
	require.Error(t, tablename.Valid(strings.Repeat("s", 64)+".vpc"), "схема длиннее 63 байт")
	require.NoError(t, tablename.Valid(strings.Repeat("s", 63)+".vpc"))

	// Две роли индекса ленты у префикса на границе — два разных имени.
	require.NotEqual(t,
		tablename.Index(atLimit, tablename.Outbox, tablename.Pending),
		tablename.Index(atLimit, tablename.Outbox, tablename.Closed))
}

// Х3 NTF-4: переход схемы снимает CHECK колонки state ленты по имени, которое
// сервер дал ему по умолчанию (<таблица>_state_check). Имя — от функции
// пакета; без схемы, как у индекса. Бюджет длины Valid его покрывает: на
// префиксе у границы имя не длиннее 63 байт, и сервер его не усёк.
func TestStateCheckIsTheDefaultNameOfTheStateColumnCheck(t *testing.T) {
	require.Equal(t, `"probe_notification_outbox_state_check"`, tablename.StateCheck("probe"))
	require.Equal(t, `"vpc_notification_outbox_state_check"`, tablename.StateCheck("kacho_vpc.vpc"))

	atLimit := ""
	for n := 1; n <= 63; n++ {
		p := "s" + strings.Repeat("v", n-1)
		if tablename.Valid(p) != nil {
			break
		}
		atLimit = p
	}
	require.NotEmpty(t, atLimit)
	require.LessOrEqual(t, len(strings.Trim(tablename.StateCheck(atLimit), `"`)), 63)
}
