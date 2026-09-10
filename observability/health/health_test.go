// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// readyStatus вызывает ReadyHandler и возвращает HTTP-код + распарсенный status.
func readyStatus(t *testing.T, a *Aggregator) (int, string, []string) {
	t.Helper()
	rec := httptest.NewRecorder()
	a.ReadyHandler()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body struct {
		Status       string `json:"status"`
		Dependencies []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"dependencies"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	down := make([]string, 0, len(body.Dependencies))
	for _, d := range body.Dependencies {
		down = append(down, d.Name)
	}
	return rec.Code, body.Status, down
}

func TestReadiness_FlipsWhenDependencyDown(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	agg := New([]Checker{
		{Name: "database", Check: func(context.Context) error { return nil }},
		{Name: "lro-worker", Check: func(context.Context) error {
			if up.Load() {
				return nil
			}
			return errors.New("dispatcher down")
		}},
	})

	// All up → 200 ready.
	if code, status, _ := readyStatus(t, agg); code != http.StatusOK || status != "ready" {
		t.Fatalf("healthy: code=%d status=%q, want 200/ready", code, status)
	}

	// Flip lro-worker down → 503 not_ready, dependency listed.
	up.Store(false)
	code, status, down := readyStatus(t, agg)
	if code != http.StatusServiceUnavailable || status != "not_ready" {
		t.Fatalf("degraded: code=%d status=%q, want 503/not_ready", code, status)
	}
	found := false
	for _, d := range down {
		if d == "lro-worker" {
			found = true
		}
	}
	if !found {
		t.Fatalf("degraded: down=%v, want lro-worker listed", down)
	}
}

func TestLiveness_StaysUpWhenDependencyDown(t *testing.T) {
	agg := New([]Checker{
		{Name: "database", Check: func(context.Context) error { return errors.New("db down") }},
	})
	rec := httptest.NewRecorder()
	agg.LiveHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness code=%d, want 200 (liveness must not depend on deps)", rec.Code)
	}
}

func TestShuttingDown_Flips503(t *testing.T) {
	agg := New([]Checker{
		{Name: "database", Check: func(context.Context) error { return nil }},
	})
	agg.SetShuttingDown()
	if code, status, _ := readyStatus(t, agg); code != http.StatusServiceUnavailable || status != "shutting_down" {
		t.Fatalf("shutdown: code=%d status=%q, want 503/shutting_down", code, status)
	}
	rec := httptest.NewRecorder()
	agg.LiveHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("shutdown liveness code=%d, want 503", rec.Code)
	}
}

func TestResultObserver_MirrorsState(t *testing.T) {
	seen := map[string]bool{}
	agg := New([]Checker{
		{Name: "database", Check: func(context.Context) error { return nil }},
		{Name: "register-drainer", Check: func(context.Context) error { return errors.New("down") }},
	}, WithResultObserver(func(dep string, up bool) { seen[dep] = up }))

	_, _ = agg.Evaluate(context.Background())
	if !seen["database"] || seen["register-drainer"] {
		t.Fatalf("observer mirror = %v, want database=true register-drainer=false", seen)
	}
}

// Slot до установки носителя отвечает НЕ ГОТОВ, а после — тем, что установили.
//
// Обе стороны утверждаются вместе. Односторонняя проба здесь зеленела бы на
// вырожденной реализации: «всегда не готов» прошло бы отрицание, «всегда готов» —
// положительный контроль, и ровно первое из двух есть тот дефект, ради которого
// Slot заведён (окно старта, молча зачтённое в готовность).
func TestSlotIsNotReadyUntilItsCarrierIsInstalled(t *testing.T) {
	var s Slot

	if err := s.Check(context.Background()); !errors.Is(err, ErrDependencyNotWired) {
		t.Fatalf("до установки Check = %v, ожидалось %v: неустановленная зависимость обязана "+
			"читаться как «ответа нет», а не как «да»", err, ErrDependencyNotWired)
	}

	s.Install(func(context.Context) error { return nil })
	if err := s.Check(context.Background()); err != nil {
		t.Fatalf("после установки исправного носителя Check = %v, ожидалось nil", err)
	}

	boom := errors.New("peer down")
	s.Install(func(context.Context) error { return boom })
	if err := s.Check(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("повторная установка не заменила носителя: Check = %v, ожидалось %v", err, boom)
	}
}

// Агрегатор, собранный из Slot, объявляет под НЕ ГОТОВЫМ, пока корень не
// подключил зависимость, и называет её ПО ИМЕНИ.
func TestAggregatorOverAnUnwiredSlotNamesTheDependency(t *testing.T) {
	var s Slot
	agg := New([]Checker{s.Checker("iam-authz")})

	ready, down := agg.Evaluate(context.Background())
	if ready || len(down) != 1 || down[0] != "iam-authz" {
		t.Fatalf("до установки Evaluate = (%v, %v), ожидалось (false, [iam-authz])", ready, down)
	}

	s.Install(func(context.Context) error { return nil })
	if ready, down = agg.Evaluate(context.Background()); !ready || len(down) != 0 {
		t.Fatalf("после установки Evaluate = (%v, %v), ожидалось (true, [])", ready, down)
	}
}
