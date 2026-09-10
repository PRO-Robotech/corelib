// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package filter

import (
	"strings"
	"testing"
)

func TestParse_NameEquals(t *testing.T) {
	ast, err := Parse(`name="default"`, []string{"name"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ast.Field != "name" || ast.Value != "default" || ast.Op != "=" {
		t.Fatalf("got %+v", ast)
	}
}

func TestParse_Empty(t *testing.T) {
	ast, err := Parse("", []string{"name"})
	if err != nil || ast != nil {
		t.Fatalf("got ast=%v err=%v, expected nil/nil", ast, err)
	}
}

func TestParse_UnknownField(t *testing.T) {
	_, err := Parse(`junk="x"`, []string{"name"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Unknown field") {
		t.Fatalf("expected Unknown field, got %v", err)
	}
}

func TestParse_NoOperator(t *testing.T) {
	_, err := Parse(`name "x"`, []string{"name"})
	if err == nil || !strings.Contains(err.Error(), "Expected an operator") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_NoQuote(t *testing.T) {
	_, err := Parse(`name=foo`, []string{"name"})
	if err == nil || !strings.Contains(err.Error(), "Expected a string") {
		t.Fatalf("got %v", err)
	}
}

func TestParse_UnterminatedQuote(t *testing.T) {
	// Value opens a quote but never closes it → the scanner runs off the end.
	// Must reject with "Expected closing quote", not parse into a FilterAST.
	// Regression guard for the filter/filter.go closing-quote reject branch.
	ast, err := Parse(`name = "foo`, []string{"name"})
	if ast != nil {
		t.Fatalf("expected nil AST for unterminated quote, got %+v", ast)
	}
	if err == nil || !strings.Contains(err.Error(), "Expected closing quote") {
		t.Fatalf("expected Expected closing quote, got %v", err)
	}
}

func TestParse_TrailingGarbage(t *testing.T) {
	// A well-formed equals followed by trailing tokens after the closing quote
	// must reject with "Unexpected token" (no AND/OR support yet), never silently
	// accept the leading clause. Regression guard for the trailing-token reject.
	ast, err := Parse(`name = "foo" extra`, []string{"name"})
	if ast != nil {
		t.Fatalf("expected nil AST for trailing garbage, got %+v", ast)
	}
	if err == nil || !strings.Contains(err.Error(), "Unexpected token") {
		t.Fatalf("expected Unexpected token, got %v", err)
	}
}

func TestParse_SpacedEquals(t *testing.T) {
	ast, err := Parse(`name = "x"`, []string{"name"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ast.Value != "x" {
		t.Fatalf("got %v", ast)
	}
}

// A field name must start with a letter or underscore — the exact identifier
// shape safeFieldRe (`^[a-zA-Z_]…`) and ToSQL's verbatim path promise. A
// digit-leading name must be rejected by Parse, otherwise it would be accepted
// yet fail safeFieldRe and get double-quoted by ToSQL (breaking the
// "legit Parse field is emitted verbatim" invariant). Regression guard for the
// Parse-vs-safeFieldRe first-char agreement.
func TestParse_DigitLeadingFieldRejected(t *testing.T) {
	// Whitelist the digit-leading name so the only thing that can reject it is
	// the first-char identifier rule, not the allowedFields check.
	ast, err := Parse(`3name = "x"`, []string{"3name"})
	if ast != nil {
		t.Fatalf("expected nil AST for digit-leading field, got %+v", ast)
	}
	if err == nil || !strings.Contains(err.Error(), "Expected a field name") {
		t.Fatalf("expected Expected a field name, got %v", err)
	}
}

// Every field name Parse accepts must pass safeFieldRe unchanged, so ToSQL
// emits it verbatim (never the defensive pgx.Identifier.Sanitize path). Locks
// the comment invariant "легитимные поля из Parse всегда проходят без изменений".
func TestParse_AcceptedFieldIsSafeVerbatim(t *testing.T) {
	for _, f := range []string{"name", "_x", "a1", "schema.table"} {
		ast, err := Parse(f+`="v"`, []string{f})
		if err != nil {
			t.Fatalf("Parse rejected legit field %q: %v", f, err)
		}
		if !safeFieldRe.MatchString(ast.Field) {
			t.Fatalf("field %q accepted by Parse but fails safeFieldRe", ast.Field)
		}
	}
}

func TestToSQL(t *testing.T) {
	ast := &FilterAST{Field: "name", Op: "=", Value: "foo"}
	frag, args := ast.ToSQL(3)
	if frag != "name = $3" {
		t.Fatalf("got %q", frag)
	}
	if len(args) != 1 || args[0] != "foo" {
		t.Fatalf("got %v", args)
	}
}

// A FilterAST built directly (bypassing Parse's allowedFields whitelist) with an
// injection payload in Field must NOT splice raw SQL into the WHERE fragment.
// ToSQL concatenates Field (values are parameterised), so Field must be
// identifier-safe or defensively quoted. Regression guard against CWE-89
// (SQL injection via unvalidated Field).
func TestToSQL_MaliciousFieldNeutralised(t *testing.T) {
	ast := &FilterAST{Field: `1=1 OR name`, Op: "=", Value: "x"}
	frag, _ := ast.ToSQL(1)
	// The raw injection substring must never appear unchanged as SQL — a safe
	// implementation quotes the whole thing into a single identifier.
	if strings.Contains(frag, "1=1 OR name = $1") {
		t.Fatalf("injection payload spliced into WHERE fragment: %q", frag)
	}
	if !strings.HasPrefix(frag, `"`) {
		t.Fatalf("expected malicious Field to be identifier-quoted, got %q", frag)
	}
}

// A legitimate whitelisted field (produced by Parse) must still be emitted
// as-is so the safe path is unchanged.
func TestToSQL_LegitFieldVerbatim(t *testing.T) {
	for _, f := range []string{"name", "network_id", "placement_type"} {
		ast := &FilterAST{Field: f, Op: "=", Value: "v"}
		frag, _ := ast.ToSQL(2)
		if frag != f+" = $2" {
			t.Fatalf("legit field %q altered: %q", f, frag)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CONTAINS — подстрока (#373).
//
// Поисковая строка списка в консоли фильтровала УЖЕ ЗАГРУЖЕННУЮ страницу:
// на списке длиннее страницы она отвечала «ничего не найдено» про ресурс,
// который существует. Уйти на сервер она может только тем выражением, которое
// сервер разбирает, а разбиралось до сих пор одно точное равенство — для
// строки поиска бесполезное: набирающий имя по частям не совпадёт никогда.

func TestParse_Contains(t *testing.T) {
	ast, err := Parse(`name CONTAINS "web"`, []string{"name"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ast.Field != "name" || ast.Op != "CONTAINS" || ast.Value != "web" {
		t.Fatalf("got %+v", ast)
	}
}

func TestParse_ContainsUnknownFieldStillRejected(t *testing.T) {
	if _, err := Parse(`secret CONTAINS "x"`, []string{"name"}); err == nil {
		t.Fatal("expected whitelist rejection for CONTAINS on unlisted field")
	}
}

// Оператор — единственная новая ветка; всё остальное, что стоит на месте
// оператора, обязано отвергаться прежним сообщением.
func TestParse_UnknownOperatorRejected(t *testing.T) {
	for _, expr := range []string{`name LIKE "x"`, `name CONTAIN "x"`, `name ~ "x"`, `name CONTAINS"x"`} {
		if _, err := Parse(expr, []string{"name"}); err == nil {
			t.Fatalf("expected rejection of %q", expr)
		}
	}
}

func TestToSQL_Contains(t *testing.T) {
	ast := &FilterAST{Field: "name", Op: "CONTAINS", Value: "web"}
	frag, args := ast.ToSQL(3)
	if frag != "name LIKE $3" {
		t.Fatalf("got %q", frag)
	}
	if len(args) != 1 || args[0] != "%web%" {
		t.Fatalf("got %v", args)
	}
}

// Подстановочные знаки LIKE, пришедшие ЗНАЧЕНИЕМ, обязаны означать себя.
// Иначе `%` в поисковой строке совпадает со всем подряд, а `_` — с любым
// символом: пользователь получает ответ про другой набор строк, чем спросил.
func TestToSQL_ContainsEscapesWildcards(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`50%`, `%50\%%`},
		{`a_b`, `%a\_b%`},
		{`c\d`, `%c\\d%`},
	} {
		ast := &FilterAST{Field: "name", Op: "CONTAINS", Value: tc.in}
		_, args := ast.ToSQL(1)
		if args[0] != tc.want {
			t.Fatalf("value %q → %q, want %q", tc.in, args[0], tc.want)
		}
	}
}

// Инъекция в Field обезвреживается и на новой ветке — не только на равенстве.
func TestToSQL_ContainsMaliciousFieldNeutralised(t *testing.T) {
	ast := &FilterAST{Field: `1=1 OR name`, Op: "CONTAINS", Value: "x"}
	frag, _ := ast.ToSQL(1)
	if !strings.HasPrefix(frag, `"`) {
		t.Fatalf("expected malicious Field to be identifier-quoted, got %q", frag)
	}
}

// TestToSQLOnAppliesOperatorToCallerColumn — предикат на колонке, которую
// выбирает ВЫЗЫВАЮЩИЙ, обязан сохранять оператор.
//
// Зачем эта функция вообще. `ToSQL` строит предикат по имени ПОЛЯ, то есть
// молча полагает, что поле контракта и колонка таблицы называются одинаково. У
// половины владельцев это неверно: колонка уточнена псевдонимом таблицы
// (`v.name`, `i.name`, `s.name`), либо предикат собирается не там, где разбирали
// выражение. У такого владельца выбора не было: применить узел он не мог, и
// забирал из него ОДНО ЗНАЧЕНИЕ — вместе с чем терял оператор. Отсутствие этой
// функции и есть причина класса #460, а не невнимательность семи авторов.
func TestToSQLOnAppliesOperatorToCallerColumn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ast      *FilterAST
		column   string
		wantFrag string
		wantArg  any
	}{
		{"равенство на уточнённой колонке", &FilterAST{Field: "name", Op: OpEquals, Value: "web"},
			"v.name", "v.name = $2", "web"},
		{"подстрока на уточнённой колонке", &FilterAST{Field: "name", Op: OpContains, Value: "we"},
			"v.name", "v.name LIKE $2", "%we%"},
		// Подстановочные знаки ЗНАЧЕНИЯ экранируются и здесь: иначе `%`,
		// набранный в строке поиска, совпал бы со всем подряд, а ответ выглядел
		// бы результатом поиска.
		{"знаки значения экранируются", &FilterAST{Field: "name", Op: OpContains, Value: "50%_x"},
			"i.name", "i.name LIKE $2", `%50\%\_x%`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frag, args := tc.ast.ToSQLOn(tc.column, 2)
			if frag != tc.wantFrag {
				t.Fatalf("фрагмент = %q, ожидался %q", frag, tc.wantFrag)
			}
			if len(args) != 1 || args[0] != tc.wantArg {
				t.Fatalf("аргументы = %#v, ожидался [%#v]", args, tc.wantArg)
			}
		})
	}
}

// TestToSQLOnIsWhatToSQLUses — у двух точек входа одно поведение, а не две копии,
// которые разойдутся на следующей правке грамматики.
func TestToSQLOnIsWhatToSQLUses(t *testing.T) {
	for _, op := range []string{OpEquals, OpContains} {
		a := &FilterAST{Field: "name", Op: op, Value: "x"}
		f1, a1 := a.ToSQL(1)
		f2, a2 := a.ToSQLOn("name", 1)
		if f1 != f2 || len(a1) != len(a2) || a1[0] != a2[0] {
			t.Fatalf("op=%s: ToSQL=%q%v расходится с ToSQLOn=%q%v", op, f1, a1, f2, a2)
		}
	}
}

// TestToSQLOnQuotesUnsafeColumn — колонка приходит от вызывающего, поэтому
// защита имени идентификатора обязана действовать и на этом входе, а не только
// на разобранном поле.
func TestToSQLOnQuotesUnsafeColumn(t *testing.T) {
	a := &FilterAST{Field: "name", Op: OpEquals, Value: "x"}
	frag, _ := a.ToSQLOn(`name"; DROP TABLE users; --`, 1)
	if strings.Contains(frag, "DROP TABLE users; --") && !strings.Contains(frag, `"`) {
		t.Fatalf("колонка попала в SQL без защиты: %q", frag)
	}
	if !strings.HasPrefix(frag, `"`) {
		t.Fatalf("небезопасная колонка обязана быть закавычена, получено %q", frag)
	}
}

// TestParse_NewmanSecurityPayloadsAreOrdinaryValues закрепляет исход, на котором
// стоят сквозные кейсы `*-LST-SEC-FILTER-SQLI` и `*-LST-FILTER-SPECIAL-CHARS`
// (issue #698).
//
// ПРЕДМЕТ. До #698 оба кейса утверждали `oneOf([200, 400])` — принимали и успех,
// и отказ, то есть приняли бы ровно ту регрессию разбора, ради которой написаны.
// Исход при этом УСТАНОВЛЕН: обе нагрузки стоят ВНУТРИ кавычек, а разборщик
// закрывает значение первой закрывающей кавычкой и требует пустой хвост — то
// есть кавычка, дефисы и спецсимволы значения на разбор не влияют вовсе.
// Значение уезжает ПАРАМЕТРОМ ($N), поэтому список отвечает 200 и пустой
// страницей, а не отказом.
//
// КОНТРОЛЬ В ОБЕ СТОРОНЫ. Рядом стоит выражение, которое разборщик обязан
// ОТВЕРГНУТЬ (`AND` он не знает — это записано в его шапке): без него «принимает»
// было бы неотличимо от «принимает всё», и утверждение о первых двух осталось бы
// недоказанным. На нём стоит третий кейс — `NET-LST-FILTER-MULTI-CONDITIONS`.
func TestParse_NewmanSecurityPayloadsAreOrdinaryValues(t *testing.T) {
	accepted := []struct {
		name, expr, value string
	}{
		{"внедрение в значение (*-LST-SEC-FILTER-SQLI)", `name="a' OR 1=1--"`, `a' OR 1=1--`},
		{"спецсимволы в значении (*-LST-FILTER-SPECIAL-CHARS)", `name="!@#$%"`, `!@#$%`},
	}
	for _, tc := range accepted {
		ast, err := Parse(tc.expr, []string{"name"})
		if err != nil {
			t.Fatalf("%s: выражение обязано разбираться, получено: %v", tc.name, err)
		}
		if ast == nil || ast.Field != "name" || ast.Op != OpEquals || ast.Value != tc.value {
			t.Fatalf("%s: разобрано не то: %+v", tc.name, ast)
		}
		// Значение обязано уехать ПАРАМЕТРОМ: иначе «200 и пустая страница»
		// было бы совпадением, а не свойством.
		frag, args := ast.ToSQL(1)
		if frag != "name = $1" || len(args) != 1 || args[0] != tc.value {
			t.Fatalf("%s: значение не параметризовано: frag=%q args=%v", tc.name, frag, args)
		}
	}

	// Зеркало: `AND` не поддержан, хвост после закрывающей кавычки — отказ.
	if _, err := Parse(`name="x" AND name="y"`, []string{"name"}); err == nil {
		t.Fatal("два условия ПРИНЯТЫ — тогда NET-LST-FILTER-MULTI-CONDITIONS " +
			"утверждает отказ, которого больше нет")
	} else if !strings.HasPrefix(err.Error(), "Bad expression at column ") {
		t.Fatalf("текст отказа — часть контракта фильтра, получено: %q", err.Error())
	}
}
