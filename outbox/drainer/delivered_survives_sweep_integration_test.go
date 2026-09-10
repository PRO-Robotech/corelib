// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer_test

// Проба предиката задачи #1714: величина «доставлено по направлению» НЕ ЗАВИСИТ
// от числа живых строк.
//
// # Почему эта проба обязана существовать до уборки, а не после
//
// Величина объявлена своим же godoc как «сколько строк направления было
// доставлено ЗА ВСЁ ВРЕМЯ» и как ЕДИНСТВЕННАЯ из четырёх, отличающая «отзывов не
// было» от «отзывы не проходят». Считалась она при этом `count(*)` по ЖИВЫМ
// строкам. Пока строки не убираются, две величины совпадают — потому расхождения
// никто и не видел.
//
// Стоит завести уборку доставленных строк (#1361), и они расходятся в ОПАСНУЮ
// сторону: на очереди, где отзыв редок, все доставленные отзывы уходят уборкой,
// величина возвращается к НУЛЮ, а ноль здесь объявлен контрактом как «не
// доставлено ни одного отзыва» — то есть исправная очередь становится
// неотличима от очереди, где отзыв не проходит.
//
// Проба воспроизводит ровно это: доставить строки обоих направлений → убрать
// доставленные → потребовать, чтобы величина НЕ УПАЛА.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox/drainer"
	"github.com/PRO-Robotech/corelib/outbox/metrics"
)

// Test_DeliveredTotal_SurvivesTheSweepOfDeliveredRows — уборка доставленных
// строк не меняет величины «доставлено по направлению».
func Test_DeliveredTotal_SurvivesTheSweepOfDeliveredRows(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()
	fa.setDefaultErr(nil) // всё применяется с первого раза

	const table = "kaname.fga_outbox"
	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{
		Table:      table,
		Directions: metrics.TupleOutboxDirections(),
	})

	// Наблюдатель провязан ТАК ЖЕ, как в композиционном корне каждого из пяти
	// владельцев: величину ведёт дренаж, а не скан.
	cfg := testCfg()
	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa,
		drainer.WithDeliveryObserver[rawPayload](
			metrics.DeliveryObserver(table, metrics.TupleOutboxDirections(), rec)))
	defer func() { dCancel(); <-done }()
	time.Sleep(150 * time.Millisecond)

	// ОБА направления, потому что предмет величины — именно отзыв: направление
	// выдачи течёт непрерывно и выглядит здоровым при любом состоянии второго.
	writeID := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"resource_kind":"apps_application","resource_id":"app-A","project_id":"prj-X"}`)
	deleteID := insertOutboxRow(t, ctx, pool, "fga.tuple.delete",
		`{"resource_kind":"apps_application","resource_id":"app-A","project_id":"prj-X"}`)
	waitForRowSent(t, ctx, pool, writeID, 30*time.Second)
	waitForRowSent(t, ctx, pool, deleteID, 30*time.Second)

	require.NoError(t, col.Scan(ctx))
	before := rec.DeliveredTotal(table, metrics.DirectionWithdrawal)
	require.Equal(t, float64(1), before,
		"положительный контроль: доставленный отзыв обязан быть виден величиной ДО уборки — "+
			"иначе проба ниже зеленела бы на величине, которой нет вовсе")

	// УБОРКА доставленных строк — ровно то, что заводит #1361.
	tag, err := pool.Exec(ctx, `DELETE FROM `+table+` WHERE sent_at IS NOT NULL`)
	require.NoError(t, err)
	require.Equal(t, int64(2), tag.RowsAffected(), "убраны обе доставленные строки")

	require.NoError(t, col.Scan(ctx))
	after := rec.DeliveredTotal(table, metrics.DirectionWithdrawal)

	assert.Equal(t, before, after,
		"величина «доставлено по направлению» УПАЛА после уборки доставленных строк: "+
			"она объявлена «за всё время», а считается по живым строкам. Ноль здесь означает "+
			"по контракту «не доставлено ни одного отзыва» — то есть уборка превращает "+
			"исправную очередь в неотличимую от той, где отзыв не проходит")
}
