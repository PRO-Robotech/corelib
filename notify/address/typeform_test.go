// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package address_test

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const addressPkgPath = "github.com/PRO-Robotech/corelib/notify/address"

// fragment — фрагмент кода вне пакета address, который обязан НЕ пройти проверку
// типов: каждый — путь, открытый строковому типу, к непустому значению мимо
// функций пакета (NTF1-B28 (форма типа)).
type fragment struct {
	name string
	body string // %[1]s — имя типа
}

var bypassFragments = []fragment{
	{"(1) преобразование из строки", `var _ = address.%[1]s("x")`},
	{"(2) нетипизированная константа", `var _ address.%[1]s = "x"`},
	{"(3) сложение", `func f(v address.%[1]s) { _ = v + v }`},
	{"(4) срез", `func f(v address.%[1]s) { _ = v[1:] }`},
	{"(5) составной литерал с именованным полем", `var _ = address.%[1]s{v: "x"}`},
	{"(5) составной литерал с позиционным полем", `var _ = address.%[1]s{"x"}`},
}

var opaqueTypeNames = []string{"Normalized", "Domain"}

// twinFragment — близнец: значение из Normalize передаётся ключу окна.
const twinFragment = `
func windowKey(n address.Normalized) (string, error) { return n.Value() }
func twin() (string, error) {
	n, err := address.Normalize("user@example.invalid")
	if err != nil { return "", err }
	return windowKey(n)
}`

// importerWith отдаёт пакет address, а стандартную библиотеку — обычным путём.
type importerWith struct {
	pkg *types.Package
	std types.Importer
}

func (i importerWith) Import(path string) (*types.Package, error) {
	if path == addressPkgPath {
		return i.pkg, nil
	}
	return i.std.Import(path)
}

// checkFragment проверяет типы фрагмента в синтетическом пакете вне address и
// возвращает ошибки проверки.
func checkFragment(pkg *types.Package, body string) []error {
	src := "package frag\n\nimport \"" + addressPkgPath + "\"\n\n" + body + "\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "frag.go", src, 0)
	if err != nil {
		return []error{fmt.Errorf("фрагмент не разобран: %w", err)}
	}
	var errs []error
	conf := types.Config{
		Importer: importerWith{pkg: pkg, std: importer.Default()},
		Error:    func(err error) { errs = append(errs, err) },
	}
	_, _ = conf.Check("example.invalid/frag", fset, []*ast.File{f}, nil)
	return errs
}

// typeFormFindings — находки пробы: фрагменты обхода, прошедшие проверку типов.
func typeFormFindings(t *testing.T, pkg *types.Package) (findings []string, checked int) {
	t.Helper()
	for _, typ := range opaqueTypeNames {
		for _, fr := range bypassFragments {
			checked++
			errs := checkFragment(pkg, fmt.Sprintf(fr.body, typ))
			if len(errs) == 0 {
				findings = append(findings, fmt.Sprintf("address.%s %s: проверку типов прошёл", typ, fr.name))
				continue
			}
			t.Logf("address.%s %s: %v", typ, fr.name, errs[0])
		}
	}
	return findings, checked
}

func loadAddress(t *testing.T) *types.Package {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName | packages.NeedTypes, Dir: "."}, ".")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	require.Empty(t, pkgs[0].Errors)
	require.Equal(t, addressPkgPath, pkgs[0].PkgPath)
	return pkgs[0].Types
}

// NTF1-B27 (форма типа): каждый фрагмент обхода — ошибка типа, близнец — без
// ошибок.
func TestOpaqueTypesAreNotConstructibleOutsideThePackage(t *testing.T) {
	pkg := loadAddress(t)
	findings, checked := typeFormFindings(t, pkg)
	t.Logf("фрагментов проверено: %d", checked)
	require.Equal(t, len(opaqueTypeNames)*len(bypassFragments), checked)
	require.Empty(t, findings)

	require.Empty(t, checkFragment(pkg, twinFragment), "близнец обязан проходить проверку типов")
}

// Инъекция: копия пакета, где типы объявлены строковыми, — фрагменты (1)–(4)
// проходят проверку типов, и проба их называет (NTF1-B27 (форма типа)).
func TestOpaqueTypesInjectionStringTypeIsFound(t *testing.T) {
	const copySrc = `package address
type Normalized string
type Domain string
func Normalize(s string) (Normalized, error) { return Normalized(s), nil }
func (n Normalized) Value() (string, error) { return string(n), nil }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "address.go", copySrc, 0)
	require.NoError(t, err)
	injected, err := (&types.Config{}).Check(addressPkgPath, fset, []*ast.File{f}, nil)
	require.NoError(t, err)

	findings, _ := typeFormFindings(t, injected)
	require.Len(t, findings, 4*len(opaqueTypeNames), "фрагменты (1)–(4) каждого типа")
	for _, typ := range opaqueTypeNames {
		for _, n := range []string{"(1)", "(2)", "(3)", "(4)"} {
			want := "address." + typ + " " + n
			found := false
			for _, got := range findings {
				found = found || strings.HasPrefix(got, want)
			}
			require.True(t, found, "находка %q не названа", want)
		}
	}
}
