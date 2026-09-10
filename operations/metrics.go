// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations

import "sync"

// Recorder — sink метрик durability-слоя LRO. Конкретный Prometheus-клиент сюда
// НЕ импортируется (corelib остается dependency-light): сервис подключает
// Prometheus-backed Recorder в composition root, тесты используют MemRecorder.
// Канонические серии (имена — на стороне сервиса, лейблы фиксированы здесь):
//
//	operations_terminal_write_retries_total{op}   — ретраи MarkDone/MarkError на transient-сбое
//	operations_terminal_write_failures_total{op}  — терминальная запись не удалась после max-ретраев
//	operations_inflight                           — gauge числа запущенных worker'ов (<= max)
//	operations_orphans_recovered_total{outcome}   — orphan'ы, разрешенные reconciler'ом (done|error)
//	operations_reconcile_runs_total               — прогоны sweep-цикла
//	operations_reconcile_errors_total             — ошибки sweep-цикла
//
// op-лейбл — "MarkDone"/"MarkError"; outcome-лейбл — "done"/"error".
type Recorder interface {
	IncTerminalWriteRetries(op string)
	IncTerminalWriteFailures(op string)
	SetInflight(n float64)
	IncOrphansRecovered(outcome string)
	IncReconcileRuns()
	IncReconcileErrors()
}

// NopRecorder — no-op Recorder, безопасный дефолт когда метрики не подключены.
type NopRecorder struct{}

// IncTerminalWriteRetries — no-op.
func (NopRecorder) IncTerminalWriteRetries(string) {}

// IncTerminalWriteFailures — no-op.
func (NopRecorder) IncTerminalWriteFailures(string) {}

// SetInflight — no-op.
func (NopRecorder) SetInflight(float64) {}

// IncOrphansRecovered — no-op.
func (NopRecorder) IncOrphansRecovered(string) {}

// IncReconcileRuns — no-op.
func (NopRecorder) IncReconcileRuns() {}

// IncReconcileErrors — no-op.
func (NopRecorder) IncReconcileErrors() {}

var _ Recorder = NopRecorder{}

// ClaimRecorder — ОПЦИОНАЛЬНОЕ расширение Recorder для наблюдаемости
// pre-execution подтверждения живости и поздней терминальной записи.
//
// Почему расширение, а не новые методы базового Recorder: интерфейс реализуют
// шесть сервисных Prometheus-адаптеров, и дописывание в него ломает их сборку.
// Расширение подключается сервисом независимо; corelib-овые Nop/Mem реализуют
// его сразу, и это закреплено проверкой присваиванием ниже.
//
// Канонические серии (имена — на стороне сервиса):
//
//	operations_execution_claim_retries_total   — повторы подтверждения живости
//	operations_execution_claim_failures_total  — отказ исполнить: живость не подтверждена
//	operations_terminal_write_already_resolved_total{op} — терминал не прошёл: строка уже разрешена
type ClaimRecorder interface {
	IncExecutionClaimRetries()
	IncExecutionClaimFailures()
	IncTerminalWriteAlreadyResolved(op string)
}

// IncExecutionClaimRetries — no-op.
func (NopRecorder) IncExecutionClaimRetries() {}

// IncExecutionClaimFailures — no-op.
func (NopRecorder) IncExecutionClaimFailures() {}

// IncTerminalWriteAlreadyResolved — no-op.
func (NopRecorder) IncTerminalWriteAlreadyResolved(string) {}

var _ ClaimRecorder = NopRecorder{}

// MemRecorder — in-memory Recorder для тестов и как безопасный дефолт.
// Concurrency-safe.
type MemRecorder struct {
	mu sync.Mutex

	terminalRetries  map[string]float64
	terminalFailures map[string]float64
	orphans          map[string]float64
	inflight         float64
	maxInflight      float64
	reconcileRuns    float64
	reconcileErrors  float64

	claimRetries        float64
	claimFailures       float64
	terminalAlreadyDone map[string]float64
}

// NewMemRecorder — пустой in-memory Recorder.
func NewMemRecorder() *MemRecorder {
	return &MemRecorder{
		terminalRetries:  map[string]float64{},
		terminalFailures: map[string]float64{},
		orphans:          map[string]float64{},

		terminalAlreadyDone: map[string]float64{},
	}
}

// IncTerminalWriteRetries инкрементит счетчик ретраев терминальной записи.
func (m *MemRecorder) IncTerminalWriteRetries(op string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminalRetries[op]++
}

// IncTerminalWriteFailures инкрементит счетчик невосстановимых терминальных записей.
func (m *MemRecorder) IncTerminalWriteFailures(op string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminalFailures[op]++
}

// SetInflight выставляет gauge числа запущенных worker'ов и трекает пик.
func (m *MemRecorder) SetInflight(n float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inflight = n
	if n > m.maxInflight {
		m.maxInflight = n
	}
}

// IncOrphansRecovered инкрементит счетчик разрешенных reconciler'ом orphan'ов.
func (m *MemRecorder) IncOrphansRecovered(outcome string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orphans[outcome]++
}

// IncReconcileRuns инкрементит счетчик прогонов sweep-цикла.
func (m *MemRecorder) IncReconcileRuns() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileRuns++
}

// IncReconcileErrors инкрементит счетчик ошибок sweep-цикла.
func (m *MemRecorder) IncReconcileErrors() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconcileErrors++
}

// IncExecutionClaimRetries инкрементит счётчик повторов подтверждения живости.
func (m *MemRecorder) IncExecutionClaimRetries() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimRetries++
}

// IncExecutionClaimFailures инкрементит счётчик отказов исполнить работу из-за
// неподтверждённой живости операции.
func (m *MemRecorder) IncExecutionClaimFailures() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claimFailures++
}

// IncTerminalWriteAlreadyResolved инкрементит счётчик терминальных записей,
// не прошедших сравнение-и-замену, потому что строка уже разрешена.
func (m *MemRecorder) IncTerminalWriteAlreadyResolved(op string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.terminalAlreadyDone[op]++
}

var _ ClaimRecorder = (*MemRecorder)(nil)

// ---- test-accessors ----

// ExecutionClaimRetries возвращает накопленные повторы подтверждения живости.
func (m *MemRecorder) ExecutionClaimRetries() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claimRetries
}

// ExecutionClaimFailures возвращает накопленные отказы исполнить работу без
// подтверждённой живости.
func (m *MemRecorder) ExecutionClaimFailures() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claimFailures
}

// TerminalWriteAlreadyResolved возвращает счётчик поздних терминальных записей
// по op-лейблу.
func (m *MemRecorder) TerminalWriteAlreadyResolved(op string) float64 {
	return m.read(m.terminalAlreadyDone, op)
}

// TerminalWriteRetries возвращает накопленные ретраи по op-лейблу.
func (m *MemRecorder) TerminalWriteRetries(op string) float64 { return m.read(m.terminalRetries, op) }

// TerminalWriteFailures возвращает накопленные failures по op-лейблу.
func (m *MemRecorder) TerminalWriteFailures(op string) float64 { return m.read(m.terminalFailures, op) }

// Inflight возвращает текущее значение gauge.
func (m *MemRecorder) Inflight() float64 { m.mu.Lock(); defer m.mu.Unlock(); return m.inflight }

// MaxInflight возвращает наблюденный пик inflight.
func (m *MemRecorder) MaxInflight() float64 { m.mu.Lock(); defer m.mu.Unlock(); return m.maxInflight }

// OrphansRecovered возвращает счетчик разрешенных orphan'ов по outcome-лейблу.
func (m *MemRecorder) OrphansRecovered(outcome string) float64 { return m.read(m.orphans, outcome) }

// ReconcileRuns возвращает счетчик прогонов sweep-цикла.
func (m *MemRecorder) ReconcileRuns() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconcileRuns
}

// ReconcileErrors возвращает счетчик ошибок sweep-цикла.
func (m *MemRecorder) ReconcileErrors() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconcileErrors
}

func (m *MemRecorder) read(src map[string]float64, key string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return src[key]
}

var _ Recorder = (*MemRecorder)(nil)
