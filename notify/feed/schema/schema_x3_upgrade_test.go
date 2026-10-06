// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package schema_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// Х3 NTF-4 (Р17), NTF1-D04: служба, у которой схема V1 уже применена,
// получает действующую версию переходом, а не правкой применённого файла.
// Переход несёт только смену двух ограничений строки ленты — CHECK состояния
// и outcome_pair; таблиц не создаёт, функций, процедур, триггеров нет
// (УК87 (а)). Заголовок несёт from — по нему notifygen -check узнаёт переход.
func TestNTF4X3_UpgradeFromV1CarriesOnlyTheTwoConstraints(t *testing.T) {
	cur := schema.Current()
	m, err := schema.Upgrade("probe", schema.V1, cur)
	require.NoError(t, err)
	require.Contains(t, m, fmt.Sprintf("%s service=probe version=%d from=1\n", schema.HeaderPrefix, int(cur)))
	require.Contains(t, m, "-- +goose Up\n")
	require.Contains(t, m, "-- +goose Down\n")

	up, down, err := schema.UpgradeDDL("probe", schema.V1, cur)
	require.NoError(t, err)
	require.Contains(t, m, up)
	require.Contains(t, m, down)
	for _, part := range []string{up, down} {
		require.Contains(t, part, `ALTER TABLE "probe_notification_outbox"`)
		require.Contains(t, part, `DROP CONSTRAINT "probe_notification_outbox_state_check"`)
		require.Contains(t, part, `ADD CONSTRAINT "probe_notification_outbox_state_check" CHECK (state IN (`)
		require.Contains(t, part, "DROP CONSTRAINT outcome_pair")
		require.Contains(t, part, "ADD CONSTRAINT outcome_pair CHECK (")
		require.NotContains(t, strings.ToUpper(part), "CREATE TABLE")
	}
	got, ok := suppressedClause(up)
	require.True(t, ok, "переход вверх не несёт клетки suppressed:\n%s", up)
	require.Equal(t, []string{"complaint", "hard_bounce", "soft_bounce", "unsubscribe"}, got)
	require.NotContains(t, down, "suppressed", "переход вниз возвращает словарь V1")

	again, err := schema.Upgrade("probe", schema.V1, cur)
	require.NoError(t, err)
	require.Equal(t, m, again, "переход побайтно воспроизводим: -check сверяет с ним применённый файл")

	upper := strings.ToUpper(m)
	for _, banned := range []string{"CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", "PROCEDURE", "TRIGGER"} {
		require.NotContains(t, upper, banned)
	}
}

// Переход выпущен ровно один — V1 → действующая. Прочие пары и негодный
// префикс — отказ с именем пары, а не пустой файл.
func TestNTF4X3_UpgradeRefusesAnUnreleasedTransition(t *testing.T) {
	cur := schema.Current()
	for _, tc := range []struct{ from, to schema.Version }{
		{schema.V1, schema.V1}, {cur, schema.V1}, {cur, cur}, {schema.V1, cur + 1}, {0, cur},
	} {
		_, err := schema.Upgrade("probe", tc.from, tc.to)
		require.ErrorContains(t, err, fmt.Sprintf("v%d→v%d", int(tc.from), int(tc.to)))
	}
	_, err := schema.Upgrade("Pro-be", schema.V1, cur)
	require.Error(t, err)
}
