// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package validate

// Пробы единственной формы имени ресурса (#715).
//
// Каждая ось проверяется В ОБЕ СТОРОНЫ. Односторонняя проба здесь бесполезна by
// construction: набор «заглавная отвергается, подчёркивание отвергается, пустая
// отвергается» остался бы зелёным на валидаторе, отвергающем ЛЮБОЙ вход, — то
// есть не отличал бы верную форму от сломанной. Поэтому у каждого запрета рядом
// стоит принимаемый близнец, отличающийся ровно проверяемым признаком.
//
// Чистые функции, Postgres не нужен.

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/ids"
)

// requireNameViolation требует InvalidArgument, НАЗЫВАЮЩИЙ поле. Проверять надо
// сообщение, а не только код: отказ, не называющий поле, оставляет вызывающего
// гадать, что именно из присланного негодно (правило «отказ формы называет поле
// и правило»).
func requireNameViolation(t *testing.T, err error, field, input string) {
	t.Helper()
	if err == nil {
		t.Fatalf("вход %q: ожидался отказ, получен nil", input)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("вход %q: ожидался gRPC status, получено: %v", input, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("вход %q: ожидался InvalidArgument, получен %v", input, st.Code())
	}
	named := strings.Contains(st.Message(), field)
	for _, d := range st.Details() {
		if s, ok := d.(interface{ String() string }); ok && strings.Contains(s.String(), field) {
			named = true
		}
	}
	if !named {
		t.Fatalf("вход %q: отказ не называет поле %q; сообщение: %s / детали: %v",
			input, field, st.Message(), st.Details())
	}
}

// TestNameForm_BothSidesOnEveryAxis — по оси на признак, принимаемый и
// отвергаемый близнецы отличаются ровно этим признаком.
func TestNameForm_BothSidesOnEveryAxis(t *testing.T) {
	cases := []struct {
		axis    string
		input   string
		wantErr bool
	}{
		// Ось «первый символ — цифра»: RFC 1123 её ДОПУСКАЕТ. Прежние четыре
		// валидатора требовали букву; это послабление, а не ужесточение.
		{axis: "цифра первой — принимается", input: "1ab"},
		{axis: "цифра первой, одна", input: "1"},
		{axis: "буква первой — принимается", input: "ab1"},

		// Ось регистра.
		{axis: "строчные — принимаются", input: "abc"},
		{axis: "заглавная — отвергается", input: "Abc", wantErr: true},
		{axis: "все заглавные — отвергаются", input: "ABC", wantErr: true},

		// Ось подчёркивания.
		{axis: "дефис — принимается", input: "a-b"},
		{axis: "подчёркивание — отвергается", input: "a_b", wantErr: true},

		// Ось точки.
		{axis: "без точки — принимается", input: "ab"},
		{axis: "точка — отвергается", input: "a.b", wantErr: true},

		// Ось пустоты.
		{axis: "один символ — принимается", input: "a"},
		{axis: "пустая строка — отвергается", input: "", wantErr: true},

		// Ось длины: граница с обеих сторон.
		{axis: "63 символа — принимаются", input: strings.Repeat("a", 63)},
		{axis: "64 символа — отвергаются", input: strings.Repeat("a", 64), wantErr: true},

		// Ось краевого дефиса.
		{axis: "дефис в середине — принимается", input: "a-b-c"},
		{axis: "дефис первым — отвергается", input: "-ab", wantErr: true},
		{axis: "дефис последним — отвергается", input: "ab-", wantErr: true},

		// Прочие символы, отвергаемые формой; принимаемый близнец — выше.
		{axis: "слэш — отвергается", input: "a/b", wantErr: true},
		{axis: "пробел — отвергается", input: "a b", wantErr: true},
		{axis: "не-ASCII — отвергается", input: "имя", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.axis, func(t *testing.T) {
			err := Name("name", tc.input)
			if tc.wantErr {
				requireNameViolation(t, err, "name", tc.input)
				return
			}
			if err != nil {
				t.Fatalf("вход %q обязан приниматься, получен отказ: %v", tc.input, err)
			}
		})
	}
}

// TestName_EmptyIsRequiredNotMalformed — пустая строка и негодная форма суть
// разные ошибки вызывающего, и отказ обязан их различать: забывший поле и
// приславший `My_Name` ошиблись по-разному.
func TestName_EmptyIsRequiredNotMalformed(t *testing.T) {
	empty := status.Convert(Name("name", ""))
	if !strings.Contains(empty.Message()+detailsText(empty), "name is required") {
		t.Fatalf("пустое имя обязано отвергаться как required; получено: %s%s",
			empty.Message(), detailsText(empty))
	}
	bad := status.Convert(Name("name", "My_Name"))
	if strings.Contains(detailsText(bad), "is required") {
		t.Fatalf("негодная форма НЕ обязана называться required; получено: %s", detailsText(bad))
	}
	if !strings.Contains(detailsText(bad), NameForm) {
		t.Fatalf("отказ формы обязан называть правило %s; получено: %s", NameForm, detailsText(bad))
	}
}

func detailsText(st *status.Status) string {
	var b strings.Builder
	for _, d := range st.Details() {
		if s, ok := d.(interface{ String() string }); ok {
			b.WriteString(s.String())
		}
	}
	return b.String()
}

// TestNameOnCreate_EmptyIsLegalInputEverythingElseIsTheSameForm — на создании
// пустое законно, всё остальное судится ТОЙ ЖЕ формой. Положительный и
// отрицательный контроль рядом: иначе «пустое проходит» зеленело бы и на
// валидаторе, пропускающем всё.
func TestNameOnCreate_EmptyIsLegalInputEverythingElseIsTheSameForm(t *testing.T) {
	if err := NameOnCreate("name", ""); err != nil {
		t.Fatalf("пустое имя — законный ВХОД создания, получен отказ: %v", err)
	}
	if err := NameOnCreate("name", "ok-name"); err != nil {
		t.Fatalf("годное имя обязано приниматься, получен отказ: %v", err)
	}
	for _, bad := range []string{"Bad_Name", "a.b", strings.Repeat("a", 64), "-x"} {
		requireNameViolation(t, NameOnCreate("name", bad), "name", bad)
	}
}

// TestNameOrDefault_EmptyBecomesTheIDNonEmptySurvives — обе стороны подстановки.
func TestNameOrDefault_EmptyBecomesTheIDNonEmptySurvives(t *testing.T) {
	id := ids.NewHyphenID("ins")
	if got := NameOrDefault("", id); got != id {
		t.Fatalf("пустое имя обязано заменяться умолчанием от id: получено %q, ожидалось %q", got, id)
	}
	if got := NameOrDefault("chosen-name", id); got != "chosen-name" {
		t.Fatalf("непустое имя обязано доживать до записи неизменным: получено %q", got)
	}
}

// TestDefaultNameForEveryKnownPrefixSatisfiesTheForm — НЕСУЩЕЕ свойство: имя по
// умолчанию всегда годно по единственной форме дерева.
//
// Проверяется по КАЖДОМУ известному prefix'у ОБЕИХ форм id, а не на одном
// примере: prefix'ы приходят из pkg/ids и пополняются новыми доменами, поэтому
// проба обязана расти вместе с каталогом, а не отставать от него. Утверждение
// «id уже удовлетворяет форме» иначе осталось бы рассуждением.
func TestDefaultNameForEveryKnownPrefixSatisfiesTheForm(t *testing.T) {
	legacy := ids.KnownPrefixes()
	hyphen := ids.KnownHyphenPrefixes()
	if len(legacy) == 0 || len(hyphen) == 0 {
		t.Fatalf("каталог prefix'ов пуст (legacy=%d, hyphen=%d) — проверять нечего, "+
			"а молчаливый зелёный означал бы «проверено»", len(legacy), len(hyphen))
	}
	checked := 0
	for p := range legacy {
		id := ids.NewID(p)
		if err := Name("name", NameOrDefault("", id)); err != nil {
			t.Fatalf("prefix %q: имя по умолчанию %q не годно по форме дерева: %v", p, id, err)
		}
		checked++
	}
	for p := range hyphen {
		id := ids.NewHyphenID(p)
		if err := Name("name", NameOrDefault("", id)); err != nil {
			t.Fatalf("hyphen-prefix %q: имя по умолчанию %q не годно по форме дерева: %v", p, id, err)
		}
		checked++
	}
	t.Logf("осмотрено prefix'ов: %d (legacy %d + hyphen %d)", checked, len(legacy), len(hyphen))
}

// TestNameOnUpdate_FiveOutcomes — все пять исходов правки, каждый со своим
// близнецом. Односторонний набор («пустое отвергается») зеленел бы на функции,
// отвергающей всё, и на функции, не пишущей ничего.
func TestNameOnUpdate_FiveOutcomes(t *testing.T) {
	cases := []struct {
		name      string
		mask      []string
		value     string
		wantApply bool
		wantErr   bool
	}{
		{name: "маска пуста, имя пусто — не трогаем", mask: nil, value: ""},
		{name: "маска пуста, имя непусто — пишем", mask: nil, value: "new-name", wantApply: true},
		{name: "маска пуста, имя негодно — отказ", mask: nil, value: "Bad_Name", wantErr: true},
		{name: "маска не называет имя — не трогаем", mask: []string{"labels"}, value: ""},
		{name: "маска не называет имя, значение есть — всё равно не трогаем",
			mask: []string{"labels"}, value: "ignored-name"},
		{name: "маска называет имя, пусто — отказ", mask: []string{"name"}, value: "", wantErr: true},
		{name: "маска называет имя, годно — пишем",
			mask: []string{"name"}, value: "new-name", wantApply: true},
		{name: "маска называет имя, негодно — отказ",
			mask: []string{"name"}, value: "Bad_Name", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apply, err := NameOnUpdate("name", tc.mask, tc.value)
			if tc.wantErr {
				requireNameViolation(t, err, "name", tc.value)
				if apply {
					t.Fatal("отказ не может сопровождаться указанием записать имя")
				}
				return
			}
			if err != nil {
				t.Fatalf("неожиданный отказ: %v", err)
			}
			if apply != tc.wantApply {
				t.Fatalf("записывать имя: получено %v, ожидалось %v", apply, tc.wantApply)
			}
		})
	}
}
