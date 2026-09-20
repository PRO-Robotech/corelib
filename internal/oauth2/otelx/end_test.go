// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package otelx_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	pkgerrors "github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"

	"github.com/PRO-Robotech/corelib/internal/oauth2/otelx"
)

// recordingSpan — САМОДЕЛЬНЫЙ держатель, а не мок из комплекта: он и есть
// предмет проверки. Комплект реализации OpenTelemetry сюда тащить нельзя — ради
// его отсутствия вырез и делался, — поэтому наблюдаем через интерфейс API.
type recordingSpan struct {
	embedded.Span

	ended      int
	statusCode codes.Code
	statusDesc string
	attrs      map[attribute.Key]attribute.Value
}

func newRecordingSpan() *recordingSpan {
	return &recordingSpan{attrs: map[attribute.Key]attribute.Value{}}
}

func (s *recordingSpan) End(...trace.SpanEndOption)              { s.ended++ }
func (s *recordingSpan) AddEvent(string, ...trace.EventOption)   {}
func (s *recordingSpan) AddLink(trace.Link)                      {}
func (s *recordingSpan) IsRecording() bool                       { return true }
func (s *recordingSpan) RecordError(error, ...trace.EventOption) {}
func (s *recordingSpan) SpanContext() trace.SpanContext          { return trace.SpanContext{} }
func (s *recordingSpan) SetName(string)                          {}
func (s *recordingSpan) TracerProvider() trace.TracerProvider    { return noopProvider{} }
func (s *recordingSpan) SetStatus(code codes.Code, description string) {
	s.statusCode, s.statusDesc = code, description
}

func (s *recordingSpan) SetAttributes(kv ...attribute.KeyValue) {
	for _, a := range kv {
		s.attrs[a.Key] = a.Value
	}
}

func (s *recordingSpan) str(k string) (string, bool) {
	v, ok := s.attrs[attribute.Key(k)]
	if !ok {
		return "", false
	}
	return v.AsString(), true
}

type noopProvider struct{ embedded.TracerProvider }

func (noopProvider) Tracer(string, ...trace.TracerOption) trace.Tracer { return nil }

var _ trace.Span = (*recordingSpan)(nil)

// richError несёт ровно те четыре опциональных интерфейса, которые читает
// апстримный `otelx.setErrorTags`. Если из обёртки выпадет любой из них,
// соответствующий подслучай покраснеет поимённо.
type richError struct{ msg string }

func (e richError) Error() string                   { return e.msg }
func (e richError) Reason() string                  { return "потому что" }
func (e richError) Debug() string                   { return "подробности" }
func (e richError) ID() string                      { return "err-42" }
func (e richError) Details() map[string]interface{} { return map[string]interface{}{"ключ": 7} }

func TestEnd_БезОшибки_СпанЗакрытБезСтатуса(t *testing.T) {
	s := newRecordingSpan()
	var err error
	func() { defer otelx.End(s, &err) }()

	assert.Equal(t, 1, s.ended, "спан обязан закрыться ровно один раз")
	assert.Equal(t, codes.Unset, s.statusCode, "без ошибки статус не выставляется")
	assert.Empty(t, s.attrs, "без ошибки теги не выставляются")
}

func TestEnd_НулевойУказатель_СпанЗакрыт(t *testing.T) {
	s := newRecordingSpan()
	func() { defer otelx.End(s, nil) }()

	assert.Equal(t, 1, s.ended)
	assert.Equal(t, codes.Unset, s.statusCode)
}

func TestEnd_ПростаяОшибка_СтатусИТриТега(t *testing.T) {
	s := newRecordingSpan()
	inner := errors.New("внутренняя")
	err := fmt.Errorf("внешняя: %w", inner)
	func() { defer otelx.End(s, &err) }()

	assert.Equal(t, 1, s.ended)
	assert.Equal(t, codes.Error, s.statusCode)
	assert.Equal(t, "внешняя: внутренняя", s.statusDesc)

	v, ok := s.str("error")
	require.True(t, ok, "тег error обязателен")
	assert.Equal(t, "внешняя: внутренняя", v)

	v, ok = s.str("error.message")
	require.True(t, ok, "тег error.message обязателен (совместимость с Datadog)")
	assert.Equal(t, "внешняя: внутренняя", v)

	v, ok = s.str("error.type")
	require.True(t, ok, "тег error.type обязателен")
	assert.Equal(t, fmt.Sprintf("%T", inner), v, "тип берётся у РАЗВЁРНУТОЙ ошибки, а не у внешней")
}

func TestEnd_ОшибкаСоСтеком_ТегErrorStack(t *testing.T) {
	s := newRecordingSpan()
	err := pkgerrors.New("со стеком")
	func() { defer otelx.End(s, &err) }()

	v, ok := s.str("error.stack")
	require.True(t, ok, "у ошибки с методом StackTrace обязан появиться error.stack")
	assert.Contains(t, v, "end_test.go", "стек обязан указывать на место возникновения")
}

func TestEnd_ОшибкаСРасширениями_ЧетыреТегаИДетали(t *testing.T) {
	s := newRecordingSpan()
	var err error = richError{msg: "богатая"}
	func() { defer otelx.End(s, &err) }()

	for k, want := range map[string]string{
		"error.reason":       "потому что",
		"error.debug":        "подробности",
		"error.id":           "err-42",
		"error.details.ключ": "7",
	} {
		v, ok := s.str(k)
		require.True(t, ok, "тег %s обязателен", k)
		assert.Equal(t, want, v, "тег %s", k)
	}
}

func TestEnd_ПаникаОшибкой_СтатусТегиИПробросДальше(t *testing.T) {
	s := newRecordingSpan()
	boom := errors.New("бум")

	recovered := func() (r any) {
		defer func() { r = recover() }()
		func() {
			var err error
			defer otelx.End(s, &err)
			panic(boom)
		}()
		return nil
	}()

	require.Equal(t, boom, recovered, "паника обязана пролететь дальше НЕИЗМЕНЁННОЙ")
	assert.Equal(t, 1, s.ended, "спан обязан закрыться и на панике")
	assert.Equal(t, codes.Error, s.statusCode)
	assert.Equal(t, "panic: бум", s.statusDesc)

	esc, ok := s.attrs[attribute.Key("exception.escaped")]
	require.True(t, ok, "тег exception.escaped обязателен")
	assert.True(t, esc.AsBool())

	typ, ok := s.str("exception.type")
	require.True(t, ok, "тег exception.type обязателен")
	assert.Equal(t, "*errors.errorString", typ)

	stack, ok := s.str("error.stack")
	require.True(t, ok, "тег error.stack обязателен на панике")
	assert.True(t, strings.Contains(stack, "end_test.go"), "стек обязан указывать на место паники, получено: %q", stack)

	msg, ok := s.str("error")
	require.True(t, ok, "на панике ошибкой выставляются и обычные теги ошибки")
	assert.Equal(t, "бум", msg)
}

func TestEnd_ПаникаСтрокой_СтатусИПробросДальше(t *testing.T) {
	s := newRecordingSpan()

	recovered := func() (r any) {
		defer func() { r = recover() }()
		func() {
			var err error
			defer otelx.End(s, &err)
			panic("строкой")
		}()
		return nil
	}()

	require.Equal(t, "строкой", recovered)
	assert.Equal(t, 1, s.ended)
	assert.Equal(t, codes.Error, s.statusCode)
	assert.Equal(t, "panic: строкой", s.statusDesc)

	typ, ok := s.str("exception.type")
	require.True(t, ok)
	assert.Equal(t, "string", typ)
}

func TestEnd_ПаникаПрочимЗначением_СтатусБезРасшифровки(t *testing.T) {
	s := newRecordingSpan()

	recovered := func() (r any) {
		defer func() { r = recover() }()
		func() {
			var err error
			defer otelx.End(s, &err)
			panic(struct{ X int }{X: 1})
		}()
		return nil
	}()

	require.Equal(t, struct{ X int }{X: 1}, recovered)
	assert.Equal(t, codes.Error, s.statusCode)
	assert.Equal(t, "panic", s.statusDesc)
}

// Контроль к предыдущим: тот же держатель, та же обёртка, НО паники нет —
// значит теги паники появляться не имеют права. Без этого случая проверки выше
// были бы зелёными и у обёртки, которая ставит exception.* всегда.
func TestEnd_БезПаники_ТеговПаникиНет(t *testing.T) {
	s := newRecordingSpan()
	err := errors.New("обычная")
	func() { defer otelx.End(s, &err) }()

	_, ok := s.attrs[attribute.Key("exception.escaped")]
	assert.False(t, ok, "без паники exception.escaped выставляться не должен")
	_, ok = s.attrs[attribute.Key("exception.type")]
	assert.False(t, ok, "без паники exception.type выставляться не должен")
}
