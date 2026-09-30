// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// SiteForm — форма места ссылки.
type SiteForm int

// Формы места ссылки.
const (
	// SiteText — текст с подстановками {{ имя }}.
	SiteText SiteForm = iota + 1
	// SiteAttr — поле целиком — имя атрибута.
	SiteAttr
)

// RefSite — место ссылки блока на атрибут: Place — поле блока ("text",
// "path", "token", "items[2]", "pairs[1].value"), Text — текст места (для
// SiteAttr — имя атрибута).
type RefSite struct {
	Place string
	Form  SiteForm
	Text  string
}

// Refs — имена атрибутов, на которые ссылается место, в порядке появления.
// Подстановка не по форме в перечень не попадает: её отвергает валидатор, и в
// проверенный каталог она не доходит.
func (s RefSite) Refs() []string {
	if s.Form == SiteAttr {
		return []string{s.Text}
	}
	segs, err := Segments(s.Text)
	if err != nil {
		return nil
	}
	var out []string
	for _, seg := range segs {
		if seg.Attr != "" {
			out = append(out, seg.Attr)
		}
	}
	return out
}

// RefSites — ЕДИНСТВЕННАЯ функция мест ссылок блока (З4, CX1-51 (а)).
// Валидатор правил when, optional, секрета и рендер notify перебирают места
// ею. Исчерпывающий switch по закрытому набору видов, без default: новый вид
// без ветки — красный линт exhaustive и красная проба мест ссылок.
func RefSites(b Block) []RefSite {
	switch b.Kind {
	case BlockHeading, BlockP, BlockWarning, BlockCode:
		return []RefSite{{Place: "text", Form: SiteText, Text: b.Text}}
	case BlockButton:
		if b.Button.Token != "" {
			// литерал пути — не место ссылки: путь задаёт шаблон
			return []RefSite{{Place: "token", Form: SiteAttr, Text: b.Button.Token}}
		}
		return []RefSite{{Place: "path", Form: SiteAttr, Text: b.Button.Path}}
	case BlockList:
		out := make([]RefSite, 0, len(b.Items))
		for i, it := range b.Items {
			out = append(out, RefSite{Place: "items[" + strconv.Itoa(i+1) + "]", Form: SiteText, Text: it})
		}
		return out
	case BlockKV:
		out := make([]RefSite, 0, len(b.Pairs))
		for i, p := range b.Pairs {
			out = append(out, RefSite{Place: "pairs[" + strconv.Itoa(i+1) + "].value", Form: SiteText, Text: p.Value})
		}
		return out
	case BlockDivider:
		return nil
	}
	return nil
}

// Segment — кусок текста места ссылки: литерал либо подстановка атрибута.
type Segment struct {
	Literal string
	Attr    string
}

var (
	substRe    = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
	attrNameRe = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z][a-z0-9]*)*$`)
	errSubst   = errors.New("подстановка не по форме {{ имя атрибута }}")
)

// Segments разбирает текст на литералы и подстановки {{ имя }}. Иное
// содержимое скобок и непарные «{{», «}}» — ошибка.
func Segments(text string) ([]Segment, error) {
	var out []Segment
	last := 0
	for _, m := range substRe.FindAllStringSubmatchIndex(text, -1) {
		lit := text[last:m[0]]
		if strings.Contains(lit, "{{") || strings.Contains(lit, "}}") {
			return nil, errSubst
		}
		if lit != "" {
			out = append(out, Segment{Literal: lit})
		}
		name := strings.TrimSpace(text[m[2]:m[3]])
		if !attrNameRe.MatchString(name) {
			return nil, errSubst
		}
		out = append(out, Segment{Attr: name})
		last = m[1]
	}
	tail := text[last:]
	if strings.Contains(tail, "{{") || strings.Contains(tail, "}}") {
		return nil, errSubst
	}
	if tail != "" {
		out = append(out, Segment{Literal: tail})
	}
	return out, nil
}

// hasSubst — есть ли в литерале признак подстановки.
func hasSubst(s string) bool { return strings.Contains(s, "{{") || strings.Contains(s, "}}") }
