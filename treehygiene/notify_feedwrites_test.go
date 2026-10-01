// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treehygiene_test

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

// УК87, CX1-74 (а): перепись писателей таблиц ленты по corelib — мест окна 3
// с координатами, находок 0, печать числа файлов Go и SQL и числа
// применимых записей реестра.
func TestAuditFeedTableWritesOnCorelib(t *testing.T) {
	r, err := treehygiene.AuditFeedTableWrites(corelibRoot(t), "api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\nместа окна:\n%s", r, strings.Join(r.WindowSiteAt, "\n"))
	if len(r.Findings) != 0 {
		t.Fatalf("находки по corelib:\n%s", kindsOf(r.Findings))
	}
	if r.WindowSites != 3 || len(r.WindowSiteAt) != 3 {
		t.Fatalf("мест окна %d, ожидалось 3", r.WindowSites)
	}
	if r.MigrationCalls != 1 {
		t.Fatalf("вызовов schema.Migration %d, ожидался 1 (cmd/notifygen)", r.MigrationCalls)
	}
	if r.Exceptions != 0 {
		t.Fatalf("применимых записей реестра %d на пинах NTF-1", r.Exceptions)
	}
}

// Дерево потребителя (kacho, kaname): мест 0, находок 0, применимых записей 0.
func TestAuditFeedTableWritesOnAConsumerTree(t *testing.T) {
	r, err := treehygiene.AuditFeedTableWrites(goSynth(t, kachoModule, map[string]string{
		"services/notify/x/x.go":                    "package x\n\n// X — пустой пакет.\nconst X = 1\n",
		"services/vpc/internal/migrations/0001.sql": "CREATE TABLE vpc_items (id text);\n",
	}), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s", r)
	if len(r.Findings) != 0 || r.Sites != 0 || r.Exceptions != 0 || r.SQLFiles != 1 {
		t.Fatalf("дерево потребителя: %s\n%s", r, kindsOf(r.Findings))
	}
}

// NTF1-B19: прямая вставка в ленту мимо feed — находка с координатой;
// близнец — use-case без такой строки: гейт молчит и печатает перепись.
func TestNTF1B19_DirectInsertIntoTheFeedIsFound(t *testing.T) {
	const usecase = `
package usecase

// Create — глагол модуля.
func Create(svc string) string { return %s }
`
	r, err := treehygiene.AuditFeedTableWrites(goSynth(t, kachoModule, map[string]string{
		"services/vpc/internal/apps/vpc/usecase/create.go": strings.Replace(usecase, "%s",
			"\"INSERT INTO vpc_notification_outbox (id) VALUES ($1)\"", 1),
	}), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\n%s", r, kindsOf(r.Findings))
	if len(r.Findings) != 1 || !strings.HasPrefix(r.Findings[0].Position, "services/vpc/internal/apps/vpc/usecase/create.go:4") {
		t.Fatalf("прямая вставка не найдена:\n%s", kindsOf(r.Findings))
	}
	r, err = treehygiene.AuditFeedTableWrites(goSynth(t, kachoModule, map[string]string{
		"services/vpc/internal/apps/vpc/usecase/create.go": strings.Replace(usecase, "%s", "svc", 1),
	}), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	if len(r.Findings) != 0 || r.Census.Files != 1 {
		t.Fatalf("близнец: %s\n%s", r, kindsOf(r.Findings))
	}
}
