// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package health_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/observability/health"
)

// Readiness-агрегатор: именованные чекеры с bounded-timeout; /readyz отражает
// здоровье зависимостей, /healthz зависит ТОЛЬКО от процесса (liveness не падает
// от блипа зависимости — защита от restart-storm). Shutdown флипает /readyz в 503.

func okChecker(name string) health.Checker {
	return health.Checker{Name: name, Check: func(context.Context) error { return nil }}
}

func downChecker(name string) health.Checker {
	return health.Checker{Name: name, Check: func(context.Context) error { return errors.New("down") }}
}

func get(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// H-C1: все готово → /readyz 200 ready, /healthz 200 ok.
func TestHealth_AllUp_Ready(t *testing.T) {
	a := health.New([]health.Checker{okChecker("database"), okChecker("lro-worker")})
	ready := get(t, a.ReadyHandler(), "/readyz")
	require.Equal(t, http.StatusOK, ready.Code)
	require.Contains(t, ready.Body.String(), `"status":"ready"`)

	live := get(t, a.LiveHandler(), "/healthz")
	require.Equal(t, http.StatusOK, live.Code)
	require.Contains(t, live.Body.String(), "ok")
}

// H-C2/H-C8: зависимость упала → /readyz 503 not_ready+dep, /healthz все еще 200.
func TestHealth_DepDown_NotReady_LivenessUp(t *testing.T) {
	a := health.New([]health.Checker{okChecker("lro-worker"), downChecker("database")})
	ready := get(t, a.ReadyHandler(), "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, ready.Code)
	body := ready.Body.String()
	require.Contains(t, body, `"status":"not_ready"`)
	require.Contains(t, body, `"name":"database"`)
	require.Contains(t, body, `"status":"down"`)

	live := get(t, a.LiveHandler(), "/healthz")
	require.Equal(t, http.StatusOK, live.Code, "liveness независим от внешних deps")
}

// H-C7: одновременно упали несколько deps → перечислены ВСЕ.
func TestHealth_AggregatesAllDownDeps(t *testing.T) {
	a := health.New([]health.Checker{downChecker("database"), downChecker("iam-authz")})
	ready := get(t, a.ReadyHandler(), "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, ready.Code)
	body := ready.Body.String()
	require.Contains(t, body, `"name":"database"`)
	require.Contains(t, body, `"name":"iam-authz"`)
}

// H-C9: зависший чекер не вешает handler — 503 в пределах bounded-timeout.
func TestHealth_BoundedTimeout_HungChecker(t *testing.T) {
	hung := health.Checker{Name: "database", Check: func(ctx context.Context) error {
		<-ctx.Done() // игнорирует cancel дольше, чем timeout (эмулируем зависшую сеть)
		return ctx.Err()
	}}
	a := health.New([]health.Checker{hung}, health.WithTimeout(50*time.Millisecond))
	start := time.Now()
	ready := get(t, a.ReadyHandler(), "/readyz")
	require.Less(t, time.Since(start), time.Second, "handler не должен висеть до k8s probe-timeout")
	require.Equal(t, http.StatusServiceUnavailable, ready.Code)
	require.Contains(t, ready.Body.String(), `"name":"database"`)
}

// H-D5: graceful-shutdown → /readyz 503 shutting_down ДО gRPC-stop; /healthz 503.
func TestHealth_ShuttingDown_Flips(t *testing.T) {
	a := health.New([]health.Checker{okChecker("database")})
	a.SetShuttingDown()
	ready := get(t, a.ReadyHandler(), "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, ready.Code)
	require.Contains(t, ready.Body.String(), `"status":"shutting_down"`)

	live := get(t, a.LiveHandler(), "/healthz")
	require.Equal(t, http.StatusServiceUnavailable, live.Code)
}

// H-B7: результаты readiness зеркалятся в observer (dependency_up gauge).
func TestHealth_ResultObserver_Mirror(t *testing.T) {
	seen := map[string]bool{}
	var mu sync.Mutex
	a := health.New(
		[]health.Checker{okChecker("database"), downChecker("iam-authz")},
		health.WithResultObserver(func(dep string, up bool) {
			mu.Lock()
			defer mu.Unlock()
			seen[dep] = up
		}),
	)
	_ = get(t, a.ReadyHandler(), "/readyz")
	mu.Lock()
	defer mu.Unlock()
	require.True(t, seen["database"])
	require.False(t, seen["iam-authz"])
}

// H-C9 (-race): параллельные /readyz не дают гонок состояния.
func TestHealth_ConcurrentReadyz_NoRace(t *testing.T) {
	a := health.New([]health.Checker{okChecker("database"), downChecker("iam-authz")})
	h := a.ReadyHandler()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = get(t, h, "/readyz")
		}()
	}
	wg.Wait()
}
