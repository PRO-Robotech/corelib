// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package main

// Сторож лимита постановки (feed.Put шаг лимитов) стоит на Go-половине;
// строку ленты шаблона resource-event формы fanout пишет функция базы на
// журнале модуля — мимо сторожа. Объявленный у такого шаблона limits не
// исполняется ничем, поэтому генератор отказывает на ЛЮБОМ limits у него —
// и init -journal, и -check, — на каждой области, которую формат допускает
// у формы fanout (project, initiator; recipient отвергает уже формат).
// Близнец — тот же шаблон без limits (fanoutTree) — зелёный.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fanoutLimitScopes — области лимита, которые формат допускает у формы fanout.
var fanoutLimitScopes = []string{"project", "initiator"}

// withLimit — шаблон resource-event с одним лимитом области scope; от
// fanoutNotification отличается ровно строками limits.
func withLimit(scope string) string {
	return strings.Replace(fanoutNotification, "recipient: fanout\n",
		"recipient: fanout\nlimits:\n  - {scope: "+scope+", window: 1h, max: 100}\n", 1)
}

// requireLimitRefusal — отказ называет файл шаблона и слово limits.
func requireLimitRefusal(t *testing.T, out string) {
	t.Helper()
	require.Contains(t, out, "svc/notifications/resource-event", "находка называет каталог шаблона: %s", out)
	require.Contains(t, out, "limits", "находка называет limits: %s", out)
}

// init -journal на шаблоне с limits — отказ, миграции функции нет.
func TestNTF3_FanoutLimits_InitRefusesAnyLimit(t *testing.T) {
	for _, scope := range fanoutLimitScopes {
		t.Run(scope, func(t *testing.T) {
			tr := fanoutTree(t)
			tr.write("svc/notifications/resource-event/notification.yaml", withLimit(scope))
			if r := tr.run(); r.code != 0 {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: make notifications с limits %s: код %d\n%s%s", scope, r.code, r.stdout, r.stderr)
			}
			r := initFanout(tr, testOptions())
			require.NotEqual(t, 0, r.code, "init с limits %s обязан отказать: %s%s", scope, r.stdout, r.stderr)
			requireLimitRefusal(t, r.stderr)
			require.Empty(t, fanoutFiles(t, tr), "миграции функции при отказе нет")
		})
	}
	// Близнец: тот же шаблон без limits — init пишет функцию.
	tr := fanoutTree(t)
	requireInitOK(t, initFanout(tr, testOptions()))
	require.Len(t, fanoutFiles(t, tr), 1)
}

// -check на дереве, где функция уже стоит, а шаблон получил limits, —
// красный; близнец без limits — зелёный.
func TestNTF3_FanoutLimits_CheckRefusesAnyLimit(t *testing.T) {
	for _, scope := range fanoutLimitScopes {
		t.Run(scope, func(t *testing.T) {
			tr := fanoutTree(t)
			requireInitOK(t, initFanout(tr, testOptions()))
			c := tr.run("-check")
			require.Equal(t, 0, c.code, "близнец без limits: %s%s", c.stdout, c.stderr)

			tr.write("svc/notifications/resource-event/notification.yaml", withLimit(scope))
			if r := tr.run(); r.code != 0 {
				t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: make notifications с limits %s: код %d\n%s%s", scope, r.code, r.stdout, r.stderr)
			}
			c = tr.run("-check")
			require.NotEqual(t, 0, c.code, "-check с limits %s обязан краснеть: %s%s", scope, c.stdout, c.stderr)
			requireLimitRefusal(t, c.stderr)
		})
	}
}
