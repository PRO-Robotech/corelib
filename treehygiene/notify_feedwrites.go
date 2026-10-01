// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_feedwrites.go — AuditFeedTableWrites: NTF1-B19 и перепись писателей
// таблиц ленты (З16, CX1-64 (б), УК87, УК89–УК94). Гейт судит ПРОИЗВОДИТЕЛЯ
// имени таблицы, а не текст оператора:
//
//   - Go, по идентичности. Место — вызов fmt.Sprintf, среди аргументов
//     которого есть вызов tablename.Of. Место несёт множество видов таблиц;
//     владелец — ближайшее объемлющее объявление функции или метода (литерал
//     функции владельцем не бывает); место вне объявления функции — находка.
//     Ведомость перечисляет для каждого владельца мультимножество мест; место
//     вне ведомости и расхождение мультимножества — находка; запись ведомости
//     без предмета — находка. Литерал по первому слову не классифицируется.
//   - Результат tablename.Of употребляется одним способом — аргументом
//     Sprintf с константной форматной строкой в том же выражении (УК90);
//     допущено ещё одно — значение поля Name составного литерала
//     retention.Subject (поле — по объекту *types.Var) в функции, которую
//     ведомость называет (УК94). Иное употребление — находка. Один вид в
//     одном Sprintf — один вызов Of, повтор — %[n]s (УК92 (б)).
//   - Go, по значению строки: строковый литерал и константное выражение с
//     суффиксом таблицы ленты вне tablename — находка (УК89 (в)). Остаток,
//     которого гейт не видит, — имя, собранное во время исполнения из
//     не-константных частей, и имя из поля Name, употреблённое как текст
//     оператора; оба держатся ревью диффа.
//   - schema.Migration зовётся ровно в одном пакете ведомости (notifygen).
//   - SQL, во ВСЕХ отслеживаемых *.sql: функция, процедура, триггер, блок
//     DO и одиночный оператор записи, называющие таблицу ленты, — находка;
//     DDL ленты (таблицы, индексы, удаление) — не находка. Исключения —
//     реестр notify_exceptions.go.
package treehygiene

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/tools/go/ast/inspector"
)

// Виды находок гейта писателей таблиц ленты.
const (
	writeOutsideLedger     = "feed-write-outside-ledger"
	writeLedgerMismatch    = "feed-write-ledger-mismatch"
	writeLedgerOrphan      = "feed-write-ledger-orphan"
	writeDuplicateKind     = "feed-write-duplicate-kind"
	writeNonConstFormat    = "feed-write-nonconst-format"
	writeSaved             = "feed-table-name-saved"
	writeOutsideFunc       = "feed-write-outside-func"
	writeNameOutsideLedger = "feed-table-name-field-outside-ledger"
	writeSuffixValue       = "feed-table-suffix-value"
	writeMigrationCaller   = "feed-schema-migration-caller"
	writeSQL               = "feed-table-sql-writer"
	writeExceptionEmpty    = "feed-exception-without-subject"
)

// feedSuffix — суффикс таблицы ленты в значении строки или тексте SQL.
var feedSuffix = regexp.MustCompile(`_notification_(outbox|window|contrib)`)

// feedOwner — запись ведомости: места владельца (каждое — множество видов
// через запятую, по возрастанию) и употребления Name по видам. ddl — владелец
// строит DDL схемы ленты: его места окна писателями окна не считаются.
type feedOwner struct {
	sites []string
	names []string
	ddl   bool
}

// feedLedger — ведомость дерева module: владельцы по полному имени
// (*types.Func.FullName) и пакет единственного вызова schema.Migration.
type feedLedger struct {
	module          string
	migrationCaller string
	owners          map[string]feedOwner
}

// corelibFeedLedger — ведомость corelib. Мест, называющих окно, три: Put
// (takeWindows), возврат вклада уборщиком истечения (expireSQL — одно место с
// видами лента, вклад, окно) и уборка прошедших окон (RetentionSubjects).
// Новый писатель окна появляется только правкой этой ведомости, и ревью
// правки судит его место в полном порядке замков (З7).
func corelibFeedLedger() feedLedger {
	f := func(name string) string { return feedPkg + "." + name }
	s := func(name string) string { return feedSchemaPkg + "." + name }
	return feedLedger{
		module:          corelibModule,
		migrationCaller: notifygenPkg,
		owners: map[string]feedOwner{
			"(*" + feedPkg + ".Source).write": {sites: []string{"Contrib", "Outbox"}},
			f("takeWindows"):                  {sites: []string{"Window"}},
			f("claimSQL"):                     {sites: []string{"Outbox"}},
			f("sealedCloseSQL"):               {sites: []string{"Outbox"}},
			f("ackTerminalSQL"):               {sites: []string{"Outbox"}},
			f("ackDeferSQL"):                  {sites: []string{"Outbox"}},
			f("ackRecordedSQL"):               {sites: []string{"Outbox"}},
			f("expireSQL"):                    {sites: []string{"Contrib,Outbox,Window"}},
			f("pendingSQL"):                   {sites: []string{"Outbox"}},
			f("closedSweepSQL"):               {sites: []string{"Outbox"}},
			f("RetentionSubjects"):            {sites: []string{"Window"}, names: []string{"Outbox", "Window"}},
			s("v1Up"):                         {sites: []string{"Contrib,Outbox,Window"}, ddl: true},
			s("v1Down"):                       {sites: []string{"Contrib,Outbox,Window"}, ddl: true},
		},
	}
}

// FeedWritesReport — исход гейта по одному дереву.
type FeedWritesReport struct {
	Census TreeCensus
	// SQLFiles — осмотрено отслеживаемых *.sql.
	SQLFiles int
	// OfCalls — вызовов tablename.Of; Sites — мест Sprintf.
	OfCalls, Sites int
	// WindowSites — мест, называющих окно, у писателей (без DDL);
	// WindowSiteAt — их координаты.
	WindowSites  int
	WindowSiteAt []string
	// NameUses — употреблений результата Of полем Name предмета уборки.
	NameUses int
	// MigrationCalls — вызовов schema.Migration.
	MigrationCalls int
	// SQLFindings — находок SQL.
	SQLFindings int
	// Exceptions — записей реестра, применимых к дереву; Recognized —
	// признанных ими функций.
	Exceptions, Recognized int
	Findings               []Finding
}

func (r FeedWritesReport) String() string {
	return fmt.Sprintf("писатели таблиц ленты: %s · SQL-файлов %d · вызовов Of %d · мест Sprintf %d · "+
		"мест окна %d · Name %d · schema.Migration %d · находок SQL %d · применимых записей реестра %d · "+
		"признанных функций %d · находок %d", r.Census, r.SQLFiles, r.OfCalls, r.Sites, r.WindowSites,
		r.NameUses, r.MigrationCalls, r.SQLFindings, r.Exceptions, r.Recognized, len(r.Findings))
}

// AuditFeedTableWrites — перепись писателей таблиц ленты по дереву root с
// ведомостью corelib и реестром исключений.
func AuditFeedTableWrites(root, stubDir string) (FeedWritesReport, error) {
	g, err := loadGoTree(root, stubDir)
	if err != nil {
		return FeedWritesReport{}, err
	}
	return auditFeedTableWrites(g, corelibFeedLedger(), feedWriteExceptions())
}

type feedSite struct {
	pos   string
	kinds string
}

func auditFeedTableWrites(g *goTree, ledger feedLedger, exceptions []feedWriteException) (FeedWritesReport, error) {
	r := FeedWritesReport{Census: g.census()}
	add := func(pos, kind, why string) {
		r.Findings = append(r.Findings, Finding{Position: pos, Kind: kind, Why: why})
	}
	own := g.module == ledger.module
	sites := map[string][]feedSite{}
	names := map[string][]feedSite{}
	var migrationCallers []string

	for _, gf := range g.files {
		if gf.pkg.PkgPath == tablenamePkg {
			continue
		}
		info := gf.pkg.TypesInfo
		r.scanSuffixValues(g, gf, add)
		in := inspector.New([]*ast.File{gf.file})
		in.WithStack([]ast.Node{(*ast.CallExpr)(nil), (*ast.SelectorExpr)(nil), (*ast.Ident)(nil)},
			func(n ast.Node, push bool, stack []ast.Node) bool {
				if !push {
					return true
				}
				switch x := n.(type) {
				case *ast.CallExpr:
					switch {
					case isPkgFunc(info, x.Fun, "fmt", "Sprintf"):
						r.site(g, info, x, stack, sites, add)
					case isPkgFunc(info, x.Fun, feedSchemaPkg, "Migration"):
						r.MigrationCalls++
						migrationCallers = append(migrationCallers, gf.pkg.PkgPath+" "+g.pos(x.Pos()))
						if !own || gf.pkg.PkgPath != ledger.migrationCaller {
							add(g.pos(x.Pos()), writeMigrationCaller,
								"schema.Migration вне "+ledger.migrationCaller+" — второй производитель миграции ленты")
						}
					case isPkgFunc(info, x.Fun, tablenamePkg, "Of"):
						r.OfCalls++
						r.ofUse(g, info, x, stack, names, add)
					}
				case *ast.Ident:
					// Значение-функция tablename.Of (не вызов) — то же сохранение.
					fn, ok := info.Uses[x].(*types.Func)
					if !ok || fn.Pkg() == nil || fn.Pkg().Path() != tablenamePkg || fn.Name() != "Of" {
						return true
					}
					if _, isCall := calleeCall(stack); !isCall {
						add(g.pos(x.Pos()), writeSaved, "tablename.Of употреблена значением-функцией — имя таблицы уходит из-под ведомости")
					}
				}
				return true
			})
	}

	// Ведомость.
	if own {
		owners := map[string]bool{}
		for o := range sites {
			owners[o] = true
		}
		for o := range names {
			owners[o] = true
		}
		for _, o := range sortedKeys(owners) {
			entry, ok := ledger.owners[o]
			for _, s := range sites[o] {
				if !entry.ddl && strings.Contains(s.kinds, "Window") {
					r.WindowSites++
					r.WindowSiteAt = append(r.WindowSiteAt, s.pos+" "+o)
				}
			}
			if !ok {
				for _, s := range sites[o] {
					add(s.pos, writeOutsideLedger, fmt.Sprintf("место %s (%s) владельца %s вне ведомости", s.kinds, s.pos, o))
				}
				for _, s := range names[o] {
					add(s.pos, writeNameOutsideLedger, "Name предмета уборки из tablename.Of у владельца "+o+" вне ведомости")
				}
				continue
			}
			if got := kindsOfSites(sites[o]); !slices.Equal(got, sortedCopy(entry.sites)) {
				at := ""
				if len(sites[o]) > 0 {
					at = sites[o][0].pos
				}
				add(at, writeLedgerMismatch, fmt.Sprintf("владелец %s: места %v, ведомость %v", o, got, sortedCopy(entry.sites)))
			}
			if got := kindsOfSites(names[o]); !slices.Equal(got, sortedCopy(entry.names)) {
				at := ""
				if len(names[o]) > 0 {
					at = names[o][0].pos
				}
				add(at, writeNameOutsideLedger, fmt.Sprintf("владелец %s: Name %v, ведомость %v", o, got, sortedCopy(entry.names)))
			}
		}
		for _, o := range sortedKeys(ledger.owners) {
			if !owners[o] {
				add("", writeLedgerOrphan, "запись ведомости "+o+" без предмета — владельца с местами в дереве нет")
			}
		}
		if r.MigrationCalls != 1 {
			add("", writeMigrationCaller, fmt.Sprintf("вызовов schema.Migration %d, ожидался один из %s: %v",
				r.MigrationCalls, ledger.migrationCaller, migrationCallers))
		}
	} else {
		for o, ss := range sites {
			for _, s := range ss {
				add(s.pos, writeOutsideLedger, "место "+s.kinds+" владельца "+o+" в дереве без ведомости")
			}
		}
		for o, ss := range names {
			for _, s := range ss {
				add(s.pos, writeNameOutsideLedger, "Name из tablename.Of у владельца "+o+" в дереве без ведомости")
			}
		}
	}

	if err := r.scanSQL(g, add); err != nil {
		return r, err
	}
	if err := r.applyExceptions(g, exceptions, add); err != nil {
		return r, err
	}
	sortFindings(r.Findings)
	return r, nil
}

// site — место Sprintf: виды аргументов-вызовов Of, форма литерала, владелец.
func (r *FeedWritesReport) site(g *goTree, info *types.Info, call *ast.CallExpr, stack []ast.Node,
	sites map[string][]feedSite, add func(pos, kind, why string)) {
	var kinds []string
	for _, a := range call.Args[1:] {
		c, ok := ast.Unparen(a).(*ast.CallExpr)
		if !ok || !isPkgFunc(info, c.Fun, tablenamePkg, "Of") {
			continue
		}
		kinds = append(kinds, ofKind(info, c))
	}
	if len(kinds) == 0 {
		return
	}
	r.Sites++
	pos := g.pos(call.Pos())
	sort.Strings(kinds)
	for i := 1; i < len(kinds); i++ {
		if kinds[i] == kinds[i-1] {
			add(pos, writeDuplicateKind, "вид "+kinds[i]+" назван двумя вызовами Of в одном Sprintf — повтор пишется %[n]s")
		}
	}
	if tv, ok := info.Types[call.Args[0]]; !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		add(pos, writeNonConstFormat, "форматная строка Sprintf с tablename.Of не константа — номер %[n]s не проверить")
	}
	owner, ok := enclosingOwner(g, info, stack)
	if !ok {
		add(pos, writeOutsideFunc, "место tablename.Of вне объявления функции")
		return
	}
	sites[owner] = append(sites[owner], feedSite{pos: pos, kinds: strings.Join(slices.Compact(kinds), ",")})
}

// ofUse — вызов Of вне аргумента Sprintf: допущено только поле Name
// составного литерала retention.Subject.
func (r *FeedWritesReport) ofUse(g *goTree, info *types.Info, call *ast.CallExpr, stack []ast.Node,
	names map[string][]feedSite, add func(pos, kind, why string)) {
	parent := stack[len(stack)-2]
	if p, ok := parent.(*ast.CallExpr); ok && isPkgFunc(info, p.Fun, "fmt", "Sprintf") && slices.Contains(p.Args[1:], ast.Expr(call)) {
		return
	}
	pos := g.pos(call.Pos())
	if kv, ok := parent.(*ast.KeyValueExpr); ok && kv.Value == call && len(stack) >= 3 {
		if cl, ok := stack[len(stack)-3].(*ast.CompositeLit); ok && isSubjectNameField(info, cl, kv) {
			owner, ok := enclosingOwner(g, info, stack)
			if !ok {
				add(pos, writeOutsideFunc, "Name предмета уборки из tablename.Of вне объявления функции")
				return
			}
			r.NameUses++
			names[owner] = append(names[owner], feedSite{pos: pos, kinds: ofKind(info, call)})
			return
		}
	}
	add(pos, writeSaved, "результат tablename.Of сохранён или передан не аргументом Sprintf — имя уходит из-под ведомости")
}

func isSubjectNameField(info *types.Info, cl *ast.CompositeLit, kv *ast.KeyValueExpr) bool {
	tv, ok := info.Types[cl]
	if !ok {
		return false
	}
	named, ok := types.Unalias(tv.Type).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != retentionPkg || named.Obj().Name() != "Subject" {
		return false
	}
	key, ok := kv.Key.(*ast.Ident)
	if !ok {
		return false
	}
	v, ok := info.Uses[key].(*types.Var)
	return ok && v.IsField() && v.Name() == "Name" && v.Pkg() != nil && v.Pkg().Path() == retentionPkg
}

// ofKind — имя константы вида во втором аргументе Of.
func ofKind(info *types.Info, call *ast.CallExpr) string {
	if len(call.Args) != 2 {
		return "?"
	}
	var id *ast.Ident
	switch a := ast.Unparen(call.Args[1]).(type) {
	case *ast.Ident:
		id = a
	case *ast.SelectorExpr:
		id = a.Sel
	}
	if id == nil {
		return "?"
	}
	if c, ok := info.Uses[id].(*types.Const); ok && c.Pkg() != nil && c.Pkg().Path() == tablenamePkg {
		return c.Name()
	}
	return "?"
}

// enclosingOwner — полное имя ближайшего объемлющего объявления функции;
// init различаются координатой объявления.
func enclosingOwner(g *goTree, info *types.Info, stack []ast.Node) (string, bool) {
	for i := len(stack) - 1; i >= 0; i-- {
		fd, ok := stack[i].(*ast.FuncDecl)
		if !ok {
			continue
		}
		obj, ok := info.Defs[fd.Name].(*types.Func)
		if !ok {
			return "init@" + g.pos(fd.Pos()), true
		}
		return obj.FullName(), true
	}
	return "", false
}

func isPkgFunc(info *types.Info, fun ast.Expr, pkg, name string) bool {
	var id *ast.Ident
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	}
	if id == nil {
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == pkg && fn.Name() == name
}

// scanSuffixValues — значение строки: литерал и константное выражение с
// суффиксом таблицы ленты. Сцепление констант свёрнуто проверкой типов;
// выражение, чья часть уже несёт суффикс, повторно не называется.
func (r *FeedWritesReport) scanSuffixValues(g *goTree, gf goFile, add func(pos, kind, why string)) {
	info := gf.pkg.TypesInfo
	matches := func(e ast.Expr) bool {
		tv, ok := info.Types[e]
		return ok && tv.Value != nil && tv.Value.Kind() == constant.String && feedSuffix.MatchString(constant.StringVal(tv.Value))
	}
	ast.Inspect(gf.file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING && matches(x) {
				add(g.pos(x.Pos()), writeSuffixValue, "строка с суффиксом таблицы ленты вне tablename")
			}
		case *ast.BinaryExpr:
			if x.Op == token.ADD && matches(x) && !matches(x.X) && !matches(x.Y) {
				add(g.pos(x.Pos()), writeSuffixValue, "константное выражение с суффиксом таблицы ленты вне tablename")
			}
		}
		return true
	})
}

func kindsOfSites(ss []feedSite) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.kinds)
	}
	sort.Strings(out)
	return out
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sqlWriteHeads — начала операторов, которые называют таблицу ленты как
// писатель либо несут тело, исполняемое базой.
var sqlWriteHeads = regexp.MustCompile(`(?is)^(CREATE\s+(OR\s+REPLACE\s+)?(CONSTRAINT\s+)?(FUNCTION|PROCEDURE|TRIGGER)\b|DO\b|INSERT\b|UPDATE\b|DELETE\b|MERGE\b|TRUNCATE\b|WITH\b|CALL\b)`)

// scanSQL — все отслеживаемые *.sql дерева.
func (r *FeedWritesReport) scanSQL(g *goTree, add func(pos, kind, why string)) error {
	for _, rel := range g.tree.SortedFiles() {
		if !strings.HasSuffix(rel, ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(g.root, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("treehygiene: %s не читается — его операторы не осмотрены: %w", rel, err)
		}
		r.SQLFiles++
		for _, st := range splitSQL(string(data)) {
			if !feedSuffix.MatchString(st.text) {
				continue
			}
			head := strings.TrimSpace(stripSQLComments(st.text))
			if !sqlWriteHeads.MatchString(head) {
				continue
			}
			r.SQLFindings++
			first := strings.Fields(head)
			word := ""
			if len(first) > 0 {
				word = strings.ToUpper(first[0])
			}
			add(fmt.Sprintf("%s:%d", rel, st.line), writeSQL,
				"оператор "+word+" называет таблицу ленты: запись ленты и функции над ней — только notify/feed")
		}
	}
	return nil
}

type sqlStatement struct {
	text string
	line int
}

// splitSQL режет текст на операторы по «;» вне кавычек, долларовых строк и
// комментариев; line — строка начала оператора.
func splitSQL(s string) []sqlStatement {
	var out []sqlStatement
	start, line, startLine := 0, 1, 1
	flush := func(end int) {
		if strings.TrimSpace(stripSQLComments(s[start:end])) != "" {
			out = append(out, sqlStatement{text: s[start:end], line: startLine})
		}
	}
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == '-' && strings.HasPrefix(s[i:], "--"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				end = len(s) - i - 2
			}
			line += strings.Count(s[i:i+2+end], "\n")
			i += 2 + end + 2
		case c == '\'':
			j := i + 1
			for j < len(s) {
				if s[j] == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			line += strings.Count(s[i:min(j+1, len(s))], "\n")
			i = j + 1
		case c == '$':
			m := dollarTag.FindString(s[i:])
			if m == "" {
				i++
				continue
			}
			end := strings.Index(s[i+len(m):], m)
			if end < 0 {
				end = len(s) - i - len(m)
			}
			stop := min(i+len(m)+end+len(m), len(s))
			line += strings.Count(s[i:stop], "\n")
			i = stop
		case c == ';':
			flush(i)
			i++
			start = i
			startLine = line
		default:
			i++
		}
		if start == i {
			continue
		}
		if strings.TrimSpace(s[start:i]) == "" {
			startLine = line
		}
	}
	if start < len(s) {
		flush(len(s))
	}
	return out
}

var dollarTag = regexp.MustCompile(`^\$[A-Za-z_]*\$`)

// stripSQLComments убирает строчные комментарии «--» (начало оператора).
func stripSQLComments(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		if i := strings.Index(l, "--"); i >= 0 {
			l = l[:i]
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
