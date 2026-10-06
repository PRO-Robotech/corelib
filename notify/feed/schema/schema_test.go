// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package schema_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// З6, §6: миграция ленты несёт три таблицы, два частичных индекса и
// ограничения; функций, процедур, триггеров нет (УК87 (а)).
func TestMigrationV1CarriesTablesAndNoFunctions(t *testing.T) {
	m, err := schema.Migration("probe", schema.V1)
	require.NoError(t, err)
	for _, want := range []string{
		"-- +goose Up",
		"-- +goose Down",
		`CREATE TABLE "probe_notification_outbox"`,
		`CREATE TABLE "probe_notification_window"`,
		`CREATE TABLE "probe_notification_contrib"`,
		"closed_carries_no_secret",
		"outcome_matches_state",
		"outcome_pair",
		"isfinite(not_before)",
		"schema_rev >= 1",
		"window_seconds > 0",
		"ON DELETE CASCADE",
		`WHERE state = 'pending'`,
		`WHERE state <> 'pending'`,
	} {
		require.Contains(t, m, want)
	}
	up := strings.ToUpper(m)
	for _, banned := range []string{"CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", "PROCEDURE", "TRIGGER"} {
		require.NotContains(t, up, banned)
	}
}

// Вывод побайтно воспроизводим: генератор сверяет с ним применённый файл (D04).
func TestMigrationIsDeterministic(t *testing.T) {
	a, err := schema.Migration("probe", schema.V1)
	require.NoError(t, err)
	b, err := schema.Migration("probe", schema.V1)
	require.NoError(t, err)
	require.Equal(t, a, b)
	// Х3 NTF-4 и C5 NTF-5: словарь исходов и строение ленты расширяются
	// новыми версиями схемы; V1 и V2 остаются выпущенными (применённая
	// миграция не правится).
	require.Equal(t, []schema.Version{schema.V1, 2, 3}, schema.Versions())
	require.Equal(t, schema.Version(3), schema.Current())
}

func TestMigrationRefusesAnUnknownVersionAndABadPrefix(t *testing.T) {
	_, err := schema.Migration("probe", schema.Version(99))
	require.Error(t, err)
	_, err = schema.Migration("Pro-be", schema.V1)
	require.Error(t, err)
}

// Пара «состояние × причина» объявлена один раз: CHECK строится из неё, и её
// же читает Ack (З6). У pending, sent и recipient_rejected причины нет.
func TestOutcomePairsAreTheOneTable(t *testing.T) {
	pairs := schema.OutcomePairs()
	require.Empty(t, pairs["pending"])
	require.Empty(t, pairs["sent"])
	require.Empty(t, pairs["recipient_rejected"])
	require.Equal(t, []string{"revoked"}, pairs["denied"])
	require.Equal(t, []string{"recipient_net"}, pairs["dropped"])
	require.Contains(t, pairs["invalid"], "attrs_invalid")
	require.Contains(t, pairs["expired"], "unclaimed")
	// Х3 NTF-4: восьмое состояние — suppressed; C5 NTF-5: девятое —
	// superseded.
	require.Len(t, schema.States(), 9)
	m, err := schema.Migration("probe", schema.Current())
	require.NoError(t, err)
	for _, st := range schema.States() {
		require.Contains(t, m, "'"+st+"'")
	}
}
