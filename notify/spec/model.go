// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"time"

	"github.com/PRO-Robotech/corelib/notify/form"
)

// Class — класс шаблона (Р7; NTF-5 Р12): security | notice | obligation.
type Class string

// Классы шаблона.
const (
	ClassSecurity Class = "security"
	ClassNotice   Class = "notice"
	// ClassObligation — извещение оператора (NTF-5 Р12): только у владельца
	// ObligationOwner, атрибуты только ObligationAttrKinds, ключей ttl и
	// limits нет, отписки нет.
	ClassObligation Class = "obligation"
)

// Classes — закрытый набор классов шаблона.
func Classes() []Class { return []Class{ClassSecurity, ClassNotice, ClassObligation} }

// ObligationOwner — единственный владелец шаблонов класса obligation (NTF-5
// Р12, ТВ3): ключ ведомости источников сборки, а не сегмент пути каталога.
const ObligationOwner = "notify"

// ObligationAttrKinds — виды атрибута, допустимые у шаблона класса obligation
// (NTF-5 Р12, ТВ4): пользовательскому и операторскому тексту неоткуда взяться.
func ObligationAttrKinds() []form.Kind { return []form.Kind{form.KindTimestamp, form.KindPath} }

// Presence — обязательность атрибута (Д21): ключ есть у каждого атрибута,
// значений два, умолчания нет.
type Presence string

// Значения обязательности.
const (
	PresenceRequired Presence = "required"
	PresenceOptional Presence = "optional"
)

// Scope — область лимита постановки: адресат, инициатор либо проект (NTF-3
// Р14).
type Scope string

// Области лимита.
const (
	ScopeRecipient Scope = "recipient"
	ScopeInitiator Scope = "initiator"
	ScopeProject   Scope = "project"
)

// Scopes — закрытый набор областей лимита.
func Scopes() []Scope { return []Scope{ScopeRecipient, ScopeInitiator, ScopeProject} }

// Recipient — форма адресата шаблона (NTF-1 Р6, NTF-3 Р27): поле recipient
// notification.yaml. Поле необязательно; отсутствие — address, форма
// выпущенной версии формата (NTF1-A08: формат только расширяется).
type Recipient string

// Формы адресата.
const (
	RecipientAddress      Recipient = "address"
	RecipientSubject      Recipient = "subject"
	RecipientFanout       Recipient = "fanout"
	RecipientAccountOwner Recipient = "account_owner"
)

// Recipients — закрытый набор форм адресата.
func Recipients() []Recipient {
	return []Recipient{RecipientAddress, RecipientSubject, RecipientFanout, RecipientAccountOwner}
}

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

// Locales — закрытый набор локалей тела и темы (NTF-3 Р21): тело и тема
// обязательны на каждой локали набора.
func Locales() []string { return []string{"ru", "en"} }

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
// Recipient — форма адресата (без поля recipient — RecipientAddress).
type Template struct {
	Dir         string
	Name        string
	Class       Class
	Recipient   Recipient
	TTL         time.Duration
	Limits      []Limit
	Attrs       []Attr
	Subject     map[string]string
	Bodies      []Body
	Revision    Revision
	HasRevision bool
}

// RequiredList — перечень обязательного класса security владельца
// (RequiredSecurityFile, NTF-2 Р3): имена шаблонов в порядке файла. Формат
// судит форму (последовательность имён шаблона, без повторов); смысл перечня
// — класс, существование шаблона, непустоту — судит гейт владельца
// (NTF2-99 (а), (в), (г)).
type RequiredList struct {
	Names []string
}

// Catalog — проверенный каталог шаблонов владельца, упорядоченный по имени.
// RequiredSecurity — перечень обязательного класса из каталога; nil — файла
// перечня в каталоге нет (отсутствие отлично от пустого перечня).
type Catalog struct {
	Templates        []Template
	RequiredSecurity *RequiredList
}

// Census — знаменатель проверки: сколько прочитано. Unsubscribe — сколько
// блоков ссылки отписки прочитано в телах (каждый — находка: у формата
// отписки в теле нет).
type Census struct {
	Templates   int
	Files       int
	Blocks      int
	Unsubscribe int
}
