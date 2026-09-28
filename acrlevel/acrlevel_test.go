// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package acrlevel_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/acrlevel"
)

// Порядок нормативный: ""/"0" < "1" < "2" < "3"; незнакомое — 0 (fail-closed).
func TestRankOrdering(t *testing.T) {
	require.Equal(t, 0, acrlevel.Rank(""), "пусто ⇒ 0")
	require.Equal(t, 0, acrlevel.Rank("0"))
	require.Equal(t, 1, acrlevel.Rank("1"))
	require.Equal(t, 2, acrlevel.Rank("2"))
	require.Equal(t, 3, acrlevel.Rank("3"))
	require.Equal(t, 0, acrlevel.Rank("garbage"), "незнакомое ⇒ 0 (fail-closed)")
	require.Equal(t, 0, acrlevel.Rank("4"), "за верхней ступенью ⇒ 0, а не «выше всех»")
}

// Требование ""/"0" — требования нет; иначе ранг предъявленного не ниже
// требуемого.
func TestSatisfies(t *testing.T) {
	t.Run("no_requirement_always_ok", func(t *testing.T) {
		require.True(t, acrlevel.Satisfies("", ""))
		require.True(t, acrlevel.Satisfies("0", ""))
		require.True(t, acrlevel.Satisfies("", "0"), `required "0" ⇒ требования нет`)
	})
	t.Run("met", func(t *testing.T) {
		require.True(t, acrlevel.Satisfies("2", "2"))
		require.True(t, acrlevel.Satisfies("3", "2"))
	})
	t.Run("not_met", func(t *testing.T) {
		require.False(t, acrlevel.Satisfies("1", "2"))
		require.False(t, acrlevel.Satisfies("", "2"), "отсутствующий acr против требования ⇒ fail-closed")
		require.False(t, acrlevel.Satisfies("garbage", "2"))
	})
}
