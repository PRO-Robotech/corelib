// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package otelx — ЭТО КОД ФУНДАМЕНТА, А НЕ АПСТРИМА. Он подменяет собой
// `github.com/ory/x/otelx` для внесённого поддерева движка и повторяет ЕГО
// СЕМАНТИКУ ДОСЛОВНО: тот же статус ошибки, тот же набор тегов (включая
// datadog-совместимые), та же разметка паники и её проброс дальше. Сверено с
// `github.com/ory/x@v0.0.729/otelx/withspan.go`.
//
// ЗАЧЕМ. Ядро движка звало из `ory/x/otelx` РОВНО ОДНУ функцию — `End`
// (замер: `grep -rn 'otelx\.' internal/oauth2` → 9 вхождений, все
// `otelx.End(span, &err)`, в 9 не-тестовых файлах). Через этот единственный
// импорт в производственный граф сборки приезжал весь `ory/x/otelx`: комплект
// реализации OpenTelemetry, экспортёры OTLP/Jaeger/Zipkin, gRPC, protobuf,
// logrus и лог-константы ORM `ory/pop` — тринадцать модулей ради одного
// `span.End()` с тегами.
//
// ТРАССИРОВКА ЭТИМ НЕ ОТКЛЮЧАЕТСЯ. Пакет зависит от API-модулей
// `go.opentelemetry.io/otel` и `go.opentelemetry.io/otel/trace` и ни от чего
// больше: провайдера берёт ИЗ КОНТЕКСТА, как и апстрим. Подставит приложение
// настоящий провайдер — спаны пойдут ровно те же. Не подставит — получит
// noop-спан, тоже как у апстрима. Отключить трассировку значило бы ослабить
// наблюдаемость под видом чистки графа; здесь снят комплект реализации, а не
// наблюдаемость.
//
// ИМЯ ПАКЕТА СОВПАДАЕТ С ЗАМЕНЯЕМЫМ НАМЕРЕННО: благодаря этому места вызова в
// поддереве остаются побайтово апстримными, и повторяемая цена обновления —
// ровно 9 строк импорта. Если новая версия апстрима позовёт из `otelx`
// что-нибудь ещё, сборка сломается громко, а не разойдётся молча.
//
// ЛЕЖИТ СНАРУЖИ ПОДДЕРЕВА НАМЕРЕННО. `internal/oauth2/` — область апстрима, и
// статические анализаторы читают её по своим правилам (см. `.github/golangci.yml`
// и шаг gosec в `.github/workflows/ci.yml`). Наш код внутри этой области попал
// бы в ту же слепую зону; здесь он линтуется и сканируется наравне с остальным
// деревом.
package otelx

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"

	pkgerrors "github.com/pkg/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
	"go.opentelemetry.io/otel/trace"
)

// End завершает спан и сам выставляет состояние ошибки, если `*err` не пуст или
// если функция паникует.
//
// Применение:
//
//	func Divide(ctx context.Context, a, b int) (ratio int, err error) {
//		ctx, span := tracer.Start(ctx, "Divide")
//		defer otelx.End(span, &err)
//		...
//	}
//
// На панике семантические соглашения OpenTelemetry выполняются НЕ полностью:
// по ним стек и тип исключения полагается отдавать событием спана, а здесь они
// ставятся тегами прямо на спан. Это поведение апстрима, и оно сохранено
// дословно — иначе вырез менял бы вид телеметрии, а не только граф сборки.
// https://opentelemetry.io/docs/specs/semconv/exceptions/exceptions-spans/
//
// Дополнительные теги (`error.message`, `error.type`, `error.stack`) — ради
// совместимости с Datadog, тоже как у апстрима:
// https://docs.datadoghq.com/standard-attributes/?product=apm&search=error
func End(span trace.Span, err *error) {
	defer span.End()
	if r := recover(); r != nil {
		setErrorStatusPanic(span, r)
		panic(r)
	}
	if err == nil || *err == nil {
		return
	}
	span.SetStatus(codes.Error, (*err).Error())
	setErrorTags(span, *err)
}

func setErrorStatusPanic(span trace.Span, recovered any) {
	span.SetAttributes(
		semconv.ExceptionEscaped(true),
		attribute.String("error.stack", stacktrace()),
	)
	if t := reflect.TypeOf(recovered); t != nil {
		span.SetAttributes(semconv.ExceptionType(t.String()))
	}
	switch e := recovered.(type) {
	case error:
		span.SetStatus(codes.Error, "panic: "+e.Error())
		setErrorTags(span, e)
	case string, fmt.Stringer:
		span.SetStatus(codes.Error, fmt.Sprintf("panic: %v", e))
	default:
		span.SetStatus(codes.Error, "panic")
	case nil:
		// ничего
	}
}

func setErrorTags(span trace.Span, err error) {
	span.SetAttributes(
		attribute.String("error", err.Error()),
		attribute.String("error.message", err.Error()),
		attribute.String("error.type", fmt.Sprintf("%T", errors.Unwrap(err))),
	)
	if e := interface{ StackTrace() pkgerrors.StackTrace }(nil); errors.As(err, &e) {
		span.SetAttributes(attribute.String("error.stack", fmt.Sprintf("%+v", e.StackTrace())))
	}
	if e := interface{ Reason() string }(nil); errors.As(err, &e) {
		span.SetAttributes(attribute.String("error.reason", e.Reason()))
	}
	if e := interface{ Debug() string }(nil); errors.As(err, &e) {
		span.SetAttributes(attribute.String("error.debug", e.Debug()))
	}
	if e := interface{ ID() string }(nil); errors.As(err, &e) {
		span.SetAttributes(attribute.String("error.id", e.ID()))
	}
	if e := interface{ Details() map[string]interface{} }(nil); errors.As(err, &e) {
		for k, v := range e.Details() {
			span.SetAttributes(attribute.String("error.details."+k, fmt.Sprintf("%v", v)))
		}
	}
}

// stacktrace повторяет апстримную: пропуск 4 кадров отсчитан от связки
// `recover` → setErrorStatusPanic → stacktrace → runtime.Callers, глубина
// которой здесь ровно та же, что в `ory/x`.
func stacktrace() string {
	pc := make([]uintptr, 5)
	n := runtime.Callers(4, pc)
	if n == 0 {
		return ""
	}
	pc = pc[:n]
	frames := runtime.CallersFrames(pc)

	var builder strings.Builder
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&builder, "%s\n\t%s:%d\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
	}
	return builder.String()
}
