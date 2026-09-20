// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"

	"github.com/PRO-Robotech/corelib/internal/oauth2/storage/tx"
)

// ПРАВКА KACHO: сами помощники транзакции переехали в лист
// `internal/oauth2/storage/tx`, а здесь остались ПСЕВДОНИМЫ.
//
// Причина переезда: `handler/oauth2` импортировал этот пакет ради трёх функций
// на чистой стандартной библиотеке, а получал вместе с ними соседний
// `memory.go` — эталонное хранилище в памяти, которое импортирует `internal`
// (моки gomock) и утаскивало gomock и testify в производственный граф сборки
// любого потребителя движка.
//
// Псевдонимы оставлены НАМЕРЕННО: чужой код, встраивающий `storage.Transactional`
// или зовущий `storage.MaybeBeginTx`, и пробы апстрима продолжают
// компилироваться без единой правки. Объявлены функциями, а не переменными: у
// переменной значение можно подменить на ходу, и поведение движка стало бы
// свойством того, кто успел присвоить последним.

// Transactional — псевдоним типа `tx.Transactional`. Встраивание и приведение
// типов работают через него без различий.
type Transactional = tx.Transactional

// MaybeBeginTx начинает транзакцию, если хранилище реализует Transactional.
func MaybeBeginTx(ctx context.Context, storage interface{}) (context.Context, error) {
	return tx.MaybeBeginTx(ctx, storage)
}

// MaybeCommitTx фиксирует транзакцию, если хранилище реализует Transactional.
func MaybeCommitTx(ctx context.Context, storage interface{}) error {
	return tx.MaybeCommitTx(ctx, storage)
}

// MaybeRollbackTx откатывает транзакцию, если хранилище реализует Transactional.
func MaybeRollbackTx(ctx context.Context, storage interface{}) error {
	return tx.MaybeRollbackTx(ctx, storage)
}
