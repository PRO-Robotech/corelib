// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treehygiene_test

// Пробы полосы X2-F NTF-3 (приёмка ac1f9fc9…; замысел issue-2918 З10
// «Посадка», Д33, CX3J-01) на реестре исключений гейта AuditFeedTableWrites:
// запись «функция resource-event формы fanout, одна на журнал модуля» с
// областью kacho признаёт функцию, которую пишет notifygen init, и только её;
// без функции в дереве области краснеет самоистечением; на дереве kaname (и
// corelib — TestAuditFeedTableWritesOnCorelib) применимых записей 0.
//
// Функция берётся у настоящего генератора этого дерева (go run
// ./cmd/notifygen), а не выписывается: копии шаблона в пробе нет.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treehygiene"
)

const (
	realKacho  = "github.com/PRO-Robotech/kacho"
	realKaname = "github.com/PRO-Robotech/kaname"
)

const x2fGo = "package x\n\n// X — пакет модуля.\nconst X = 1\n"

const x2fItems = "-- +goose Up\nCREATE TABLE vpc_items (id text);\n\n-- +goose Down\nDROP TABLE vpc_items;\n"

// generatedFanout — миграции, которые notifygen init пишет на дереве
// источника с шаблоном resource-event формы fanout и объявлением журнала:
// имя файла → содержимое. Провал generate — «проба не исполнилась»; провал
// init с объявлением журнала — отсутствие формы fanout у генератора.
func generatedFanout(t *testing.T) (map[string]string, error) {
	t.Helper()
	src := t.TempDir()
	files := map[string]string{
		"svc/svc.go": "package svc\n",
		"svc/notifications/resource-event/notification.yaml": `name: resource-event
class: notice
ttl: 72h
recipient: fanout
attributes:
  kind: {type: text, presence: required}
  resource_id: {type: text, presence: required}
  scope: {type: text, presence: required}
  change: {type: text, presence: required}
  occurred_at: {type: timestamp, presence: required}
  name: {type: text, presence: optional}
subject:
  ru: "{{ kind }} {{ resource_id }}: {{ change }}"
  en: "{{ kind }} {{ resource_id }}: {{ change }}"
`,
		"svc/notifications/resource-event/body.ru.yaml": "blocks:\n  - p: \"{{ kind }} {{ resource_id }} {{ scope }} {{ change }} {{ occurred_at }}\"\n  - p: \"{{ name }}\"\n    when: name\n",
		"svc/notifications/resource-event/body.en.yaml": "blocks:\n  - p: \"{{ kind }} {{ resource_id }} {{ scope }} {{ change }} {{ occurred_at }}\"\n  - p: \"{{ name }}\"\n    when: name\n",
		"svc/journal.yaml": `module: svc
table: svc_journal
columns:
  kind: resource_kind
  id: resource_id
  change: event_type
  payload: payload
  project: project_id
  initiator: initiator
  occurred_at: created_at
kinds:
  Volume: {name_form: dns, scope: project}
  Repository: {name_form: none, scope: project}
  notification: {name_form: none, scope: cluster}
changes:
  created: CREATED
  updated: UPDATED
  deleted: DELETED
`,
	}
	for rel, body := range files {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
	}
	notifygen := func(args ...string) ([]byte, error) {
		cmd := exec.Command("go", append([]string{"run", "./cmd/notifygen", "-root", src}, args...)...)
		cmd.Dir = corelibRoot(t)
		return cmd.CombinedOutput()
	}
	if out, err := notifygen(); err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: make notifications на фикстуре fanout: %v\n%s", err, out)
	}
	if out, err := notifygen("init", "-service", "svc", "-migrations", "svc/migrations", "-journal", "svc/journal.yaml"); err != nil {
		return nil, &initRefused{out: string(out), err: err}
	}
	entries, err := os.ReadDir(filepath.Join(src, "svc", "migrations"))
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(src, "svc", "migrations", e.Name()))
		if err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

type initRefused struct {
	out string
	err error
}

func (e *initRefused) Error() string {
	return "notifygen init -journal: " + e.err.Error() + "\n" + e.out
}

// kachoMigrations — файлы генератора под services/svc/internal/migrations
// дерева потребителя; edit правит содержимое функции (не миграции ленты).
func kachoMigrations(gen map[string]string, edit func(string) string) map[string]string {
	files := map[string]string{
		"services/svc/x/x.go":                       x2fGo,
		"services/svc/internal/migrations/0001.sql": x2fItems,
	}
	for name, body := range gen {
		if edit != nil && strings.Contains(body, "notify_feed_resource_event") {
			body = edit(body)
		}
		files["services/svc/internal/migrations/"+name] = body
	}
	return files
}

func hasKind(fs []treehygiene.Finding, kind, sub string) bool {
	for _, f := range fs {
		if f.Kind == kind && strings.Contains(f.Why+f.Position, sub) {
			return true
		}
	}
	return false
}

// З10 «Посадка», Д33: на дереве kacho без функции resource-event запись
// реестра применима и краснеет самоистечением с именем записи; близнец —
// то же дерево модуля kaname: применимых записей 0, находок 0. Отличие —
// путь модуля.
func TestNTF3_X2F_ResourceEventEntryExpiresOnAKachoTreeWithoutTheFunction(t *testing.T) {
	files := map[string]string{
		"services/vpc/x/x.go":                       x2fGo,
		"services/vpc/internal/migrations/0001.sql": x2fItems,
	}
	twin, err := treehygiene.AuditFeedTableWrites(goSynth(t, realKaname, files), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился на дереве kaname: %v", err)
	}
	t.Logf("kaname: %s", twin)
	if twin.Exceptions != 0 || len(twin.Findings) != 0 {
		t.Fatalf("дерево kaname: применимых записей %d, находок %d — ожидалось 0 и 0\n%s",
			twin.Exceptions, len(twin.Findings), kindsOf(twin.Findings))
	}

	r, err := treehygiene.AuditFeedTableWrites(goSynth(t, realKacho, files), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился на дереве kacho: %v", err)
	}
	t.Logf("kacho: %s\n%s", r, kindsOf(r.Findings))
	if r.Exceptions != 1 {
		t.Fatalf("применимых к дереву kacho записей реестра %d, ожидалась 1 (функция resource-event формы fanout)", r.Exceptions)
	}
	if !hasKind(r.Findings, "feed-exception-without-subject", "resource-event") {
		t.Fatalf("запись resource-event без функции в дереве не краснеет самоистечением:\n%s", kindsOf(r.Findings))
	}
}

// З10, Д33, CX3J-01: функцию, которую пишет notifygen init, запись признаёт
// (признанных 1, находок 0); близнец — та же функция с правкой тела (одна
// подстрока: имя настройки флага) — не признана: находка SQL с координатой
// файла и самоистечение записи. Отличие — правка тела.
func TestNTF3_X2F_ResourceEventEntryRecognizesOnlyTheGeneratedFunction(t *testing.T) {
	gen, err := generatedFanout(t)
	if err != nil {
		t.Fatalf("генератор не дал функции resource-event: %v", err)
	}
	var fn string
	for name, body := range gen {
		if strings.Contains(body, "notify_feed_resource_event") {
			fn = "services/svc/internal/migrations/" + name
		}
	}
	if fn == "" {
		t.Fatalf("в выводе init нет функции notify_feed_resource_event: %v", keys(gen))
	}

	r, err := treehygiene.AuditFeedTableWrites(goSynth(t, realKacho, kachoMigrations(gen, nil)), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\n%s", r, kindsOf(r.Findings))
	if r.Exceptions != 1 || r.Recognized != 1 || len(r.Findings) != 0 {
		t.Fatalf("функция генератора: применимых %d, признанных %d, находок %d — ожидалось 1, 1, 0\n%s",
			r.Exceptions, r.Recognized, len(r.Findings), kindsOf(r.Findings))
	}

	edited, err := treehygiene.AuditFeedTableWrites(goSynth(t, realKacho, kachoMigrations(gen, func(s string) string {
		return strings.Replace(s, "kacho_feed.enabled", "kacho_feed.enabledx", 1)
	})), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("правленое тело: %s\n%s", edited, kindsOf(edited.Findings))
	if edited.Recognized != 0 || !hasKind(edited.Findings, "feed-table-sql-writer", fn) {
		t.Fatalf("правленое тело признано либо не найдено с координатой %s:\n%s", fn, kindsOf(edited.Findings))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
