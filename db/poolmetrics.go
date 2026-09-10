// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// poolStatsSubsystem — общий сегмент имени всех девяти семейств.
const poolStatsSubsystem = "db_pool"

// poolStatsCollector — состояние ОДНОГО пула соединений, читаемое В МОМЕНТ СБОРА.
//
// # Что было невидимо до него
//
// Насыщение пула не наблюдалось ничем: во всём не-тестовом дереве `(*pgxpool.Pool).Stat()`
// не звался ни разу (единственное вхождение — оснастка проб). Снаружи это означает, что
// «запрос ждал свободного соединения» и «запрос сам по себе медленный» дают ОДНУ И ТУ ЖЕ
// картину — растянутую задержку RPC, — а лечатся они противоположным: первое поднятием
// потолка пула, второе правкой запроса или индекса. Выбирать между ними приходилось
// догадкой.
//
// # Почему В МОМЕНТ СБОРА, а не выборкой в фоне
//
// Величины пула — мгновенный снимок, а не накопленный ряд, и фоновая горутина, кладущая
// их в гейджи раз в N секунд, добавляет к ним ровно две вещи, обе вредные: собственный
// возраст (собиратель читает значение, которому уже до N секунд) и собственную живость
// (умерла горутина — гейджи застыли на последнем значении и продолжают выглядеть
// исправными). Пик занятости длиной меньше периода такая выборка не показывает НИКОГДА,
// а это ровно тот пик, ради которого наблюдение и заводится. Чтение на сборе не имеет
// ни того ни другого: значение ровно такое, каким оно было в момент запроса собирателя,
// и его отсутствие означает, что процесс не ответил, а не что «всё хорошо».
//
// Чтение при этом идёт ИЗ ПРОЦЕССА — `Stat()` возвращает счётчики, которые пул ведёт у
// себя в памяти, — поэтому сбор не ходит по сети и не гаснет вместе с базой (гейт дерева
// `TestDiagnosticCollectorsDoNotDialOut`).
//
// # Что именно различает «пул исчерпан» и «запрос медленный»
//
// Пять гейджей отвечают на «сколько сейчас» (занято / свободно / всего / потолок /
// открывается) и годятся для панели, но на вопрос «была ли очередь» не отвечают: между
// двумя сборами пул успевает опустеть и наполниться, и оба раза покажет здоровые числа.
// Отвечает пара СЧЁТЧИКОВ:
//
//   - `…_empty_acquire_total` — выдач, заставших пул ПУСТЫМ, то есть переждавших очередь.
//     Ноль здесь означает, что ожидания не было ни разу за всю жизнь процесса, каким бы
//     ни был мгновенный снимок;
//   - `…_acquire_wait_seconds_total` — суммарное время, проведённое В ОЖИДАНИИ соединения.
//
// Их частное — средняя длительность ожидания; отношение `empty_acquire_total` к
// `acquire_total` — доля запросов, которым пришлось ждать. Обе величины монотонны,
// поэтому промежуток между сборами их не прячет.
//
// Рядом стоит `…_canceled_acquire_total`: выдачи, брошенные вызывающим (истёк срок
// запроса) до того, как соединение нашлось. Он отличает «ждали и дождались» от «ждали и
// не дождались» — второе снаружи выглядит таймаутом самого RPC.
//
// # Почему `pool` — ПОСТОЯННАЯ метка, а не переменная
//
// У сервиса пулов бывает больше одного (у kaname это ведущий и реплика для чтений), и
// без различающей метки их числа схлопнулись бы в одну серию — то есть занятость реплики
// читалась бы как занятость ведущего. Значение метки приходит из композиционного корня
// литералом и НИКОГДА из данных запроса, поэтому число серий не растёт с трафиком.
// Постоянной, а не переменной, она сделана ещё и затем, чтобы два коллектора одного
// сервиса различались объявлением: тогда повторная регистрация того же пула отвергается
// реестром, а регистрация второго — проходит.
type poolStatsCollector struct {
	// pool — наблюдаемый пул. nil означает, что на этой посадке его нет (у kaname
	// так выглядит ненастроенная реплика); тогда Collect не отдаёт НИЧЕГО, а не нули:
	// нули утверждали бы про пул, которого не существует.
	pool *pgxpool.Pool

	acquired     *prometheus.Desc
	idle         *prometheus.Desc
	total        *prometheus.Desc
	maxConns     *prometheus.Desc
	constructing *prometheus.Desc

	acquireTotal         *prometheus.Desc
	emptyAcquireTotal    *prometheus.Desc
	canceledAcquireTotal *prometheus.Desc
	acquireWaitSeconds   *prometheus.Desc
}

// NewPoolStatsCollector собирает коллектор состояния пула `pool`.
//
// `namespace` становится префиксом имён (kaname передаёт `kaname`), `poolName` —
// значением постоянной метки `pool` (`primary`, `replica`), поэтому несколько пулов
// одного сервиса не сталкиваются.
//
// `pool == nil` допустим и НЕ является ошибкой: композиционный корень, у которого пула
// нет, регистрирует коллектор так же, как и остальные, а тот не отдаёт ни одной серии.
// Ронять на этом /metrics всего процесса значило бы гасить диагностику из-за
// ненастроенной необязательной части.
func NewPoolStatsCollector(namespace string, poolName string, pool *pgxpool.Pool) prometheus.Collector {
	labels := prometheus.Labels{"pool": poolName}
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(
			prometheus.BuildFQName(namespace, poolStatsSubsystem, name),
			help, nil, labels)
	}
	return &poolStatsCollector{
		pool: pool,
		acquired: desc("acquired_conns",
			"Connections currently checked out of the pool (busy right now)."),
		idle: desc("idle_conns",
			"Connections currently open and free in the pool (available right now)."),
		total: desc("total_conns",
			"Connections currently open by the pool — busy plus idle plus being constructed."),
		maxConns: desc("max_conns",
			"Configured ceiling on open connections. Answers what acquired_conns is approaching."),
		constructing: desc("constructing_conns",
			"Connections being opened right now. A number that stays high means the pool is "+
				"growing under load, i.e. the ceiling is being reached."),
		acquireTotal: desc("acquire_total",
			"Acquires served by the pool since process start. The denominator that tells "+
				"\"nobody waited\" apart from \"nobody asked\"."),
		emptyAcquireTotal: desc("empty_acquire_total",
			"Acquires that found the pool EMPTY and had to wait for a connection. This is the "+
				"saturation signal: it distinguishes a request queued on the pool from a request "+
				"that was simply slow in Postgres — the two look identical in RPC latency."),
		canceledAcquireTotal: desc("canceled_acquire_total",
			"Acquires abandoned by the caller (context cancelled or deadline exceeded) before a "+
				"connection became free. Tells \"waited and got one\" apart from \"waited and gave up\"."),
		acquireWaitSeconds: desc("acquire_wait_seconds_total",
			"Cumulative time callers spent WAITING for a free connection. Divided by "+
				"empty_acquire_total it gives the mean wait; that quotient, not RPC latency, is "+
				"what says whether the pool ceiling is the bottleneck."),
	}
}

// Describe объявляет все девять семейств — и делает это НЕЗАВИСИМО от того, есть ли
// пул.
//
// Коллектор без объявлений реестр считает непроверяемым: он не сверяет его серии с
// чужими, и повторная регистрация того же пула прошла бы молча, дав на проводе две
// одинаковые серии. Объявления при nil-пуле стоят ровно за этим; сбор при этом
// по-прежнему не отдаёт ничего.
func (c *poolStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.acquired
	ch <- c.idle
	ch <- c.total
	ch <- c.maxConns
	ch <- c.constructing
	ch <- c.acquireTotal
	ch <- c.emptyAcquireTotal
	ch <- c.canceledAcquireTotal
	ch <- c.acquireWaitSeconds
}

// Collect снимает пул НА МЕСТЕ и отдаёт девять величин одного снимка.
//
// Снимок берётся один на весь сбор: девять отдельных обращений к пулу дали бы девять
// разных моментов, и «занято + свободно» могло бы не сойтись с «всего» — расхождение,
// которое читается как дефект пула, хотя это дефект наблюдения.
func (c *poolStatsCollector) Collect(ch chan<- prometheus.Metric) {
	if c.pool == nil {
		return
	}
	s := c.snapshot()
	for _, m := range []struct {
		desc  *prometheus.Desc
		kind  prometheus.ValueType
		value float64
	}{
		{c.acquired, prometheus.GaugeValue, s.acquired},
		{c.idle, prometheus.GaugeValue, s.idle},
		{c.total, prometheus.GaugeValue, s.total},
		{c.maxConns, prometheus.GaugeValue, s.maxConns},
		{c.constructing, prometheus.GaugeValue, s.constructing},
		{c.acquireTotal, prometheus.CounterValue, s.acquireTotal},
		{c.emptyAcquireTotal, prometheus.CounterValue, s.emptyAcquireTotal},
		{c.canceledAcquireTotal, prometheus.CounterValue, s.canceledAcquireTotal},
		{c.acquireWaitSeconds, prometheus.CounterValue, s.acquireWaitSeconds},
	} {
		ch <- prometheus.MustNewConstMetric(m.desc, m.kind, m.value)
	}
}

// poolSnapshot — девять величин ОДНОГО обращения к пулу.
type poolSnapshot struct {
	acquired     float64
	idle         float64
	total        float64
	maxConns     float64
	constructing float64

	acquireTotal         float64
	emptyAcquireTotal    float64
	canceledAcquireTotal float64
	acquireWaitSeconds   float64
}

// snapshot читает счётчики пула из памяти процесса. Отдельный метод, а не тело Collect:
// величины берутся одним обращением, и это видно.
func (c *poolStatsCollector) snapshot() poolSnapshot {
	st := c.pool.Stat()
	return poolSnapshot{
		acquired:     float64(st.AcquiredConns()),
		idle:         float64(st.IdleConns()),
		total:        float64(st.TotalConns()),
		maxConns:     float64(st.MaxConns()),
		constructing: float64(st.ConstructingConns()),

		acquireTotal:         float64(st.AcquireCount()),
		emptyAcquireTotal:    float64(st.EmptyAcquireCount()),
		canceledAcquireTotal: float64(st.CanceledAcquireCount()),
		acquireWaitSeconds:   st.AcquireDuration().Seconds(),
	}
}

// Compile-time: реализуем контракт коллектора. Потеря метода в рефакторинге не сломала бы
// сборку у вызывающего, который регистрирует нас интерфейсом, — она тихо прекратила бы
// публиковать единственные серии, отличающие ожидание в пуле от медленного запроса.
var _ prometheus.Collector = (*poolStatsCollector)(nil)
