// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package health — ЕДИНСТВЕННЫЙ в дереве носитель разведённых живости и
// готовности.
//
// Готовность (`/readyz`) строится из ИМЕНОВАННЫХ проверок критичных зависимостей
// (база / канал к владельцу прав / дренер регистраций / исполнитель операций).
// Отказ зависимости снимает под из ротации (503), а живость (`/healthz`) при этом
// остаётся 200 — она зависит ТОЛЬКО от процесса, чтобы блип зависимости не вызвал
// шторм перезапусков. Каждый чекер ограничен собственным сроком: зависшая
// зависимость считается недоступной, но не вешает обработчик.
//
// На гашении SetShuttingDown переводит `/readyz` в 503 (kubelet перестаёт слать
// трафик) ДО остановки gRPC-серверов.
//
// # Почему носитель ОДИН, а не по копии на сервис
//
// Копий было три, и они были кодом идентичны — то есть один механизм, о котором
// дерево говорило трижды. Разойтись им было нечем: копии не собираются вместе и
// друг друга не читают, поэтому расхождение пришло бы молча. Замер, из которого
// это выведено (задача продукта #1655): трём сервисам из семи готовность
// требовалось завести впервые, и заведение по месту дало бы ШЕСТЬ копий вместо
// трёх (`architecture.md` §«Переиспользование через общий фундамент pkg/»).
//
// # Чего этот пакет НЕ решает
//
// ЧТО именно проверяет каждая зависимость — вопрос композиционного корня: он
// один знает, какая база у сервиса своя и к кому он ходит. Пакет отвечает лишь за
// то, чтобы ответ был РАЗВЕДЁН на два и чтобы ни один чекер не мог подвесить
// обработчик.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Slot — зависимость, чей носитель появляется ПОЗЖЕ поверхности.
//
// # Зачем
//
// Диагностическая поверхность поднимается РАНО и намеренно: пока её нет, проба
// живости получает отказ соединения, и медленный старт читается kubelet'ом как
// мёртвый процесс — то есть шторм перезапусков ровно там, где процесс исправно
// поднимается. Часть зависимостей при этом ещё не создана: соединение с соседом
// набирается ниже по тексту корня.
//
// Прямое замыкание на переменную корня здесь негодно ДВАЖДЫ: обработчик читает её
// из другой горутины (гонка, и `-race` это покажет), а до присваивания он увидит
// нулевое значение и объявит зависимость исправной. Slot закрывает оба: доступ
// синхронизирован, а до установки ответ — НЕ ГОТОВ.
//
// Умолчание выбрано fail-closed осознанно: неустановленная зависимость есть
// «ответа нет», а неполученный ответ не является «да».
type Slot struct {
	mu    sync.RWMutex
	check func(ctx context.Context) error
}

// Install подключает носителя зависимости. Повторный вызов заменяет прежнего —
// корень вправе пересобрать соединение, не пересобирая поверхность.
func (s *Slot) Install(check func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.check = check
}

// Check — тело чекера. До Install отвечает [ErrDependencyNotWired]: «носителя
// ещё нет» и «носитель отвечает» обязаны быть различимы, иначе окно старта
// молча зачитывается в готовность.
func (s *Slot) Check(ctx context.Context) error {
	s.mu.RLock()
	check := s.check
	s.mu.RUnlock()
	if check == nil {
		return ErrDependencyNotWired
	}
	return check(ctx)
}

// Checker собирает Slot в именованную проверку.
func (s *Slot) Checker(name string) Checker {
	return Checker{Name: name, Check: s.Check}
}

// ErrDependencyNotWired — зависимость объявлена корнем, но её носитель ещё не
// установлен. Причина «не готов» без раскрытия внутренних деталей наружу.
var ErrDependencyNotWired = errors.New("dependency not wired yet")

// defaultCheckerTimeout — per-checker бюджет; зависший ping считается down, не
// блокирует handler до k8s probe-timeout.
const defaultCheckerTimeout = time.Second

// Checker — именованная проверка одной зависимости. Check возвращает nil, если
// зависимость здорова; любая ошибка (или таймаут) трактуется как down.
type Checker struct {
	Name  string
	Check func(ctx context.Context) error
}

// Aggregator агрегирует readiness по набору чекеров и разводит liveness/readiness.
// Потокобезопасен (shutting — под mutex'ом; чекеры read-only).
type Aggregator struct {
	checkers []Checker
	timeout  time.Duration
	observer func(dependency string, up bool)

	mu       sync.RWMutex
	shutting bool
}

// Option — функциональная опция Aggregator.
type Option func(*Aggregator)

// WithTimeout задаёт per-checker бюджет (дефолт 1s).
func WithTimeout(d time.Duration) Option {
	return func(a *Aggregator) {
		if d > 0 {
			a.timeout = d
		}
	}
}

// WithResultObserver подключает зеркало результата readiness (напр.
// dependency_up Prometheus-gauge): вызывается на каждый чекер при каждой оценке.
func WithResultObserver(f func(dependency string, up bool)) Option {
	return func(a *Aggregator) { a.observer = f }
}

// New конструирует Aggregator поверх набора чекеров.
func New(checkers []Checker, opts ...Option) *Aggregator {
	a := &Aggregator{checkers: checkers, timeout: defaultCheckerTimeout}
	for _, o := range opts {
		o(a)
	}
	return a
}

// SetShuttingDown помечает процесс в состоянии graceful-shutdown — readiness
// сразу флипает в 503, liveness тоже отдаёт 503 (процесс гасится).
func (a *Aggregator) SetShuttingDown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.shutting = true
}

func (a *Aggregator) isShuttingDown() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.shutting
}

// Evaluate прогоняет все чекеры конкурентно с per-checker bounded-timeout и
// возвращает готовность + имена упавших зависимостей (в порядке объявления
// чекеров). Результат зеркалится в observer.
func (a *Aggregator) Evaluate(ctx context.Context) (bool, []string) {
	type res struct {
		name string
		up   bool
	}
	results := make(chan res, len(a.checkers))
	for _, c := range a.checkers {
		go func() {
			results <- res{name: c.Name, up: a.runChecker(ctx, c)}
		}()
	}
	upByName := make(map[string]bool, len(a.checkers))
	for range a.checkers {
		r := <-results
		upByName[r.name] = r.up
		if a.observer != nil {
			a.observer(r.name, r.up)
		}
	}

	var down []string
	for _, c := range a.checkers {
		if !upByName[c.Name] {
			down = append(down, c.Name)
		}
	}
	return len(down) == 0, down
}

// runChecker исполняет один Check с bounded-timeout. Даже если Check игнорирует
// cancel (зависшая сеть), handler не блокируется дольше timeout — результат
// читается через select.
func (a *Aggregator) runChecker(parent context.Context, c Checker) bool {
	ctx, cancel := context.WithTimeout(parent, a.timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Check(ctx) }()
	select {
	case err := <-done:
		return err == nil
	case <-ctx.Done():
		return false
	}
}

// LiveHandler — liveness probe. 200 `ok`, пока процесс жив и не в shutdown.
// НЕ зависит от внешних зависимостей (защита от restart-storm).
func (a *Aggregator) LiveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if a.isShuttingDown() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("shutting down"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

// readyResponse — JSON-тело /readyz.
type readyResponse struct {
	Status       string           `json:"status"`
	Dependencies []dependencyView `json:"dependencies,omitempty"`
}

type dependencyView struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ReadyHandler — readiness probe. 200 `{"status":"ready"}`, когда все критичные
// зависимости здоровы; 503 со списком упавших — иначе; 503
// `{"status":"shutting_down"}` на graceful-shutdown.
func (a *Aggregator) ReadyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if a.isShuttingDown() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(readyResponse{Status: "shutting_down"})
			return
		}
		ready, down := a.Evaluate(r.Context())
		if ready {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(readyResponse{Status: "ready"})
			return
		}
		deps := make([]dependencyView, 0, len(down))
		for _, name := range down {
			deps = append(deps, dependencyView{Name: name, Status: "down"})
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(readyResponse{Status: "not_ready", Dependencies: deps})
	}
}
