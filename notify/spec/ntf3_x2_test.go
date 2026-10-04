// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec_test

// Пробы полосы X2 NTF-3 (приёмка sub-phase-NTF-3, отпечаток ac1f9fc9…) на
// формате шаблона и единственных объявлениях notify/spec: набор локалей
// {ru, en} (Р21), поле recipient (Р27), область лимита project (Р14), закрытый
// набор отношений справочника {v_get} (З27, CX3B-13), форма строки сигнала
// notification_feed:<модуль> (З10 п.5).
//
// Контракт, который пробы называют (выбор полосы RED): spec.DirectoryRelations()
// и spec.FeedSignal(module) (SignalRow, error) с полями Key, ObjectType, Object.

import (
	"regexp"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed"
	"github.com/PRO-Robotech/corelib/notify/spec"
)

// withEN — a01 с телом и темой на локали en.
func withEN(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := a01(t)
	fsys["invite/body.en.yaml"] = &fstest.MapFile{Data: fsys["invite/body.ru.yaml"].Data}
	edit(t, fsys, "invite/notification.yaml", `  ru: "Приглашение в облако"`, "  ru: \"Приглашение в облако\"\n  en: \"Invitation to the cloud\"")
	return fsys
}

var wordEN = regexp.MustCompile(`(^|[^a-z])en([^a-z]|$)`)

func namesLocaleEN(f spec.Finding) bool {
	return f.Field == "subject.en" || f.File == "invite/body.en.yaml" || wordEN.MatchString(f.Detail)
}

// Р21: набор локалей — ровно {ru, en}; объявление одно (spec.Locales).
func TestNTF3_R21_LocaleSetIsRuEn(t *testing.T) {
	got := slices.Clone(spec.Locales())
	slices.Sort(got)
	require.Equal(t, []string{"en", "ru"}, got)
}

// Р21: шаблон с телом и темой на каждой локали набора принят; близнец без en —
// находка, называющая локаль en (тела на локали набора нет).
func TestNTF3_R21_EveryTemplateHasEveryLocale(t *testing.T) {
	_, census, fs := load(t, withEN(t))
	require.Empty(t, fs, "шаблон на {ru, en} обязан приниматься")
	require.Equal(t, 4, census.Files)

	_, _, fs = load(t, a01(t))
	require.NotEmpty(t, fs, "шаблон без локали en обязан отвергаться")
	require.True(t, slices.ContainsFunc(fs, namesLocaleEN), "находка не называет локаль en: %v", fs)
}

// Р27: поле recipient notification.yaml — закрытый набор
// {address, subject, fanout, account_owner}; значение вне набора — находка на
// поле recipient. Близнецы — каждое значение набора.
func TestNTF3_150_RecipientFieldIsAClosedSet(t *testing.T) {
	for _, form := range []string{"address", "subject", "fanout", "account_owner"} {
		t.Run(form, func(t *testing.T) {
			fsys := withEN(t)
			edit(t, fsys, "invite/notification.yaml", "ttl: 168h", "ttl: 168h\nrecipient: "+form)
			_, _, fs := load(t, fsys)
			require.Empty(t, fs, "recipient: %s", form)
		})
	}
	fsys := withEN(t)
	edit(t, fsys, "invite/notification.yaml", "ttl: 168h", "ttl: 168h\nrecipient: courier")
	_, _, fs := load(t, fsys)
	require.Len(t, fs, 1, "ровно одна находка: %v", fs)
	require.Equal(t, "invite/notification.yaml", fs[0].File)
	require.Equal(t, "recipient", fs[0].Field)
}

// Р14: область лимита project принята; близнец — область вне набора (tenant) —
// RuleLimitScope.
func TestNTF3_R14_ProjectLimitScope(t *testing.T) {
	fsys := withEN(t)
	edit(t, fsys, "invite/notification.yaml", "{scope: initiator, window: 1h, max: 20}", "{scope: project, window: 1h, max: 200}")
	cat, _, fs := load(t, fsys)
	require.Empty(t, fs, "scope: project")
	require.Len(t, cat.Templates, 1)
	require.Equal(t, spec.Scope("project"), cat.Templates[0].Limits[1].Scope)

	fsys = withEN(t)
	edit(t, fsys, "invite/notification.yaml", "{scope: initiator, window: 1h, max: 20}", "{scope: tenant, window: 1h, max: 200}")
	_, _, fs = load(t, fsys)
	requireOne(t, fs, spec.RuleLimitScope, "invite/notification.yaml")
}

// З27, CX3B-13: закрытый набор отношений справочника объявлен один раз в
// notify/spec и равен {v_get}.
func TestNTF3_29_DirectoryRelationSetIsVGet(t *testing.T) {
	require.Equal(t, []string{"v_get"}, spec.DirectoryRelations())
}

// З10 п.5: форма строки сигнала — одна, в notify/spec: ключ журнала
// notification (тот же, что feed.JournalKey), тип модели notification_feed,
// объект notification_feed:<модуль>. Имя модуля не DNS-метка — отказ; близнец —
// storage.
func TestNTF3_Z10_FeedSignalRowHasOneForm(t *testing.T) {
	row, err := spec.FeedSignal("storage")
	require.NoError(t, err)
	require.Equal(t, "notification", row.Key)
	require.Equal(t, feed.JournalKey, row.Key, "Go-половина (feed.Put шаг 6) пишет тот же ключ")
	require.Equal(t, "notification_feed", row.ObjectType)
	require.Equal(t, "notification_feed:storage", row.Object)

	for _, bad := range []string{"", "Storage", "storage_x"} {
		_, err := spec.FeedSignal(bad)
		require.Error(t, err, "модуль %q", bad)
	}
}
