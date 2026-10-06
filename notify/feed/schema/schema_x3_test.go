// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package schema_test

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
)

// Выпущенное содержимое миграции V1 для службы probe: sha256 и длина,
// измерены на origin/77-notify@bdb3799 (go run над schema.Migration("probe",
// schema.V1)). Применённая миграция не правится (ban #5): Х3 расширяет
// словарь НОВОЙ версией, а V1 остаётся побайтово прежней.
const (
	v1ProbeSHA256 = "eae62e82c9f99ab2ba56de6dc1a8def2261c9a943568e3e940d2e8c855f3c175"
	v1ProbeLen    = 3154
)

// Охрана Х3: V1 побайтово как выпущена. Красная — на правке V1 вместо новой
// версии.
func TestNTF4X3_V1MigrationStaysAsReleased(t *testing.T) {
	m, err := schema.Migration("probe", schema.V1)
	require.NoError(t, err)
	require.Equal(t, v1ProbeLen, len(m))
	require.Equal(t, v1ProbeSHA256, fmt.Sprintf("%x", sha256.Sum256([]byte(m))))
	require.NotContains(t, m, "suppressed", "V1 не несёт словаря Х3")
}

// suppressedClause — причины из клетки suppressed выражения outcome_pair.
func suppressedClause(m string) ([]string, bool) {
	const head = "(state = 'suppressed' AND outcome_reason IN ("
	i := strings.Index(m, head)
	if i < 0 {
		return nil, false
	}
	rest := m[i+len(head):]
	j := strings.Index(rest, "))")
	if j < 0 {
		return nil, false
	}
	var out []string
	for _, w := range strings.Split(rest[:j], ",") {
		out = append(out, strings.Trim(strings.TrimSpace(w), "'"))
	}
	sort.Strings(out)
	return out, true
}

// Х3 NTF-4 (Р17): действующая версия схемы — новая, после V1; её миграция
// несёт состояние suppressed в CHECK состояния и клетку outcome_pair ровно с
// четырьмя причинами; функций, процедур, триггеров нет (УК87 (а)).
func TestNTF4X3_CurrentSchemaCarriesSuppressedWithItsFourReasons(t *testing.T) {
	vs := schema.Versions()
	require.Equal(t, schema.V1, vs[0], "V1 остаётся выпущенной")
	cur := schema.Current()
	require.Equal(t, vs[len(vs)-1], cur, "действующая — последняя выпущенная")
	require.Greater(t, int(cur), int(schema.V1), "Х3 заводит новую версию схемы, а не правит V1")

	m, err := schema.Migration("probe", cur)
	require.NoError(t, err)
	require.Contains(t, m, fmt.Sprintf("%s service=probe version=%d\n", schema.HeaderPrefix, int(cur)))
	require.Contains(t, schema.States(), "suppressed")
	require.Contains(t, m, "'suppressed'")

	got, ok := suppressedClause(m)
	require.True(t, ok, "в outcome_pair нет клетки suppressed:\n%s", m)
	require.Equal(t, []string{"complaint", "hard_bounce", "soft_bounce", "unsubscribe"}, got)

	up := strings.ToUpper(m)
	for _, banned := range []string{"CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", "PROCEDURE", "TRIGGER"} {
		require.NotContains(t, up, banned)
	}
}

// Самопроверка разбора клетки: законная клетка читается, отсутствующая — нет.
func TestSuppressedClauseReaderKnowsBothSides(t *testing.T) {
	got, ok := suppressedClause("x OR (state = 'suppressed' AND outcome_reason IN ('soft_bounce', 'complaint'))\n")
	require.True(t, ok)
	require.Equal(t, []string{"complaint", "soft_bounce"}, got)
	_, ok = suppressedClause("(state = 'denied' AND outcome_reason IN ('revoked'))")
	require.False(t, ok)
}
