// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treehygiene

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/gitenv"
)

// synthModule — путь синтетического модуля под notify/feed: правило internal
// Go пускает его пакеты к tablename так же, как пакеты ленты.
const synthModule = feedPkg + "/zsynth"

// writesTree — отслеживаемое синтетическое дерево модуля synthModule в одном
// рабочем пространстве с настоящим corelib.
func writesTree(t *testing.T, files map[string]string) string {
	t.Helper()
	core, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	root := t.TempDir()
	all := map[string]string{
		"go.mod":    fmt.Sprintf("module %s\n\ngo 1.26.0\n", synthModule),
		"go.work":   fmt.Sprintf("go 1.26.0\n\nuse .\nuse %s\n", core),
		"README.md": "# синтетическое дерево пробы\n",
	}
	for k, v := range files {
		all[k] = strings.TrimLeft(v, "\n")
	}
	for rel, body := range all {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
	}
	for _, args := range [][]string{{"init", "--quiet", "-b", "main"}, {"add", "-A"}} {
		if out, err := gitenv.Command(root, args...).CombinedOutput(); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// synthFeed — три оператора окна в форме З7 и З10 (Put, возврат вклада с
// WITH и %[n]s, уборка прошедших окон литералом функции в Sweep), уборка
// закрытых строк с %[1]s и предметы уборки с Name из tablename.Of.
const synthFeed = `
package feed

import (
	"context"
	"fmt"
	"time"

	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/retention"
)

func put(svc string) string {
	return fmt.Sprintf("INSERT INTO %s AS w (count) VALUES (1) ON CONFLICT DO UPDATE SET count = w.count + 1", tablename.Of(svc, tablename.Window))
}

func expire(svc string) string {
	return fmt.Sprintf("WITH x AS (UPDATE %[1]s SET state = 'expired' RETURNING id) SELECT FROM %[2]s c JOIN x ON true; UPDATE %[3]s w SET count = w.count - 1 FROM %[3]s", tablename.Of(svc, tablename.Outbox), tablename.Of(svc, tablename.Contrib), tablename.Of(svc, tablename.Window))
}

func closed(svc string) string {
	return fmt.Sprintf("DELETE FROM %[1]s WHERE id IN (SELECT id FROM %[1]s)", tablename.Of(svc, tablename.Outbox))
}

// RetentionSubjects — предметы уборки.
func RetentionSubjects(svc string) []retention.Subject {
	return []retention.Subject{
		{Name: tablename.Of(svc, tablename.Outbox), Grace: time.Hour,
			Sweep: func(context.Context, time.Duration, int) (int64, bool, error) { _ = closed(svc); return 0, false, nil }},
		{Name: tablename.Of(svc, tablename.Window), Grace: time.Hour,
			Sweep: func(context.Context, time.Duration, int) (int64, bool, error) {
				_ = fmt.Sprintf("DELETE FROM %[1]s WHERE key IN (SELECT key FROM %[1]s)", tablename.Of(svc, tablename.Window))
				return 0, false, nil
			}},
	}
}
`

const synthGen = `
package gen

import "github.com/PRO-Robotech/corelib/notify/feed/schema"

// Init — единственный вызов schema.Migration.
func Init() (string, error) { return schema.Migration("probe", schema.V1) }
`

func synthLedger() feedLedger {
	f := synthModule + "/feed"
	return feedLedger{
		module:          synthModule,
		migrationCaller: synthModule + "/gen",
		owners: map[string]feedOwner{
			f + ".put":               {sites: []string{"Window"}},
			f + ".expire":            {sites: []string{"Contrib,Outbox,Window"}},
			f + ".closed":            {sites: []string{"Outbox"}},
			f + ".RetentionSubjects": {sites: []string{"Window"}, names: []string{"Outbox", "Window"}},
		},
	}
}

func synthWrites(t *testing.T, files map[string]string) FeedWritesReport {
	t.Helper()
	all := map[string]string{"feed/feed.go": synthFeed, "gen/gen.go": synthGen}
	for k, v := range files {
		if v == "" {
			delete(all, k)
			continue
		}
		all[k] = v
	}
	g, err := loadGoTree(writesTree(t, all), "api")
	if err != nil {
		t.Fatalf("дерево не загружено: %v", err)
	}
	r, err := auditFeedTableWrites(g, synthLedger(), nil)
	if err != nil {
		t.Fatalf("гейт не исполнился: %v", err)
	}
	t.Logf("%s\n%s", r, findingsText(r.Findings))
	return r
}

func findingsText(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.String() + "\n")
	}
	return b.String()
}

func requireOne(t *testing.T, r FeedWritesReport, file, kind string) {
	t.Helper()
	for _, f := range r.Findings {
		if strings.HasPrefix(f.Position, file) && (kind == "" || f.Kind == kind) {
			return
		}
	}
	t.Fatalf("нет находки вида %q в %s:\n%s", kind, file, findingsText(r.Findings))
}

// УК87, УК92, УК93, УК94 (близнец): три оператора окна, возврат вклада с WITH
// и %[n]s, уборка закрытых строк, уборщики литералами функций в Sweep и Name
// из tablename.Of, один вызов schema.Migration — зелёный, мест окна 3.
func TestAuditFeedTableWritesTwinIsGreenWithThreeWindowSites(t *testing.T) {
	r := synthWrites(t, nil)
	if len(r.Findings) != 0 {
		t.Fatalf("близнец дал находки:\n%s", findingsText(r.Findings))
	}
	if r.WindowSites != 3 {
		t.Fatalf("мест окна %d, ожидалось 3", r.WindowSites)
	}
	if r.NameUses != 2 {
		t.Fatalf("употреблений Name %d, ожидалось 2", r.NameUses)
	}
	if r.MigrationCalls != 1 {
		t.Fatalf("вызовов schema.Migration %d", r.MigrationCalls)
	}
}

// УК87, УК89, УК90, УК92, УК93: инъекции — каждая красная с координатой.
func TestAuditFeedTableWritesInjections(t *testing.T) {
	replace := func(old, repl string) map[string]string {
		if !strings.Contains(synthFeed, old) {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: в фикстуре нет %q", old)
		}
		return map[string]string{"feed/feed.go": strings.Replace(synthFeed, old, repl, 1)}
	}
	extra := func(src string) map[string]string {
		return map[string]string{"feed/extra.go": "package feed\n\nimport (\n\t\"fmt\"\n\n\t\"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename\"\n\t\"github.com/PRO-Robotech/corelib/retention\"\n)\n\nvar _ = retention.Subject{}\nvar _ = fmt.Sprint\nvar _ = tablename.Window\n" + src}
	}
	t.Run("четвёртый оператор окна вне ведомости", func(t *testing.T) {
		requireOne(t, synthWrites(t, extra(`
func reset(svc string) string { return fmt.Sprintf("UPDATE %s SET count = 0", tablename.Of(svc, tablename.Window)) }
`)), "feed/extra.go", writeOutsideLedger)
	})
	t.Run("второй вызов Of окна в одном Sprintf", func(t *testing.T) {
		requireOne(t, synthWrites(t, replace(`FROM %[3]s", tablename.Of(svc, tablename.Outbox), tablename.Of(svc, tablename.Contrib), tablename.Of(svc, tablename.Window))`,
			`FROM %[4]s", tablename.Of(svc, tablename.Outbox), tablename.Of(svc, tablename.Contrib), tablename.Of(svc, tablename.Window), tablename.Of(svc, tablename.Window))`)),
			"feed/feed.go", writeDuplicateKind)
	})
	t.Run("второе место окна в Put", func(t *testing.T) {
		requireOne(t, synthWrites(t, replace(`func put(svc string) string {`,
			`func put(svc string) string {
	_ = fmt.Sprintf("UPDATE %s SET count = 1", tablename.Of(svc, tablename.Window))`)), "feed/feed.go", writeLedgerMismatch)
	})
	t.Run("литерал функции с местом окна внутри Put", func(t *testing.T) {
		requireOne(t, synthWrites(t, replace(`func put(svc string) string {`,
			`func put(svc string) string {
	_ = func() string { return fmt.Sprintf("UPDATE %s SET count = 1", tablename.Of(svc, tablename.Window)) }`)), "feed/feed.go", writeLedgerMismatch)
	})
	t.Run("форматная строка не литерал", func(t *testing.T) {
		requireOne(t, synthWrites(t, extra(`
func dyn(svc, q string) string { return fmt.Sprintf(q, tablename.Of(svc, tablename.Outbox)) }
`)), "feed/extra.go", writeNonConstFormat)
	})
	t.Run("сохранённое имя (УК90)", func(t *testing.T) {
		r := synthWrites(t, extra(`
type store struct{ window string }

func newStore(svc string) *store {
	s := &store{}
	s.window = tablename.Of(svc, tablename.Window)
	return s
}

func (s *store) reset() string { return fmt.Sprintf("UPDATE %s SET count = 0", s.window) }
`))
		requireOne(t, r, "feed/extra.go:18", writeSaved)
	})
	t.Run("место в инициализаторе пакетной переменной", func(t *testing.T) {
		requireOne(t, synthWrites(t, extra(`
var q = fmt.Sprintf("UPDATE %s SET count = 0", tablename.Of("probe", tablename.Window))
`)), "feed/extra.go", writeOutsideFunc)
	})
	t.Run("Name в иной функции (УК94)", func(t *testing.T) {
		requireOne(t, synthWrites(t, extra(`
func other(svc string) retention.Subject { return retention.Subject{Name: tablename.Of(svc, tablename.Window)} }
`)), "feed/extra.go", writeNameOutsideLedger)
	})
	t.Run("x := Of рядом с литералом (УК94)", func(t *testing.T) {
		requireOne(t, synthWrites(t, extra(`
func other2(svc string) retention.Subject {
	x := tablename.Of(svc, tablename.Window)
	return retention.Subject{Name: x}
}
`)), "feed/extra.go", writeSaved)
	})
	t.Run("Name литералом (УК94)", func(t *testing.T) {
		requireOne(t, synthWrites(t, extra(`
func other3() retention.Subject { return retention.Subject{Name: "probe_notification_window"} }
`)), "feed/extra.go", writeSuffixValue)
	})
	t.Run("сцепление с суффиксом в пакете модуля (УК89)", func(t *testing.T) {
		requireOne(t, synthWrites(t, map[string]string{"svc/repo.go": `
package svc

// Reset — запись окна мимо ленты.
func Reset(svc string) string { return "UPDATE " + svc + "_notification_window SET count = 0" }
`}), "svc/repo.go", writeSuffixValue)
	})
	t.Run("константа сцеплением (УК89)", func(t *testing.T) {
		requireOne(t, synthWrites(t, map[string]string{"svc/c.go": `
package svc

// Suffix — суффикс сцеплением констант.
const Suffix = "_notification_" + "window"
`}), "svc/c.go", writeSuffixValue)
	})
	t.Run("второй вызов schema.Migration", func(t *testing.T) {
		requireOne(t, synthWrites(t, map[string]string{"svc/m.go": `
package svc

import "github.com/PRO-Robotech/corelib/notify/feed/schema"

// M — второй производитель миграции.
func M() (string, error) { return schema.Migration("vpc", schema.V1) }
`}), "svc/m.go", writeMigrationCaller)
	})
	t.Run("запись ведомости без предмета", func(t *testing.T) {
		requireOne(t, synthWrites(t, replace(`func closed(svc string) string {
	return fmt.Sprintf("DELETE FROM %[1]s WHERE id IN (SELECT id FROM %[1]s)", tablename.Of(svc, tablename.Outbox))
}`, `func closed(svc string) string { return svc }`)), "", writeLedgerOrphan)
	})
}

// УК87, УК89: SQL — функция, блок DO и одиночный оператор записи над таблицей
// ленты в любом отслеживаемом *.sql; DDL ленты — не находка.
func TestAuditFeedTableWritesSQL(t *testing.T) {
	for name, tc := range map[string]struct{ file, sql string }{
		"блок DO в миграции": {"migrations/0002_x.sql",
			"-- +goose Up\nDO $$ BEGIN UPDATE vpc_notification_window SET count = 0; END $$;\n"},
		"DELETE в посеве вне migrations": {"deploy/seed/seed.sql",
			"DELETE FROM vpc_notification_contrib;\n"},
		"функция миграции пишет окно": {"migrations/0003_f.sql",
			"-- +goose Up\n-- +goose StatementBegin\nCREATE OR REPLACE FUNCTION vpc_reset() RETURNS void LANGUAGE sql AS $f$ UPDATE vpc_notification_window SET count = 0 $f$;\n-- +goose StatementEnd\n"},
		"триггер над лентой": {"migrations/0004_t.sql",
			"CREATE TRIGGER t AFTER INSERT ON vpc_items FOR EACH ROW EXECUTE FUNCTION vpc_notification_outbox_fill();\n"},
	} {
		t.Run(name, func(t *testing.T) {
			r := synthWrites(t, map[string]string{tc.file: tc.sql})
			requireOne(t, r, tc.file, writeSQL)
			if r.SQLFiles != 1 {
				t.Fatalf("SQL-файлов осмотрено %d", r.SQLFiles)
			}
		})
	}
	ddl := "-- +goose Up\nCREATE TABLE vpc_notification_outbox (id text PRIMARY KEY);\nCREATE INDEX vpc_notification_outbox_closed_idx ON vpc_notification_outbox (id);\n-- +goose Down\nDROP TABLE vpc_notification_outbox;\n"
	r := synthWrites(t, map[string]string{"migrations/0001_feed.sql": ddl})
	if len(r.Findings) != 0 || r.SQLFindings != 0 {
		t.Fatalf("DDL ленты дал находки:\n%s", findingsText(r.Findings))
	}
}

// УК48, CX1-43, CX1-74: реестр исключений — запись без довода или предиката
// снятия красная; запись, которой нечего исключать, красная с именем; запись
// чужой области не применяется и не краснеет.
func TestFeedWriteExceptionsRegistry(t *testing.T) {
	g, err := loadGoTree(writesTree(t, map[string]string{"feed/feed.go": synthFeed, "gen/gen.go": synthGen}), "api")
	if err != nil {
		t.Fatalf("дерево не загружено: %v", err)
	}
	zero := func(*goTree) (int, error) { return 0, nil }
	one := func(*goTree) (int, error) { return 1, nil }
	for name, e := range map[string]feedWriteException{
		"без довода":           {Name: "x", Scope: synthModule, Removal: one},
		"без предиката снятия": {Name: "x", Scope: synthModule, Reason: "довод"},
		"без имени":            {Scope: synthModule, Reason: "довод", Removal: one},
	} {
		if _, err := auditFeedTableWrites(g, synthLedger(), []feedWriteException{e}); err == nil {
			t.Errorf("%s: запись принята", name)
		}
	}
	r, err := auditFeedTableWrites(g, synthLedger(), []feedWriteException{
		{Name: "пустая", Scope: synthModule, Reason: "довод", Removal: zero},
	})
	if err != nil {
		t.Fatal(err)
	}
	requireOne(t, r, "", writeExceptionEmpty)
	if r.Exceptions != 1 {
		t.Fatalf("применимых записей %d", r.Exceptions)
	}

	r, err = auditFeedTableWrites(g, synthLedger(), []feedWriteException{
		{Name: "чужая область", Scope: "example.invalid/kacho", Reason: "довод", Removal: zero},
		{Name: "своя", Scope: synthModule, Reason: "довод", Removal: one},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 0 || r.Exceptions != 1 || r.Recognized != 1 {
		t.Fatalf("область записи: %s\n%s", r, findingsText(r.Findings))
	}
	failing := func(*goTree) (int, error) { return 0, errors.New("предикат не исполнился") }
	if _, err := auditFeedTableWrites(g, synthLedger(), []feedWriteException{
		{Name: "сломанная", Scope: synthModule, Reason: "довод", Removal: failing},
	}); err == nil {
		t.Fatal("предикат снятия, не исполнившийся, принят за ноль")
	}
}

// УК48, CX1-43: запись реестра, признающая файл SQL, гасит находки SQL
// ровно этого файла; близнец — тот же оператор записи ленты в файле, который
// запись не признаёт: находка с координатой. Отличие — признание файла.
// Синтетика, а не живая запись: живая запись реестра признаёт только
// функцию resource-event, а проба судит признание файла как таковое.
func TestFeedWriteExceptionRecognizesOnlyItsFile(t *testing.T) {
	const mark = "-- признано синтетической записью\n"
	const write = "DELETE FROM vpc_notification_contrib;\n"
	g, err := loadGoTree(writesTree(t, map[string]string{
		"feed/feed.go":          synthFeed,
		"gen/gen.go":            synthGen,
		"migrations/0001_a.sql": mark + write,
		"migrations/0002_b.sql": write,
	}), "api")
	if err != nil {
		t.Fatalf("дерево не загружено: %v", err)
	}
	r, err := auditFeedTableWrites(g, synthLedger(), []feedWriteException{{
		Name: "синтетическая", Scope: synthModule, Reason: "довод",
		Removal:    func(*goTree) (int, error) { return 1, nil },
		Recognizes: func(b []byte) bool { return strings.HasPrefix(string(b), mark) },
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s\n%s", r, findingsText(r.Findings))
	requireOne(t, r, "migrations/0002_b.sql", writeSQL)
	if len(r.Findings) != 1 || r.SQLFiles != 2 || r.Exceptions != 1 {
		t.Fatalf("признание файла: %s\n%s", r, findingsText(r.Findings))
	}
}

// Реестр несёт ровно одну запись — функцию resource-event формы fanout с
// областью kacho (Д33, NTF-3 З10, X2-F): с доводом, предикатом снятия и
// признанием файлов. Вторая запись появляется только правкой этой пробы.
func TestFeedWriteExceptionsCarryOnlyTheResourceEventEntry(t *testing.T) {
	es := feedWriteExceptions()
	if len(es) != 1 {
		t.Fatalf("записей реестра %d, ожидалась 1 (resource-event)", len(es))
	}
	e := es[0]
	if !strings.Contains(e.Name, "resource-event") || e.Scope != kachoModulePath || e.Reason == "" ||
		e.Removal == nil || e.Recognizes == nil {
		t.Fatalf("запись реестра не по форме: имя %q, область %q", e.Name, e.Scope)
	}
}
