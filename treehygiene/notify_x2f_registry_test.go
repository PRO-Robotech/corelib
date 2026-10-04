// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treehygiene_test

// Пробы полосы X2-F NTF-3 (приёмка ac1f9fc9…; замысел issue-2918 З10
// «Посадка», Д33, CX3J-01) на реестре исключений гейта AuditFeedTableWrites
// после Д120: запись «функция resource-event формы fanout, одна на журнал
// модуля» с областью kacho идёт в реестр ВМЕСТЕ со своим предметом — её
// возвращает полоса kacho, которая заводит функции resource-event, тем же
// изменением, с повышением пина. До того запись в реестре не стоит: дерево
// kacho без функции зелёное (самоистечением не краснеет), а функция,
// которую пишет notifygen init, на дереве kacho без записи — находка SQL с
// координатой файла: завести функцию, не вернув запись, нельзя.
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
// дерева потребителя.
func kachoMigrations(gen map[string]string) map[string]string {
	files := map[string]string{
		"services/svc/x/x.go":                       x2fGo,
		"services/svc/internal/migrations/0001.sql": x2fItems,
	}
	for name, body := range gen {
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

// Д120: на дереве kacho без функции resource-event применимых записей
// реестра 0 и находок 0 — запись без предмета в реестре не стоит; близнец —
// то же дерево модуля kaname: 0 и 0. Отличие — путь модуля.
func TestD120_KachoTreeWithoutTheFunctionCarriesNoRegistryEntry(t *testing.T) {
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
	if r.Exceptions != 0 || len(r.Findings) != 0 {
		t.Fatalf("дерево kacho без функции resource-event: применимых записей %d, находок %d — ожидалось 0 и 0 (Д120)\n%s",
			r.Exceptions, len(r.Findings), kindsOf(r.Findings))
	}
}

// Д120, З10, Д33: функция, которую пишет notifygen init, на дереве kacho без
// записи реестра — находка SQL с координатой файла функции (признанных 0);
// близнец — то же дерево без файла функции: находок 0. Отличие — файл
// функции. Запись возвращает полоса kacho вместе с функцией.
func TestD120_GeneratedFunctionOnAKachoTreeIsAFindingWithoutTheEntry(t *testing.T) {
	gen, err := generatedFanout(t)
	if err != nil {
		t.Fatalf("генератор не дал функции resource-event: %v", err)
	}
	var fnName string
	for name, body := range gen {
		if strings.Contains(body, "notify_feed_resource_event") {
			fnName = name
		}
	}
	if fnName == "" {
		t.Fatalf("в выводе init нет функции notify_feed_resource_event: %v", keys(gen))
	}
	fn := "services/svc/internal/migrations/" + fnName

	r, err := treehygiene.AuditFeedTableWrites(goSynth(t, realKacho, kachoMigrations(gen)), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\n%s", r, kindsOf(r.Findings))
	if r.Exceptions != 0 || r.Recognized != 0 || !hasKind(r.Findings, "feed-table-sql-writer", fn) {
		t.Fatalf("функция генератора без записи: применимых %d, признанных %d — ожидалось 0, 0 и находка SQL в %s\n%s",
			r.Exceptions, r.Recognized, fn, kindsOf(r.Findings))
	}

	without := map[string]string{}
	for name, body := range gen {
		if name != fnName {
			without[name] = body
		}
	}
	twin, err := treehygiene.AuditFeedTableWrites(goSynth(t, realKacho, kachoMigrations(without)), "pkg/api")
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("без функции: %s\n%s", twin, kindsOf(twin.Findings))
	if len(twin.Findings) != 0 {
		t.Fatalf("дерево без функции дало находки:\n%s", kindsOf(twin.Findings))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
