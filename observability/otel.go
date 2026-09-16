// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package observability

import (
	"context"
	"log/slog"

	"github.com/PRO-Robotech/corelib/envknob"
)

// ShutdownFn — функция завершения работы провайдера телеметрии.
type ShutdownFn func(context.Context) error

// EnvOTLPEndpoint — адрес приёмника телеметрии.
//
// Имя — СТАНДАРТНОЕ имя спецификации OpenTelemetry, а не наша копия его с
// приставкой платформы. Приставка здесь была не вопросом вкуса: агент и
// библиотека сбора читают стандартное имя, поэтому переменная с приставкой не
// настраивала НИЧЕГО, кроме нашей собственной ветки, а оператор, задавший
// стандартную, оставался без нашей (`PRO-Robotech/corelib#11`).
const EnvOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"

// LegacyEnvOTLPEndpoint — прежнее написание, принимаемое ОКНОМ перехода.
const LegacyEnvOTLPEndpoint = "KACHO_OTEL_EXPORTER_OTLP_ENDPOINT"

// InitOtel инициализирует экспорт телеметрии по endpoint'у из
// [EnvOTLPEndpoint] и возвращает ShutdownFn для graceful-flush.
//
// Если endpoint не задан — телеметрия отключена, возвращается no-op. Если endpoint
// задан, но OTLP-exporter в этой сборке не подключен, функция НЕ делает вид, что
// телеметрия работает: пишет явный WARN (чтобы оператор не считал, что трейсы
// уходят) и возвращает no-op shutdown. Это честный контракт вместо «тихого»
// no-op, который ранее молча терял телеметрию при настроенном endpoint'е.
func InitOtel(ctx context.Context, serviceName string) (ShutdownFn, error) {
	noop := func(context.Context) error { return nil }
	endpoint := envknob.Get(EnvOTLPEndpoint, LegacyEnvOTLPEndpoint)
	if endpoint == "" {
		return noop, nil
	}
	slog.Warn("OTLP endpoint configured but trace exporter is not wired in this build; "+
		"distributed tracing is DISABLED (only structured slog logging is active)",
		"service", serviceName, "endpoint", endpoint)
	return noop, nil
}
