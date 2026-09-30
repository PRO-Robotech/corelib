// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package spec_test

import (
	"errors"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/spec"
)

// a01 читает каталог A01 из testdata в память: варианты строятся правкой
// копии по одному отличию на каталог.
func a01(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	for _, name := range []string{"notification.yaml", "body.ru.yaml", "revision.yaml"} {
		b, err := os.ReadFile(path.Join("testdata", "a01", "invite", name))
		require.NoError(t, err)
		fsys["invite/"+name] = &fstest.MapFile{Data: b}
	}
	return fsys
}

// edit заменяет old на new в файле; замена, не попавшая ни разу, — отказ:
// вариант без отличия проверял бы близнеца.
func edit(t *testing.T, fsys fstest.MapFS, file, old, new string) {
	t.Helper()
	f, ok := fsys[file]
	require.True(t, ok, "файла %s нет", file)
	require.Contains(t, string(f.Data), old, "правка не попала в %s", file)
	fsys[file] = &fstest.MapFile{Data: []byte(strings.Replace(string(f.Data), old, new, 1))}
}

func load(t *testing.T, fsys fstest.MapFS) (spec.Catalog, spec.Census, spec.Findings) {
	t.Helper()
	cat, census, err := spec.LoadFS(fsys, ".")
	if err == nil {
		return cat, census, nil
	}
	var fs spec.Findings
	require.True(t, errors.As(err, &fs), "ошибка не находки валидатора: %v", err)
	require.NotEmpty(t, fs)
	return cat, census, fs
}

func requireOne(t *testing.T, fs spec.Findings, rule, file string) spec.Finding {
	t.Helper()
	require.Len(t, fs, 1, "ровно одна находка, получено: %v", fs)
	require.Equal(t, rule, fs[0].Rule)
	require.Equal(t, file, fs[0].File)
	require.Contains(t, fs[0].Error(), rule, "текст находки называет правило")
	require.Contains(t, fs[0].Error(), file, "текст находки называет файл")
	return fs[0]
}

func requireHas(t *testing.T, fs spec.Findings, rule, file string) spec.Finding {
	t.Helper()
	for _, f := range fs {
		if f.Rule == rule && f.File == file {
			return f
		}
	}
	t.Fatalf("нет находки %q в %s среди %v", rule, file, fs)
	return spec.Finding{}
}

// NTF1-A01: находок ноль, знаменатель > 0.
func TestA01ValidTemplateIsAccepted(t *testing.T) {
	cat, census, fs := load(t, a01(t))
	require.Empty(t, fs)
	t.Logf("шаблонов %d, файлов %d, блоков %d", census.Templates, census.Files, census.Blocks)
	require.Equal(t, 1, census.Templates)
	require.Equal(t, 3, census.Files)
	require.Equal(t, 3, census.Blocks)

	require.Len(t, cat.Templates, 1)
	tpl := cat.Templates[0]
	require.Equal(t, "invite", tpl.Name)
	require.Equal(t, spec.ClassSecurity, tpl.Class)
	require.Len(t, tpl.Attrs, 2)
	require.Equal(t, "inviter_name", tpl.Attrs[0].Name)
	require.Equal(t, spec.PresenceRequired, tpl.Attrs[0].Presence)
	require.True(t, tpl.HasRevision)
	require.Equal(t, 1, tpl.Revision.Number)
	require.Len(t, tpl.Bodies, 1)
	require.Equal(t, spec.BlockButton, tpl.Bodies[0].Blocks[2].Kind)
	require.Equal(t, "token", tpl.Bodies[0].Blocks[2].Button.Token)
	require.Len(t, tpl.Limits, 2)
}

// Load читает каталог с диска тем же валидатором.
func TestLoadReadsTheDirectory(t *testing.T) {
	_, census, err := spec.Load(path.Join("testdata", "a01"))
	require.NoError(t, err)
	require.Equal(t, 1, census.Templates)
}

// NTF1-A02: блок вне закрытого набора — файл, номер блока, правило.
func TestA02BlockOutsideTheClosedSet(t *testing.T) {
	fsys := a01(t)
	edit(t, fsys, "invite/body.ru.yaml", `  - heading: "Вас пригласили"`, `  - html: "<b>…</b>"`)
	_, _, fs := load(t, fsys)
	f := requireOne(t, fs, spec.RuleBlockKind, "invite/body.ru.yaml")
	require.Equal(t, 1, f.Block)
	require.Contains(t, f.Error(), "блок 1")
}

// NTF1-A03: кнопка с полной ссылкой; с token без литерала пути; литерал вне формы.
func TestA03ButtonLinkIsPathOrToken(t *testing.T) {
	fsys := a01(t)
	edit(t, fsys, "invite/body.ru.yaml", `path: "/iam/invitations/accept"`, `path: "https://example.invalid/x"`)
	_, _, fs := load(t, fsys)
	f := requireOne(t, fs, spec.RuleLinkIsPathOrToken, "invite/body.ru.yaml")
	require.Equal(t, 3, f.Block)

	fsys = a01(t)
	edit(t, fsys, "invite/body.ru.yaml", `, path: "/iam/invitations/accept"`, ``)
	_, _, fs = load(t, fsys)
	f = requireOne(t, fs, spec.RuleButtonTokenNeedsLiteral, "invite/body.ru.yaml")
	require.Equal(t, 3, f.Block)

	fsys = a01(t)
	edit(t, fsys, "invite/body.ru.yaml", `path: "/iam/invitations/accept"`, `path: "/iam/invitations/accept/"`)
	_, _, fs = load(t, fsys)
	f = requireOne(t, fs, spec.RuleButtonPathForm, "invite/body.ru.yaml")
	require.Equal(t, 3, f.Block)
	require.Contains(t, f.Error(), "хвостовая", "находка называет правило формы пути")
}

// NTF1-A04: класс security без limits.
func TestA04SecurityWithoutLimits(t *testing.T) {
	fsys := a01(t)
	edit(t, fsys, "invite/notification.yaml", "limits:\n  - {scope: recipient, window: 24h, max: 3}\n  - {scope: initiator, window: 1h, max: 20}\n", "")
	_, _, fs := load(t, fsys)
	f := requireOne(t, fs, spec.RuleSecurityNeedsLimits, "invite/notification.yaml")
	require.Contains(t, f.Error(), "invite")
}

// NTF1-A05: ссылка на необъявленный атрибут — атрибут, файл, блок.
func TestA05UndeclaredAttribute(t *testing.T) {
	fsys := a01(t)
	edit(t, fsys, "invite/body.ru.yaml", `"{{ inviter_name }} приглашает`, `"{{ account_name }} приглашает`)
	_, _, fs := load(t, fsys)
	f := requireOne(t, fs, spec.RuleUndeclaredAttr, "invite/body.ru.yaml")
	require.Equal(t, 2, f.Block)
	require.Equal(t, "account_name", f.Detail)
	require.Contains(t, f.Error(), "account_name")
}

// NTF1-A06: локаль вне {ru}.
func TestA06LocaleOutsideTheSet(t *testing.T) {
	fsys := a01(t)
	fsys["invite/body.xx.yaml"] = fsys["invite/body.ru.yaml"]
	_, _, fs := load(t, fsys)
	requireOne(t, fs, spec.RuleLocale, "invite/body.xx.yaml")

	delete(fsys, "invite/body.ru.yaml")
	_, _, fs = load(t, fsys)
	requireHas(t, fs, spec.RuleLocale, "invite/body.xx.yaml")
}

// NTF1-A07: секретный атрибут вне допустимого блока; близнец — в блоке code.
func TestA07SecretOutsideItsBlocks(t *testing.T) {
	fsys := a01(t)
	edit(t, fsys, "invite/notification.yaml", "  token: {type: token", "  code: {type: secret, presence: required}\n  token: {type: token")
	edit(t, fsys, "invite/body.ru.yaml", `  - heading: "Вас пригласили"`, `  - heading: "Код {{ code }}"`)
	_, _, fs := load(t, fsys)
	f := requireOne(t, fs, spec.RuleSecretPlace, "invite/body.ru.yaml")
	require.Equal(t, 1, f.Block)

	fsys = a01(t)
	edit(t, fsys, "invite/notification.yaml", "  token: {type: token", "  code: {type: secret, presence: required}\n  token: {type: token")
	edit(t, fsys, "invite/body.ru.yaml", `  - heading: "Вас пригласили"`, `  - code: "{{ code }}"`)
	_, _, fs = load(t, fsys)
	require.Empty(t, fs, "близнец: секрет в блоке code")

	fsys = a01(t)
	edit(t, fsys, "invite/notification.yaml", "  token: {type: token", "  code: {type: secret, presence: required}\n  token: {type: token")
	edit(t, fsys, "invite/body.ru.yaml", `  - heading: "Вас пригласили"`, `  - code: "{{ code }}"`)
	edit(t, fsys, "invite/notification.yaml", `ru: "Приглашение в облако"`, `ru: "Код {{ code }}"`)
	_, _, fs = load(t, fsys)
	requireOne(t, fs, spec.RuleSecretPlace, "invite/notification.yaml")
}

// NTF1-A10: нулевое значение поля формата — не значение. По одному отличию на
// каталог, ровно одна находка.
func TestA10ZeroValueOfAFormatFieldIsNotAValue(t *testing.T) {
	const n, b, r = "invite/notification.yaml", "invite/body.ru.yaml", "invite/revision.yaml"
	cases := []struct {
		name, file, old, new, rule, field string
	}{
		{"(а) presence нет", n, "inviter_name: {type: text, presence: required}", "inviter_name: {type: text}", spec.RulePresenceMissing, "attributes.inviter_name.presence"},
		{"(б) ttl 0s", n, "ttl: 168h", "ttl: 0s", spec.RuleTTLPositive, "ttl"},
		{"(в) ttl нет", n, "ttl: 168h\n", "", spec.RuleTTLPositive, "ttl"},
		{"(г) число limits 0", n, "window: 1h, max: 20", "window: 1h, max: 0", spec.RuleLimitMax, "limits[2].max"},
		{"(д) тема пуста", n, `ru: "Приглашение в облако"`, `ru: ""`, spec.RuleSubjectEmpty, "subject.ru"},
		{"(е) блоков нет", b, "blocks:\n" + bodyBlocksA01, "blocks: []\n", spec.RuleBodyEmpty, "blocks"},
		{"(ж) тип пуст", n, "inviter_name: {type: text,", `inviter_name: {type: "",`, spec.RuleAttrKind, "attributes.inviter_name.type"},
		{"(з) class нет", n, "class: security\n", "", spec.RuleClass, "class"},
		{"(и) ревизия 0", r, "revision: 1", "revision: 0", spec.RuleRevision, "revision"},
		{"(к) ttl 721h", n, "ttl: 168h", "ttl: 721h", spec.RuleTTLRange, "ttl"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fsys := a01(t)
			edit(t, fsys, c.file, c.old, c.new)
			_, _, fs := load(t, fsys)
			f := requireOne(t, fs, c.rule, c.file)
			require.Equal(t, c.field, f.Field)
			require.Contains(t, f.Error(), c.field, "находка называет поле")
		})
	}
}

const bodyBlocksA01 = `  - heading: "Вас пригласили"
  - p: "{{ inviter_name }} приглашает вас в облако."
  - button: {text: "Принять приглашение", token: token, path: "/iam/invitations/accept"}
`

// Близнецы A10 на границах: ttl 1s, каждое число limits 1, ревизия 1 — и
// верхняя граница ttl 720h.
func TestA10TwinsOnTheBoundaries(t *testing.T) {
	for _, ttl := range []string{"1s", "720h"} {
		fsys := a01(t)
		edit(t, fsys, "invite/notification.yaml", "ttl: 168h", "ttl: "+ttl)
		edit(t, fsys, "invite/notification.yaml", "max: 3", "max: 1")
		edit(t, fsys, "invite/notification.yaml", "max: 20", "max: 1")
		_, _, fs := load(t, fsys)
		require.Empty(t, fs, "ttl %s", ttl)
	}
	// ниже нижней границы и не в целых секундах — вне формы
	for ttl, rule := range map[string]string{"500ms": spec.RuleTTLRange, "1500ms": spec.RuleTTLWholeSeconds, "-1s": spec.RuleTTLPositive} {
		fsys := a01(t)
		edit(t, fsys, "invite/notification.yaml", "ttl: 168h", "ttl: "+ttl)
		_, _, fs := load(t, fsys)
		requireOne(t, fs, rule, "invite/notification.yaml")
	}
}

// a11 — каталог Given A11: inviter_name optional, тело из четырёх блоков.
func a11(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := a01(t)
	edit(t, fsys, "invite/notification.yaml", "inviter_name: {type: text, presence: required}", "inviter_name: {type: text, presence: optional}")
	fsys["invite/body.ru.yaml"] = &fstest.MapFile{Data: []byte(`blocks:
  - heading: "Вас пригласили"
  - p: "{{ inviter_name }} приглашает вас в облако."
    when: inviter_name
  - p: "Приглашение действует неделю."
  - button: {text: "Принять приглашение", token: token, path: "/iam/invitations/accept"}
`)}
	return fsys
}

// NTF1-A11: атрибут optional — значение ключа из двух, блок с ним — только под
// when, в теме его нет.
func TestA11OptionalAttribute(t *testing.T) {
	const n, b = "invite/notification.yaml", "invite/body.ru.yaml"
	_, _, fs := load(t, a11(t))
	require.Empty(t, fs, "близнец: каталог Given без отличий")

	cases := []struct {
		name, file, old, new, rule string
		block                      int
	}{
		{"(а) presence maybe", n, "presence: optional", "presence: maybe", spec.RulePresenceValue, 0},
		{"(б) p без when ссылается", b, `  - p: "Приглашение действует неделю."`, `  - p: "От {{ inviter_name }}."`, spec.RuleOptionalNeedsWhen, 3},
		{"(в) тема ссылается", n, `ru: "Приглашение в облако"`, `ru: "Приглашение от {{ inviter_name }}"`, spec.RuleSubjectRequiredOnly, 0},
		{"(г) when на required", b, "when: inviter_name", "when: token", spec.RuleWhenOnRequired, 2},
		{"(д) when на необъявленном", b, "when: inviter_name", "when: account_name", spec.RuleWhenUndeclared, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fsys := a11(t)
			edit(t, fsys, c.file, c.old, c.new)
			_, _, fs := load(t, fsys)
			f := requireOne(t, fs, c.rule, c.file)
			require.Equal(t, c.block, f.Block)
		})
	}
}

// Каталог шаблона с посторонним файлом, неизвестным ключом и повтором ключа —
// находки, а не молчание: формат закрыт.
func TestFormatIsClosed(t *testing.T) {
	fsys := a01(t)
	fsys["invite/notes.txt"] = &fstest.MapFile{Data: []byte("x")}
	_, _, fs := load(t, fsys)
	requireOne(t, fs, spec.RuleFile, "invite/notes.txt")

	fsys = a01(t)
	edit(t, fsys, "invite/notification.yaml", "ttl: 168h", "ttl: 168h\nreply_to: x@example.invalid")
	_, _, fs = load(t, fsys)
	requireOne(t, fs, spec.RuleUnknownKey, "invite/notification.yaml")

	fsys = a01(t)
	edit(t, fsys, "invite/notification.yaml", "ttl: 168h", "ttl: 168h\nttl: 1h")
	_, _, fs = load(t, fsys)
	requireOne(t, fs, spec.RuleDuplicateKey, "invite/notification.yaml")

	fsys = a01(t)
	edit(t, fsys, "invite/notification.yaml", "name: invite", "name: other")
	_, _, fs = load(t, fsys)
	requireOne(t, fs, spec.RuleName, "invite/notification.yaml")

	fsys = a01(t)
	edit(t, fsys, "invite/body.ru.yaml", `"{{ inviter_name }} приглашает`, `"{{ inviter_name | upper }} приглашает`)
	_, _, fs = load(t, fsys)
	requireOne(t, fs, spec.RuleRef, "invite/body.ru.yaml")

	fsys = a01(t)
	edit(t, fsys, "invite/body.ru.yaml", `text: "Принять приглашение"`, `text: "Принять от {{ inviter_name }}"`)
	_, _, fs = load(t, fsys)
	requireOne(t, fs, spec.RuleLiteralNoRef, "invite/body.ru.yaml")

	fsys = a01(t)
	edit(t, fsys, "invite/notification.yaml", "  token: {type: token", "  to: {type: text, presence: required}\n  token: {type: token")
	_, _, fs = load(t, fsys)
	requireOne(t, fs, spec.RuleAttrName, "invite/notification.yaml")
}

// Пустой каталог — перепись ноль, а не находка и не «проверено»: вызывающий
// судит знаменатель сам.
func TestEmptyCatalogCensusIsZero(t *testing.T) {
	cat, census, err := spec.LoadFS(fstest.MapFS{"x/.keep": {}}, "x")
	require.NoError(t, err)
	require.Empty(t, cat.Templates)
	require.Equal(t, spec.Census{}, census)
}
