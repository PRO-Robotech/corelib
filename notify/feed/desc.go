// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"fmt"
	"time"

	"github.com/PRO-Robotech/corelib/notify/form"
)

// Class — класс шаблона: security | notice.
type Class string

// Классы шаблона.
const (
	ClassSecurity Class = "security"
	ClassNotice   Class = "notice"
)

// Scope — область лимита: адресат либо инициатор.
type Scope string

// Области лимита.
const (
	ScopeRecipient Scope = "recipient"
	ScopeInitiator Scope = "initiator"
)

// Presence — обязательность атрибута (Д21).
type Presence string

// Значения обязательности.
const (
	PresenceRequired Presence = "required"
	PresenceOptional Presence = "optional"
)

// Границы ttl (Р7): expires_at = enqueued_at + ttl.
const (
	TTLMin = time.Second
	TTLMax = 720 * time.Hour
)

// Limit — лимит постановки: не больше Max писем на ключ области Scope за окно
// длиной WindowSeconds секунд.
type Limit struct {
	Scope         Scope
	WindowSeconds int32
	Max           int32
}

// LimitLess — ЕДИНСТВЕННОЕ сравнение лимитов (З5, УК86 (а)): Scope побайтно,
// затем WindowSeconds целым числом. Порядок строки длительности расходится с
// числом («24h» < «30m»), и такой порядок у Put дал бы взаимную блокировку с
// уборщиком (М26). Его зовут генератор и сторож Put.
func LimitLess(a, b Limit) bool {
	if a.Scope != b.Scope {
		return a.Scope < b.Scope
	}
	return a.WindowSeconds < b.WindowSeconds
}

// AttrDesc — объявленный атрибут шаблона. Subject — входит ли в тему.
type AttrDesc struct {
	Name     string
	Kind     form.Kind
	Presence Presence
	Subject  bool
}

// TemplateDesc — описание шаблона, которое порождает notifygen (литерал
// xDesc рядом с SendX). Рукописного описания в рабочем коде нет — это держит
// гейт ссылки на feed.Put (З16); Put судит описание сам и на упорядоченность
// генератора не полагается.
type TemplateDesc struct {
	Name      string
	Class     Class
	SchemaRev int32
	TTL       time.Duration
	Limits    []Limit
	Attrs     []AttrDesc
}

func (d TemplateDesc) invalid(rule string, a ...any) error {
	return fmt.Errorf("%w: шаблон %s: %s", ErrAttrsInvalid, d.Name, fmt.Sprintf(rule, a...))
}

// Validate судит описание (З7, шаг 2): имя, класс, ревизия ≥ 1, ttl в
// [TTLMin..TTLMax], лимиты строго возрастают по LimitLess (повтор пары
// (scope, window_seconds) — тоже нарушение), атрибуты с именем, видом и
// обязательностью, тема — только на required. Нарушение — ErrAttrsInvalid с
// именем шаблона.
func (d TemplateDesc) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("%w: шаблон без имени", ErrAttrsInvalid)
	}
	switch d.Class {
	case ClassSecurity, ClassNotice:
	default:
		return d.invalid("класс вне перечня")
	}
	if d.SchemaRev < 1 {
		return d.invalid("schema_rev < 1")
	}
	if d.TTL < TTLMin || d.TTL > TTLMax {
		return d.invalid("ttl вне [%s..%s]", TTLMin, TTLMax)
	}
	for i, l := range d.Limits {
		switch l.Scope {
		case ScopeRecipient, ScopeInitiator:
		default:
			return d.invalid("limits[%d]: область вне перечня", i)
		}
		if l.WindowSeconds <= 0 {
			return d.invalid("limits[%d]: window_seconds ≤ 0", i)
		}
		if l.Max < 1 {
			return d.invalid("limits[%d]: max < 1", i)
		}
		if i > 0 && !LimitLess(d.Limits[i-1], l) {
			return d.invalid("limits не возрастают строго по (scope, window_seconds) на позиции %d", i)
		}
	}
	seen := make(map[string]bool, len(d.Attrs))
	for i, a := range d.Attrs {
		if a.Name == "" {
			return d.invalid("attrs[%d]: атрибут без имени", i)
		}
		if seen[a.Name] {
			return d.invalid("атрибут %s объявлен дважды", a.Name)
		}
		seen[a.Name] = true
		switch a.Presence {
		case PresenceRequired:
		case PresenceOptional:
			if a.Subject {
				return d.invalid("атрибут %s: тема — только атрибуты required", a.Name)
			}
		default:
			return d.invalid("атрибут %s: обязательность вне перечня", a.Name)
		}
	}
	return nil
}

func (d TemplateDesc) hasScope(s Scope) bool {
	for _, l := range d.Limits {
		if l.Scope == s {
			return true
		}
	}
	return false
}
