// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package tablename_test

import (
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
	require.Equal(t, `"probe_notification_outbox_pending_idx"`, tablename.Index("probe", tablename.Outbox, "pending"))
	require.Equal(t, `"vpc_notification_outbox_closed_idx"`, tablename.Index("kacho_vpc.vpc", tablename.Outbox, "closed"))
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
