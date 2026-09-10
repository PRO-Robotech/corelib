// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

// «Неизвестно кто» не должно иметь ИМЕНИ.
//
// owner_anonymous_test.go закрывает случай, когда принципала в контексте нет
// вовсе. Это половина: у нас есть и ИМЕНОВАННАЯ анонимность — пара
// {system, anonymous}, которую edge выставляет запросу без credential'а. Пара
// непуста, поэтому её ключ владения выглядит как настоящий и совпадает сам с
// собой: любые два безымянных запроса делят один ключ, то есть один читает и
// отменяет операции другого.
//
// Замок: именованный маркер анонимности обязан вести себя как ОТСУТСТВИЕ
// принципала — не давать ключа владения и отсекаться до предиката владельца.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/operations"
)

// anonymousMarker — то, что edge кладёт в контекст запросу без credential'а.
func anonymousMarker() operations.Principal {
	return operations.Principal{Type: "system", ID: "anonymous"}
}

// TestOwnerIsAnonymous_TrueForNamedMarker — предикат «ключа владения нет»
// обязан быть истинным и для именованной анонимности, а не только для пустой
// пары.
func TestOwnerIsAnonymous_TrueForNamedMarker(t *testing.T) {
	owner := operations.OwnerFromPrincipal(anonymousMarker())
	require.True(t, owner.IsAnonymous(),
		"именованный маркер анонимности обязан быть анонимным ключом, а не владельцем")
}

// TestOwnerFromContext_NamedAnonymousMarkerYieldsNoOwner — контекст с
// именованным маркером не даёт ключа владения, ровно как пустой контекст.
func TestOwnerFromContext_NamedAnonymousMarkerYieldsNoOwner(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), anonymousMarker())
	owner, ok := operations.OwnerFromContext(ctx)
	require.False(t, ok, "именованный аноним не владелец: ключ выдаваться не должен")
	require.Equal(t, operations.Owner{}, owner,
		"на именованном анониме обязан вернуться нулевой ключ, а не пара {system, anonymous}")
}

// TestOwnedRepo_NamedAnonymousOwnerNeverReachesPredicate — backstop на слое
// repo: ключ именованного анонима обязан быть отвергнут ДО построения запроса.
// Пул здесь nil: если guard'а нет, вызов доходит до SQL и падает на nil-пуле —
// это и есть доказательство, что такой ключ доезжал до предиката владельца.
func TestOwnedRepo_NamedAnonymousOwnerNeverReachesPredicate(t *testing.T) {
	owned, ok := operations.AsOwned(operations.NewRepo(nil, "public"))
	require.True(t, ok, "pgRepo must implement OwnedOperationRepo")
	anon := operations.OwnerFromPrincipal(anonymousMarker())

	_, err := owned.GetOwned(context.Background(), "opq00000000000000001", anon)
	require.ErrorIs(t, err, operations.ErrNotFound)

	_, err = owned.CancelOwned(context.Background(), "opq00000000000000001", anon)
	require.ErrorIs(t, err, operations.ErrNotFound)

	page, next, err := owned.ListOwned(context.Background(), operations.ListFilter{PageSize: 50}, anon)
	require.NoError(t, err)
	require.Empty(t, page, "именованный аноним не должен видеть ни одной операции")
	require.Empty(t, next)
}
