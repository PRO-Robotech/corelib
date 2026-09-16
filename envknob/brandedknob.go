// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// brandedknob.go — разбор ИМЁН РУЧЕК с приставкой платформы в прод-коде
// фундамента.
//
// # Что здесь находка
//
// Строковый литерал вида `KACHO_*` в не-проверочном коде фундамента — находка,
// КРОМЕ одного случая: он есть значение константы, чьё имя начинается с
// `Legacy`, то есть объявлен ОКНОМ перехода (см. шапку пакета). Так остаток
// перестаёт быть остатком: он назван решением, и его снятие видно диффом.
//
// # Почему РАЗБОР, а не поиск по образцу
//
// Приставка платформы законно стоит в прозе — в godoc, объясняющем переход, и в
// текстах отказов, называющих оператору прежнее написание. Поиск по образцу
// краснел бы на собственном объяснении. Разбор судит УЗЕЛ-ЛИТЕРАЛ и знает,
// какой константе он принадлежит.
//
// # Чего разбор НЕ видит — названо
//
//  1. имя, собранное конкатенацией (`"KACHO_" + suffix`): разбор судит литерал
//     целиком. В дереве таких нет (проверено предикатом пробы), и появление
//     такой формы — предмет расширения, а не край;
//  2. имя, пришедшее из данных — файла настроек, чарта. Это не код фундамента;
//  3. приставка ДРУГОГО продукта. Предмет здесь один — платформа: её имя
//     фундамент носил, её и снимает. Появится вторая — словарь расширяется.
package envknob

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// BrandedPrefix — приставка платформы, которую фундамент носить не вправе.
const BrandedPrefix = "KACHO_"

// legacyConstPrefix — имя константы, объявляющей ОКНО перехода.
const legacyConstPrefix = "Legacy"

// BrandedKnob — одно имя ручки с приставкой платформы и его координата.
type BrandedKnob struct {
	File string
	Line int
	// Name — само имя ручки.
	Name string
	// Const — имя константы, чьим значением литерал является; пусто, если он не
	// константа вовсе.
	Const string
	// InWindow — литерал объявлен окном перехода (`Legacy…`).
	InWindow bool
}

// BrandedKnobCensus — объём осмотренного.
type BrandedKnobCensus struct {
	// Files — файлов разобрано.
	Files int
	// Literals — строковых литералов осмотрено.
	Literals int
	// Branded — из них с приставкой платформы.
	Branded int
	// InWindow — из них объявленных окном перехода.
	InWindow int
}

// ScanBrandedKnobs разбирает один файл и возвращает найденные имена с приставкой.
//
// Второе возвращаемое — сколько строковых литералов осмотрено: «ноль находок»
// обязано быть отличимо от «ноль прочитанного».
func ScanBrandedKnobs(path string, src []byte) (found []BrandedKnob, literals int, err error) {
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
	if perr != nil {
		return nil, 0, perr
	}

	// Литерал → имя константы, чьим значением он объявлен.
	owner := map[*ast.BasicLit]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || i >= len(vs.Names) {
					continue
				}
				owner[lit] = vs.Names[i].Name
			}
		}
		return true
	})

	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		literals++
		v, uerr := strconv.Unquote(lit.Value)
		if uerr != nil || !strings.HasPrefix(v, BrandedPrefix) {
			return true
		}
		name := owner[lit]
		found = append(found, BrandedKnob{
			File:     path,
			Line:     fset.Position(lit.Pos()).Line,
			Name:     v,
			Const:    name,
			InWindow: strings.HasPrefix(name, legacyConstPrefix),
		})
		return true
	})

	sort.Slice(found, func(a, b int) bool { return found[a].Line < found[b].Line })
	return found, literals, nil
}

// UnwindowedBrandedKnobs — те из найденных, что окном НЕ объявлены.
func UnwindowedBrandedKnobs(all []BrandedKnob) []BrandedKnob {
	var out []BrandedKnob
	for _, k := range all {
		if !k.InWindow {
			out = append(out, k)
		}
	}
	return out
}
