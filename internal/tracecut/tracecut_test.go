// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// tracecut_test.go — доказательство того, что разбор различает ЗАКРЫТО и
// ЧЕМ ЗАКРЫТО, а не ловит знак.
//
// Без этой половины «ноль находок» у проверки состава означало бы и «вырез
// цел», и «разбор ничего не увидел». Каждый мир ниже отличается от законного
// близнеца РОВНО ОДНИМ фактом.
package tracecut_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/internal/tracecut"
)

// lawfulSource — форма, в которой поддерево открывает и закрывает спан:
// открытие с именем-литералом и отложенное закрытие через обёртку фундамента.
// Строка открытия — 11-я, строка закрытия — 12-я: на них опираются координаты
// находок ниже.
const lawfulSource = `package oauth2

import (
	"context"

	"github.com/PRO-Robotech/corelib/internal/otelx"
	"go.opentelemetry.io/otel/trace"
)

func (f *Fosite) NewAccessRequest(ctx context.Context) (err error) {
	ctx, span := trace.SpanFromContext(ctx).TracerProvider().Tracer("engine").Start(ctx, "Fosite.NewAccessRequest")
	defer otelx.End(span, &err)
	_ = ctx
	return nil
}
`

// mutate возвращает законный исходник с ОДНОЙ заменой; замена, не нашедшая
// своего места, роняет пробу — иначе мир совпал бы с близнецом молча.
func mutate(t *testing.T, from, to string) string {
	t.Helper()
	if strings.Count(lawfulSource, from) != 1 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: замена %q встречается в законном исходнике не ровно один раз", from)
	}
	return strings.Replace(lawfulSource, from, to, 1)
}

func scanOne(t *testing.T, name, src string) tracecut.Opening {
	t.Helper()
	found, err := tracecut.ScanSpanOpenings(name, []byte(src))
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: синтетика не разобралась: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("открытий найдено %d, ожидалось 1: %+v", len(found), found)
	}
	return found[0]
}

// TestLawfulSourceIsScannedWhole — ЗАКОННЫЙ БЛИЗНЕЦ и положительный контроль
// сразу: разбор обязан НАЙТИ открытие и назвать всё о нём.
func TestLawfulSourceIsScannedWhole(t *testing.T) {
	t.Parallel()
	o := scanOne(t, "lawful.go", lawfulSource)
	if o.Name != "Fosite.NewAccessRequest" {
		t.Errorf("имя спана %q, ожидалось %q", o.Name, "Fosite.NewAccessRequest")
	}
	if o.Func != "Fosite.NewAccessRequest" {
		t.Errorf("объемлющая функция %q, ожидалась %q", o.Func, "Fosite.NewAccessRequest")
	}
	if o.Line != 11 {
		t.Errorf("строка открытия %d, ожидалась 11 — находку некуда пойти смотреть", o.Line)
	}
	if len(o.Closers) != 1 {
		t.Fatalf("отложенных End найдено %d, ожидался один: %+v", len(o.Closers), o.Closers)
	}
	c := o.Closers[0]
	if want := "github.com/PRO-Robotech/corelib/internal/otelx"; c.Import != want || c.Receiver != "otelx" || c.Line != 12 {
		t.Errorf("закрытие %+v, ожидались путь %q, имя otelx и строка 12", c, want)
	}
}

// TestSpanWithoutDeferredEndIsSeenAsAbandoned — один факт разницы: строки
// `defer otelx.End` нет, а импорт обёртки в файле ОСТАЛСЯ. Разбор, судящий
// импорт вместо отложенного вызова, здесь промолчал бы.
func TestSpanWithoutDeferredEndIsSeenAsAbandoned(t *testing.T) {
	t.Parallel()
	o := scanOne(t, "abandoned.go", mutate(t, "defer otelx.End(span, &err)", "_, _ = span, otelx.End"))
	if o.Closed() {
		t.Errorf("спан открыт и брошен, а разбор считает его закрытым: %+v", o.Closers)
	}
}

// TestUpstreamCloserIsSeenByImportPath — один факт разницы: путь импорта
// закрывающего пакета апстримный. Имя пакета и вызов побайтово те же: в этом
// виде вырез отменяется очередным подъёмом версии, и отличить его можно
// только по строке импорта.
func TestUpstreamCloserIsSeenByImportPath(t *testing.T) {
	t.Parallel()
	o := scanOne(t, "upstream.go", mutate(t, `"github.com/PRO-Robotech/corelib/internal/otelx"`, `"github.com/ory/x/otelx"`))
	if len(o.Closers) != 1 || o.Closers[0].Import != "github.com/ory/x/otelx" {
		t.Errorf("закрытие %+v, ожидался путь github.com/ory/x/otelx — разбор не различает "+
			"одноимённые пакеты по пути импорта", o.Closers)
	}
}

// TestMethodEndHasNoImportPath — один факт разницы: спан закрыт своим же
// методом. Путь импорта у такого закрытия пуст, а имя слева — переменная.
func TestMethodEndHasNoImportPath(t *testing.T) {
	t.Parallel()
	o := scanOne(t, "method.go", mutate(t, "defer otelx.End(span, &err)", "defer span.End()"))
	if len(o.Closers) != 1 || o.Closers[0].Import != "" || o.Closers[0].Receiver != "span" {
		t.Errorf("закрытие %+v, ожидался метод span.End без пути импорта", o.Closers)
	}
}

// TestImportAliasDoesNotHideTheCloser — псевдоним НЕ ОБХОД: вердикт не
// зависит от того, как назвали импорт в месте вызова.
func TestImportAliasDoesNotHideTheCloser(t *testing.T) {
	t.Parallel()
	src := mutate(t, `"github.com/PRO-Robotech/corelib/internal/otelx"`, `tr "github.com/PRO-Robotech/corelib/internal/otelx"`)
	src = strings.Replace(src, "defer otelx.End", "defer tr.End", 1)
	o := scanOne(t, "alias.go", src)
	if want := "github.com/PRO-Robotech/corelib/internal/otelx"; len(o.Closers) != 1 || o.Closers[0].Import != want {
		t.Errorf("закрытие %+v, ожидался путь %q", o.Closers, want)
	}
}

// TestNonLiteralSpanNameIsStillAnOpening — открытие с именем не литералом
// остаётся ОТКРЫТИЕМ: разбор, отбрасывающий его, сделал бы такую форму
// невидимой обеим половинам держателя.
func TestNonLiteralSpanNameIsStillAnOpening(t *testing.T) {
	t.Parallel()
	o := scanOne(t, "nonliteral.go", mutate(t, `Start(ctx, "Fosite.NewAccessRequest")`, `Start(ctx, spanName)`))
	if o.Name != "" {
		t.Errorf("имя спана %q, ожидалось пустое — второй довод не литерал", o.Name)
	}
}

// TestFuncLiteralIsItsOwnScope — отложенный вызов исполняется при выходе из
// СВОЕЙ функции. Спан, открытый во вложенном литерале, отложенным End
// объемлющей функции не закрыт, и наоборот.
func TestFuncLiteralIsItsOwnScope(t *testing.T) {
	t.Parallel()
	const src = `package oauth2

import "github.com/PRO-Robotech/corelib/internal/otelx"

func Outer(tracer T) (err error) {
	defer otelx.End(nil, &err)
	run := func() (err error) {
		_, span := tracer.Start(ctx, "inner")
		_ = span
		return nil
	}
	return run()
}
`
	o := scanOne(t, "funclit.go", src)
	if o.Closed() {
		t.Errorf("спан во вложенном литерале засчитан закрытым отложенным End объемлющей "+
			"функции: %+v", o.Closers)
	}
	if o.Func != "Outer.func1" {
		t.Errorf("область %q, ожидалась Outer.func1", o.Func)
	}
}

// TestFileWithoutSpansGivesAnEmptyAnswer — граница: пусто это ПУСТО, а не отказ.
func TestFileWithoutSpansGivesAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	found, err := tracecut.ScanSpanOpenings("empty.go", []byte("package oauth2\n\nfunc Sum(a, b int) int { return a + b }\n"))
	if err != nil {
		t.Fatalf("файл без спанов — законный вход, а разбор отказал: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("в файле без спанов найдено %d открытий: %+v", len(found), found)
	}
}

// TestUnparsableFileIsARefusal — «не прочиталось» не выдаётся за «чисто».
func TestUnparsableFileIsARefusal(t *testing.T) {
	t.Parallel()
	found, err := tracecut.ScanSpanOpenings("broken.go", []byte("package oauth2\n\nfunc ("))
	if err == nil {
		t.Fatalf("битый файл разобрался и дал %d открытий — «ноль находок» стало бы "+
			"неотличимо от чистого файла", len(found))
	}
}
