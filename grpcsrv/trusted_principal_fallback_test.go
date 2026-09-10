// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// Отсутствие носителя личности — это отсутствие, а не bootstrap.
//
// Аксессор доверенного принципала на контексте БЕЗ носителя отдавал
// системный принципал (в паре с trusted=false). Пара «значение + флаг»
// защищает ровно до первого вызывающего, который прочитает значение и не
// посмотрит на флаг: он получит bootstrap-личность на запросе, у которого
// личности нет. Значение обязано быть пустым само по себе — тогда ошибиться
// нечем.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/grpcsrv"
	"github.com/PRO-Robotech/corelib/operations"
)

// TestTrustedPrincipalFromContext_NoCarrier_ZeroPrincipal — контекст, через
// который интерсептор не проходил, не несёт личности.
func TestTrustedPrincipalFromContext_NoCarrier_ZeroPrincipal(t *testing.T) {
	p, trusted := grpcsrv.TrustedPrincipalFromContext(context.Background())
	require.False(t, trusted, "без носителя доверять нечему")
	require.Equal(t, operations.Principal{}, p,
		"без носителя личности нет — bootstrap не подставляется")
	require.True(t, p.IsAnonymous(), "и она обязана читаться как «никто»")
}

// TestTrustedPrincipalFromContext_NilContext_ZeroPrincipal — то же на nil-ctx.
func TestTrustedPrincipalFromContext_NilContext_ZeroPrincipal(t *testing.T) {
	p, trusted := grpcsrv.TrustedPrincipalFromContext(nil) //nolint:staticcheck // nil-ctx is the defensive branch under test
	require.False(t, trusted)
	require.Equal(t, operations.Principal{}, p)
}

// TestWithTrustedACR_NoPriorCarrier_DoesNotInventPrincipal — конструктор,
// проставляющий только acr, не обязан придумывать личность за запрос.
func TestWithTrustedACR_NoPriorCarrier_DoesNotInventPrincipal(t *testing.T) {
	ctx := grpcsrv.WithTrustedACR(context.Background(), "2", true)
	p, trusted := grpcsrv.TrustedPrincipalFromContext(ctx)
	require.True(t, trusted, "флаг доверия — то, что конструктор и просили выставить")
	require.Equal(t, operations.Principal{}, p,
		"acr сам по себе никого не называет — личность остаётся пустой")
}

// TestWithTrustedACR_KeepsPreviouslyRecordedPrincipal — сужаем выдумывание, а
// не перенос: уже записанная личность конструктором acr не затирается.
func TestWithTrustedACR_KeepsPreviouslyRecordedPrincipal(t *testing.T) {
	alice := operations.Principal{Type: "user", ID: "usr-alice"}
	ctx := grpcsrv.WithTrustedPrincipal(context.Background(), alice, true)
	ctx = grpcsrv.WithTrustedACR(ctx, "2", true)
	p, trusted := grpcsrv.TrustedPrincipalFromContext(ctx)
	require.True(t, trusted)
	require.Equal(t, alice, p)
}
