// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/notify/form"
)

// УК86 (а): сравнение лимитов — одна функция: scope побайтно, затем
// window_seconds целым числом. Пары, где строка расходится с числом (600 и
// 3600; «30m» = 1800 и «24h» = 86400), упорядочены по числу.
func TestUK86_LimitLessOrdersByScopeThenSecondsAsNumbers(t *testing.T) {
	r := func(sec int32) feed.Limit { return feed.Limit{Scope: feed.ScopeRecipient, WindowSeconds: sec, Max: 1} }
	i := func(sec int32) feed.Limit { return feed.Limit{Scope: feed.ScopeInitiator, WindowSeconds: sec, Max: 1} }
	require.True(t, feed.LimitLess(r(600), r(3600)))
	require.False(t, feed.LimitLess(r(3600), r(600)))
	require.True(t, feed.LimitLess(r(1800), r(86400)))
	require.False(t, feed.LimitLess(r(600), r(600)))
	require.True(t, feed.LimitLess(i(86400), r(60)), "scope побайтно: initiator < recipient")

	ls := []feed.Limit{r(86400), i(60), r(1800), r(3600), r(600)}
	sort.Slice(ls, func(a, b int) bool { return feed.LimitLess(ls[a], ls[b]) })
	require.Equal(t, []feed.Limit{i(60), r(600), r(1800), r(3600), r(86400)}, ls)
}

func validDesc() feed.TemplateDesc {
	return feed.TemplateDesc{
		Name: "probe-hello", Class: feed.ClassNotice, SchemaRev: 1, TTL: 72 * time.Hour, Recipient: feed.RecipientAddress,
		Limits: []feed.Limit{
			{Scope: feed.ScopeRecipient, WindowSeconds: 1800, Max: 1},
			{Scope: feed.ScopeRecipient, WindowSeconds: 86400, Max: 3},
		},
		Attrs: []feed.AttrDesc{{Name: "name", Kind: form.KindText, Presence: feed.PresenceRequired, Subject: true}},
	}
}

// УК86 (б), УК77, NTF1-B29 (ревизия): описание судится до SQL; нарушение —
// ErrAttrsInvalid с именем шаблона и правилом.
func TestUK86_DescValidationNamesTheTemplate(t *testing.T) {
	require.NoError(t, validDesc().Validate(), "близнец — возрастающее описание")

	cases := map[string]func(*feed.TemplateDesc){
		"убывающее окно одной области": func(d *feed.TemplateDesc) { d.Limits[0], d.Limits[1] = d.Limits[1], d.Limits[0] },
		"повтор пары (scope, window)":  func(d *feed.TemplateDesc) { d.Limits[1].WindowSeconds = 1800 },
		"ревизия 0":                    func(d *feed.TemplateDesc) { d.SchemaRev = 0 },
		"класс вне перечня":            func(d *feed.TemplateDesc) { d.Class = "bulk" },
		"ttl ноль":                     func(d *feed.TemplateDesc) { d.TTL = 0 },
		"ttl сверх 720h":               func(d *feed.TemplateDesc) { d.TTL = 721 * time.Hour },
		"окно ноль":                    func(d *feed.TemplateDesc) { d.Limits[0].WindowSeconds = 0 },
		"max ноль":                     func(d *feed.TemplateDesc) { d.Limits[0].Max = 0 },
		"область вне перечня":          func(d *feed.TemplateDesc) { d.Limits[0].Scope = "tenant" },
		"атрибут без имени":            func(d *feed.TemplateDesc) { d.Attrs[0].Name = "" },
		"повтор имени атрибута":        func(d *feed.TemplateDesc) { d.Attrs = append(d.Attrs, d.Attrs[0]) },
		"обязательность вне перечня":   func(d *feed.TemplateDesc) { d.Attrs[0].Presence = "maybe" },
		"тема на optional":             func(d *feed.TemplateDesc) { d.Attrs[0].Presence = feed.PresenceOptional },
		"имя шаблона пусто":            func(d *feed.TemplateDesc) { d.Name = "" },
		"форма адресата не объявлена":  func(d *feed.TemplateDesc) { d.Recipient = "" },
		"форма адресата вне перечня":   func(d *feed.TemplateDesc) { d.Recipient = "courier" },
		"лимит адресата у fanout":      func(d *feed.TemplateDesc) { d.Recipient = feed.RecipientFanout },
	}
	for name, mutate := range cases {
		d := validDesc()
		d.Limits = append([]feed.Limit(nil), d.Limits...)
		d.Attrs = append([]feed.AttrDesc(nil), d.Attrs...)
		mutate(&d)
		err := d.Validate()
		require.ErrorIs(t, err, feed.ErrAttrsInvalid, name)
		if d.Name != "" {
			require.Contains(t, err.Error(), d.Name, "%s: ошибка называет шаблон", name)
		}
	}
	require.Contains(t, func() string {
		d := validDesc()
		d.SchemaRev = 0
		return d.Validate().Error()
	}(), "schema_rev")
}

// NTF-3 Р14, Р27: близнецы отказов описания — лимит на проект у любой формы и
// форма fanout без лимита на адресата (с лимитом на проект) принимаются.
func TestDescAcceptsProjectScopeAndFanoutWithoutRecipientLimit(t *testing.T) {
	d := validDesc()
	// project < recipient побайтно (LimitLess): лимит проекта — первым.
	d.Limits = append([]feed.Limit{{Scope: feed.ScopeProject, WindowSeconds: 3600, Max: 200}}, d.Limits...)
	require.NoError(t, d.Validate(), "лимит на проект")

	f := validDesc()
	f.Recipient = feed.RecipientFanout
	f.Limits = []feed.Limit{{Scope: feed.ScopeProject, WindowSeconds: 3600, Max: 200}}
	require.NoError(t, f.Validate(), "fanout без лимита на адресата")
	for _, form := range feed.RecipientForms() {
		g := validDesc()
		g.Recipient = form
		g.Limits = nil
		require.NoError(t, g.Validate(), "форма %s", form)
	}
}
