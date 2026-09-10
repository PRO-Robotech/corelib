// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

// redrive_only_test.go — конструктор backstop'а отвергает конфигурацию, при которой
// возврат отравленных строк был бы неверен, ДО первого запроса к базе.
//
// RedrivePoisoned — чистый SQL поверх таблицы очереди: доменного состояния он не
// читает вовсе. Значит единственное, что конструктор обязан решить заранее, —
// таблица названа и ключ партиции задан тем же столбцом, что у дренажа этой
// таблицы. Незаданный ключ читался бы как «упорядочивать нечем» и оживил бы
// намерение поверх уже доставленного преемника.
//
// Здесь стояли ещё две пробы — про то, что backstop конструируется БЕЗ доменных
// адаптеров и что проходы сверки на нём отказывают, а не разыменовывают nil.
// Предмета у них больше нет: адаптеров и самих проходов в пакете не осталось
// (#760), конструктор один.

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox/reconciler"
)

// nonNilPool is a pool handle that is non-nil but never dialled. These tests only
// exercise construction and the adapter guards, and both must decide before any
// query is issued — if a guard ever reached the database, this pool would make that
// obvious rather than silently passing.
func nonNilPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return &pgxpool.Pool{}
}

func TestNewRedriveOnly_ConstructsFromTableAndPartitionAlone(t *testing.T) {
	t.Parallel()

	r, err := reconciler.NewRedriveOnly(nonNilPool(t), reconciler.Config{
		Table:           "kacho_storage.fga_register_outbox",
		PartitionColumn: reconciler.RegisterOutboxPartition,
	}, nil)
	require.NoError(t, err, "конструктор backstop'а обязан обходиться таблицей и ключом партиции")
	require.NotNil(t, r)
}

func TestNewRedriveOnly_StillRequiresPoolAndTable(t *testing.T) {
	t.Parallel()

	_, err := reconciler.NewRedriveOnly(nil, reconciler.Config{
		Table: "t", PartitionColumn: reconciler.RegisterOutboxPartition,
	}, nil)
	require.Error(t, err, "a nil pool must still be refused")

	_, err = reconciler.NewRedriveOnly(nonNilPool(t), reconciler.Config{
		PartitionColumn: reconciler.RegisterOutboxPartition,
	}, nil)
	require.Error(t, err, "an empty table must still be refused — the redrive is SQL over that table")
}

// TestNewRedriveOnly_RefusesAnUnsetPartitionKey — the ordering key has no default
// ON PURPOSE, and this is what keeps it that way.
//
// The revival's guard is "do not raise a poisoned intent past a DELIVERED successor
// of the same partition". With no key there is no partition, so every poisoned row
// looks unopposed and the pass revives all of them — including a grant whose own
// revocation already landed. The queue would be repaired straight back into the
// over-grant the ordering exists to prevent, and nothing would look wrong: the
// backstop is present, it runs, it reports rows revived.
//
// So the value that means "no ordering" must be unconstructible, not merely
// discouraged. The lawful twin below is the other half — a real key still builds,
// otherwise this would pass on a constructor that refuses everything.
func TestNewRedriveOnly_RefusesAnUnsetPartitionKey(t *testing.T) {
	t.Parallel()

	_, err := reconciler.NewRedriveOnly(nonNilPool(t), reconciler.Config{Table: "t"}, nil)
	require.Error(t, err, "an unset partition key must refuse to build — it would read as \"no ordering\"")
	require.Contains(t, err.Error(), "PartitionColumn",
		"the refusal must name the knob, or the operator cannot act on it: %v", err)

	// The name is interpolated into the redrive statement. It is checked at
	// construction so a bad value cannot become a syntax error minutes later on the
	// first pass — and cannot carry SQL at all.
	for _, bad := range []string{"resource id", "resource_id; DROP TABLE x", "1col", `"quoted"`} {
		_, err := reconciler.NewRedriveOnly(nonNilPool(t), reconciler.Config{
			Table: "t", PartitionColumn: bad,
		}, nil)
		require.Errorf(t, err, "a non-identifier partition key must be refused: %q", bad)
	}

	// Lawful twin: both keys in the platform build.
	for _, good := range []string{reconciler.RegisterOutboxPartition, "tuple_key"} {
		r, err := reconciler.NewRedriveOnly(nonNilPool(t), reconciler.Config{
			Table: "t", PartitionColumn: good,
		}, nil)
		require.NoErrorf(t, err, "a real partition key must build: %q", good)
		require.NotNil(t, r)
	}
}
