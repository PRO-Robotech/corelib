// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package otelx_test

import (
	"errors"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"

	"github.com/PRO-Robotech/corelib/internal/otelx"
)

// recordingSpan — САМОДЕЛЬНЫЙ держатель, а не мок из комплекта: он и есть
// предмет проверки. Комплект реализации OpenTelemetry сюда тащить нельзя — ради
// его отсутствия обёртка и заведена, — поэтому наблюдаем через интерфейс API.
type recordingSpan struct {
	embedded.Span

	ended      int
	statusCode codes.Code
	statusDesc string
	attrs      map[attribute.Key]attribute.Value
	recorded   []recordedError
}

type recordedError struct {
	err        error
	stackTrace bool
	attrs      []attribute.KeyValue
}

func newRecordingSpan() *recordingSpan {
	return &recordingSpan{attrs: map[attribute.Key]attribute.Value{}}
}

func (s *recordingSpan) End(...trace.SpanEndOption)            { s.ended++ }
func (s *recordingSpan) AddEvent(string, ...trace.EventOption) {}
func (s *recordingSpan) AddLink(trace.Link)                    {}
func (s *recordingSpan) IsRecording() bool                     { return true }
func (s *recordingSpan) SpanContext() trace.SpanContext        { return trace.SpanContext{} }
func (s *recordingSpan) SetName(string)                        {}
func (s *recordingSpan) TracerProvider() trace.TracerProvider  { return noopProvider{} }
func (s *recordingSpan) SetStatus(code codes.Code, description string) {
	s.statusCode, s.statusDesc = code, description
}

func (s *recordingSpan) RecordError(err error, opts ...trace.EventOption) {
	cfg := trace.NewEventConfig(opts...)
	s.recorded = append(s.recorded, recordedError{err: err, stackTrace: cfg.StackTrace(), attrs: cfg.Attributes()})
}

func (s *recordingSpan) SetAttributes(kv ...attribute.KeyValue) {
	for _, a := range kv {
		s.attrs[a.Key] = a.Value
	}
}

type noopProvider struct{ embedded.TracerProvider }

func (noopProvider) Tracer(string, ...trace.TracerOption) trace.Tracer { return nil }

var _ trace.Span = (*recordingSpan)(nil)

// panicking — вызов, паникующий значением v, с отложенным End; отдаёт то, что
// пролетело дальше обёртки.
func panicking(s trace.Span, v any) (recovered any) {
	defer func() { recovered = recover() }()
	func() {
		var err error
		defer otelx.End(s, &err)
		panic(v)
	}()
	return nil
}

func TestEndWithoutErrorClosesTheSpanAndMarksNothing(t *testing.T) {
	s := newRecordingSpan()
	var err error
	func() { defer otelx.End(s, &err) }()

	if s.ended != 1 {
		t.Errorf("спан закрыт %d раз, ожидался ровно один", s.ended)
	}
	if s.statusCode != codes.Unset || len(s.recorded) != 0 || len(s.attrs) != 0 {
		t.Errorf("без ошибки спан отмечен: статус %v, записанных ошибок %d, атрибутов %d",
			s.statusCode, len(s.recorded), len(s.attrs))
	}
}

func TestEndWithANilPointerClosesTheSpan(t *testing.T) {
	s := newRecordingSpan()
	func() { defer otelx.End(s, nil) }()

	if s.ended != 1 || s.statusCode != codes.Unset {
		t.Errorf("спан закрыт %d раз, статус %v; ожидались 1 и Unset", s.ended, s.statusCode)
	}
}

func TestEndRecordsTheErrorAndSetsTheErrorStatus(t *testing.T) {
	s := newRecordingSpan()
	inner := errors.New("внутренняя")
	err := fmt.Errorf("внешняя: %w", inner)
	func() { defer otelx.End(s, &err) }()

	if s.ended != 1 {
		t.Errorf("спан закрыт %d раз, ожидался один", s.ended)
	}
	if s.statusCode != codes.Error || s.statusDesc != "внешняя: внутренняя" {
		t.Errorf("статус %v %q, ожидались Error и текст ошибки", s.statusCode, s.statusDesc)
	}
	if len(s.recorded) != 1 || s.recorded[0].err != err {
		t.Fatalf("ошибка записана событием %d раз (%v), ожидался один раз именно эта", len(s.recorded), s.recorded)
	}
	if s.recorded[0].stackTrace {
		t.Error("у обычной ошибки записан стек места закрытия спана — он не говорит о причине ничего")
	}
}

func TestEndOnAPanicRecordsItAndLetsItThrough(t *testing.T) {
	s := newRecordingSpan()
	boom := errors.New("бум")

	if got := panicking(s, boom); got != boom {
		t.Fatalf("дальше пролетело %v, ожидалась та же паника %v", got, boom)
	}
	if s.ended != 1 {
		t.Errorf("на панике спан закрыт %d раз, ожидался один", s.ended)
	}
	if s.statusCode != codes.Error || s.statusDesc != "panic: бум" {
		t.Errorf("статус %v %q, ожидались Error и \"panic: бум\"", s.statusCode, s.statusDesc)
	}
	if len(s.recorded) != 1 {
		t.Fatalf("паника записана событием %d раз, ожидался один", len(s.recorded))
	}
	got := s.recorded[0]
	if !errors.Is(got.err, boom) {
		t.Errorf("записана ошибка %v, ожидалась оборачивающая панику %v", got.err, boom)
	}
	if !got.stackTrace {
		t.Error("у паники не записан стек — место паники потеряно")
	}
	if !hasBool(got.attrs, "exception.escaped", true) {
		t.Errorf("паника не отмечена вылетевшей за спан: %v", got.attrs)
	}
}

func TestEndOnANonErrorPanicRecordsItsValue(t *testing.T) {
	s := newRecordingSpan()

	if got := panicking(s, "строкой"); got != "строкой" {
		t.Fatalf("дальше пролетело %v, ожидалась та же паника", got)
	}
	if s.statusCode != codes.Error || s.statusDesc != "panic: строкой" {
		t.Errorf("статус %v %q, ожидались Error и \"panic: строкой\"", s.statusCode, s.statusDesc)
	}
	if len(s.recorded) != 1 || s.recorded[0].err.Error() != "panic: строкой" {
		t.Errorf("записано %v, ожидалась одна ошибка \"panic: строкой\"", s.recorded)
	}
}

// TestEndWithoutAPanicMarksNoPanic — близнец двух предыдущих: та же обёртка,
// та же ошибка, паники нет — и отметки паники нет. Без него пробы выше были
// бы зелёными и у обёртки, отмечающей панику всегда.
func TestEndWithoutAPanicMarksNoPanic(t *testing.T) {
	s := newRecordingSpan()
	err := errors.New("обычная")
	func() { defer otelx.End(s, &err) }()

	if len(s.recorded) != 1 || hasBool(s.recorded[0].attrs, "exception.escaped", true) {
		t.Errorf("без паники ошибка отмечена вылетевшей: %v", s.recorded)
	}
}

func hasBool(attrs []attribute.KeyValue, key string, want bool) bool {
	for _, a := range attrs {
		if string(a.Key) == key && a.Value.Type() == attribute.BOOL && a.Value.AsBool() == want {
			return true
		}
	}
	return false
}
