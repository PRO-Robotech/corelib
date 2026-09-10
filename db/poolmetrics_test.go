// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/corelib/db"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// wantPoolStatsNames — ВСЕ девять семейств, как они обязаны выглядеть на
// /metrics при namespace `kaname`. Перечень выписан здесь литералом
// намеренно: имя серии — контракт с панелями и правилами тревог, и вывод его
// из того же кода, который его строит, не утверждал бы ничего.
var wantPoolStatsNames = []string{
	"kaname_db_pool_acquired_conns",
	"kaname_db_pool_idle_conns",
	"kaname_db_pool_total_conns",
	"kaname_db_pool_max_conns",
	"kaname_db_pool_constructing_conns",
	"kaname_db_pool_acquire_total",
	"kaname_db_pool_empty_acquire_total",
	"kaname_db_pool_canceled_acquire_total",
	"kaname_db_pool_acquire_wait_seconds_total",
}

// collectMetrics снимает коллектор ровно так, как это сделает сбор: через
// Collect, без реестра.
func collectMetrics(t *testing.T, c prometheus.Collector) []prometheus.Metric {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	go func() {
		c.Collect(ch)
		close(ch)
	}()
	var out []prometheus.Metric
	for m := range ch {
		out = append(out, m)
	}
	return out
}

// describeDescs снимает объявления коллектора — они существуют и до первого
// сбора, и до того, как у процесса появится пул.
func describeDescs(t *testing.T, c prometheus.Collector) []*prometheus.Desc {
	t.Helper()
	ch := make(chan *prometheus.Desc, 64)
	go func() {
		c.Describe(ch)
		close(ch)
	}()
	var out []*prometheus.Desc
	for d := range ch {
		out = append(out, d)
	}
	return out
}

// TestPoolStatsCollectorNilPoolEmitsNothing — композиционный корень, у которого
// пула нет (у kaname это реплика при ненастроенном slave-url), не обязан
// ронять всю диагностическую поверхность процесса.
//
// Проверяется ИСХОД сбора, а не отсутствие паники само по себе: коллектор,
// отдающий на nil-пуле нули, утверждал бы про пул, которого нет, — а это ровно
// тот класс, ради которого метрики и заводятся.
func TestPoolStatsCollectorNilPoolEmitsNothing(t *testing.T) {
	t.Parallel()

	c := coredb.NewPoolStatsCollector("kaname", "replica", nil)

	require.NotPanics(t, func() { _ = collectMetrics(t, c) },
		"Collect на nil-пуле не имеет права уронить /metrics всего процесса")
	require.Empty(t, collectMetrics(t, c),
		"нулевые значения на несуществующем пуле утверждали бы про него неправду")
}

// TestPoolStatsCollectorDescribesAllNineFamilies — все девять семейств
// объявлены, с namespace в имени и с постоянной меткой pool.
//
// Проверка по объявлениям, а не по собранному: объявление есть у процесса,
// который ещё не обслужил ни одного запроса, и именно оно определяет, что
// увидит собиратель.
func TestPoolStatsCollectorDescribesAllNineFamilies(t *testing.T) {
	t.Parallel()

	descs := describeDescs(t, coredb.NewPoolStatsCollector("kaname", "primary", nil))
	require.Len(t, descs, len(wantPoolStatsNames))

	var got []string
	for _, d := range descs {
		s := d.String()
		require.Contains(t, s, `pool="primary"`,
			"метка pool обязана быть ПОСТОЯННОЙ: без неё два пула одного сервиса схлопнутся в одну серию")
		for _, name := range wantPoolStatsNames {
			if strings.Contains(s, `fqName: "`+name+`"`) {
				got = append(got, name)
			}
		}
	}
	sort.Strings(got)
	want := append([]string(nil), wantPoolStatsNames...)
	sort.Strings(want)
	require.Equal(t, want, got)
}

// TestPoolStatsCollectorDoubleRegistrationDoesNotPanic — регистрация ВТОРОГО
// коллектора того же пула отвергается ошибкой, а не паникой.
//
// Это предпосылка провязки в композиционном корне: Registry.RegisterPoolStats
// обязан пережить повторный вызов, а MustRegister на повторе роняет процесс —
// то есть наблюдение убивало бы сервис, который оно наблюдает.
func TestPoolStatsCollectorDoubleRegistrationDoesNotPanic(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(coredb.NewPoolStatsCollector("kaname", "primary", nil)))

	var err error
	require.NotPanics(t, func() {
		err = reg.Register(coredb.NewPoolStatsCollector("kaname", "primary", nil))
	})
	require.Error(t, err)
	require.IsType(t, prometheus.AlreadyRegisteredError{}, err,
		"повтор обязан быть УЗНАВАЕМ — иначе вызывающий не отличит его от настоящей ошибки")

	// Положительный контроль: другой пул того же сервиса регистрируется, то есть
	// отказ выше — про повтор, а не про то, что второй коллектор не берут вовсе.
	require.NoError(t, reg.Register(coredb.NewPoolStatsCollector("kaname", "replica", nil)))
}

// TestPoolStatsCollectorReportsLiveNumbers — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: числа
// настоящие, а не нули.
//
// Проба, проверяющая только присутствие семейств, осталась бы зелёной, если бы
// каждое значение было вшитой нулевой константой, — а это и есть тот дефект,
// ради которого коллектор пишется. Поэтому здесь удерживается соединение и
// утверждается, что занятых стало не меньше одного.
func TestPoolStatsCollectorReportsLiveNumbers(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (testcontainers); skipped with -short")
	}
	ctx := context.Background()
	dsn := pgtest.NewDB(t)

	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(coredb.NewPoolStatsCollector("kaname", "primary", pool)))

	// Держим соединение: сбор обязан увидеть его ЗАНЯТЫМ, а не свободным.
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()

	body := scrape(t, reg)

	// Все девять семейств доезжают до провода с постоянной меткой pool.
	for _, name := range wantPoolStatsNames {
		require.Contains(t, body, name+`{pool="primary"}`,
			"семейство %s не доехало до /metrics", name)
	}

	require.GreaterOrEqual(t, sampleValue(t, body, "kaname_db_pool_acquired_conns"), 1.0,
		"удерживается соединение — занятых обязано быть не меньше одного; ноль здесь означал бы вшитую константу")
	require.GreaterOrEqual(t, sampleValue(t, body, "kaname_db_pool_total_conns"), 1.0)
	require.Greater(t, sampleValue(t, body, "kaname_db_pool_max_conns"), 0.0,
		"потолок пула обязан быть положительным — иначе делить на него нечего")
	require.GreaterOrEqual(t, sampleValue(t, body, "kaname_db_pool_acquire_total"), 1.0,
		"счётчик выдач обязан двигаться: он и отличает «пул простаивает» от «наблюдения нет»")
}

// scrape снимает реестр ровно тем путём, каким его снимет собиратель, — через
// promhttp, а не через внутренние структуры.
func scrape(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL) //nolint:noctx // test
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// sampleValue достаёт значение единственной серии семейства из текста экспозиции.
func sampleValue(t *testing.T, body, name string) float64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		idx := strings.LastIndex(line, " ")
		require.Greater(t, idx, 0, "неразбираемая строка экспозиции: %q", line)
		v, err := strconv.ParseFloat(strings.TrimSpace(line[idx+1:]), 64)
		require.NoError(t, err)
		return v
	}
	t.Fatalf("серия %s не найдена в экспозиции", name)
	return 0
}
