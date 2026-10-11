// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/principalwire"
)

// TestEveryFundamentKeyIsBoundByTheListener — каталог `principalwire` и этот
// пакет согласны о том, какие ключи провода читает общий слой слушателя, в ОБЕ
// стороны.
//
// Пометка `Fundament` в каталоге — обещание: «фундамент читает этот ключ своим
// объявлением». Обещание без объявления собирается чисто, и ключ до
// обработчика не доезжает; объявление без обещания значит, что каталог
// перестал описывать провод. Платформа держит то же свойство своим гейтом
// дерева — но лишь когда поднимет пин; здесь оно судится в момент правки, в
// том же модуле, где живут обе стороны.
//
// Предмет опознаётся узлом разбора: константа, чьё значение — селектор
// `<имя импорта principalwire>.<Ident>`, где имя импорта берётся из
// объявления импорта ЭТОГО файла (псевдоним обходил бы разбор по имени
// пакета). Комментарий и строковый литерал предметом не являются.
func TestEveryFundamentKeyIsBoundByTheListener(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("обход каталога пакета: %v", err)
	}
	fset := token.NewFileSet()
	bound := map[string][]string{}
	parsed, constSpecs := 0, 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("чтение %s: %v", f, err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatalf("разбор %s: %v", f, err)
		}
		parsed++
		alias := importName(file, principalwire.ImportPath)
		if alias == "" {
			continue
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				constSpecs++
				for i, v := range vs.Values {
					sel, ok := v.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					x, ok := sel.X.(*ast.Ident)
					if !ok || x.Name != alias {
						continue
					}
					name := "_"
					if i < len(vs.Names) {
						name = vs.Names[i].Name
					}
					pos := fset.Position(vs.Pos())
					bound[sel.Sel.Name] = append(bound[sel.Sel.Name],
						pos.Filename+":"+strconv.Itoa(pos.Line)+" "+name)
				}
			}
		}
	}

	catalogue := map[string]principalwire.Key{}
	var promised, missing, extra []string
	for _, k := range principalwire.Keys() {
		if k.Ident != "" {
			catalogue[k.Ident] = k
		}
		if !k.Fundament {
			continue
		}
		promised = append(promised, k.Name)
		if k.Ident == "" || len(bound[k.Ident]) == 0 {
			missing = append(missing, k.Name+" ("+k.Ident+")")
		}
	}
	for ident, at := range bound {
		k, known := catalogue[ident]
		if known && !k.Fundament {
			extra = append(extra, k.Name+" ← "+strings.Join(at, ", "))
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	t.Logf("перепись: файлов разобрано %d, объявлений констант %d, ключей каталога связано %d, "+
		"помечено Fundament %d (%s)", parsed, constSpecs, len(bound), len(promised),
		strings.Join(promised, ", "))

	if len(missing) > 0 {
		t.Errorf("каталог объявил ключ читаемым у фундамента, а этот пакет его не объявил — %d: %s\n"+
			"Снятие: завести константу `= principalwire.<Ident>` и читать ключ под решением о "+
			"доверии, либо снять пометку Fundament.", len(missing), strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		t.Errorf("этот пакет объявил ключ, который каталог читаемым у фундамента не объявлял — %d:\n  %s",
			len(extra), strings.Join(extra, "\n  "))
	}
	// Предпосылка — после находок: «прочитал и не нашёл» отлично от «искать
	// стало нечего».
	if parsed == 0 {
		t.Fatal("разобрано ноль файлов пакета — молчание пробы сказано ни о чём")
	}
	if len(promised) == 0 || len(bound) == 0 {
		t.Fatalf("сверять не с чем: помечено Fundament %d, связано %d", len(promised), len(bound))
	}
}

// importName — имя, под которым файл видит пакет path; "" — не импортирует.
func importName(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return path[strings.LastIndex(path, "/")+1:]
	}
	return ""
}
