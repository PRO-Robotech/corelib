// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// c5_format_test.go — ветви формата C5 вне проб NTF5-55/56/117 и NTF2-99:
// владелец не назван (LoadFS), отписка по классу, перечень обязательного
// класса в проверенном каталоге. Каждая проба — с близнецом, отличающимся
// одним фактом.
package spec_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/spec"
)

// LoadFS без владельца: шаблон obligation — находка «владелец не назван»
// (класс допустим только у notify, а владелец неизвестен). Близнец — тот же
// каталог через LoadOwnerFS владельца notify.
func TestC5_ObligationWithoutANamedOwnerIsRefused(t *testing.T) {
	fsys := obligationFS(".", "notice-outage-started", obligationNotification, obligationBodyRU, obligationBodyEN)
	requireAccepted(t, fsys, ".", spec.ObligationOwner)
	_, _, err := spec.LoadFS(fsys, ".")
	f := requireOneNaming(t, err, "владелец не назван", "notice-outage-started/notification.yaml")
	require.Equal(t, spec.RuleObligationOwner, f.Rule)
}

// Отписка в теле — находка правила своего класса, а не «блок вне закрытого
// набора», и она сосчитана в Census.Unsubscribe. Близнец — тот же шаблон без
// блока: находок 0, ссылок отписки 0.
func TestC5_UnsubscribeBlockIsNamedByClass(t *testing.T) {
	for _, tc := range []struct {
		class, extra, rule, owner string
	}{
		{"obligation", "", spec.RuleUnsubscribeObligation, spec.ObligationOwner},
		{"notice", "ttl: 24h\n", spec.RuleUnsubscribeBody, ""},
		{"security", "ttl: 24h\nlimits:\n  - {scope: recipient, window: 24h, max: 3}\n", spec.RuleUnsubscribeSecurity, ""},
	} {
		notification := replaceOnce(t, obligationNotification, "class: obligation\n", "class: "+tc.class+"\n"+tc.extra)
		twin := obligationFS(".", "notice-outage-started", notification, obligationBodyRU, obligationBodyEN)
		_, census, err := spec.LoadOwnerFS(twin, ".", tc.owner)
		require.Empty(t, findingsOf(t, err), "близнец класса %s", tc.class)
		require.Zero(t, census.Unsubscribe)

		bodyRU := obligationBodyRU + "  - unsubscribe: \"Отписаться\"\n"
		bad := obligationFS(".", "notice-outage-started", notification, bodyRU, obligationBodyEN)
		_, census, err = spec.LoadOwnerFS(bad, ".", tc.owner)
		f := requireOneNaming(t, err, "notice-outage-started/body.ru.yaml")
		require.Equal(t, tc.rule, f.Rule, "класс %s", tc.class)
		require.Equal(t, 1, census.Unsubscribe, "класс %s", tc.class)
	}
}

// Перечень обязательного класса: файла нет — nil; есть — имена в порядке
// файла; пустой документ — пустой перечень (смысл судит гейт владельца);
// повтор имени и элемент не имя шаблона — находка формы с именем файла.
func TestC5_RequiredSecurityListIsPartOfTheCatalog(t *testing.T) {
	notification := replaceOnce(t, obligationNotification, "class: obligation\n", "class: notice\nttl: 24h\n")
	base := func() fstest.MapFS {
		return obligationFS(".", "notice-outage-started", notification, obligationBodyRU, obligationBodyEN)
	}
	cat, _, err := spec.LoadFS(base(), ".")
	require.NoError(t, err)
	require.Nil(t, cat.RequiredSecurity, "файла перечня нет — nil, а не пустой перечень")

	with := func(content string) fstest.MapFS {
		fsys := base()
		fsys[spec.RequiredSecurityFile] = &fstest.MapFile{Data: []byte(content)}
		return fsys
	}
	cat, census, err := spec.LoadFS(with("- invite\n- recovery\n"), ".")
	require.NoError(t, err)
	require.NotNil(t, cat.RequiredSecurity)
	require.Equal(t, []string{"invite", "recovery"}, cat.RequiredSecurity.Names)
	require.Equal(t, 4, census.Files, "перечень прочитан и сосчитан")

	cat, _, err = spec.LoadFS(with(""), ".")
	require.NoError(t, err)
	require.NotNil(t, cat.RequiredSecurity)
	require.Empty(t, cat.RequiredSecurity.Names)

	for content, detail := range map[string]string{
		"- invite\n- invite\n": "имя повторено: invite",
		"- Invite\n":           "элемент — имя шаблона",
		"- [invite]\n":         "элемент — имя шаблона",
	} {
		_, _, err := spec.LoadFS(with(content), ".")
		f := requireOneNaming(t, err, spec.RequiredSecurityFile, detail)
		require.Equal(t, spec.RuleRequiredListForm, f.Rule)
	}
}
