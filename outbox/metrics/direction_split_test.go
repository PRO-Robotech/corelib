// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package metrics_test

// direction_split_test.go — снятие обязано наблюдаться ОТДЕЛЬНО от выдачи.
//
// Почему table-wide метрик недостаточно. Очередь несёт оба направления сразу, и все три
// таблично-сводные величины считают их вместе. Пока выдача идёт (а она идёт всегда —
// ресурсы создаются), сводные ряды выглядят здоровыми независимо от того, доехало ли
// хоть одно снятие: глубина мала, потому что выдачи дренятся; возраст головы мал по той
// же причине; отравленных нет, потому что отказа не было. «Работает» и «не отозвано»
// дают ОДИНАКОВУЮ картину — ровно то, что правила требуют сделать различимым
// (data-integrity.md: «ноль доставленных строк за всю жизнь очереди» обязано быть
// заметно; security.md §Hardening-инвариант 8: «ноль отказов за всю жизнь контроля»).
//
// Реальный прецедент проекта — 198 строк регистрации, ни одна не доставлена, и это было
// ненаблюдаемо, потому что синхронный путь работал. Здесь тот же класс на другом
// направлении: замер 2026-08-04 нашёл 479 регистраций репозиториев против 60 снятий, и
// ни один сводный ряд об этом не говорил.
//
// Поэтому Collector обязан уметь разложить те же самые величины по направлению.
//
// # ЧИСЛО ДОСТАВЛЕННЫХ СКАН БОЛЬШЕ НЕ СТАВИТ — и это не отказ от величины
//
// Величина осталась и по-прежнему есть единственное, что отличает «снятий не было» от
// «снятия не доезжают». Сменился ПРОИЗВОДИТЕЛЬ: её ведёт наблюдатель дренажа
// (`DeliveryObserver` → `drainer.WithDeliveryObserver`), а не `count(*)` по живым
// строкам (#1714). Причина — объявление величины: «за всё время». Счёт по живым строкам
// совпадал с объявленным ровно до тех пор, пока строки не убираются; уборка доставленных
// (#1361) обнулила бы её на ИСПРАВНОЙ очереди, где отзыв редок, и ноль прочитался бы по
// контракту как «не доставлено ни одного отзыва».
//
// Поэтому утверждения о доставленных уехали ОТСЮДА к своему новому производителю —
// `Test_DeliveryObserver_CountsDeliveriesByDirection` ниже и
// `Test_DeliveredTotal_SurvivesTheSweepOfDeliveredRows` в пакете дренажа, — а не были
// сняты: предмет жив, у него другой источник.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox/metrics"
)

// Test_CollectorScan_SplitsByDirection_ExposesUndeliveredWithdrawals — главный случай.
//
// Наполнение подобрано так, чтобы сводные ряды выглядели ЗДОРОВО при полностью мёртвом
// снятии: выдачи доставлены, снятия — нет. Утверждается, что разложение по направлению
// это показывает, а сводная картина — нет.
func Test_CollectorScan_SplitsByDirection_ExposesUndeliveredWithdrawals(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)
	const tbl = "kacho_apps.fga_register_outbox"

	// Выдачи: все доставлены.
	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, sent_at)
		 SELECT 'fga.register', now() FROM generate_series(1, 5)`)
	require.NoError(t, err)
	// Снятия: ни одно не доставлено, старейшее висит давно.
	_, err = pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, created_at)
		 VALUES ('fga.unregister', now() - interval '600 seconds'),
		        ('fga.unregister', now() - interval '10 seconds')`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{
		Table:       tbl,
		MaxAttempts: 10,
		Directions: map[string][]string{
			"grant":      {"fga.register"},
			"withdrawal": {"fga.unregister"},
		},
	})
	require.NoError(t, col.Scan(ctx))

	// Доставленных здесь НЕ утверждается: их ставит наблюдатель дренажа, а не скан
	// (см. шапку файла). Скан обязан отвечать на вопрос «что ЛЕЖИТ», и ровно это
	// проверяется ниже.
	assert.Equal(t, float64(0), rec.DeliveredTotal(tbl, "grant"),
		"скан НЕ ставит доставленных: величина ведётся событием доставки, и проход "+
			"скана не вправе её ни поднять, ни обнулить")

	assert.Equal(t, float64(0), rec.BacklogDepthByDirection(tbl, "grant"))
	assert.Equal(t, float64(2), rec.BacklogDepthByDirection(tbl, "withdrawal"),
		"неснятые записи считаются отдельно от выдач")

	assert.Equal(t, float64(0), rec.OldestPendingAgeByDirection(tbl, "grant"),
		"у направления без незакрытых записей возраст нулевой")
	assert.Greater(t, rec.OldestPendingAgeByDirection(tbl, "withdrawal"), float64(500),
		"возраст САМОЙ СТАРОЙ неснятой записи — отдельная величина: именно она отвечает "+
			"на вопрос «как давно отзыв перестал доезжать»")
}

// Test_CollectorScan_DirectionSplit_DoesNotDisturbTheTableWideSeries — законный близнец.
//
// Разложение ДОБАВЛЯЕТ ряды, а не подменяет существующие: сводные величины обязаны
// остаться прежними, иначе всякий уже написанный порог начал бы означать другое.
func Test_CollectorScan_DirectionSplit_DoesNotDisturbTheTableWideSeries(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)
	const tbl = "kacho_apps.fga_register_outbox"

	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, created_at)
		 VALUES ('fga.register', now() - interval '40 seconds'), ('fga.unregister', now())`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	require.NoError(t, metrics.NewCollector(pool, rec, metrics.CollectorConfig{
		Table:      tbl,
		Directions: map[string][]string{"grant": {"fga.register"}, "withdrawal": {"fga.unregister"}},
	}).Scan(ctx))

	assert.Equal(t, float64(2), rec.BacklogDepth(tbl),
		"сводная глубина считает оба направления, как и прежде")
	assert.Greater(t, rec.OldestPendingAgeSeconds(tbl), float64(30),
		"сводный возраст головы прежний")
}

// Test_CollectorScan_WithoutDirections_RecordsNoSplit — послабление истекает само.
//
// Не сконфигурировано разложение — рядов по направлению нет вовсе. Это важно именно как
// ОТСУТСТВИЕ ряда, а не нуль в нём: нуль читался бы как «снятий не было», то есть
// неотличимо от предмета, ради которого ряд и заведён.
func Test_CollectorScan_WithoutDirections_RecordsNoSplit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)
	const tbl = "kacho_apps.fga_register_outbox"
	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type) VALUES ('fga.unregister')`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	require.NoError(t, metrics.NewCollector(pool, rec, metrics.CollectorConfig{Table: tbl}).Scan(ctx))

	assert.Empty(t, rec.Directions(tbl),
		"без конфигурации разложения рядов по направлению быть не должно — "+
			"отсутствие ряда честнее нуля, который читается как «снятий не было»")
	assert.Equal(t, float64(1), rec.BacklogDepth(tbl), "сводные ряды работают как прежде")
}

// Test_DeliveryObserver_CountsDeliveriesByDirection — новый ПРОИЗВОДИТЕЛЬ величины.
//
// Утверждает три вещи, и третья — та, ради которой словарь вообще передаётся
// наблюдателю: событие ВНЕ словаря не приписывается чужому направлению.
func Test_DeliveryObserver_CountsDeliveriesByDirection(t *testing.T) {
	t.Parallel()

	const tbl = "kacho_apps.fga_register_outbox"
	rec := metrics.NewMemRecorder()
	obs := metrics.DeliveryObserver(tbl, metrics.RegisterOutboxDirections(), rec)
	require.NotNil(t, obs, "приёмник умеет разбивку — наблюдатель обязан собраться")

	obs(metrics.EventFGARegister)
	obs(metrics.EventFGARegister)
	obs(metrics.EventFGAUnregister)
	obs("fga.something.else") // вне словаря

	assert.Equal(t, float64(2), rec.DeliveredTotal(tbl, metrics.DirectionGrant),
		"счётчик МОНОТОНЕН: две доставки выдачи — двойка, а не «сколько лежит»")
	assert.Equal(t, float64(1), rec.DeliveredTotal(tbl, metrics.DirectionWithdrawal),
		"направление отзыва считается отдельно — ради него разбивка и заведена")
	assert.NotContains(t, rec.Directions(tbl), "fga.something.else",
		"событие вне словаря не заводит своего направления и не приписывается чужому: "+
			"молча приписать его значило бы солгать именно той величине, ради точности "+
			"которой разбивка заведена")
}

// Test_DeliveryObserver_RecorderWithoutTheSplit_GetsNoObserver — законный близнец.
//
// Приёмник, заведённый ДО разбивки, удовлетворяет [metrics.Recorder] и не умеет
// [metrics.DirectionRecorder]. Наблюдателя он не получает вовсе — а не получает
// пустышку, которая молча считала бы в никуда. Отсутствие серии здесь честный сигнал:
// ноль прочитался бы как «отзывов не было».
func Test_DeliveryObserver_RecorderWithoutTheSplit_GetsNoObserver(t *testing.T) {
	t.Parallel()

	assert.Nil(t, metrics.DeliveryObserver("t", metrics.RegisterOutboxDirections(), recorderWithoutSplit{}),
		"приёмник без разбивки наблюдателя не получает")
	assert.Nil(t, metrics.DeliveryObserver("t", nil, metrics.NewMemRecorder()),
		"без словаря направлений считать нечем — наблюдателя нет")
}

// recorderWithoutSplit — приёмник, заведённый до разбивки: умеет [metrics.Recorder]
// и НЕ умеет [metrics.DirectionRecorder].
type recorderWithoutSplit struct{}

func (recorderWithoutSplit) SetBacklogDepth(string, float64)            {}
func (recorderWithoutSplit) SetOldestPendingAgeSeconds(string, float64) {}
func (recorderWithoutSplit) SetPoisonedCount(string, float64)           {}
func (recorderWithoutSplit) IncPoisoned(string)                         {}
