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
// Провайдер НАБЛЮДАЕМЫЙ: он запоминает каждый открытый спан, его имя, факт
// закрытия, статус и теги. Этого ровно достаточно, чтобы отличить «спан создан
// и несёт признаки» от «спана нет».
package tracecut_test

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// recordedSpan — один наблюдённый спан.
type recordedSpan struct {
	embedded.Span

	provider *recordingProvider
	name     string

	mu         sync.Mutex
	ended      int
	statusCode codes.Code
	statusDesc string
	attrs      map[attribute.Key]attribute.Value
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

func (s *recordedSpan) SetAttributes(kv ...attribute.KeyValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range kv {
		s.attrs[a.Key] = a.Value
	}
}

func (s *recordedSpan) AddEvent(string, ...trace.EventOption)   {}
func (s *recordedSpan) AddLink(trace.Link)                      {}
func (s *recordedSpan) IsRecording() bool                       { return true }
func (s *recordedSpan) RecordError(error, ...trace.EventOption) {}
func (s *recordedSpan) SetName(string)                          {}
func (s *recordedSpan) SpanContext() trace.SpanContext          { return trace.SpanContext{} }
func (s *recordedSpan) TracerProvider() trace.TracerProvider    { return s.provider }

// endedCount, status и attr читают наблюдённое под тем же замком, под которым
// оно писалось: движок открывает спан в одной горутине, а `defer otelx.End`
// может исполниться в другой, если путь уводит в горутину.
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

func (s *recordedSpan) attr(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.attrs[attribute.Key(key)]
	if !ok {
		return "", false
	}
	return v.AsString(), true
}

var _ trace.Span = (*recordedSpan)(nil)

// recordingTracer открывает спаны и складывает их в провайдер.
type recordingTracer struct {
	embedded.Tracer

	provider *recordingProvider
	scope    string
}

func (t *recordingTracer) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	s := &recordedSpan{
		provider: t.provider,
		name:     name,
		attrs:    map[attribute.Key]attribute.Value{},
	}
	t.provider.record(t.scope, s)
	return trace.ContextWithSpan(ctx, s), s
}

var _ trace.Tracer = (*recordingTracer)(nil)

// recordingProvider — подставной провайдер. Раздаёт наблюдаемые трассировщики
// и хранит всё, что они открыли.
type recordingProvider struct {
	embedded.TracerProvider

	mu     sync.Mutex
	scopes []string
	spans  []*recordedSpan
}

func newRecordingProvider() *recordingProvider { return &recordingProvider{} }

func (p *recordingProvider) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	return &recordingTracer{provider: p, scope: name}
}

func (p *recordingProvider) record(scope string, s *recordedSpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopes = append(p.scopes, scope)
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

// scopeOf возвращает имя области инструментирования, из которой открыт первый
// спан с данным именем.
func (p *recordingProvider) scopeOf(name string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, s := range p.spans {
		if s.name == name {
			return p.scopes[i], true
		}
	}
	return "", false
}

var _ trace.TracerProvider = (*recordingProvider)(nil)

// rootContext возвращает контекст с КОРНЕВЫМ спаном подставного провайдера.
//
// Так ставит контекст настоящий вызывающий: движок берёт провайдер ИЗ КОНТЕКСТА
// (`trace.SpanFromContext(ctx).TracerProvider()`), а не из глобали. Это
// семантика апстрима, и она сохранена вырезом дословно; проба обязана
// воспроизводить именно её, иначе судила бы не тот путь.
func rootContext(p *recordingProvider) context.Context {
	root := &recordedSpan{provider: p, name: "проба.корень", attrs: map[attribute.Key]attribute.Value{}}
	return trace.ContextWithSpan(context.Background(), root)
}
