// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// obligation_test.go — класс obligation в едином валидаторе (приёмка NTF-5
// kacho#2924, Р12, §3 З1, З19; NTF5-55, NTF5-56, NTF5-117; замысел
// issue-2924 З10 п.1, CX5-20; маршрут C5).
//
// КОНТРАКТ, который зовут пробы (на базе полосы его нет):
//
//	func spec.LoadOwnerFS(fsys fs.FS, root, owner string) (spec.Catalog, spec.Census, error)
//
// owner — ключ ведомости источников сборки (NTF-3 sources.yaml; пространство
// ленты), а не сегмент пути каталога (CX5-20). Класс obligation допустим
// только у владельца notify; атрибуты шаблона obligation — только timestamp и
// path; ключей ttl и limits у него нет (Р12). Находки — тем же типом
// spec.Findings, что у LoadFS.
//
// Порядок каждой пробы несущий: сначала посылка фикстуры (тот же шаблон
// классом notice с ttl проходит нынешний LoadFS — фикстура формата исправна),
// затем положительный близнец (владелец notify, шаблон без нарушения — находок
// 0), затем отрицательный вариант, отличающийся от близнеца ровно одним
// фактом. Так сломанная фикстура не выдаёт себя за отсутствующую возможность.
//
// Имена атрибутов — в форме формата NTF-1 (snake_case, RuleAttrName):
// приёмка пишет startsAt, endsAt, accountName, и эти имена формат отвергает
// своим правилом независимо от класса — с ними отрицательный вариант NTF5-56
// краснел бы именем атрибута ДО всякого правила obligation.
package spec_test

import (
	"errors"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/spec"
)

// obligationNotification — шаблон NTF5-57 в форме формата: класс obligation,
// атрибуты starts_at, ends_at (timestamp) и link (path), без ttl и limits.
const obligationNotification = `name: notice-outage-started
class: obligation
attributes:
  starts_at: {type: timestamp, presence: required}
  ends_at: {type: timestamp, presence: required}
  link: {type: path, presence: required}
subject:
  ru: "Сбой сервиса"
  en: "Service outage"
`

const obligationBodyRU = `blocks:
  - heading: "Сбой сервиса"
  - kv: [{key: "Начало", value: "{{ starts_at }}"}, {key: "Окончание", value: "{{ ends_at }}"}]
  - button: {text: "Подробнее", path: link}
`

const obligationBodyEN = `blocks:
  - heading: "Service outage"
  - kv: [{key: "Started", value: "{{ starts_at }}"}, {key: "Ends", value: "{{ ends_at }}"}]
  - button: {text: "Details", path: link}
`

// obligationFS — каталог из одного шаблона name под корнем root.
func obligationFS(root, name, notification, bodyRU, bodyEN string) fstest.MapFS {
	p := path.Join(root, name) + "/"
	return fstest.MapFS{
		p + "notification.yaml": &fstest.MapFile{Data: []byte(notification)},
		p + "body.ru.yaml":      &fstest.MapFile{Data: []byte(bodyRU)},
		p + "body.en.yaml":      &fstest.MapFile{Data: []byte(bodyEN)},
	}
}

// replaceOnce — правка с проверкой, что она попала: вариант без отличия
// проверял бы близнеца.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	require.Contains(t, s, old, "правка фикстуры не попала")
	return strings.Replace(s, old, new, 1)
}

// findingsOf — находки валидатора; ошибка не находок — отказ пробы.
func findingsOf(t *testing.T, err error) spec.Findings {
	t.Helper()
	if err == nil {
		return nil
	}
	var fs spec.Findings
	require.True(t, errors.As(err, &fs), "ошибка не находки валидатора: %v", err)
	return fs
}

// requireFixtureFormat — посылка: тот же шаблон классом notice с ttl принят
// нынешним валидатором. Иначе фикстура сломана форматом, а не классом.
func requireFixtureFormat(t *testing.T, notification, bodyRU, bodyEN string) {
	t.Helper()
	asNotice := replaceOnce(t, notification, "class: obligation\n", "class: notice\nttl: 24h\n")
	_, census, err := spec.LoadFS(obligationFS(".", "notice-outage-started", asNotice, bodyRU, bodyEN), ".")
	require.NoError(t, err, "ФИКСТУРА: шаблон классом notice с ttl не принят форматом — проба беспредметна")
	require.Equal(t, 1, census.Templates)
}

// requireAccepted — положительный близнец: владелец, шаблон без нарушения,
// находок 0, класс obligation, срока нет.
func requireAccepted(t *testing.T, fsys fstest.MapFS, root, owner string) spec.Template {
	t.Helper()
	cat, census, err := spec.LoadOwnerFS(fsys, root, owner)
	require.Empty(t, findingsOf(t, err), "близнец (владелец %s): находок ждали 0", owner)
	require.Equal(t, 1, census.Templates)
	require.Len(t, cat.Templates, 1)
	tpl := cat.Templates[0]
	require.Equal(t, spec.Class("obligation"), tpl.Class)
	require.Zero(t, tpl.TTL, "у шаблона obligation срока нет (Р12, §3 З19)")
	require.Empty(t, tpl.Limits, "у шаблона obligation лимитов отключения нет (Р12)")
	return tpl
}

// requireOneNaming — ровно одна находка, и её текст называет каждое из parts.
func requireOneNaming(t *testing.T, err error, parts ...string) spec.Finding {
	t.Helper()
	fs := findingsOf(t, err)
	require.Len(t, fs, 1, "ровно одна находка, получено: %v", fs)
	for _, p := range parts {
		require.Contains(t, fs[0].Error(), p, "находка называет %q", p)
	}
	return fs[0]
}

// NTF5-55: шаблон класса obligation у владельца, отличного от notify, —
// отказ сборки с именем владельца и шаблона. Близнец — тот же каталог у
// владельца notify; факт один — владелец.
func TestNTF5_55_ObligationOfAForeignOwnerIsRefused(t *testing.T) {
	notification := `name: x
class: obligation
attributes:
  starts_at: {type: timestamp, presence: required}
subject:
  ru: "Плановые работы"
  en: "Planned maintenance"
`
	bodyRU := "blocks:\n  - heading: \"Работы\"\n  - p: \"Начало: {{ starts_at }}\"\n"
	bodyEN := "blocks:\n  - heading: \"Maintenance\"\n  - p: \"Starts: {{ starts_at }}\"\n"
	asNotice := replaceOnce(t, notification, "class: obligation\n", "class: notice\nttl: 24h\n")
	_, _, err := spec.LoadFS(obligationFS(".", "x", asNotice, bodyRU, bodyEN), ".")
	require.NoError(t, err, "ФИКСТУРА: шаблон x классом notice не принят форматом")

	fsys := obligationFS(".", "x", notification, bodyRU, bodyEN)
	requireAccepted(t, fsys, ".", "notify")

	_, _, err = spec.LoadOwnerFS(fsys, ".", "storage")
	requireOneNaming(t, err, "storage", "x/notification.yaml")
}

// CX5-20 (замысел З10 п.1): владелец — ключ ведомости, а не сегмент пути.
// Каталог вложен как …/notify/notifications/<шаблон> у чужого источника
// storage — отказ называет владельца-источника. Близнец — тот же путь у
// владельца notify; факт один — владелец.
func TestNTF5_ObligationOwnerIsTheLedgerKeyNotAPathSegment(t *testing.T) {
	requireFixtureFormat(t, obligationNotification, obligationBodyRU, obligationBodyEN)
	const nested = "services/storage/internal/notify/notifications"
	fsys := obligationFS(nested, "notice-outage-started", obligationNotification, obligationBodyRU, obligationBodyEN)
	requireAccepted(t, fsys, nested, "notify")

	_, _, err := spec.LoadOwnerFS(fsys, nested, "storage")
	requireOneNaming(t, err, "storage", "notice-outage-started")
}

// NTF5-56: атрибут типа text в шаблоне класса obligation — отказ сборки с
// именем атрибута. Близнец — тот же атрибут типа timestamp; факт один — тип.
func TestNTF5_56_TextAttributeOfObligationIsRefused(t *testing.T) {
	requireFixtureFormat(t, obligationNotification, obligationBodyRU, obligationBodyEN)
	withAttr := func(kind string) string {
		return replaceOnce(t, obligationNotification, "  link: {type: path, presence: required}\n",
			"  link: {type: path, presence: required}\n  account_name: {type: "+kind+", presence: required}\n")
	}
	bodyRU := replaceOnce(t, obligationBodyRU, `  - heading: "Сбой сервиса"`, `  - heading: "Сбой сервиса"
  - p: "{{ account_name }}"`)
	bodyEN := replaceOnce(t, obligationBodyEN, `  - heading: "Service outage"`, `  - heading: "Service outage"
  - p: "{{ account_name }}"`)

	twin := obligationFS(".", "notice-outage-started", withAttr("timestamp"), bodyRU, bodyEN)
	tpl := requireAccepted(t, twin, ".", "notify")
	require.Len(t, tpl.Attrs, 4)

	bad := obligationFS(".", "notice-outage-started", withAttr("text"), bodyRU, bodyEN)
	_, _, err := spec.LoadOwnerFS(bad, ".", "notify")
	requireOneNaming(t, err, "account_name", "notice-outage-started/notification.yaml")
}

// NTF5-117: ключ ttl в шаблоне класса obligation — отказ сборки, находка
// называет шаблон и ключ ttl. Близнец (NTF5-57) — тот же шаблон без ttl;
// факт один — ключ ttl задан / не задан.
func TestNTF5_117_TTLOfObligationIsRefused(t *testing.T) {
	requireFixtureFormat(t, obligationNotification, obligationBodyRU, obligationBodyEN)
	twin := obligationFS(".", "notice-outage-started", obligationNotification, obligationBodyRU, obligationBodyEN)
	requireAccepted(t, twin, ".", "notify")

	withTTL := replaceOnce(t, obligationNotification, "class: obligation\n", "class: obligation\nttl: 720h\n")
	bad := obligationFS(".", "notice-outage-started", withTTL, obligationBodyRU, obligationBodyEN)
	_, _, err := spec.LoadOwnerFS(bad, ".", "notify")
	f := requireOneNaming(t, err, "notice-outage-started/notification.yaml")
	require.Equal(t, "ttl", f.Field, "находка о ключе ttl: %s", f.Error())
}

// Р12: у класса obligation нет ключа limits отключения. Близнец — тот же
// шаблон без limits; факт один — ключ limits задан / не задан.
func TestNTF5_LimitsOfObligationAreRefused(t *testing.T) {
	requireFixtureFormat(t, obligationNotification, obligationBodyRU, obligationBodyEN)
	twin := obligationFS(".", "notice-outage-started", obligationNotification, obligationBodyRU, obligationBodyEN)
	requireAccepted(t, twin, ".", "notify")

	withLimits := replaceOnce(t, obligationNotification, "class: obligation\n",
		"class: obligation\nlimits:\n  - {scope: recipient, window: 24h, max: 3}\n")
	bad := obligationFS(".", "notice-outage-started", withLimits, obligationBodyRU, obligationBodyEN)
	_, _, err := spec.LoadOwnerFS(bad, ".", "notify")
	f := requireOneNaming(t, err, "notice-outage-started/notification.yaml")
	require.Equal(t, "limits", f.Field, "находка о ключе limits: %s", f.Error())
}
