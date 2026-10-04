// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package resourceevent — форма fanout генератора (NTF-3 З10): функция базы
// resource-event на таблице журнала модуля и её триггер. Тело функции — вывод
// выпущенной версии шаблона на входах модуля (таблица видов и рода изменения
// из объявления журнала, schema_rev шаблона resource-event, строка сигнала из
// формы notify/spec). Пакет — единственный производитель тела: notifygen init
// пишет миграцию, notifygen -check и гейт AuditFeedTableWrites признают тело
// только сверкой с выводом шаблона (извлечь входы из тела, вывести шаблон на
// них, сравнить побайтно). Копии шаблона у читателей нет.
//
// Рантайм источника пакет не тянет: его импортируют генератор и гейт, а не
// notify/feed (NTF1-D05).
package resourceevent

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/notify/spec"
)

// Имена объектов базы и шаблона.
const (
	// TemplateName — шаблон строки ленты.
	TemplateName = "resource-event"
	// FunctionName — функция базы и её триггер; одна на журнал модуля.
	FunctionName = "notify_feed_resource_event"
)

// NameForm — объявление вида журнала о его имени (subscription.NameForm).
type NameForm string

// Формы имени.
const (
	NameFormDNS  NameForm = "dns"
	NameFormNone NameForm = "none"
)

// Scope — якорь вида журнала (subscription.Scope).
type Scope string

// Якоря.
const (
	ScopeProject Scope = "project"
	ScopeCluster Scope = "cluster"
)

// Kind — строка таблицы видов: вид журнала, форма имени, якорь.
type Kind struct {
	Name     string
	NameForm NameForm
	Scope    Scope
}

// Change — строка таблицы рода изменения: слово журнала → род платформы.
type Change struct {
	Word   string
	Change string
}

// Columns — колонки журнала модуля, которые читает функция.
type Columns struct {
	Kind       string
	ID         string
	Change     string
	Payload    string
	Project    string
	Initiator  string
	OccurredAt string
}

// Inputs — входы тела функции. Kinds без ключа строки сигнала и упорядочены
// по имени, Changes — по слову: порядок ключа фиксирован, тело однозначно.
type Inputs struct {
	// Service — префикс таблиц ленты (<Service>_notification_outbox).
	Service string
	// Module — имя модуля: объект строки сигнала notification_feed:<Module>.
	Module string
	// SchemaRev — ревизия шаблона resource-event (revision.yaml).
	SchemaRev int
	// TTL — ttl строки ленты из шаблона, целых секунд.
	TTL time.Duration
	// Table — таблица журнала модуля.
	Table   string
	Columns Columns
	Kinds   []Kind
	Changes []Change
	// SignalChange — слово журнала строки сигнала: то, что переводится в
	// spec.FeedSignalChange.
	SignalChange string
}

// Body — одно определение функции: выпущенная версия шаблона и её входы.
type Body struct {
	// Version — отпечаток выпущенной версии шаблона.
	Version string
	Inputs  Inputs
}

// File — миграция функции. Prev пуст — первая миграция журнала: функция и
// триггер; иначе — CREATE OR REPLACE той же функции, откат которой
// возвращает прежнее определение Prev.
type File struct {
	Up   Body
	Prev *Body
}

// Ошибки входов и разбора.
var (
	// ErrInputs — входы вне формы; тела нет.
	ErrInputs = errors.New("resourceevent: входы функции вне формы")
	// ErrNotFunction — файл не несёт функции resource-event.
	ErrNotFunction = errors.New("resourceevent: файл без функции resource-event")
	// ErrUnreadable — тело несёт функцию, но входы из него не извлекаются.
	ErrUnreadable = errors.New("resourceevent: входы из тела не извлекаются")
	// ErrVersion — отпечаток версии шаблона не выпущен.
	ErrVersion = errors.New("resourceevent: версия шаблона не выпущена")
)

// changeSet — закрытый набор рода изменения платформы (Р3).
var changeSet = map[string]bool{"CREATED": true, "UPDATED": true, "DELETED": true}

var (
	sqlIdent   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	tableIdent = regexp.MustCompile(`^([a-z][a-z0-9_]{0,62}\.)?[a-z][a-z0-9_]{0,62}$`)
	kindWord   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,62}$`)
)

// Validate судит входы: каждое слово, попадающее в текст тела, — из своей
// закрытой формы (без кавычек и пробелов), таблицы непусты и упорядочены,
// род изменения — из закрытого набора, слово строки сигнала одно.
func (in Inputs) Validate() error {
	var bad []error
	add := func(format string, a ...any) { bad = append(bad, fmt.Errorf(format, a...)) }
	if err := tablename.Valid(in.Service); err != nil {
		add("service: %w", err)
	}
	if _, err := spec.FeedSignal(in.Module); err != nil {
		add("module: %w", err)
	}
	if in.SchemaRev < 1 {
		add("schema_rev %d < 1", in.SchemaRev)
	}
	if in.TTL < time.Second || in.TTL%time.Second != 0 || in.TTL > spec.TTLMax {
		add("ttl %s вне целых секунд [1s..%s]", in.TTL, spec.TTLMax)
	}
	if !tableIdent.MatchString(in.Table) {
		add("table %q негодна как имя Postgres", in.Table)
	}
	for name, c := range map[string]string{
		"kind": in.Columns.Kind, "id": in.Columns.ID, "change": in.Columns.Change, "payload": in.Columns.Payload,
		"project": in.Columns.Project, "initiator": in.Columns.Initiator, "occurred_at": in.Columns.OccurredAt,
	} {
		if !sqlIdent.MatchString(c) {
			add("columns.%s %q негодна как имя колонки", name, c)
		}
	}
	if len(in.Kinds) == 0 {
		add("таблица видов пуста")
	}
	for i, k := range in.Kinds {
		switch {
		case !kindWord.MatchString(k.Name):
			add("вид %q вне формы слова", k.Name)
		case k.Name == spec.FeedSignalKey:
			add("ключ строки сигнала %q в таблице видов: строк ленты он не даёт", k.Name)
		case k.NameForm != NameFormDNS && k.NameForm != NameFormNone:
			add("вид %s: name_form %q вне dns|none", k.Name, k.NameForm)
		case k.Scope != ScopeProject && k.Scope != ScopeCluster:
			add("вид %s: scope %q вне project|cluster", k.Name, k.Scope)
		}
		if i > 0 && in.Kinds[i-1].Name >= k.Name {
			add("таблица видов не упорядочена по имени: %s после %s", k.Name, in.Kinds[i-1].Name)
		}
	}
	if len(in.Changes) == 0 {
		add("таблица рода изменения пуста")
	}
	signal := 0
	for i, c := range in.Changes {
		if !kindWord.MatchString(c.Word) {
			add("слово рода изменения %q вне формы слова", c.Word)
		}
		if !changeSet[c.Change] {
			add("слово %s: род %q вне CREATED|UPDATED|DELETED", c.Word, c.Change)
		}
		if i > 0 && in.Changes[i-1].Word >= c.Word {
			add("таблица рода изменения не упорядочена по слову: %s после %s", c.Word, in.Changes[i-1].Word)
		}
		if c.Word == in.SignalChange {
			if c.Change != spec.FeedSignalChange {
				add("слово строки сигнала %s переводится в %s, форма notify/spec — %s", c.Word, c.Change, spec.FeedSignalChange)
			}
			signal++
		}
	}
	if signal != 1 {
		add("слово строки сигнала %q вне таблицы рода изменения", in.SignalChange)
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %w", ErrInputs, errors.Join(bad...))
	}
	return nil
}

// SignalChangeWord — слово журнала строки сигнала: единственное слово
// таблицы, переводимое в spec.FeedSignalChange. Ноль или два таких слова —
// отказ: строка сигнала не угадывается.
func SignalChangeWord(changes []Change) (string, error) {
	var words []string
	for _, c := range changes {
		if c.Change == spec.FeedSignalChange {
			words = append(words, c.Word)
		}
	}
	if len(words) != 1 {
		sort.Strings(words)
		return "", fmt.Errorf("%w: слов журнала, переводимых в %s (род строки сигнала, notify/spec), %d: %v — нужно ровно одно",
			ErrInputs, spec.FeedSignalChange, len(words), words)
	}
	return words[0], nil
}

// Attr — атрибут строки ленты, который ставит SQL-половина: вид формы и
// опускается ли ключ (у optional — «не задан» отсутствием ключа).
type Attr struct {
	Name     string
	Kind     string
	Optional bool
}

// AttrsOf — атрибуты, которые ставит тело версии version, по имени.
func AttrsOf(version string) ([]Attr, error) {
	v, ok := released[version]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrVersion, version)
	}
	return append([]Attr(nil), v.attrs...), nil
}

// ReservedAttr — атрибут SQL-половины, которого шаблон не объявляет: имя
// занято ключом окна постановки (notify/spec), значение функция берёт из
// колонки инициатора строки журнала (Р3).
const ReservedAttr = "initiator"
