// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// brandedknob.go — разбор ИМЁН РУЧЕК с приставкой платформы в прод-коде
// фундамента.
//
// # Что здесь находка
//
// Строковый литерал, начинающийся с приставки, которую назвал ВЫЗЫВАЮЩИЙ, в
// не-проверочном коде фундамента — находка, КРОМЕ одного случая: он есть
// значение константы, чьё имя начинается с `Legacy`, то есть объявлен ОКНОМ
// перехода (см. шапку пакета). Так остаток перестаёт быть остатком: он назван
// решением, и его снятие видно диффом.
//
// # Почему приставку называет ВЫЗЫВАЮЩИЙ, а не этот файл
//
// Приставка — предмет разбора, а не его принадлежность, и держать её здесь
// значило бы нарушить ровно ту норму, которую разбор стережёт: фундамент читают
// ОБА продукта, и имя платформы в нём — то самое, что снимается. Константа
// `BrandedPrefix = "KACHO_"` здесь стояла и БЫЛА НАЙДЕНА этим же гейтом —
// вердикт справедлив: литерал не «предикат», он такой же бренд платформы в
// фундаменте, как и остальные семь, только без окна.
//
// Исключать собственный файл было нельзя: перечень прощённых не истекает сам и
// унаследовал бы следующую слепую зону. Приставка вынесена ПАРАМЕТРОМ — тогда
// разбор судит свой файл наравне с прочими и молчит на нём по существу, а не по
// послаблению. Это же отвечает и на «появится второй продукт»: словарь
// расширяется у вызывающего.
//
// Глобального состояния это не заводит: приставка — довод вызова, а не
// изменяемая переменная уровня пакета (отвергнутая в шапке `envknob`
// альтернатива была про ЧТЕНИЕ ручек, а не про разбор дерева).
//
// # Чего разбор НЕ видит — названо
//
//  1. имя, собранное конкатенацией (`"KACHO_" + suffix`): разбор судит литерал
//     целиком. В дереве таких нет (проверено предикатом пробы), и появление
//     такой формы — предмет расширения, а не край;
//  2. имя, пришедшее из данных — файла настроек, чарта. Это не код фундамента;
//  3. приставка ДРУГОГО продукта — ровно до тех пор, пока вызывающий её не
//     назвал: словарь принадлежит ему.
package envknob

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// legacyConstPrefix — имя константы, объявляющей ОКНО перехода.
const legacyConstPrefix = "Legacy"

// ErrNoPrefix — приставка не названа. Пустая приставка сделала бы находкой
// КАЖДЫЙ строковый литерал дерева: «не сужаем» здесь означало бы «всё подряд»,
// и гейт утонул бы в находках с первого же прогона.
var ErrNoPrefix = errors.New("envknob: приставка не названа — разбор без неё судил бы каждый литерал")

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

// ScanBrandedKnobs разбирает один файл и возвращает найденные имена с приставкой.
//
// prefix — приставка, которую фундамент носить не вправе; её называет
// ВЫЗЫВАЮЩИЙ (почему — в шапке файла). Пустая отвергается [ErrNoPrefix].
//
// Второе возвращаемое — сколько строковых литералов осмотрено: «ноль находок»
// обязано быть отличимо от «ноль прочитанного».
func ScanBrandedKnobs(path string, src []byte, prefix string) (found []BrandedKnob, literals int, err error) {
	if prefix == "" {
		return nil, 0, ErrNoPrefix
	}

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
		if uerr != nil || !strings.HasPrefix(v, prefix) {
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
