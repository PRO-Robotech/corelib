// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package tablename — ЕДИНСТВЕННЫЙ производитель имён таблиц ленты извещений
// (§6, УК89 (б)). Суффиксы таблиц литералом живут только здесь; пакет
// внутренний, и вне поддерева notify/feed компилятор его не импортирует.
// Гейт писателей таблиц ленты (З16) судит вызовы Of по идентичности объекта.
package tablename

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Kind — вид таблицы ленты.
type Kind int

// Виды таблиц ленты.
const (
	// Outbox — лента: строка на письмо.
	Outbox Kind = iota + 1
	// Window — окно лимита.
	Window
	// Contrib — вклад строки ленты в окна.
	Contrib
)

// Kinds — закрытый перечень видов.
func Kinds() []Kind { return []Kind{Outbox, Window, Contrib} }

// suffixes — суффиксы видов перечня. Вида вне перечня здесь нет.
var suffixes = map[Kind]string{
	Outbox:  "_notification_outbox",
	Window:  "_notification_window",
	Contrib: "_notification_contrib",
}

// refused — имя для вида вне перечня: пустой идентификатор в кавычках. Сервер
// отвергает его разбором любого оператора (SQLSTATE 42601), поэтому ни
// таблица, ни индекс под ним не создаются и не пишутся, а путь Put не паникует.
const refused = `""`

// maxIdentBytes — предел длины идентификатора Postgres (NAMEDATALEN − 1); длиннее
// сервер молча усекает.
const maxIdentBytes = 63

// Role — роль индекса таблицы ленты. Закрытый перечень: длину производного
// имени индекса Valid считает по нему.
type Role string

// Роли индексов ленты.
const (
	// Pending — строки, ждущие доставки.
	Pending Role = "pending"
	// Closed — строки с исходом.
	Closed Role = "closed"
	// Thread — строки нити, ждущие доставки (с V3: голова нити во взятии).
	Thread Role = "thread"
)

// Roles — закрытый перечень ролей индекса.
func Roles() []Role { return []Role{Pending, Closed, Thread} }

// ident — часть имени службы: идентификатор Postgres в нижнем регистре.
var ident = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Valid судит префикс службы: идентификатор либо «схема.идентификатор».
// Источник зовёт её при сборке (feed.NewSource, schema.Migration) до того, как
// имя попадёт в оператор.
//
// Длина: схема — не длиннее maxIdentBytes; служба — так, чтобы самое длинное
// производное имя (таблица любого вида, индекс любого вида и роли либо CHECK
// колонки state или class)
// помещалось в maxIdentBytes. Иначе сервер молча усёк бы имена, и два индекса
// ленты совпали бы.
func Valid(svc string) error {
	parts := strings.Split(svc, ".")
	if len(parts) > 2 {
		return fmt.Errorf("tablename: префикс %q глубже пары «схема.служба»", svc)
	}
	for _, p := range parts {
		if !ident.MatchString(p) {
			return fmt.Errorf("tablename: префикс %q негоден как имя Postgres", svc)
		}
	}
	if len(parts) == 2 && len(parts[0]) > maxIdentBytes {
		return fmt.Errorf("tablename: схема префикса %q длиннее %d байт", svc, maxIdentBytes)
	}
	if room := maxIdentBytes - longestDerivedTail(); len(parts[len(parts)-1]) > room {
		return fmt.Errorf("tablename: служба префикса %q длиннее %d байт — производные имена усекутся", svc, room)
	}
	return nil
}

// longestDerivedTail — длина самой длинной части производного имени после
// префикса службы: суффикс таблицы, суффикс, роль и «_idx» индекса либо хвост
// CHECK колонки state или class.
func longestDerivedTail() int {
	longest := max(len(stateCheckTail), len(classCheckTail))
	for _, k := range Kinds() {
		longest = max(longest, len(suffixes[k]))
		for _, r := range Roles() {
			longest = max(longest, len(indexTail(k, r)))
		}
	}
	return longest
}

func indexTail(k Kind, role Role) string { return suffixes[k] + "_" + string(role) + "_idx" }

// stateCheckTail — хвост имени CHECK колонки state ленты: сервер даёт
// ограничению колонки имя «<таблица>_<колонка>_check».
var stateCheckTail = suffixes[Outbox] + "_state_check"

// classCheckTail — хвост имени CHECK колонки class ленты (то же правило
// сервера, что у stateCheckTail).
var classCheckTail = suffixes[Outbox] + "_class_check"

// StateCheck — имя CHECK колонки state ленты службы svc, которое сервер дал
// ему по умолчанию (переход схемы снимает и ставит его заново под тем же
// именем). Схемы не несёт: ограничение живёт в схеме своей таблицы.
func StateCheck(svc string) string { return columnCheck(svc, stateCheckTail) }

// ClassCheck — имя CHECK колонки class ленты службы svc, данное сервером по
// умолчанию (переход V2 → V3 ставит его заново с классом obligation).
func ClassCheck(svc string) string { return columnCheck(svc, classCheckTail) }

func columnCheck(svc, tail string) string {
	parts := strings.Split(svc, ".")
	return pgx.Identifier{parts[len(parts)-1] + tail}.Sanitize()
}

// Of — имя таблицы вида k службы svc, уже экранированное для подстановки в
// оператор. svc судит Valid; на непроверенном префиксе Of не зовётся.
// Вид вне перечня — refused.
func Of(svc string, k Kind) string {
	suffix, ok := suffixes[k]
	if !ok {
		return refused
	}
	return pgx.Identifier(strings.Split(svc+suffix, ".")).Sanitize()
}

// Index — имя индекса таблицы k с ролью role. Индекс схемы не несёт: он живёт в
// схеме своей таблицы.
// Вид вне перечня — refused.
func Index(svc string, k Kind, role Role) string {
	if _, ok := suffixes[k]; !ok {
		return refused
	}
	parts := strings.Split(svc, ".")
	return pgx.Identifier{parts[len(parts)-1] + indexTail(k, role)}.Sanitize()
}
