// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package otelx — обёртка трассировки для внесённого поддерева движка OAuth2.
//
// ЭТО КОД ФУНДАМЕНТА, написанный для него, а не перенесённый из апстрима. Движок
// звал из `github.com/ory/x/otelx` РОВНО ОДНУ функцию — `End(span, &err)` в
// отложенном вызове (замер: `grep -rn 'otelx\.' internal/oauth2` → 9
// вхождений в 9 не-тестовых файлах), и через этот единственный импорт в граф
// сборки приезжал весь `ory/x/otelx`: комплект реализации OpenTelemetry,
// экспортёры OTLP/Jaeger/Zipkin, gRPC, protobuf, logrus и лог-константы ORM
// `ory/pop`. Здесь у функции та же подпись, так что места вызова в поддереве
// остаются апстримными, а исполнение — своё: исход операции отмечается
// штатными средствами API OpenTelemetry (событие ошибки и статус спана), без
// набора тегов апстрима.
//
// ТРАССИРОВКА ЭТИМ НЕ ОТКЛЮЧАЕТСЯ. Пакет зависит от API-модулей
// `go.opentelemetry.io/otel` и `go.opentelemetry.io/otel/trace` и ни от чего
// больше: провайдера движок берёт из контекста. Подставит приложение
// настоящий провайдер — спаны пойдут; не подставит — будет noop-спан.
//
// ИМЯ ПАКЕТА СОВПАДАЕТ С ЗАМЕНЯЕМЫМ НАМЕРЕННО: благодаря этому повторяемая цена
// обновления апстрима — ровно 9 строк импорта. Если новая версия апстрима
// позовёт из `otelx` что-нибудь ещё, сборка сломается громко, а не разойдётся
// молча.
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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// escaped — признак исключения, вылетевшего за пределы спана (семантические
// соглашения OpenTelemetry об исключениях).
var escaped = attribute.Bool("exception.escaped", true)

// End закрывает спан операции и отмечает на нём её исход. Вызывается ТОЛЬКО
// отложенно, с указателем на именованную ошибку:
//
//	func Divide(ctx context.Context, a, b int) (ratio int, err error) {
//		ctx, span := tracer.Start(ctx, "Divide")
//		defer otelx.End(span, &err)
//		...
//	}
//
// Исходов три, и спан закрывается в каждом ровно один раз:
//
//   - успех (`err` пуст или `*err` пуст) — спан закрывается без отметок;
//   - ошибка — она записывается событием спана, статус — Error с её текстом;
//   - паника — она записывается событием со стеком места паники и признаком
//     вылета за спан, статус — Error с текстом «panic: …», и паника летит
//     дальше НЕИЗМЕНЁННОЙ: обёртка наблюдает, а не лечит.
//
// recover работает здесь потому, что End и есть отложенная функция.
func End(span trace.Span, err *error) {
	if recovered := recover(); recovered != nil {
		markPanic(span, recovered)
		span.End()
		panic(recovered)
	}
	if err != nil && *err != nil {
		span.RecordError(*err)
		span.SetStatus(codes.Error, (*err).Error())
	}
	span.End()
}

// markPanic отмечает на спане панику со значением recovered. Значение-ошибка
// оборачивается (`%w`), чтобы вид ошибки остался разбираемым; прочее —
// печатается.
func markPanic(span trace.Span, recovered any) {
	var cause error
	if e, isErr := recovered.(error); isErr {
		cause = fmt.Errorf("panic: %w", e)
	} else {
		cause = errors.New("panic: " + fmt.Sprint(recovered))
	}
	span.RecordError(cause, trace.WithStackTrace(true), trace.WithAttributes(escaped))
	span.SetStatus(codes.Error, cause.Error())
}
