// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"strconv"
	"strings"
)

// Правила валидатора. Текст правила — часть находки: его читает автор
// шаблона и проверяют пробы (NTF1-A02…A07, A10, A11).
const (
	RuleYAML                    = "файл не разбирается как YAML"
	RuleFile                    = "файл вне раскладки шаблона"
	RuleNoNotification          = "каталог шаблона без notification.yaml"
	RuleUnknownKey              = "ключ вне формата"
	RuleDuplicateKey            = "ключ повторён"
	RuleFieldForm               = "значение поля не той формы"
	RuleMissingField            = "обязательное поле отсутствует"
	RuleName                    = "имя шаблона — имя его каталога формы [a-z][a-z0-9]*(-[a-z][a-z0-9]*)*"
	RuleClass                   = "класс из закрытого набора"
	RuleTTLPositive             = "ttl — положительная длительность"
	RuleTTLRange                = "ttl в [1s..720h]"
	RuleTTLWholeSeconds         = "ttl — целое число секунд"
	RuleLimitMax                = "число limits ≥ 1"
	RuleLimitWindow             = "окно лимита — положительная длительность в целых секундах"
	RuleLimitScope              = "область лимита — recipient или initiator"
	RuleSecurityNeedsLimits     = "класс security требует limits"
	RuleAttrName                = "имя атрибута формы [a-z][a-z0-9]*(_[a-z][a-z0-9]*)*, кроме to и initiator"
	RuleAttrKind                = "тип атрибута из закрытого набора"
	RulePresenceMissing         = "обязательность атрибута не объявлена"
	RulePresenceValue           = "обязательность — required или optional"
	RuleSubjectEmpty            = "тема непуста"
	RuleSubjectRequiredOnly     = "тема — только атрибуты required"
	RuleLocale                  = "локаль вне {ru}"
	RuleLocalesAgree            = "у темы и тела одни и те же локали"
	RuleBodyEmpty               = "тело — хотя бы один блок"
	RuleBlockKind               = "блок вне закрытого набора"
	RuleBlockEmpty              = "блок непуст"
	RuleRef                     = "подстановка — {{ имя атрибута }}"
	RuleLiteralNoRef            = "подстановка вне места ссылки"
	RuleUndeclaredAttr          = "ссылка на необъявленный атрибут"
	RuleSecretPlace             = "секретный атрибут допустим только в блоках code и button.token" // #nosec G101 -- текст правила валидатора, не учётные данные
	RuleLinkIsPathOrToken       = "ссылка — путь или токен; origin задаёт установка"               // #nosec G101 -- текст правила валидатора, не учётные данные
	RuleButtonPathForm          = "литерал пути кнопки вне формы пути"
	RuleButtonTokenNeedsLiteral = "кнопка с token требует литерал пути" // #nosec G101 -- текст правила валидатора, не учётные данные
	RuleButtonPathAttr          = "path кнопки без token — атрибут вида path"
	RuleButtonTokenAttr         = "token кнопки — атрибут вида token" // #nosec G101 -- текст правила валидатора, не учётные данные
	RuleOptionalNeedsWhen       = "атрибут optional — только в блоке с when"
	RuleWhenOnOther             = "when блока — на том атрибуте optional, на который блок ссылается"
	RuleWhenOnRequired          = "when — только на атрибуте optional"
	RuleWhenUndeclared          = "when на необъявленном атрибуте"
	RuleRevision                = "ревизия — целое ≥ 1"
	RuleFingerprintForm         = "отпечаток набора — v<N>:sha256:<64 hex>"
)

// Finding — находка валидатора: файл (от корня каталога), строка, номер
// блока (с 1; 0 — не о блоке), поле, правило и уточнение (имя атрибута,
// причина формы). Значений атрибутов находка не несёт — их у шаблона нет.
type Finding struct {
	File   string
	Line   int
	Block  int
	Field  string
	Rule   string
	Detail string
}

// Error — находка одной строкой: файл, строка, блок, поле, правило, уточнение.
func (f Finding) Error() string {
	var b strings.Builder
	b.WriteString(f.File)
	if f.Line > 0 {
		b.WriteString(":" + strconv.Itoa(f.Line))
	}
	if f.Block > 0 {
		b.WriteString(": блок " + strconv.Itoa(f.Block))
	}
	if f.Field != "" {
		b.WriteString(": поле " + f.Field)
	}
	b.WriteString(": " + f.Rule)
	if f.Detail != "" {
		b.WriteString(" (" + f.Detail + ")")
	}
	return b.String()
}

// Findings — все находки проверки каталога; как ошибка Load они возвращаются
// целиком, а не первой.
type Findings []Finding

// Error — находки построчно с их числом.
func (fs Findings) Error() string {
	lines := make([]string, 0, len(fs)+1)
	lines = append(lines, "notify/spec: находок "+strconv.Itoa(len(fs)))
	for _, f := range fs {
		lines = append(lines, f.Error())
	}
	return strings.Join(lines, "\n")
}
