// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package filter — простой парсер filter-выражений API Kachō.
//
// Текущая поддержка:
//
//	<field> = "<value>"           — точное равенство
//	<field> CONTAINS "<value>"    — подстрока (LIKE %…%, подстановочные знаки
//	                                значения экранируются)
//
// Где <field> — whitelisted set (например "name"), <value> — double-quoted
// строка. Возвращает (FilterAST, error). FilterAST использует SQL-binding
// (без string concat) при превращении в WHERE clause.
//
// CONTAINS заведён под строку поиска в списке: точное равенство ей бесполезно
// (набирающий имя по частям не совпадёт никогда), поэтому без него поиск
// оставался бы клиентским — то есть отвечал бы про загруженную страницу, а не
// про список.
//
// Значение проверяется по двум осям — длина и запрещённый знак; правила и их
// довод — у MaxValueLen ниже. Алфавит значения НЕ сужается, и это решение, а не
// упущение: фильтруемые поля дерева несут не только имена (идентификаторы, вид
// размещения в верхнем регистре, точечный вид области), поэтому алфавит формы
// имени отверг бы законный фильтр.
//
// Формат сообщений об ошибках:
//
//	"Bad expression at column N. Unknown field: \"<field>\""
//	"Bad expression at column N. Expected an operator"
//	"Bad expression at column N. Expected a string, integer, date-time or boolean value"
//	"Bad expression at column N. Value of \"<field>\" is longer than 256 characters"
//	"Bad expression at column N. Value of \"<field>\" contains a NUL character"
//
// Поддержка AND/OR/STARTS_WITH/IN — не заведена: у неё нет вызывающего.
package filter

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// safeFieldRe — идентификатор-колонка, безопасная для дословной подстановки в SQL:
// ровно то подмножество символов, которое эмитит Parse (буквы/цифры/подчёркивание/
// точка, начиная с буквы или подчёркивания). Любое отклонение (пробел, кавычка,
// оператор, скобка) означает, что Field сконструирован в обход Parse-whitelist'а и
// подлежит защитному quoting'у.
var safeFieldRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)

// OpEquals / OpContains — операторы, которые эмитит Parse. Значение Op вне этого
// набора ToSQL трактует как равенство: расширять набор надо в обоих местах разом.
const (
	OpEquals   = "="
	OpContains = "CONTAINS"
)

// likeEscaper экранирует подстановочные знаки LIKE, пришедшие ЗНАЧЕНИЕМ.
// Обратная косая — первой: иначе экранирующие символы, добавленные для % и _,
// были бы экранированы повторно.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// MaxValueLen — предел длины значения фильтра, В ЗНАКАХ.
//
// ПОЧЕМУ ПРЕДЕЛ ВООБЩЕ ЕСТЬ (задача продукта #1654). Значение приходит от
// клиента и уезжает в запрос параметром; у оператора `CONTAINS` оно становится
// образцом `LIKE '%…%'`, и стоимость каждой просмотренной строки растёт вместе
// с длиной образца. Такой запрос дёшев в отправке и не дёшев в обслуживании, а
// предела у него не было ни одного: `page_size` конвенция ограничивает, значение
// фильтра оставалось единственной неограниченной строкой на пути чтения.
//
// ПОЧЕМУ ИМЕННО 256. Всё, что фильтр в этом дереве способен СОПОСТАВИТЬ, короче:
// имя ресурса — DNS-label, не длиннее 63 знаков; идентификаторы — около 21;
// точечный вид области и вид размещения — меньше двадцати. 256 — вчетверо
// больше самого длинного из них, поэтому законного вызывающего предел не задевает
// и запас на будущее поле оставляет. Предел объявлен ЧИСЛОМ и вывезен наружу:
// строящий клиента узнаёт его из контракта, а не отказом.
//
// ПОЧЕМУ В ЗНАКАХ, А НЕ В БАЙТАХ. Имя и метка бывают не только латинскими;
// байтовый предел отверг бы кириллическое значение вдвое короче объявленного —
// то есть правило зависело бы от письменности, а не от длины.
const MaxValueLen = 256

// FilterAST — узел AST. Для текущего узла-минимума: одно сравнение.
type FilterAST struct {
	Field string
	Op    string // OpEquals | OpContains
	Value string
}

// ParseError — ошибка парсинга с message в фиксированном формате.
type ParseError struct {
	Column  int
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("Bad expression at column %d. %s", e.Column, e.Message)
}

// Parse разбирает filter-выражение.
// allowedFields — whitelist полей.
//
// Возвращает (nil, nil) для пустого input — означает "no filter".
// Возвращает *FilterAST или *ParseError.
func Parse(input string, allowedFields []string) (*FilterAST, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	// 1. Извлекаем имя поля. Идентификатор: первый символ — буква или '_',
	// далее — буквы/цифры/'_'/'.'. Тот же набор, что принимает safeFieldRe,
	// поэтому легитимное поле дословно проходит ToSQL без защитного quoting'а.
	col := 1
	i := 0
	fieldStart := i
	if i < len(input) && (isLetter(input[i]) || input[i] == '_') {
		i++
		for i < len(input) && (isAlphaNum(input[i]) || input[i] == '_' || input[i] == '.') {
			i++
		}
	}
	field := input[fieldStart:i]
	if field == "" {
		return nil, &ParseError{Column: col, Message: "Expected a field name"}
	}
	// Проверяем whitelist
	allowed := false
	for _, f := range allowedFields {
		if f == field {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, &ParseError{Column: col, Message: fmt.Sprintf("Unknown field: %q", field)}
	}

	// 2. Пропускаем (опциональные) пробелы
	for i < len(input) && input[i] == ' ' {
		i++
	}

	// 3. Оператор: `=` либо ключевое слово CONTAINS.
	//
	// Ключевое слово требует пробела после себя (`CONTAINS"x"` — не оператор):
	// иначе `CONTAINS` склеивался бы со значением и разбор зависел бы от того,
	// где именно вызывающий поставил кавычку.
	opCol := i + 1
	var op string
	switch {
	case i < len(input) && input[i] == '=':
		op = OpEquals
		i++
	case strings.HasPrefix(input[i:], OpContains+" "):
		op = OpContains
		i += len(OpContains)
	default:
		return nil, &ParseError{Column: opCol, Message: "Expected an operator"}
	}

	// 4. Опциональные пробелы
	for i < len(input) && input[i] == ' ' {
		i++
	}

	// 5. Значение: должно быть в "..." (double-quoted string)
	if i >= len(input) || input[i] != '"' {
		return nil, &ParseError{Column: i + 1, Message: "Expected a string, integer, date-time or boolean value"}
	}
	i++ // открывающая "
	valStart := i
	for i < len(input) && input[i] != '"' {
		i++
	}
	if i >= len(input) {
		return nil, &ParseError{Column: i + 1, Message: "Expected closing quote"}
	}
	value := input[valStart:i]
	i++ // закрывающая "

	// Значение — единственное, что вызывающий берёт от клиента ДОСЛОВНО, поэтому
	// его правила стоят здесь, а не у каждого из вызывающих: восемнадцать
	// одинаковых проверок разошлись бы молча.
	//
	// Отказ называет ПОЛЕ и ПРАВИЛО (`api-conventions.md` §Error-format): «invalid
	// filter» не говорит, чем значение негодно, и клиенту остаётся угадывать.
	// Само значение в отказ не выносится — оно и есть то, что признано негодным,
	// а невидимый знак в тексте отказа ломает журнал читателя.
	if idx := strings.IndexByte(value, 0); idx >= 0 {
		// Нулевой байт Postgres в тексте не хранит и отвергает параметр
		// (`22021`). Этого кода нет в общем классификаторе, поэтому без этой
		// проверки ввод клиента возвращался ему же отказом INTERNAL — платформа
		// объявляла себя сломанной на том, что прислал он сам.
		return nil, &ParseError{
			Column:  valStart + idx + 1,
			Message: fmt.Sprintf("Value of %q contains a NUL character", field),
		}
	}
	if n := utf8.RuneCountInString(value); n > MaxValueLen {
		return nil, &ParseError{
			Column: valStart + 1,
			Message: fmt.Sprintf("Value of %q is longer than %d characters",
				field, MaxValueLen),
		}
	}

	// 6. Хвост — должен быть пустой
	for i < len(input) && input[i] == ' ' {
		i++
	}
	if i < len(input) {
		return nil, &ParseError{Column: i + 1, Message: "Unexpected token"}
	}

	return &FilterAST{Field: field, Op: op, Value: value}, nil
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isAlphaNum(b byte) bool {
	return isLetter(b) || (b >= '0' && b <= '9')
}

// ToSQL превращает AST в безопасный SQL fragment.
// Возвращает (whereFragment, args). whereFragment использует placeholder $N,
// где N стартует с argStartIdx.
//
// Например: ast{Field:"name",Op:"=",Value:"foo"}, argStartIdx=3
//
//	→ ("name = $3", []any{"foo"}, nil)
func (a *FilterAST) ToSQL(argStartIdx int) (string, []any) {
	return a.ToSQLOn(a.Field, argStartIdx)
}

// ToSQLOn — то же, что ToSQL, но предикат строится на КОЛОНКЕ, которую называет
// вызывающий, а не на имени разобранного поля.
//
// Зачем это отдельная точка входа. `ToSQL` полагает, что поле контракта и
// колонка таблицы называются одинаково. У доброй половины владельцев это
// неверно: колонка уточнена псевдонимом (`v.name`, `i.name`, `s.name`), либо
// предикат собирается не в том слое, где разбирали выражение. Такому владельцу
// применить узел было НЕЧЕМ — и он забирал из узла одно значение, теряя вместе
// с ним ОПЕРАТОР: запрос подстроки молча отвечал точным равенством. Это и есть
// содержание #460; отсутствие этой функции — его причина, а не невнимательность
// авторов.
//
// `column` обязана быть идентификатором (при необходимости уточнённым точкой) —
// не выражением. Всё, что не проходит `safeFieldRe`, защитно закавычивается, как
// и `Field` в `ToSQL`: колонка приходит от вызывающего, поэтому защита имени
// действует и на этом входе. Владельцу, которому нужна не колонка, а выражение
// (`lower(email)`), эта функция не подходит — он обязан разобрать оператор сам и
// назвать неподдерживаемый отказом, а не свести его к равенству.
func (a *FilterAST) ToSQLOn(column string, argStartIdx int) (string, []any) {
	// Value параметризуется ($N) — инъекция через него невозможна. Колонка же
	// конкатенируется в SQL, поэтому дословно эмитим только идентификатор-safe
	// имя; иначе — защитно оборачиваем через pgx.Identifier.Sanitize (двойные
	// кавычки + экранирование), гарантируя невозможность выхода за пределы
	// идентификатора (CWE-89).
	if !safeFieldRe.MatchString(column) {
		column = pgx.Identifier{column}.Sanitize()
	}
	if a.Op == OpContains {
		// Подстановочные знаки, пришедшие ЗНАЧЕНИЕМ, экранируются: иначе `%` в
		// поисковой строке совпадает со всем подряд, и ответ приходит про другой
		// набор строк, чем спросили. Экранирующий символ — обратная косая,
		// умолчание LIKE в Postgres.
		return fmt.Sprintf("%s LIKE $%d", column, argStartIdx), []any{"%" + likeEscaper.Replace(a.Value) + "%"}
	}
	return fmt.Sprintf("%s = $%d", column, argStartIdx), []any{a.Value}
}
