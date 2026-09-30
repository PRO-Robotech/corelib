// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"time"

	"github.com/PRO-Robotech/corelib/notify/form"
)

// Class — класс шаблона (Р7): security | notice.
type Class string

// Классы шаблона.
const (
	ClassSecurity Class = "security"
	ClassNotice   Class = "notice"
)

// Presence — обязательность атрибута (Д21): ключ есть у каждого атрибута,
// значений два, умолчания нет.
type Presence string

// Значения обязательности.
const (
	PresenceRequired Presence = "required"
	PresenceOptional Presence = "optional"
)

// Scope — область лимита постановки: адресат либо инициатор.
type Scope string

// Области лимита.
const (
	ScopeRecipient Scope = "recipient"
	ScopeInitiator Scope = "initiator"
)

// BlockKind — вид блока тела. Набор закрыт (Р7).
type BlockKind string

// Виды блока тела.
const (
	BlockHeading BlockKind = "heading"
	BlockP       BlockKind = "p"
	BlockButton  BlockKind = "button"
	BlockCode    BlockKind = "code"
	BlockList    BlockKind = "list"
	BlockKV      BlockKind = "kv"
	BlockWarning BlockKind = "warning"
	BlockDivider BlockKind = "divider"
)

// BlockKinds — закрытый набор видов блока; единственное его объявление. Проба
// мест ссылок берёт перечень отсюда (CX1-51).
func BlockKinds() []BlockKind {
	return []BlockKind{BlockHeading, BlockP, BlockButton, BlockCode, BlockList, BlockKV, BlockWarning, BlockDivider}
}

// Locales — закрытый набор локалей тела и темы.
func Locales() []string { return []string{"ru"} }

// Границы ttl (Р7).
const (
	TTLMin = time.Second
	TTLMax = 720 * time.Hour
)

// Limit — лимит постановки: не больше Max писем за окно Window на ключ Scope.
type Limit struct {
	Scope  Scope
	Window time.Duration
	Max    int
}

// Attr — объявленный атрибут шаблона. Subject — входит ли атрибут в тему
// (выводится из ссылок темы, а не объявляется).
type Attr struct {
	Name     string
	Kind     form.Kind
	Presence Presence
	Subject  bool
}

// Pair — пара блока kv: Key — литерал, Value — текст с подстановками.
type Pair struct {
	Key   string
	Value string
}

// Button — кнопка. Две формы (Р7):
//   - Token пуст: Path — имя атрибута вида path;
//   - Token — имя атрибута вида token, Path — литерал пути формы form.ParsePath.
type Button struct {
	Text  string
	Path  string
	Token string
}

// Block — блок тела. Index — номер блока в файле, с 1. Поле по виду: Text у
// heading, p, warning, code; Items у list; Pairs у kv; Button у button; у
// divider полей нет. When — имя атрибута optional, при незаданном значении
// которого блок в письмо не попадает.
type Block struct {
	Index  int
	Kind   BlockKind
	When   string
	Text   string
	Items  []string
	Pairs  []Pair
	Button Button
}

// Body — тело одной локали.
type Body struct {
	Locale string
	Blocks []Block
}

// Revision — содержимое revision.yaml: номер ревизии и отпечаток набора
// ревизии в написании v<N>:sha256:<hex>.
type Revision struct {
	Number      int
	Fingerprint string
}

// Template — проверенный шаблон. Attrs упорядочены по имени, Bodies — по
// локали, Limits — в порядке файла.
type Template struct {
	Dir         string
	Name        string
	Class       Class
	TTL         time.Duration
	Limits      []Limit
	Attrs       []Attr
	Subject     map[string]string
	Bodies      []Body
	Revision    Revision
	HasRevision bool
}

// Catalog — проверенный каталог шаблонов владельца, упорядоченный по имени.
type Catalog struct {
	Templates []Template
}

// Census — знаменатель проверки: сколько прочитано.
type Census struct {
	Templates int
	Files     int
	Blocks    int
}
