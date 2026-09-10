// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

// Безымянный запрос не оставляет за собой ключа владения.
//
// anonymous_marker_test.go закрывает ЧТЕНИЕ: именованный аноним не получает
// ключа. Осталась сторона ЗАПИСИ — NewFromContext переносит принципал из ctx в
// операцию, и на безымянном запросе он записал бы `{system, anonymous}` в
// колонки владельца. Строки, помеченные одним общим именем, — это и есть
// предмет проблемы; правильнее не создавать их вовсе, чем полагаться на то, что
// читатель их отсеет.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/operations"
)

// TestNewFromContext_AnonymousMarkerNotStampedAsPrincipal — операция,
// созданная безымянным запросом, не несёт его ярлык как принципала-владельца.
func TestNewFromContext_AnonymousMarkerNotStampedAsPrincipal(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "system", ID: operations.AnonymousPrincipalID,
	})
	op, err := operations.New("enp", "create network", nil)
	require.NoError(t, err)

	fromCtx, err := operations.NewFromContext(ctx, "enp", "create network", nil)
	require.NoError(t, err)
	require.Equal(t, operations.Principal{}, fromCtx.Principal,
		"ярлык анонима не должен попадать в принципала операции")
	require.Equal(t, op.CreatedBy, fromCtx.CreatedBy,
		"created_by остаётся дефолтным, как у операции без ctx-принципала")
}

// TestNewFromContext_RealPrincipalStillStamped — сужаем анонимность, а не
// атрибуцию: настоящий принципал по-прежнему записывается как владелец.
func TestNewFromContext_RealPrincipalStillStamped(t *testing.T) {
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "user", ID: "usr-alice", DisplayName: "alice@example.com",
	})
	op, err := operations.NewFromContext(ctx, "enp", "create network", nil)
	require.NoError(t, err)
	require.Equal(t, "user", op.Principal.Type)
	require.Equal(t, "usr-alice", op.Principal.ID)
	require.Equal(t, "usr-alice", op.CreatedBy)
}
