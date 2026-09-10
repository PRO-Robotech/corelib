// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package outbox_test

// Уборка ДОСТАВЛЕННЫХ строк очереди дренажа (#1361).
//
// Дренаж помечает доставленную строку `sent_at` и НЕ УДАЛЯЕТ её никогда: и клейм,
// и применение, и повтор ключуются на `sent_at IS NULL`, а операторов снятия в
// пакете было ноль. Строка при этом заводится в writer-транзакции КАЖДОЙ мутации,
// то есть темп задаёт арендатор, и рост был монотонным и вечным у семи очередей
// шести владельцев.
//
// # Почему у этой уборки предикат СЛОЖНЕЕ, чем «доставлено и старо»
//
// Доставленную строку читает ВТОРОЙ потребитель — `reconciler.RedrivePoisoned`.
// Отравленная строка НЕ оживляется, если в её партиции уже доставлена БОЛЕЕ
// ПОЗДНЯЯ: повтор устаревшего намерения отменил бы то, что уже применено, а на
// очереди регистрации это ВОСКРЕШАЕТ доступ, который был отозван.
//
// Значит уборка, снявшая эту более позднюю строку, снимает и защиту — молча и
// необратимо. Поэтому предикат щадит доставленную строку, у которой в партиции
// есть НЕДОСТАВЛЕННЫЙ предшественник, и выражает это ТЕМ ЖЕ ключом партиции,
// которым пользуются клейм и анти-джойн.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox"
)

const registerQueueTable = "test_register_outbox"

// newQueueSweeper собирает уборщика очереди на этой фикстуре.
func newQueueSweeper(t *testing.T, pool *pgxpool.Pool) *outbox.QueueSweeper {
	t.Helper()
	sw, err := outbox.NewQueueSweeper(pool, outbox.QueueRetentionConfig{
		Table:           registerQueueTable,
		PartitionColumn: "resource_id",
	})
	require.NoError(t, err)
	return sw
}

// Test_QueueSweep_RemovesDeliveredRowsPastTheWindow — основной случай.
func Test_QueueSweep_RemovesDeliveredRowsPastTheWindow(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupOutboxPG(t)
	_, err := pool.Exec(ctx, `
		INSERT INTO test_register_outbox (resource_id, event_type, sent_at) VALUES
		    ('res-old-1', 'fga.register', now() - interval '30 days'),
		    ('res-old-2', 'fga.register', now() - interval '30 days')`)
	require.NoError(t, err)
	// Положительный контроль: свежая доставленная строка обязана УЦЕЛЕТЬ — иначе
	// проба зеленела бы на уборке, сносящей всё подряд.
	_, err = pool.Exec(ctx, `
		INSERT INTO test_register_outbox (resource_id, event_type, sent_at)
		VALUES ('res-fresh', 'fga.register', now())`)
	require.NoError(t, err)

	n, full, err := newQueueSweeper(t, pool).Sweep(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "снимаются обе строки за окном")
	assert.False(t, full, "партия не полна — убрано всё, что подлежало")
	assert.Equal(t, []string{"res-fresh"}, remainingResources(t, ctx, pool),
		"свежая доставленная строка обязана уцелеть: окно ещё не истекло")
}

// Test_QueueSweep_NeverRemovesAPoisonedRow — требование задачи, названное явно.
//
// Отравленная строка несёт НАМЕРЕНИЕ, которое не доехало. Сняв её, уборка
// потеряла бы это намерение молча — и оператор, разбирающий «почему доступ не
// отозван», не нашёл бы даже следа.
func Test_QueueSweep_NeverRemovesAPoisonedRow(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupOutboxPG(t)
	_, err := pool.Exec(ctx, `
		INSERT INTO test_register_outbox (resource_id, event_type, created_at, sent_at, attempt_count) VALUES
		    ('res-poisoned', 'fga.unregister', now() - interval '90 days', NULL, 10),
		    ('res-pending',  'fga.unregister', now() - interval '90 days', NULL, 0)`)
	require.NoError(t, err)

	n, _, err := newQueueSweeper(t, pool).Sweep(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n,
		"недоставленная строка не снимается НИ ПРИ КАКОМ возрасте: у неё нет отметки "+
			"доставки, а значит её намерение ещё не применено")
	assert.ElementsMatch(t, []string{"res-poisoned", "res-pending"}, remainingResources(t, ctx, pool))
}

// Test_QueueSweep_SparesADeliveredRowThatProtectsAPoisonedPredecessor — несущий
// случай, ради которого предикат вообще не сводится к «доставлено и старо».
//
// В одной партиции: отравленный предшественник (id меньше) и доставленная
// более поздняя строка. Именно эта доставленная строка не даёт реконсайлеру
// оживить предшественника. Снять её значит вернуть отзыву возможность быть
// отменённым повтором устаревшего намерения.
func Test_QueueSweep_SparesADeliveredRowThatProtectsAPoisonedPredecessor(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupOutboxPG(t)
	// Порядок вставки задаёт порядок id: предшественник — недоставленный.
	_, err := pool.Exec(ctx, `
		INSERT INTO test_register_outbox (resource_id, event_type, created_at, sent_at, attempt_count)
		VALUES ('res-A', 'fga.register', now() - interval '90 days', NULL, 10)`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO test_register_outbox (resource_id, event_type, created_at, sent_at)
		VALUES ('res-A', 'fga.unregister', now() - interval '89 days', now() - interval '89 days')`)
	require.NoError(t, err)
	// Законный близнец: та же форма, но БЕЗ недоставленного предшественника —
	// эту строку снять можно и должно. Без него проба зеленела бы на уборке,
	// которая не снимает вообще ничего.
	_, err = pool.Exec(ctx, `
		INSERT INTO test_register_outbox (resource_id, event_type, sent_at)
		VALUES ('res-B', 'fga.unregister', now() - interval '89 days')`)
	require.NoError(t, err)

	n, _, err := newQueueSweeper(t, pool).Sweep(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "снимается только строка партиции без предшественника")
	assert.ElementsMatch(t, []string{"res-A", "res-A"}, remainingResources(t, ctx, pool),
		"доставленная строка партиции res-A ОБЯЗАНА уцелеть: она — единственное, что "+
			"не даёт реконсайлеру оживить отравленного предшественника и тем отменить "+
			"уже применённое снятие доступа")
}

// Test_NewQueueSweeper_RefusesWithoutAPartitionColumn — отказ сборки, не молчание.
//
// Без ключа партиции предикат «пощадить защищающую строку» НЕВЫРАЗИМ, и уборщик,
// собранный без него, снимал бы защиту у каждой очереди. Отказ на сборке, потому
// что тихо собранный уборщик обнаружился бы воскрешённым доступом.
func Test_NewQueueSweeper_RefusesWithoutAPartitionColumn(t *testing.T) {
	t.Parallel()

	pool := setupOutboxPG(t)
	_, err := outbox.NewQueueSweeper(pool, outbox.QueueRetentionConfig{Table: registerQueueTable})
	require.Error(t, err, "уборщик без ключа партиции собираться не вправе")
	assert.Contains(t, err.Error(), "PartitionColumn")
}

// remainingResources — идентификаторы строк, переживших проход, в порядке id.
func remainingResources(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT resource_id FROM test_register_outbox ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		out = append(out, id)
	}
	require.NoError(t, rows.Err())
	return out
}
