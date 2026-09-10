// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

// СВЕРКА ДВУХ МЕХАНИЗМОВ: уборка доставленных строк (#1361) и защита реконсайлера
// от воскрешения отозванного доступа обязаны быть ОДНИМ решением, а не двумя.
//
// # Почему проба живёт здесь, а не рядом с уборкой
//
// Каждый механизм по отдельности защитим и покрыт своими пробами: уборка щадит
// доставленную строку с недоставленным предшественником, реконсайлер не оживляет
// отравленную строку при доставленном преемнике. Расходятся они только ВМЕСТЕ —
// и расхождение это невидимо ни одной пробе, утверждающей о своей половине
// (`architecture.md` §«Параллельные полосы одного механизма обязаны сверяться
// МЕЖДУ СОБОЙ», `security.md` §«Разрыв, невидимый ни с одной стороны»).
//
// Вопрос, который задаётся сквозь обе стороны: пережив уборку, продолжает ли
// отравленная строка оставаться неоживляемой. Если нет — отзыв доступа
// отменяется повтором устаревшего намерения, и обнаружилось бы это воскрешённым
// доступом у арендатора, а не красной пробой.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox"
	"github.com/PRO-Robotech/corelib/outbox/reconciler"
)

// Test_SweepOfDeliveredRows_KeepsTheRedriveGuard — сквозной случай.
//
// Партия: отравленная регистрация (предшественник) и доставленное снятие
// (преемник) одного ресурса. Снятие доставлено давно — то есть уборка обязана
// была бы его снять, если бы судила только по возрасту и признаку доставки.
// После прохода уборки реконсайлер обязан по-прежнему НЕ оживлять отравленную
// строку.
func Test_SweepOfDeliveredRows_KeepsTheRedriveGuard(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupRegisterOutboxPG(t)
	const table = "kacho_svc.fga_register_outbox"

	// Предшественник: отравленная РЕГИСТРАЦИЯ (доступ выдать).
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_svc.fga_register_outbox
		    (event_type, resource_id, created_at, sent_at, attempt_count)
		VALUES ('fga.register', 'res-A', now() - interval '90 days', NULL, 10)`)
	require.NoError(t, err)
	// Преемник: доставленное СНЯТИЕ (доступ отозвать) — давно доставлено.
	_, err = pool.Exec(ctx, `
		INSERT INTO kacho_svc.fga_register_outbox
		    (event_type, resource_id, created_at, sent_at)
		VALUES ('fga.unregister', 'res-A', now() - interval '89 days', now() - interval '89 days')`)
	require.NoError(t, err)

	sw, err := outbox.NewQueueSweeper(pool, outbox.QueueRetentionConfig{
		Table:           table,
		PartitionColumn: reconciler.RegisterOutboxPartition,
	})
	require.NoError(t, err)

	removed, _, err := sw.Sweep(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	require.Equal(t, int64(0), removed,
		"уборка обязана пощадить доставленное снятие: ниже него в той же партиции "+
			"лежит недоставленная регистрация, и только оно не даёт её оживить")

	// А теперь ГЛАВНОЕ: защита обязана продолжать действовать.
	rc, err := reconciler.NewRedriveOnly(pool, reconciler.Config{
		Table:           table,
		PartitionColumn: reconciler.RegisterOutboxPartition,
	}, testSweepLogger())
	require.NoError(t, err)

	revived, err := rc.RedrivePoisoned(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, revived,
		"отравленная РЕГИСТРАЦИЯ не оживляется: её снятие уже доставлено, и повтор "+
			"выдал бы доступ, который был отозван. Уборка не вправе снять то, на чём "+
			"держится этот отказ")

	var poisonedAttempts int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attempt_count FROM kacho_svc.fga_register_outbox
		  WHERE event_type = 'fga.register'`).Scan(&poisonedAttempts))
	assert.Equal(t, 10, poisonedAttempts,
		"счётчик попыток не сброшен — строка осталась отравленной, а не вернулась в клейм")
}

// Test_SweepOfDeliveredRows_RemovesWhatNothingProtects — законный близнец.
//
// Та же форма, но предшественника нет вовсе: доставленную строку снять и можно,
// и должно. Без этого случая проба выше зеленела бы на уборке, которая не
// снимает НИЧЕГО, — а такая уборка неотличима от отсутствующей.
func Test_SweepOfDeliveredRows_RemovesWhatNothingProtects(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupRegisterOutboxPG(t)
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_svc.fga_register_outbox
		    (event_type, resource_id, created_at, sent_at)
		VALUES ('fga.unregister', 'res-B', now() - interval '89 days', now() - interval '89 days')`)
	require.NoError(t, err)

	sw, err := outbox.NewQueueSweeper(pool, outbox.QueueRetentionConfig{
		Table:           "kacho_svc.fga_register_outbox",
		PartitionColumn: reconciler.RegisterOutboxPartition,
	})
	require.NoError(t, err)

	removed, _, err := sw.Sweep(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed,
		"доставленную строку, которой нечего защищать, уборка обязана снять — иначе "+
			"механизма нет вовсе")
}

// testSweepLogger — журнал, который проба не читает: предмет здесь исход, а не
// текст жалобы.
func testSweepLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
