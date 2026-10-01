// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// notify_typesafety.go — NTF1-B28 (гейт): значения формы и нормализованного
// адреса не пишутся в обход функций их пакетов. Узел «обход типобезопасности»
// (Р7) — семь видов, по идентичности из разбора и проверки типов:
//
//	(1) импорт unsafe (в том числе «_»);
//	(2)–(4) ссылка на reflect.NewAt, (reflect.Value).UnsafePointer,
//	        (reflect.Value).UnsafeAddr — вызов или значение-функция;
//	(5) объявление функции без тела;
//	(6) импорт "C";
//	(7) файл исходника не на Go из перечня Р7.
//
// Виды (1), (5), (6) — разбором КАЖДОГО отслеживаемого не-тестового файла .go
// без учёта ограничений сборки; (7) — по индексу git, каталог без .go с файлом
// не на Go обходом не пропускается (CX1-40 (б)); (2)–(4) — проверкой типов.
// Узел не зависит от того, знает ли пакет типы notify/form и notify/address.
// Допустимых мест нет. Сгенерированный файл пропускается, только если лежит в
// каталоге стабов и до строки package несёт «// Code generated … DO NOT EDIT.».
package treehygiene

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// TypeSafetyKind — вид узла обхода типобезопасности.
type TypeSafetyKind string

// Семь видов узла (Р7).
const (
	KindImportUnsafe  TypeSafetyKind = "import-unsafe"
	KindReflectNewAt  TypeSafetyKind = "reflect-NewAt"
	KindUnsafePointer TypeSafetyKind = "reflect-UnsafePointer"
	KindUnsafeAddr    TypeSafetyKind = "reflect-UnsafeAddr"
	KindBodyless      TypeSafetyKind = "func-without-body"
	KindImportC       TypeSafetyKind = "import-C"
	KindNonGoSource   TypeSafetyKind = "non-go-source"
)

var typeSafetyKinds = []TypeSafetyKind{
	KindImportUnsafe, KindReflectNewAt, KindUnsafePointer, KindUnsafeAddr, KindBodyless, KindImportC, KindNonGoSource,
}

// TypeSafetyKinds — закрытый перечень видов (копия).
func TypeSafetyKinds() []TypeSafetyKind { return slices.Clone(typeSafetyKinds) }

// nonGoSourceExtensions — перечень Р7: ассемблер, объектный файл, SWIG,
// исходники и заголовки C, C++, Objective-C, Fortran. Сверен с классификацией
// go/build в обе стороны пробой УК45.
var nonGoSourceExtensions = []string{
	".s", ".S", ".sx", ".syso", ".swig", ".swigcxx",
	".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx",
	".m", ".f", ".F", ".for", ".f90",
}

// NonGoSourceExtensions — перечень расширений вида (7) (копия).
func NonGoSourceExtensions() []string { return slices.Clone(nonGoSourceExtensions) }

// TypeSafetyReport — исход гейта по одному дереву.
type TypeSafetyReport struct {
	Census TreeCensus
	// Parsed — файлов .go, разобранных без учёта ограничений сборки.
	Parsed int
	// Kinds — узлов по каждому из семи видов (ключ есть у каждого вида).
	Kinds    map[TypeSafetyKind]int
	Findings []Finding
}

func (r TypeSafetyReport) String() string {
	parts := make([]string, 0, len(typeSafetyKinds))
	for _, k := range typeSafetyKinds {
		parts = append(parts, fmt.Sprintf("%s %d", k, r.Kinds[k]))
	}
	return fmt.Sprintf("обход типобезопасности: %s · разобрано без ограничений сборки %d · видов %d: %s",
		r.Census, r.Parsed, len(typeSafetyKinds), strings.Join(parts, ", "))
}

// AuditTypeSafetyBypass — NTF1-B28 (гейт) по дереву root; stubDir — каталог
// стабов дерева (corelib — api, kacho и kaname — pkg/api).
func AuditTypeSafetyBypass(root, stubDir string) (TypeSafetyReport, error) {
	g, err := loadGoTree(root, stubDir)
	if err != nil {
		return TypeSafetyReport{}, err
	}
	r := TypeSafetyReport{Census: g.census(), Kinds: map[TypeSafetyKind]int{}}
	for _, k := range typeSafetyKinds {
		r.Kinds[k] = 0
	}
	add := func(pos string, k TypeSafetyKind, why string) {
		r.Kinds[k]++
		r.Findings = append(r.Findings, Finding{Position: pos, Kind: string(k), Why: why})
	}
	stub := strings.Trim(filepath.ToSlash(stubDir), "/") + "/"

	// (1), (5), (6), (7) — по индексу, без ограничений сборки.
	fset := token.NewFileSet()
	for _, rel := range g.tree.SortedFiles() {
		if slices.Contains(nonGoSourceExtensions, filepath.Ext(rel)) {
			add(rel, KindNonGoSource, "исходник не на Go: тело, которого проверка типов не видит")
			continue
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(g.root, filepath.FromSlash(rel)), nil, parser.ParseComments)
		if perr != nil {
			return r, fmt.Errorf("treehygiene: %s не разбирается — его узлы не осмотрены: %w", rel, perr)
		}
		if strings.HasPrefix(rel, stub) && ast.IsGenerated(f) {
			continue
		}
		r.Parsed++
		at := func(p token.Pos) string {
			pp := fset.Position(p)
			return fmt.Sprintf("%s:%d:%d", rel, pp.Line, pp.Column)
		}
		for _, spec := range f.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			switch path {
			case unsafeImport:
				add(at(spec.Pos()), KindImportUnsafe, "импорт unsafe")
			case cgoImport:
				add(at(spec.Pos()), KindImportC, `импорт "C"`)
			}
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body == nil {
				add(at(fd.Pos()), KindBodyless, "функция "+fd.Name.Name+" без тела: тело вне проверки типов")
			}
		}
	}

	// (2)–(4) — по идентичности из проверки типов.
	for _, gf := range g.files {
		ast.Inspect(gf.file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			fn, ok := gf.pkg.TypesInfo.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != reflectPkg {
				return true
			}
			recv := ""
			if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
				if named, ok := types.Unalias(derefType(sig.Recv().Type())).(*types.Named); ok {
					recv = named.Obj().Name()
				}
			}
			switch {
			case recv == "" && fn.Name() == "NewAt":
				add(g.pos(id.Pos()), KindReflectNewAt, "ссылка на reflect.NewAt")
			case recv == "Value" && fn.Name() == "UnsafePointer":
				add(g.pos(id.Pos()), KindUnsafePointer, "ссылка на (reflect.Value).UnsafePointer")
			case recv == "Value" && fn.Name() == "UnsafeAddr":
				add(g.pos(id.Pos()), KindUnsafeAddr, "ссылка на (reflect.Value).UnsafeAddr")
			}
			return true
		})
	}
	if r.Parsed == 0 {
		return r, fmt.Errorf("treehygiene: не разобрано ни одного файла .go — пустой обход не вердикт")
	}
	sortFindings(r.Findings)
	return r, nil
}

func derefType(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}
