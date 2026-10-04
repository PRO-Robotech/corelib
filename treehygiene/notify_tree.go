// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_tree.go — общий разбор дерева для гейтов NTF-1 (З16): не-тестовые
// пакеты, разобранные и проверенные типами, только из индекса git, со
// сгенерированными стабами каталога стабов, отложенными в сторону.
//
// # Почему проверка типов, а не образец
//
// Узлы гейтов NTF-1 опознаются по ИДЕНТИЧНОСТИ объекта (`*types.Func`,
// `*types.TypeName`): вызов через псевдоним импорта, значение-функция и
// метод через встраивание видны проверке типов и не видны образцу текста.
// Имя в комментарии, строке или тексте ошибки узлом не является.
//
// # Чего разбор не видит — и кто это видит
//
// Проверка типов идёт в контексте сборки процесса: файл под ограничением
// сборки, которое не выполняется, в пакет не входит. Поэтому виды, которые
// приёмка судит «без учёта ограничений сборки» (импорт `unsafe` и `"C"`,
// функция без тела, файл не на Go), разбираются отдельно — разбором каждого
// отслеживаемого файла, без проверки типов (notify_typesafety.go).
package treehygiene

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/corelib/treecorpus"
)

// Пути пакетов, о которых говорят гейты NTF-1. Объявлены однажды.
const (
	corelibModule = "github.com/PRO-Robotech/corelib"
	feedPkg       = corelibModule + "/notify/feed"
	feedSchemaPkg = feedPkg + "/schema"
	// resourceEventPkg — тело функции resource-event формы fanout (NTF-3
	// З10): вставка строки ленты в SQL-половине.
	resourceEventPkg = feedPkg + "/resourceevent"
	tablenamePkg     = feedPkg + "/internal/tablename"
	addressPkg       = corelibModule + "/notify/address"
	formPkg          = corelibModule + "/notify/form"
	retentionPkg     = corelibModule + "/retention"
	notifygenPkg     = corelibModule + "/cmd/notifygen"
	idnaPkg          = "golang.org/x/net/idna"
	unsafeImport     = "unsafe"
	cgoImport        = "C"
	reflectPkg       = "reflect"
	generatedMarker  = "сгенерированный файл каталога стабов"
)

// goFile — не-тестовый отслеживаемый файл пакета, проверенного типами.
type goFile struct {
	rel  string
	file *ast.File
	pkg  *packages.Package
}

// goTree — дерево для гейтов: индекс git и пакеты, проверенные типами.
type goTree struct {
	root      string
	module    string
	tree      *treecorpus.Tree
	fset      *token.FileSet
	files     []goFile
	packages  int
	generated int
	// buildFailures — пакеты с отказом сборки go list при целых разборе и
	// проверке типов; печатаются в переписи.
	buildFailures []string
}

// TreeCensus — объём осмотренного одним гейтом по одному дереву. Печатается
// всегда: «ноль находок» обязано быть отличимо от «ноль прочитанного».
type TreeCensus struct {
	// Corelib — версия модуля corelib, из которой исполнен гейт.
	Corelib string
	// Module — путь модуля обойдённого дерева.
	Module string
	// Packages, Files — не-тестовых пакетов и файлов, проверенных типами.
	Packages, Files int
	// Generated — пропущено сгенерированных файлов каталога стабов.
	Generated int
	// BuildFailures — пакетов с отказом сборки при целых разборе и типах.
	BuildFailures int
}

func (c TreeCensus) String() string {
	return fmt.Sprintf("corelib %s · модуль %s · пакетов %d · файлов %d · пропущено сгенерированных %d · с отказом сборки %d",
		c.Corelib, c.Module, c.Packages, c.Files, c.Generated, c.BuildFailures)
}

// CorelibVersion — версия модуля corelib, из которой исполнен процесс
// (`debug.ReadBuildInfo`): зависимость потребителя либо главный модуль.
func CorelibVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "неизвестна (сборка без сведений о модулях)"
	}
	if bi.Main.Path == corelibModule {
		return "главный модуль " + bi.Main.Version
	}
	for _, d := range bi.Deps {
		if d.Path == corelibModule {
			if d.Replace != nil {
				return d.Version + " => " + d.Replace.Path
			}
			return d.Version
		}
	}
	return "не в графе модулей процесса"
}

// loadGoTree читает индекс git дерева root и проверяет типами его
// не-тестовые пакеты. Файл вне индекса предметом не является; сгенерированный
// файл пропускается, только если лежит в каталоге стабов stubDir и до строки
// package несёт «// Code generated … DO NOT EDIT.». Пакет с ошибками
// разбора или типов — отказ с именем пакета: его узлы не осмотрены.
func loadGoTree(root, stubDir string) (*goTree, error) {
	tree, err := treecorpus.NewTree(root)
	if err != nil {
		return nil, err
	}
	stub := strings.Trim(filepath.ToSlash(stubDir), "/")
	if stub == "" || stub == "." {
		return nil, errors.New("treehygiene: каталог стабов не назван — сгенерированный файл вне его пропускать нельзя")
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports | packages.NeedModule,
		Dir:  tree.Root(),
		Env:  os.Environ(),
		Fset: token.NewFileSet(),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("treehygiene: пакеты дерева %s не загружены: %w", root, err)
	}
	g := &goTree{root: tree.Root(), tree: tree, fset: cfg.Fset}
	var broken []string
	for _, p := range pkgs {
		if fatal := fatalErrors(p); len(fatal) > 0 {
			broken = append(broken, fmt.Sprintf("%s: %v", p.PkgPath, fatal[0]))
			continue
		}
		if len(p.Errors) > 0 {
			// Отказ сборки (go list), а не разбора и не типов: узлы пакета
			// проверка типов видела целиком. Так выглядит пакет с функцией без
			// тела, чьё тело только для чужой архитектуры (NTF1-B28 (7)).
			g.buildFailures = append(g.buildFailures, fmt.Sprintf("%s: %v", p.PkgPath, p.Errors[0]))
		}
		if g.module == "" && p.Module != nil && p.Module.Main {
			g.module = p.Module.Path
		}
		counted := false
		for _, f := range p.Syntax {
			// Имя файла — по позиции с учётом //line: у пакета cgo разобран
			// порождённый файл, и директивы ведут его узлы к исходнику дерева.
			rel, err := filepath.Rel(g.root, cfg.Fset.Position(f.Package).Filename)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !tree.HasFile(rel) || strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if strings.HasPrefix(rel, stub+"/") && ast.IsGenerated(f) {
				g.generated++
				continue
			}
			g.files = append(g.files, goFile{rel: rel, file: f, pkg: p})
			counted = true
		}
		if counted {
			g.packages++
		}
	}
	if len(broken) > 0 {
		sort.Strings(broken)
		return nil, fmt.Errorf("treehygiene: пакеты не разобраны — их узлы не осмотрены: %s", strings.Join(broken, "; "))
	}
	if len(g.files) == 0 {
		return nil, errors.New("treehygiene: в дереве нет ни одного не-тестового файла Go, проверенного типами — " +
			"«ноль находок» на «ноль прочитанного» не вердикт")
	}
	sort.Slice(g.files, func(i, j int) bool { return g.files[i].rel < g.files[j].rel })
	return g, nil
}

func (g *goTree) census() TreeCensus {
	return TreeCensus{Corelib: CorelibVersion(), Module: g.module, Packages: g.packages,
		Files: len(g.files), Generated: g.generated, BuildFailures: len(g.buildFailures)}
}

// fatalErrors — ошибки разбора и проверки типов: с ними узлы пакета
// осмотрены не целиком. Отказ go list (сборки) к ним не относится.
func fatalErrors(p *packages.Package) []packages.Error {
	var out []packages.Error
	for _, e := range p.Errors {
		if e.Kind != packages.ListError {
			out = append(out, e)
		}
	}
	if len(out) == 0 && (p.Types == nil || p.TypesInfo == nil) && len(p.GoFiles) > 0 {
		out = append(out, packages.Error{Msg: "проверка типов не исполнилась", Kind: packages.UnknownError})
	}
	return out
}

// pos — координата узла от корня дерева.
func (g *goTree) pos(p token.Pos) string {
	at := g.fset.Position(p)
	rel, err := filepath.Rel(g.root, at.Filename)
	if err != nil {
		rel = at.Filename
	}
	return fmt.Sprintf("%s:%d:%d", filepath.ToSlash(rel), at.Line, at.Column)
}

// Finding — находка гейта NTF-1: координата, вид узла и пояснение.
type Finding struct {
	Position string
	Kind     string
	Why      string
}

func (f Finding) String() string { return fmt.Sprintf("%s: [%s] %s", f.Position, f.Kind, f.Why) }

func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Position < fs[j].Position })
}
