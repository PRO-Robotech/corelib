// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package tracecut держит ВЫРЕЗ ТЕЛЕМЕТРИИ во внесённом поддереве движка.
//
// # Предмет
//
// Апстрим `github.com/ory/fosite` открывает спаны сам и закрывает их через
// `github.com/ory/x/otelx`. При внесении поддерева эта обвязка заменена своей —
// `github.com/PRO-Robotech/corelib/internal/otelx` (зачем именно — там же в
// документации пакета). Замена создаёт класс отказа, которого у апстрима нет:
//
//	обёртка оказывается ПУСТЫШКОЙ, трассировка исчезает,
//	а поведение движка не меняется НИ В ЧЁМ.
//
// Пробы апстрима этот отказ не видят ПО ПОСТРОЕНИЮ: они судят, какой ответ
// движок отдал на какой запрос, а не открылся ли при этом спан. Пустая обёртка
// прошла бы их все до одной.
//
// # Чем предмет держится
//
// Двумя половинами, и ни одна не заменяет другую.
//
//	ПОВЕДЕНИЕ   internal/tracecut/enginespan_test.go — подставляет живой
//	            провайдер трассировки, гоняет КАЖДЫЙ путь движка, на котором
//	            апстрим открывал спан, и требует, чтобы спан был ДЕЙСТВИТЕЛЬНО
//	            создан, закрыт и нёс признаки ошибки: статус и теги.
//
//	СОСТАВ      internal/tracecut/coverage_test.go — сверяет ПЕРЕЧЕНЬ путей,
//	            которые гоняет поведенческая половина, с ФАКТИЧЕСКИМ составом
//	            открытий спанов в поддереве, снятым этим разбором. Без неё
//	            поведенческая половина судила бы перечень, а не класс: десятый
//	            путь, приехавший со следующей версией апстрима, остался бы вне
//	            суда молча.
//
// # Что именно считает разбор
//
// ОТКРЫТИЕ — вызов `….Start(ctx, "имя")` со строковым литералом вторым
// доводом. ЗАКРЫТИЕ — отложенный вызов `End` из пакета, чей путь импорта
// разбор возвращает ДОСЛОВНО. Путь возвращается, а не сверяется здесь,
// намеренно: разбор отвечает на вопрос «чем закрыто», а решение «чем закрывать
// законно» принимает проверка дерева. Иначе тот же список лежал бы в двух
// местах.
//
// Имя пакета к делу не относится: наша обёртка зовётся `otelx` ровно так же,
// как заменённая, — благодаря этому места вызова в поддереве остаются
// побайтово апстримными. Различить их можно ТОЛЬКО по пути импорта.
package tracecut

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
)

// Opening — одно открытие спана вместе с тем, чем оно закрывается.
type Opening struct {
	// File и Line — координата открытия, как её дал разбор.
	File string
	Line int

	// Func — имя объемлющей функции; для метода — «Тип.Метод».
	Func string

	// Name — имя спана из строкового литерала.
	Name string

	// CloserImport — ПУТЬ ИМПОРТА пакета, через который спан закрывается
	// отложенным вызовом `End` в той же функции. Пусто означает, что такого
	// вызова в функции НЕТ, — то есть спан открыт и брошен.
	CloserImport string
}

// Closed сообщает, закрывается ли спан вообще.
func (o Opening) Closed() bool { return o.CloserImport != "" }

// ScanSpanOpenings возвращает все открытия спанов в одном файле Go.
//
// Разбор идёт по ТЕКСТУ, без сборки и без разрешения типов: проверке состава
// нужен ответ и о файле, который не собирается, — иначе «сборка сломалась»
// становилось бы неотличимо от «открытий нет».
//
// Ошибка возвращается только на неразобравшемся файле. Файл без открытий —
// законный ПУСТОЙ ответ: смотреть было на что, просто нечего было найти.
func ScanSpanOpenings(path string, src []byte) ([]Opening, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("tracecut: %s не разобрался: %w", path, err)
	}

	imports := importsByLocalName(file)

	var out []Opening
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		closer := deferredCloserImport(fn.Body, imports)
		name := funcName(fn)
		for _, open := range spanStarts(fn.Body) {
			out = append(out, Opening{
				File:         path,
				Line:         fset.Position(open.Pos()).Line,
				Func:         name,
				Name:         literalSpanName(open),
				CloserImport: closer,
			})
		}
	}
	return out, nil
}

// importsByLocalName сопоставляет местное имя пакета его пути импорта.
//
// Местное имя берётся из явного псевдонима, а без псевдонима — из ПОСЛЕДНЕГО
// элемента пути. Это догадка, и она названа: настоящее имя пакета лежит в его
// исходниках, которых разбор по тексту не читает. Для предмета догадка точна —
// обе стороны выреза зовутся `otelx` и лежат в каталогах `otelx`; разойдись
// они, проверка состава увидела бы пустой путь закрытия и покраснела, а не
// промолчала.
func importsByLocalName(file *ast.File) map[string]string {
	out := make(map[string]string, len(file.Imports))
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		local := lastPathElement(path)
		if spec.Name != nil {
			if spec.Name.Name == "_" || spec.Name.Name == "." {
				continue
			}
			local = spec.Name.Name
		}
		out[local] = path
	}
	return out
}

func lastPathElement(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// deferredCloserImport возвращает путь импорта пакета, чей `End` отложен в теле
// функции. Пусто — отложенного `End` нет.
func deferredCloserImport(body *ast.BlockStmt, imports map[string]string) string {
	var found string
	ast.Inspect(body, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		stmt, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		sel, ok := stmt.Call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "End" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if path, ok := imports[pkg.Name]; ok {
			found = path
		}
		return true
	})
	return found
}

// spanStarts возвращает вызовы `….Start(ctx, "имя")` в теле функции.
func spanStarts(body *ast.BlockStmt) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Start" || len(call.Args) < 2 {
			return true
		}
		if literalSpanName(call) == "" {
			return true
		}
		out = append(out, call)
		return true
	})
	return out
}

// literalSpanName достаёт имя спана из второго довода. Пусто означает, что
// второй довод не строковый литерал.
func literalSpanName(call *ast.CallExpr) string {
	if len(call.Args) < 2 {
		return ""
	}
	lit, ok := call.Args[1].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	name, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return name
}

// funcName возвращает «Тип.Метод» для метода и «Функция» для функции.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return receiverTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	default:
		return "?"
	}
}
