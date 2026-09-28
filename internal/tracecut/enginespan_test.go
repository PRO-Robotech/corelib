// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// enginespan_test.go — ПОВЕДЕНЧЕСКАЯ половина держателя выреза телеметрии.
//
// Под живым провайдером трассировки гоняется КАЖДЫЙ путь движка, на котором
// апстрим открывал спан, и спрашивается не «какой ответ отдал движок», а
// «появился ли спан и что он несёт».
//
// Разница с internal/otelx/end_test.go названа, а не умолчана. Там обёртке
// подаётся спан ПРЯМО В РУКИ и судится она одна. Здесь спана никто не подаёт:
// его обязан открыть сам движок, а обёртка — получить и закрыть. Пустышкой
// может оказаться не только обёртка, но и связка; эта проба ловит оба
// случая, та — только первый.
//
// Судится то, чем исход отмечает ТЕКУЩАЯ обёртка: ошибка — одним событием
// ошибки (RecordError) и статусом Error с её текстом; успех — ни тем, ни
// другим. Набора тегов апстрима у обёртки нет (internal/otelx/end.go), и
// проба его не требует.
package tracecut_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	fosite "github.com/PRO-Robotech/corelib/internal/oauth2"
	"github.com/PRO-Robotech/corelib/internal/oauth2/storage"
	"github.com/PRO-Robotech/corelib/internal/otelx"
)

// engineScope — имя области инструментирования, которым движок зовёт
// трассировщик. Вместе со спаном исчезло бы и оно.
const engineScope = "github.com/PRO-Robotech/corelib/internal/oauth2"

// enginePath — один путь, на котором апстрим открывает спан.
//
// Полноту перечня сверяет coverage_test.go с фактическим составом поддерева;
// здесь перечень — только способ добраться до каждого пути.
type enginePath struct {
	// span — имя, под которым путь обязан открыть спан.
	span string

	// failing — путь обязан завершиться ошибкой. Путь без ошибки судится
	// второй стороной контракта обёртки: спан закрыт, отметок ошибки нет.
	failing bool

	// drive доводит движок до места, где спан открывается, и возвращает
	// полученную ошибку.
	drive func(ctx context.Context, f *fosite.Fosite) error
}

func postRequest(form url.Values) *http.Request {
	r, err := http.NewRequest(http.MethodPost, "https://foundation.invalid/", strings.NewReader(form.Encode()))
	if err != nil {
		panic("НЕ ВЫПОЛНИЛОСЬ: не собрался POST-запрос: " + err.Error())
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func getRequest(q url.Values) *http.Request {
	r, err := http.NewRequest(http.MethodGet, "https://foundation.invalid/?"+q.Encode(), nil)
	if err != nil {
		panic("НЕ ВЫПОЛНИЛОСЬ: не собрался GET-запрос: " + err.Error())
	}
	return r
}

// enginePaths — все пути движка, открывающие спан.
//
// Доводы подобраны так, чтобы путь дошёл до открытия спана и упёрся в первую
// же проверку: предмет здесь — спан, а не успешный сценарий OAuth2.
func enginePaths() []enginePath {
	return []enginePath{
		{
			span: "Fosite.NewPushedAuthorizeRequest", failing: true,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewPushedAuthorizeRequest(ctx, postRequest(url.Values{}))
				return err
			},
		},
		{
			span: "Fosite.NewRevocationRequest", failing: true,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				return f.NewRevocationRequest(ctx, postRequest(url.Values{}))
			},
		},
		{
			span: "Fosite.NewAccessRequest", failing: true,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewAccessRequest(ctx, postRequest(url.Values{}), new(fosite.DefaultSession))
				return err
			},
		},
		{
			span: "Fosite.NewAuthorizeRequest", failing: true,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewAuthorizeRequest(ctx, getRequest(url.Values{}))
				return err
			},
		},
		{
			span: "Fosite.NewIntrospectionRequest", failing: true,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewIntrospectionRequest(ctx, postRequest(url.Values{}), new(fosite.DefaultSession))
				return err
			},
		},
		{
			span: "Fosite.IntrospectToken", failing: true,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				_, _, err := f.IntrospectToken(ctx, "token", fosite.AccessToken, new(fosite.DefaultSession))
				return err
			},
		},
		{
			span: "Fosite.NewAuthorizeResponse", failing: true,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewAuthorizeResponse(ctx, fosite.NewAuthorizeRequest(), new(fosite.DefaultSession))
				return err
			},
		},
		{
			span: "Fosite.NewAccessResponse", failing: true,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewAccessResponse(ctx, fosite.NewAccessRequest(new(fosite.DefaultSession)))
				return err
			},
		},
		{
			// ЗАКОННЫЙ БЛИЗНЕЦ остальных восьми: тот же движок, тот же
			// провайдер, один факт разницы — путь не возвращает ошибки. Без
			// него проба не отличала бы «обёртка отмечает ошибку по делу» от
			// «обёртка отмечает всё подряд».
			span: "Fosite.NewPushedAuthorizeResponse", failing: false,
			drive: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewPushedAuthorizeResponse(ctx, fosite.NewAuthorizeRequest(), new(fosite.DefaultSession))
				return err
			},
		},
	}
}

// engine собирает провайдер OAuth2 на памяти и пустой настройке: предмет пробы
// лежит до всякой настройки.
func engine() *fosite.Fosite {
	return fosite.NewOAuth2Provider(storage.NewMemoryStore(), new(fosite.Config))
}

// spanProblems — суд над спаном одного пути: что не так со спаном span,
// если путь вернул err. Пустой ответ — спан создан движком, закрыт ровно
// однажды и отмечен ровно так, как велит исход.
func spanProblems(p *recordingProvider, span string, err error) []string {
	spans := p.byName(span)
	if len(spans) != 1 {
		return []string{fmt.Sprintf("ТРАССИРОВКА ИСЧЕЗЛА: спанов %q открыто %d, ожидался один; "+
			"открыто за прогон: %q", span, len(spans), p.started())}
	}
	s := spans[0]
	var out []string
	if n := s.endedCount(); n != 1 {
		out = append(out, fmt.Sprintf("спан закрыт %d раз, ожидался ровно один", n))
	}
	if s.scope != engineScope {
		out = append(out, fmt.Sprintf("спан открыт из области %q, ожидалась %q", s.scope, engineScope))
	}
	code, desc := s.status()
	recorded := s.recordedErrors()
	if err == nil {
		if code != codes.Unset || len(recorded) != 0 {
			out = append(out, fmt.Sprintf("путь прошёл без ошибки, а спан отмечен: статус %v %q, "+
				"событий ошибки %d", code, desc, len(recorded)))
		}
		return out
	}
	if code != codes.Error || desc != err.Error() {
		out = append(out, fmt.Sprintf("статус %v %q, а путь вернул ошибку %q — отказ движка не "+
			"виден в трассировке", code, desc, err.Error()))
	}
	switch {
	case len(recorded) != 1:
		out = append(out, fmt.Sprintf("событий ошибки %d, ожидалось одно", len(recorded)))
	case !errors.Is(recorded[0], err):
		out = append(out, fmt.Sprintf("событие ошибки несёт %q, а путь вернул %q", recorded[0], err))
	}
	return out
}

// TestEveryEnginePathOpensClosesAndMarksItsSpan — спан ЕСТЬ и НЕСЁТ ИСХОД.
//
// Вердикт берётся не с ответа движка, а с провайдера: пустая обёртка тем и
// опасна, что движок ведёт себя ровно так же.
func TestEveryEnginePathOpensClosesAndMarksItsSpan(t *testing.T) {
	t.Parallel()
	var failing, succeeding int
	for _, path := range enginePaths() {
		if path.failing {
			failing++
		} else {
			succeeding++
		}
		t.Run(path.span, func(t *testing.T) {
			t.Parallel()
			p := &recordingProvider{}
			err := path.drive(rootContext(p), engine())
			if path.failing != (err != nil) {
				t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: путь %s вернул %v при ожидании «ошибка: %t» — гнали не тот путь",
					path.span, err, path.failing)
			}
			for _, problem := range spanProblems(p, path.span, err) {
				t.Errorf("%s: %s", path.span, problem)
			}
		})
	}
	if failing == 0 || succeeding == 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: путей с ошибкой %d, без ошибки %d — одна из сторон контракта "+
			"обёртки не судится", failing, succeeding)
	}
}

// syntheticPath — путь, устроенный как путь движка: спан из провайдера
// контекста под областью движка, исход — err. closeSpan закрывает спан так,
// как велит мир.
func syntheticPath(ctx context.Context, fail bool, closeSpan func(span trace.Span, err *error)) (err error) {
	ctx, span := trace.SpanFromContext(ctx).TracerProvider().Tracer(engineScope).Start(ctx, "synthetic")
	defer closeSpan(span, &err)
	_ = ctx
	if fail {
		return errors.New("отказ синтетического пути")
	}
	return nil
}

// TestSpanJudgeFailsOnASpanClosedPastTheWrapper — суд над спаном способен
// упасть и способен смолчать.
//
// Законный близнец закрывает спан обёрткой фундамента и обязан дать ноль.
// Инъекции отличаются от него одним фактом каждая и обязаны дать отказ.
func TestSpanJudgeFailsOnASpanClosedPastTheWrapper(t *testing.T) {
	t.Parallel()
	worlds := []struct {
		name      string
		fail      bool
		closeSpan func(span trace.Span, err *error)
		wantRed   string
	}{
		{name: "обёртка, ошибка", fail: true, closeSpan: otelx.End},
		{name: "обёртка, успех", fail: false, closeSpan: otelx.End},
		{
			name: "метод End мимо обёртки", fail: true, wantRed: "не виден в трассировке",
			closeSpan: func(span trace.Span, _ *error) { span.End() },
		},
		{
			name: "спан не закрыт", fail: true, wantRed: "закрыт 0 раз",
			closeSpan: func(trace.Span, *error) {},
		},
		{
			name: "закрыт дважды", fail: true, wantRed: "закрыт 2 раз",
			closeSpan: func(span trace.Span, err *error) { span.End(); otelx.End(span, err) },
		},
	}
	for _, w := range worlds {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()
			p := &recordingProvider{}
			err := syntheticPath(rootContext(p), w.fail, w.closeSpan)
			problems := strings.Join(spanProblems(p, "synthetic", err), "; ")
			if w.wantRed == "" && problems != "" {
				t.Errorf("законный близнец дал отказ: %s", problems)
			}
			if w.wantRed != "" && !strings.Contains(problems, w.wantRed) {
				t.Errorf("инъекция не дала отказа %q; суд сказал: %q", w.wantRed, problems)
			}
		})
	}

	p := &recordingProvider{}
	if got := strings.Join(spanProblems(p, "synthetic", nil), "; "); !strings.Contains(got, "ТРАССИРОВКА ИСЧЕЗЛА") {
		t.Errorf("спан не открывался вовсе, а суд сказал: %q", got)
	}
}

// TestWithoutProviderInContextNoSpansOpen — ГРАНИЦА, а не дефект, и
// положительный контроль остальных: спаны появляются ИЗ-ЗА подставленного
// провайдера, а не сами по себе.
func TestWithoutProviderInContextNoSpansOpen(t *testing.T) {
	t.Parallel()
	p := &recordingProvider{}
	_, err := engine().NewAccessRequest(context.Background(), postRequest(url.Values{}), new(fosite.DefaultSession))
	if err == nil {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: путь обязан был упереться в ошибку")
	}
	if got := p.started(); len(got) != 0 {
		t.Errorf("провайдер не подставлялся в контекст, а спаны на нём открылись: %v", got)
	}
}
