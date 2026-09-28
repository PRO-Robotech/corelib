// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// recorder_test.go — ПОДСТАВНОЙ провайдер трассировки, собранный НА API
// OpenTelemetry и ни на чём больше.
//
// Комплект реализации (`go.opentelemetry.io/otel/sdk`) сюда не берётся
// намеренно: ради его отсутствия в графе сборки и делался вырез телеметрии
// (см. `internal/otelx/end.go`). Проба, притащившая комплект обратно прямой
// зависимостью, судила бы дерево, отличное от поставляемого.
//
// Провайдер НАБЛЮДАЕМЫЙ: он запоминает каждый открытый спан, его имя, область
// инструментирования, число закрытий, статус и записанные ошибки — ровно то,
// чем обёртка фундамента отмечает исход операции.
package tracecut_test

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// recordedSpan — один наблюдённый спан. Замок нужен потому, что `defer
// otelx.End` может исполниться не в той горутине, что открыла спан.
type recordedSpan struct {
	embedded.Span

	provider *recordingProvider
	name     string
	scope    string

	mu         sync.Mutex
	ended      int
	statusCode codes.Code
	statusDesc string
	errs       []error
}

func (s *recordedSpan) End(...trace.SpanEndOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended++
}

func (s *recordedSpan) SetStatus(code codes.Code, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCode, s.statusDesc = code, description
}

func (s *recordedSpan) RecordError(err error, _ ...trace.EventOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

func (s *recordedSpan) SetAttributes(...attribute.KeyValue)   {}
func (s *recordedSpan) AddEvent(string, ...trace.EventOption) {}
func (s *recordedSpan) AddLink(trace.Link)                    {}
func (s *recordedSpan) IsRecording() bool                     { return true }
func (s *recordedSpan) SetName(string)                        {}
func (s *recordedSpan) SpanContext() trace.SpanContext        { return trace.SpanContext{} }
func (s *recordedSpan) TracerProvider() trace.TracerProvider  { return s.provider }

func (s *recordedSpan) endedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

func (s *recordedSpan) status() (codes.Code, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusCode, s.statusDesc
}

func (s *recordedSpan) recordedErrors() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error(nil), s.errs...)
}

var _ trace.Span = (*recordedSpan)(nil)

// recordingTracer открывает спаны и складывает их в провайдер.
type recordingTracer struct {
	embedded.Tracer

	provider *recordingProvider
	scope    string
}

func (t *recordingTracer) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	s := &recordedSpan{provider: t.provider, name: name, scope: t.scope}
	t.provider.record(s)
	return trace.ContextWithSpan(ctx, s), s
}

var _ trace.Tracer = (*recordingTracer)(nil)

// recordingProvider раздаёт наблюдаемые трассировщики и хранит всё, что они
// открыли.
type recordingProvider struct {
	embedded.TracerProvider

	mu    sync.Mutex
	spans []*recordedSpan
}

func (p *recordingProvider) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	return &recordingTracer{provider: p, scope: name}
}

func (p *recordingProvider) record(s *recordedSpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spans = append(p.spans, s)
}

// started возвращает имена открытых спанов в порядке открытия.
func (p *recordingProvider) started() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.spans))
	for i, s := range p.spans {
		out[i] = s.name
	}
	return out
}

// byName возвращает спаны с данным именем.
func (p *recordingProvider) byName(name string) []*recordedSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*recordedSpan
	for _, s := range p.spans {
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

var _ trace.TracerProvider = (*recordingProvider)(nil)

// rootContext возвращает контекст с КОРНЕВЫМ спаном подставного провайдера.
//
// Так ставит контекст настоящий вызывающий: движок берёт провайдер ИЗ КОНТЕКСТА
// (`trace.SpanFromContext(ctx).TracerProvider()`), а не из глобали. Это
// семантика апстрима, сохранённая вырезом дословно; проба воспроизводит
// именно её, иначе судила бы не тот путь.
func rootContext(p *recordingProvider) context.Context {
	return trace.ContextWithSpan(context.Background(), &recordedSpan{provider: p, name: "probe.root"})
}
