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
	"testing"

	"github.com/PRO-Robotech/corelib/internal/tracecut"
)

// законныйИсходник — форма, в которой поддерево открывает и закрывает спан:
// открытие с именем-литералом и отложенное закрытие через обёртку фундамента.
const законныйИсходник = `package oauth2

import (
	"context"

	"github.com/PRO-Robotech/corelib/internal/otelx"
	"go.opentelemetry.io/otel/trace"
)

func (f *Fosite) NewAccessRequest(ctx context.Context) (err error) {
	ctx, span := trace.SpanFromContext(ctx).TracerProvider().Tracer("движок").Start(ctx, "Fosite.NewAccessRequest")
	defer otelx.End(span, &err)
	_ = ctx
	return nil
}
`

// TestЗаконныйИсходникРазобранЦеликом — ЗАКОННЫЙ БЛИЗНЕЦ и положительный
// контроль сразу: разбор обязан НАЙТИ открытие и назвать всё о нём.
//
// Если бы он молчал здесь, ноль находок на инъекциях ничего бы не значил.
func TestЗаконныйИсходникРазобранЦеликом(t *testing.T) {
	t.Parallel()
	found, err := tracecut.ScanSpanOpenings("законный.go", []byte(законныйИсходник))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: законная синтетика не разобралась: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("открытий найдено %d, ожидалось 1: %+v", len(found), found)
	}
	o := found[0]
	if o.Name != "Fosite.NewAccessRequest" {
		t.Errorf("имя спана %q, ожидалось %q", o.Name, "Fosite.NewAccessRequest")
	}
	if o.Func != "Fosite.NewAccessRequest" {
		t.Errorf("объемлющая функция %q, ожидалась %q", o.Func, "Fosite.NewAccessRequest")
	}
	if !o.Closed() {
		t.Error("законный исходник закрывает спан, а разбор считает его брошенным")
	}
	if want := "github.com/PRO-Robotech/corelib/internal/otelx"; o.CloserImport != want {
		t.Errorf("путь закрывающего пакета %q, ожидался %q", o.CloserImport, want)
	}
	if o.Line == 0 {
		t.Error("координата открытия не проставлена — находку некуда пойти смотреть")
	}
}

// TestСпанБезОтложенногоЗакрытияВиденКакБрошенный — ИНЪЕКЦИЯ ПЕРВАЯ.
//
// Один факт разницы против законного близнеца: строка `defer otelx.End` убрана.
// Всё остальное — тот же файл, тот же импорт, то же открытие.
func TestСпанБезОтложенногоЗакрытияВиденКакБрошенный(t *testing.T) {
	t.Parallel()
	const брошенный = `package oauth2

import (
	"context"

	"github.com/PRO-Robotech/corelib/internal/otelx"
	"go.opentelemetry.io/otel/trace"
)

func (f *Fosite) NewAccessRequest(ctx context.Context) (err error) {
	ctx, span := trace.SpanFromContext(ctx).TracerProvider().Tracer("движок").Start(ctx, "Fosite.NewAccessRequest")
	_, _ = span, otelx.End
	_ = ctx
	return nil
}
`
	found, err := tracecut.ScanSpanOpenings("брошенный.go", []byte(брошенный))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("открытий найдено %d, ожидалось 1: %+v", len(found), found)
	}
	if found[0].Closed() {
		t.Errorf("спан открыт и брошен, а разбор считает его закрытым через %q. "+
			"Импорт обёртки в файле ОСТАЛСЯ — значит разбор судит импорт, а не "+
			"отложенный вызов", found[0].CloserImport)
	}
}

// TestСпанЗакрытыйОбвязкойАпстримаВиденПоПутиИмпорта — ИНЪЕКЦИЯ ВТОРАЯ.
//
// Один факт разницы против законного близнеца: путь импорта закрывающего
// пакета — апстримный. ИМЯ пакета то же самое (`otelx`), вызов побайтово тот
// же (`otelx.End(span, &err)`), отложен он так же. Это и есть вид, в котором
// вырез телеметрии отменяется очередным подъёмом версии: по тексту вызова
// отличить нельзя НИЧЕМ, кроме строки импорта.
func TestСпанЗакрытыйОбвязкойАпстримаВиденПоПутиИмпорта(t *testing.T) {
	t.Parallel()
	const апстримный = `package oauth2

import (
	"context"

	"github.com/ory/x/otelx"
	"go.opentelemetry.io/otel/trace"
)

func (f *Fosite) NewAccessRequest(ctx context.Context) (err error) {
	ctx, span := trace.SpanFromContext(ctx).TracerProvider().Tracer("движок").Start(ctx, "Fosite.NewAccessRequest")
	defer otelx.End(span, &err)
	_ = ctx
	return nil
}
`
	found, err := tracecut.ScanSpanOpenings("апстримный.go", []byte(апстримный))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("открытий найдено %d, ожидалось 1: %+v", len(found), found)
	}
	if !found[0].Closed() {
		t.Fatal("спан закрыт отложенным вызовом, а разбор считает его брошенным")
	}
	if want := "github.com/ory/x/otelx"; found[0].CloserImport != want {
		t.Errorf("путь закрывающего пакета %q, ожидался %q — разбор не различает "+
			"одноимённые пакеты по пути импорта, а на этом стоит вся проверка выреза",
			found[0].CloserImport, want)
	}
}

// TestПсевдонимИмпортаНеПрячетЗакрывающийПакет — псевдоним НЕ ОБХОД.
//
// Один факт разницы против законного близнеца: обёртка импортирована под
// другим именем. Разбор обязан вернуть тот же ПУТЬ: вердикт о вырезе не должен
// зависеть от того, как назвали импорт в месте вызова.
func TestПсевдонимИмпортаНеПрячетЗакрывающийПакет(t *testing.T) {
	t.Parallel()
	const сПсевдонимом = `package oauth2

import (
	"context"

	трасса "github.com/PRO-Robotech/corelib/internal/otelx"
	"go.opentelemetry.io/otel/trace"
)

func (f *Fosite) NewAccessRequest(ctx context.Context) (err error) {
	ctx, span := trace.SpanFromContext(ctx).TracerProvider().Tracer("движок").Start(ctx, "Fosite.NewAccessRequest")
	defer трасса.End(span, &err)
	_ = ctx
	return nil
}
`
	found, err := tracecut.ScanSpanOpenings("псевдоним.go", []byte(сПсевдонимом))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("открытий найдено %d, ожидалось 1: %+v", len(found), found)
	}
	if want := "github.com/PRO-Robotech/corelib/internal/otelx"; found[0].CloserImport != want {
		t.Errorf("путь закрывающего пакета %q, ожидался %q", found[0].CloserImport, want)
	}
}

// TestФайлБезСпановДаётПустойОтвет — граница: пусто это ПУСТО, а не отказ.
func TestФайлБезСпановДаётПустойОтвет(t *testing.T) {
	t.Parallel()
	const безСпанов = `package oauth2

func Сумма(a, b int) int { return a + b }
`
	found, err := tracecut.ScanSpanOpenings("пусто.go", []byte(безСпанов))
	if err != nil {
		t.Fatalf("файл без спанов — законный вход, а разбор отказал: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("в файле без спанов найдено %d открытий: %+v", len(found), found)
	}
}

// TestНеразобравшийсяФайлЭтоОтказ — «не прочиталось» не выдаётся за «чисто».
func TestНеразобравшийсяФайлЭтоОтказ(t *testing.T) {
	t.Parallel()
	found, err := tracecut.ScanSpanOpenings("битый.go", []byte("package oauth2\n\nfunc ("))
	if err == nil {
		t.Fatalf("битый файл разобрался и дал %d открытий — «ноль находок» стало бы "+
			"неотличимо от чистого файла", len(found))
	}
}
