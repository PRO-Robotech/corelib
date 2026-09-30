// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package form_test

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const formPkgPath = "github.com/PRO-Robotech/corelib/notify/form"

// fragment — фрагмент кода вне пакета form, который обязан НЕ пройти проверку
// типов: каждый — путь, открытый строковому типу, к непустому значению мимо
// функций пакета (NTF1-B28 (форма типа)).
type fragment struct {
	name string
	body string // %[1]s — имя типа
}

var bypassFragments = []fragment{
	{"(1) преобразование из строки", `var _ = form.%[1]s("x")`},
	{"(2) нетипизированная константа", `var _ form.%[1]s = "//a"`},
	{"(3) сложение", `func f(v form.%[1]s) { _ = v + v }`},
	{"(4) срез", `func f(v form.%[1]s) { _ = v[1:] }`},
	{"(5) составной литерал с именованным полем", `var _ = form.%[1]s{v: "x"}`},
	{"(5) составной литерал с позиционным полем", `var _ = form.%[1]s{"x", true}`},
}

var opaqueTypeNames = []string{"Path", "Token", "HeaderText"}

// twinFragment — близнец: значение из функции пакета передаётся приёмнику.
const twinFragment = `
func build(p form.Path) string { s, err := p.Value(); if err != nil { return "" }; return s }
func twin() string { p, err := form.ParsePath("/a"); if err != nil { return "" }; return build(p) }`

// importerWith отдаёт пакет form, а стандартную библиотеку — обычным путём.
type importerWith struct {
	form *types.Package
	std  types.Importer
}

func (i importerWith) Import(path string) (*types.Package, error) {
	if path == formPkgPath {
		return i.form, nil
	}
	return i.std.Import(path)
}

// checkFragment проверяет типы фрагмента в синтетическом пакете вне form и
// возвращает ошибки проверки.
func checkFragment(formPkg *types.Package, body string) []error {
	src := "package frag\n\nimport \"" + formPkgPath + "\"\n\n" + body + "\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "frag.go", src, 0)
	if err != nil {
		return []error{fmt.Errorf("фрагмент не разобран: %w", err)}
	}
	var errs []error
	conf := types.Config{
		Importer: importerWith{form: formPkg, std: importer.Default()},
		Error:    func(err error) { errs = append(errs, err) },
	}
	_, _ = conf.Check("example.invalid/frag", fset, []*ast.File{f}, nil)
	return errs
}

// typeFormFindings — находки пробы: фрагменты обхода, прошедшие проверку типов.
func typeFormFindings(t *testing.T, formPkg *types.Package) (findings []string, checked int) {
	t.Helper()
	for _, typ := range opaqueTypeNames {
		for _, fr := range bypassFragments {
			checked++
			errs := checkFragment(formPkg, fmt.Sprintf(fr.body, typ))
			if len(errs) == 0 {
				findings = append(findings, fmt.Sprintf("form.%s %s: проверку типов прошёл", typ, fr.name))
				continue
			}
			t.Logf("form.%s %s: %v", typ, fr.name, errs[0])
		}
	}
	return findings, checked
}

func loadForm(t *testing.T) *types.Package {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedTypes, Dir: "."}, ".")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	require.Empty(t, pkgs[0].Errors)
	require.Equal(t, formPkgPath, pkgs[0].PkgPath)
	return pkgs[0].Types
}

// NTF1-B28 (форма типа): каждый фрагмент обхода — ошибка типа, близнец — без
// ошибок.
func TestOpaqueTypesAreNotConstructibleOutsideThePackage(t *testing.T) {
	formPkg := loadForm(t)
	findings, checked := typeFormFindings(t, formPkg)
	t.Logf("фрагментов проверено: %d", checked)
	require.Equal(t, len(opaqueTypeNames)*len(bypassFragments), checked)
	require.Empty(t, findings)

	require.Empty(t, checkFragment(formPkg, twinFragment), "близнец обязан проходить проверку типов")
}

// Инъекция: копия пакета, где типы объявлены строковыми, — фрагменты (1)–(4)
// проходят проверку типов, и проба их называет.
func TestOpaqueTypesInjectionStringTypeIsFound(t *testing.T) {
	const copySrc = `package form
type Path string
type Token string
type HeaderText string
func ParsePath(s string) (Path, error) { return Path(s), nil }
func (p Path) Value() (string, error) { return string(p), nil }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "form.go", copySrc, 0)
	require.NoError(t, err)
	injected, err := (&types.Config{}).Check(formPkgPath, fset, []*ast.File{f}, nil)
	require.NoError(t, err)

	findings, _ := typeFormFindings(t, injected)
	require.Len(t, findings, 4*len(opaqueTypeNames), "фрагменты (1)–(4) каждого типа")
	for _, typ := range opaqueTypeNames {
		for _, n := range []string{"(1)", "(2)", "(3)", "(4)"} {
			want := "form." + typ + " " + n
			found := false
			for _, got := range findings {
				found = found || strings.HasPrefix(got, want)
			}
			require.True(t, found, "находка %q не названа", want)
		}
	}
}

// NTF1-B28, З1: form не тянет формат шаблонов, рендер и почту.
func TestFormDependsOnNoFormatRenderOrMail(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err)
	deps := strings.Fields(string(out))
	t.Logf("зависимостей notify/form: %d", len(deps))
	require.Contains(t, deps, formPkgPath, "перепись зависимостей пуста — это не зелёный")
	for _, banned := range []string{
		"github.com/PRO-Robotech/corelib/notify/spec",
		"html/template", "text/template", "net/smtp", "mime/multipart",
	} {
		require.NotContains(t, deps, banned)
	}
}
