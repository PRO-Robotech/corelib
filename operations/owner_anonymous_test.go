// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

// «Личность по умолчанию» не может быть владельцем ничего.
//
// PrincipalFromContext на пустом (и на явно очищенном) контексте отдаёт
// SystemPrincipal() = {system, bootstrap} — это backward-compat fallback для
// ЗАПИСИ операций фоновыми путями, а не удостоверение личности. Если owner-ключ
// для ЧТЕНИЯ выводится из него, анонимный вызывающий совпадает с
// ownership-предикатом на каждой операции, записанной системным принципалом.
//
// Правильный вывод owner-ключа — OwnerFromContext: он различает «принципал явно
// установлен» и «принципала не было», и во втором случае не отдаёт ключ вовсе.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/operations"
)

// TestOwnerFromContext_AnonymousYieldsNoOwner — пустой ctx не даёт owner-ключа.
func TestOwnerFromContext_AnonymousYieldsNoOwner(t *testing.T) {
	owner, ok := operations.OwnerFromContext(context.Background())
	require.False(t, ok, "anonymous ctx must not yield an owner key")
	require.Equal(t, operations.Owner{}, owner, "anonymous ctx must yield the zero owner, never the system fallback")
}

// TestOwnerFromContext_ClearedPrincipalYieldsNoOwner — принципал, снятый
// transport-слоем на недоверенном форвардере, тоже не даёт ключа.
func TestOwnerFromContext_ClearedPrincipalYieldsNoOwner(t *testing.T) {
	ctx := operations.WithoutPrincipal(operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: "usr-1"}))
	owner, ok := operations.OwnerFromContext(ctx)
	require.False(t, ok, "explicitly cleared principal must not yield an owner key")
	require.Equal(t, operations.Owner{}, owner)
}

// TestOwnerFromContext_ExplicitPrincipalYieldsOwner — подлинный принципал даёт
// свой ключ (сужаем анонимность, не ломаем владельца).
func TestOwnerFromContext_ExplicitPrincipalYieldsOwner(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: "usr-1"})
	owner, ok := operations.OwnerFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, operations.Owner{PrincipalType: "user", PrincipalID: "usr-1"}, owner)
}

// TestOwnerFromContext_ExplicitSystemPrincipalYieldsOwner — ЯВНО установленный
// системный принципал (доверенный internal-путь) остаётся владельцем: сужается
// именно анонимность, а не bootstrap-личность как таковая.
func TestOwnerFromContext_ExplicitSystemPrincipalYieldsOwner(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.SystemPrincipal())
	owner, ok := operations.OwnerFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, operations.Owner{PrincipalType: "system", PrincipalID: "bootstrap"}, owner)
}

// TestOwnedRepo_ZeroOwnerNeverReachesPredicate — backstop на слое repo: нулевой
// owner-ключ (что и отдаёт OwnerFromContext на анонимном ctx) обязан быть
// отвергнут ДО того, как попадёт в SQL-предикат владельца. Пул здесь nil:
// если guard'а нет, вызов доходит до запроса и падает на nil-пуле — это и есть
// доказательство, что нулевой ключ доезжал до предиката.
func TestOwnedRepo_ZeroOwnerNeverReachesPredicate(t *testing.T) {
	owned, ok := operations.AsOwned(operations.NewRepo(nil, "public"))
	require.True(t, ok, "pgRepo must implement OwnedOperationRepo")

	_, err := owned.GetOwned(context.Background(), "opq00000000000000001", operations.Owner{})
	require.ErrorIs(t, err, operations.ErrNotFound)

	_, err = owned.CancelOwned(context.Background(), "opq00000000000000001", operations.Owner{})
	require.ErrorIs(t, err, operations.ErrNotFound)

	page, next, err := owned.ListOwned(context.Background(), operations.ListFilter{PageSize: 50}, operations.Owner{})
	require.NoError(t, err)
	require.Empty(t, page, "zero owner must list nothing")
	require.Empty(t, next)
}
