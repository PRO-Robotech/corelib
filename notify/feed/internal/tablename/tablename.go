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

func (k Kind) suffix() string {
	switch k {
	case Outbox:
		return "_notification_outbox"
	case Window:
		return "_notification_window"
	case Contrib:
		return "_notification_contrib"
	}
	panic(fmt.Sprintf("tablename: вид таблицы %d вне перечня", int(k)))
}

// ident — часть имени службы: идентификатор Postgres в нижнем регистре.
var ident = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Valid судит префикс службы: идентификатор либо «схема.идентификатор».
// Источник зовёт её при сборке (feed.NewSource, schema.Migration) до того, как
// имя попадёт в оператор.
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
	return nil
}

// Of — имя таблицы вида k службы svc, уже экранированное для подстановки в
// оператор. svc судит Valid; на непроверенном префиксе Of не зовётся.
func Of(svc string, k Kind) string {
	return pgx.Identifier(strings.Split(svc+k.suffix(), ".")).Sanitize()
}

// Index — имя индекса таблицы k с ролью role. Индекс схемы не несёт: он живёт в
// схеме своей таблицы.
func Index(svc string, k Kind, role string) string {
	parts := strings.Split(svc, ".")
	return pgx.Identifier{parts[len(parts)-1] + k.suffix() + "_" + role + "_idx"}.Sanitize()
}
