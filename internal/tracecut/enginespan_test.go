// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// enginespan_test.go — ПОВЕДЕНЧЕСКАЯ половина держателя выреза телеметрии.
//
// Предмет и устройство пары — в документации пакета [tracecut]; здесь они не
// пересказываются. Здесь — опыт: под живым провайдером трассировки гоняется
// КАЖДЫЙ путь движка, на котором апстрим открывал спан, и спрашивается не
// «какой ответ отдал движок», а «появился ли спан и что он несёт».
//
// Разница с internal/otelx/end_test.go названа, а не умолчана. Там обёртке
// подаётся спан ПРЯМО В РУКИ и судится она одна. Здесь спана никто не подаёт:
// его обязан открыть сам движок, а обёртка — получить и закрыть. Пустышкой
// может оказаться не только обёртка, но и связка; вторая проба ловит оба
// случая, первая — только первый.
package tracecut_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"

	fosite "github.com/PRO-Robotech/corelib/internal/oauth2"
	"github.com/PRO-Robotech/corelib/internal/oauth2/storage"
)

// scopeДвижка — имя области инструментирования, которым движок зовёт
// трассировщик. Оно апстримное с точностью до переписанного пути импорта и
// названо здесь, потому что вместе со спаном исчезло бы и оно.
const scopeДвижка = "github.com/PRO-Robotech/corelib/internal/oauth2"

// путьДвижка — один путь, на котором апстрим открывает спан.
//
// Перечень этих путей НЕ является предметом суда сам по себе: его полноту
// сверяет coverage_test.go с фактическим составом поддерева. Здесь перечень —
// только способ добраться до каждого пути.
type путьДвижка struct {
	// спан — имя, под которым путь обязан открыть спан.
	спан string

	// ошибочный — путь обязан завершиться ошибкой. Пути без ошибки судятся
	// второй половиной контракта обёртки: спан закрыт, статуса ошибки нет.
	ошибочный bool

	// гнать доводит движок до места, где спан открывается, и возвращает
	// полученную ошибку.
	гнать func(ctx context.Context, f *fosite.Fosite) error
}

func постЗапрос(form url.Values) *http.Request {
	r, err := http.NewRequest(http.MethodPost, "https://фундамент.invalid/", strings.NewReader(form.Encode()))
	if err != nil {
		panic("проба НЕ ИСПОЛНЯЛАСЬ: не собрался POST-запрос: " + err.Error())
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func гетЗапрос(q url.Values) *http.Request {
	r, err := http.NewRequest(http.MethodGet, "https://фундамент.invalid/?"+q.Encode(), nil)
	if err != nil {
		panic("проба НЕ ИСПОЛНЯЛАСЬ: не собрался GET-запрос: " + err.Error())
	}
	return r
}

// путиДвижка — все пути движка, открывающие спан.
//
// Доводы подобраны так, чтобы путь дошёл до открытия спана и упёрся в первую
// же проверку: предмет здесь — спан, а не успешный сценарий OAuth2, и чем
// короче путь до вердикта, тем меньше у пробы поводов покраснеть не по делу.
func путиДвижка() []путьДвижка {
	return []путьДвижка{
		{
			спан: "Fosite.NewPushedAuthorizeRequest", ошибочный: true,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewPushedAuthorizeRequest(ctx, постЗапрос(url.Values{}))
				return err
			},
		},
		{
			спан: "Fosite.NewRevocationRequest", ошибочный: true,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				return f.NewRevocationRequest(ctx, постЗапрос(url.Values{}))
			},
		},
		{
			спан: "Fosite.NewAccessRequest", ошибочный: true,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewAccessRequest(ctx, постЗапрос(url.Values{}), new(fosite.DefaultSession))
				return err
			},
		},
		{
			спан: "Fosite.NewAuthorizeRequest", ошибочный: true,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewAuthorizeRequest(ctx, гетЗапрос(url.Values{}))
				return err
			},
		},
		{
			спан: "Fosite.NewIntrospectionRequest", ошибочный: true,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewIntrospectionRequest(ctx, постЗапрос(url.Values{}), new(fosite.DefaultSession))
				return err
			},
		},
		{
			спан: "Fosite.IntrospectToken", ошибочный: true,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				_, _, err := f.IntrospectToken(ctx, "токен", fosite.AccessToken, new(fosite.DefaultSession))
				return err
			},
		},
		{
			спан: "Fosite.NewAuthorizeResponse", ошибочный: true,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewAuthorizeResponse(ctx, fosite.NewAuthorizeRequest(), new(fosite.DefaultSession))
				return err
			},
		},
		{
			спан: "Fosite.NewAccessResponse", ошибочный: true,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewAccessResponse(ctx, fosite.NewAccessRequest(new(fosite.DefaultSession)))
				return err
			},
		},
		{
			// ЗАКОННЫЙ БЛИЗНЕЦ остальных восьми: тот же движок, тот же
			// провайдер, один факт разницы — путь не возвращает ошибки.
			// Спан обязан быть ТОТ ЖЕ, а статуса ошибки на нём быть не
			// обязано. Без этого случая проба не отличала бы «обёртка ставит
			// статус ошибки по делу» от «обёртка красит всё подряд».
			спан: "Fosite.NewPushedAuthorizeResponse", ошибочный: false,
			гнать: func(ctx context.Context, f *fosite.Fosite) error {
				_, err := f.NewPushedAuthorizeResponse(ctx, fosite.NewAuthorizeRequest(), new(fosite.DefaultSession))
				return err
			},
		},
	}
}

// движок собирает провайдер OAuth2 на памяти и пустой настройке: предмет пробы
// лежит до всякой настройки.
func движок() *fosite.Fosite {
	return fosite.NewOAuth2Provider(storage.NewMemoryStore(), new(fosite.Config))
}

// TestКаждыйПутьДвижкаОткрываетСпанИЗакрываетЕго — спан ЕСТЬ.
//
// Пустая обёртка тем и опасна, что движок ведёт себя ровно так же. Поэтому
// вердикт берётся не с ответа движка, а с провайдера: ровно один спан с
// ожидаемым именем, открытый из области инструментирования движка и закрытый
// ровно один раз.
func TestКаждыйПутьДвижкаОткрываетСпанИЗакрываетЕго(t *testing.T) {
	t.Parallel()
	for _, путь := range путиДвижка() {
		t.Run(путь.спан, func(t *testing.T) {
			t.Parallel()
			провайдер := newRecordingProvider()
			err := путь.гнать(rootContext(провайдер), движок())

			if путь.ошибочный && err == nil {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: путь %s обязан был упереться в ошибку, "+
					"а вернул nil — значит гнали не тот путь", путь.спан)
			}
			if !путь.ошибочный && err != nil {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: путь %s обязан был пройти без ошибки, "+
					"а вернул %v", путь.спан, err)
			}

			спаны := провайдер.byName(путь.спан)
			if len(спаны) == 0 {
				t.Fatalf("ТРАССИРОВКА ИСЧЕЗЛА: путь %s пройден, а спан не создан. "+
					"Открыто за прогон: %v", путь.спан, провайдер.started())
			}
			if len(спаны) != 1 {
				t.Fatalf("путь %s открыл %d спанов с этим именем, ожидался один: %v",
					путь.спан, len(спаны), провайдер.started())
			}
			if n := спаны[0].endedCount(); n != 1 {
				t.Errorf("спан %s закрыт %d раз(а), ожидался ровно один: обёртка "+
					"обязана закрывать спан и ровно однажды", путь.спан, n)
			}
			if scope, ok := провайдер.scopeOf(путь.спан); !ok || scope != scopeДвижка {
				t.Errorf("спан %s открыт из области %q, ожидалась %q",
					путь.спан, scope, scopeДвижка)
			}
		})
	}
}

// TestОшибочныйПутьКраситСпанСтатусомИТегами — спан НЕСЁТ ТЕ ЖЕ ПРИЗНАКИ.
//
// Спан, который создан и закрыт, но не несёт ни статуса, ни тегов, — это
// половина пустышки: трассировка формально жива, а разобрать по ней отказ
// нельзя. Поэтому судится не факт спана, а его содержимое.
//
// Теги `error` и `error.message` сверяются ПО ЗНАЧЕНИЮ с текстом ошибки,
// которую вернул сам путь: совпадение случайным быть не может.
func TestОшибочныйПутьКраситСпанСтатусомИТегами(t *testing.T) {
	t.Parallel()
	for _, путь := range путиДвижка() {
		if !путь.ошибочный {
			continue
		}
		t.Run(путь.спан, func(t *testing.T) {
			t.Parallel()
			провайдер := newRecordingProvider()
			err := путь.гнать(rootContext(провайдер), движок())
			if err == nil {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: путь %s не дал ошибки", путь.спан)
			}
			спаны := провайдер.byName(путь.спан)
			if len(спаны) != 1 {
				t.Fatalf("ТРАССИРОВКА ИСЧЕЗЛА: путь %s дал ошибку %v, а спанов с этим "+
					"именем %d", путь.спан, err, len(спаны))
			}
			спан := спаны[0]

			code, desc := спан.status()
			if code != codes.Error {
				t.Errorf("спан %s: статус %v, ожидался %v — отказ движка не виден "+
					"в трассировке", путь.спан, code, codes.Error)
			}
			if desc != err.Error() {
				t.Errorf("спан %s: описание статуса %q, а путь вернул ошибку %q",
					путь.спан, desc, err.Error())
			}

			// Три тега обёртка ставит на ЛЮБОЙ ошибке, без условий.
			for тег, ожидание := range map[string]string{
				"error":         err.Error(),
				"error.message": err.Error(),
			} {
				got, ok := спан.attr(тег)
				if !ok {
					t.Errorf("спан %s: тега %q нет", путь.спан, тег)
					continue
				}
				if got != ожидание {
					t.Errorf("спан %s: тег %q = %q, ожидалось %q", путь.спан, тег, got, ожидание)
				}
			}
			if got, ok := спан.attr("error.type"); !ok || got == "" {
				t.Errorf("спан %s: тег error.type пуст или отсутствует", путь.спан)
			}

			// Тег стека обёртка ставит по опциональному интерфейсу ошибки.
			// Все отказы движка приезжают через errorsx.WithStack и стек
			// несут; выпадение этой ветви из обёртки было бы потерей
			// признака, а не сменой сорта ошибки.
			if got, ok := спан.attr("error.stack"); !ok || got == "" {
				t.Errorf("спан %s: тег error.stack пуст или отсутствует — обёртка "+
					"перестала читать стек ошибки", путь.спан)
			}
		})
	}
}

// TestБезошибочныйПутьНеКраситСпан — ЗАКОННЫЙ БЛИЗНЕЦ ошибочных путей.
//
// Один факт разницы против них: путь возвращает nil. Спан обязан быть открыт и
// закрыт, а статуса ошибки и тегов на нём быть не обязано. Без этой стороны
// «статус Error» был бы неотличим от «обёртка красит всё, до чего дотянулась».
func TestБезошибочныйПутьНеКраситСпан(t *testing.T) {
	t.Parallel()
	var судимых int
	for _, путь := range путиДвижка() {
		if путь.ошибочный {
			continue
		}
		судимых++
		t.Run(путь.спан, func(t *testing.T) {
			t.Parallel()
			провайдер := newRecordingProvider()
			if err := путь.гнать(rootContext(провайдер), движок()); err != nil {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: безошибочный путь %s вернул %v", путь.спан, err)
			}
			спаны := провайдер.byName(путь.спан)
			if len(спаны) != 1 {
				t.Fatalf("ТРАССИРОВКА ИСЧЕЗЛА: путь %s пройден, спанов с этим именем %d",
					путь.спан, len(спаны))
			}
			спан := спаны[0]
			if n := спан.endedCount(); n != 1 {
				t.Errorf("спан %s закрыт %d раз(а), ожидался ровно один", путь.спан, n)
			}
			if code, desc := спан.status(); code != codes.Unset {
				t.Errorf("спан %s: путь прошёл без ошибки, а статус %v (%q)",
					путь.спан, code, desc)
			}
			if got, ok := спан.attr("error"); ok {
				t.Errorf("спан %s: путь прошёл без ошибки, а тег error = %q", путь.спан, got)
			}
		})
	}
	if судимых == 0 {
		t.Fatal("ни одного безошибочного пути в перечне — «молчит» неотличимо от «не смотрел»")
	}
}

// TestБезПровайдераВКонтекстеСпановНет — ГРАНИЦА, а не дефект.
//
// Движок берёт провайдер ИЗ КОНТЕКСТА, как и апстрим: нет спана в контексте —
// нет и провайдера, спаны не рождаются. Это семантика апстрима, сохранённая
// вырезом дословно, и она закреплена здесь, чтобы «спанов нет» на голом
// контексте читалось как известная граница, а не как находка.
//
// Проба же стоит и как положительный контроль остальных: она доказывает, что
// спаны в них появляются ИЗ-ЗА подставленного провайдера, а не сами по себе.
func TestБезПровайдераВКонтекстеСпановНет(t *testing.T) {
	t.Parallel()
	провайдер := newRecordingProvider()
	_, err := движок().NewAccessRequest(context.Background(), постЗапрос(url.Values{}), new(fosite.DefaultSession))
	if err == nil {
		t.Fatal("проба НЕ ИСПОЛНЯЛАСЬ: путь обязан был упереться в ошибку")
	}
	if got := провайдер.started(); len(got) != 0 {
		t.Errorf("провайдер не подставлялся в контекст, а спаны на нём открылись: %v", got)
	}
}
