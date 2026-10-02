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
// обёртка оказывается пустышкой либо путь закрывает спан мимо неё —
// трассировка исчезает, а поведение движка не меняется НИ В ЧЁМ.
//
// Пробы апстрима этот отказ не видят ПО ПОСТРОЕНИЮ: они судят, какой ответ
// движок отдал на какой запрос, а не открылся ли при этом спан.
//
// # Чем предмет держится
//
// Двумя половинами, и ни одна не заменяет другую.
//
//	ПОВЕДЕНИЕ   enginespan_test.go — подставляет живой провайдер трассировки,
//	            гоняет КАЖДЫЙ путь движка, на котором апстрим открывал спан, и
//	            требует, чтобы спан был создан, закрыт ровно однажды и отмечен
//	            исходом так, как отмечает его текущая обёртка.
//
//	СОСТАВ      coverage_test.go — судит этим разбором ФАКТИЧЕСКИЙ состав
//	            открытий спанов в поддереве: каждое закрыто обёрткой
//	            фундамента, и перечень поведенческой половины равен составу.
//	            Без неё поведенческая половина судила бы перечень, а не класс.
//
// # Что именно считает разбор
//
// ОТКРЫТИЕ — вызов `….Start(ctx, имя, …)`, то есть селектор Start не меньше
// чем с двумя доводами; имя-литерал разбор достаёт, иное открытие не
// отбрасывает, а отдаёт с пустым именем. ЗАКРЫТИЕ — отложенный вызов
// `….End(…)` в той же области: функции либо функционального литерала, потому
// что отложенный вызов исполняется при выходе из СВОЕЙ функции. Закрытие
// через функцию пакета разбор отдаёт с ДОСЛОВНЫМ путём импорта, закрытие
// методом значения — с пустым путём.
//
// Имя пакета к делу не относится: наша обёртка зовётся `otelx` ровно так же,
// как заменённая, — благодаря этому места вызова в поддереве остаются
// побайтово апстримными. Различить их можно ТОЛЬКО по пути импорта.
//
// Обёртка законна только ОТЛОЖЕННОЙ НАПРЯМУЮ (`defer otelx.End(span, &err)`):
// её recover работает лишь тогда, когда отложенная функция — она сама. Форма
// `defer func() { otelx.End(span, &err) }()` поэтому законной не считается —
// закрытие во вложенном литерале принадлежит литералу.
package tracecut

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

// Closer — один отложенный вызов End в области открытия.
type Closer struct {
	// Import — путь импорта пакета, чья функция End отложена. Пусто, если End —
	// метод значения (`defer span.End()`), а не функция пакета.
	Import string

	// Receiver — выражение слева от `.End`: местное имя пакета либо значение.
	Receiver string

	// Line — строка отложенного вызова.
	Line int
}

// Opening — одно открытие спана вместе с тем, чем оно закрывается.
type Opening struct {
	// File и Line — координата открытия, как её дал разбор.
	File string
	Line int

	// Func — область открытия: «Функция», «Тип.Метод», для литерала —
	// «<объемлющая>.funcN», вне функций — «<пакет>».
	Func string

	// Name — имя спана из строкового литерала; пусто, если второй довод не
	// литерал.
	Name string

	// Closers — отложенные вызовы End в той же области. Пусто означает, что
	// спан открыт и брошен.
	Closers []Closer
}

// Closed сообщает, закрывается ли спан вообще.
func (o Opening) Closed() bool { return len(o.Closers) > 0 }

// ScanSpanOpenings возвращает все открытия спанов в одном файле Go.
//
// Разбор идёт по ТЕКСТУ, без сборки и без разрешения типов: проверке состава
// нужен ответ и о файле, который не собирается, — иначе «сборка сломалась»
// становилось бы неотличимо от «открытий нет».
//
// Ошибка возвращается только на неразобравшемся файле. Файл без открытий —
// законный ПУСТОЙ ответ.
func ScanSpanOpenings(path string, src []byte) ([]Opening, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("tracecut: %s не разобрался: %w", path, err)
	}
	s := &scanner{fset: fset, path: path, imports: importsByLocalName(file)}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				s.scope(funcName(d), d.Body)
			}
		case *ast.GenDecl:
			s.scope("<пакет>", d)
		}
	}
	return s.out, nil
}

type scanner struct {
	fset    *token.FileSet
	path    string
	imports map[string]string
	out     []Opening
}

// scope разбирает одну область: открытия и отложенные End, не заходя во
// вложенные функциональные литералы — у каждого из них своя область.
func (s *scanner) scope(name string, root ast.Node) {
	var (
		starts  []*ast.CallExpr
		closers []Closer
		lits    []*ast.FuncLit
	)
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			lits = append(lits, n)
			return false
		case *ast.DeferStmt:
			if c, ok := s.closer(n); ok {
				closers = append(closers, c)
			}
		case *ast.CallExpr:
			if isSpanStart(n) {
				starts = append(starts, n)
			}
		}
		return true
	})
	for _, call := range starts {
		s.out = append(s.out, Opening{
			File:    s.path,
			Line:    s.fset.Position(call.Pos()).Line,
			Func:    name,
			Name:    literalSpanName(call),
			Closers: append([]Closer(nil), closers...),
		})
	}
	for i, lit := range lits {
		s.scope(fmt.Sprintf("%s.func%d", name, i+1), lit.Body)
	}
}

// closer распознаёт отложенный вызов `….End(…)`.
//
// Местное имя пакета сопоставляется с импортом без разрешения типов: значение,
// названное так же, как импортированный пакет, разбор примет за пакет. Для
// предмета это безопасно в одну сторону — такое затенение дало бы закрытию
// путь обёртки только в файле, где обёртка и импортирована.
func (s *scanner) closer(d *ast.DeferStmt) (Closer, bool) {
	sel, ok := d.Call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "End" {
		return Closer{}, false
	}
	c := Closer{Receiver: types.ExprString(sel.X), Line: s.fset.Position(d.Pos()).Line}
	if id, ok := sel.X.(*ast.Ident); ok {
		c.Import = s.imports[id.Name]
	}
	return c, true
}

// importsByLocalName сопоставляет местное имя пакета его пути импорта.
//
// Местное имя берётся из явного псевдонима, а без псевдонима — из ПОСЛЕДНЕГО
// элемента пути. Это догадка, и она названа: настоящее имя пакета лежит в его
// исходниках, которых разбор по тексту не читает. Для предмета догадка точна —
// обе стороны выреза зовутся `otelx` и лежат в каталогах `otelx`; разойдись
// они, закрытие получило бы пустой путь, и суд покраснел бы, а не промолчал.
func importsByLocalName(file *ast.File) map[string]string {
	out := make(map[string]string, len(file.Imports))
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		local := path[strings.LastIndex(path, "/")+1:]
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

// isSpanStart — вызов селектора Start не меньше чем с двумя доводами.
func isSpanStart(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Start" && len(call.Args) >= 2
}

// literalSpanName достаёт имя спана из второго довода. Пусто означает, что
// второй довод не строковый литерал.
func literalSpanName(call *ast.CallExpr) string {
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
		return types.ExprString(expr)
	}
}

// Source — один файл поддерева с содержимым.
type Source struct {
	Path string
	Src  []byte
}

// Census — объём осмотренного: без него «ноль находок» неотличимо от «ноль
// прочитанного».
type Census struct {
	Files    int
	Lines    int
	Openings int
}

func (c Census) String() string {
	return fmt.Sprintf("осмотрено файлов %d · строк %d · открытий спанов %d", c.Files, c.Lines, c.Openings)
}

// Finding — открытие спана, которого вырез не держит.
type Finding struct {
	Opening Opening
	Reason  string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d %s: спан %q — %s", f.Opening.File, f.Opening.Line, f.Opening.Func, f.Opening.Name, f.Reason)
}

// Report — исход суда над поддеревом.
type Report struct {
	Census   Census
	Openings []Opening
	Findings []Finding
}

var (
	// ErrEmptyWalk — суду не подали ни одного файла.
	ErrEmptyWalk = errors.New("tracecut: обход поддерева не прочитал ни одного файла")

	// ErrNoOpenings — файлы прочитаны, а открытий спанов в них нет: вырез снят
	// целиком либо разбор перестал видеть свой предмет.
	ErrNoOpenings = errors.New("tracecut: в поддереве не найдено ни одного открытия спана")
)

// Judge судит поддерево: каждое открытие спана обязано закрываться ровно одним
// отложенным вызовом End пакета wrapper.
//
// Пустой обход и поддерево без открытий — ОТКАЗ, а не пустой успех; отказ
// несёт объём осмотренного. Неразобравшийся файл роняет суд целиком: открытие
// в нём выпало бы из переписи молча.
func Judge(sources []Source, wrapper string) (Report, error) {
	var rep Report
	if wrapper == "" {
		return rep, errors.New("tracecut: путь импорта обёртки не задан, судить не с чем")
	}
	if len(sources) == 0 {
		return rep, ErrEmptyWalk
	}
	for _, src := range sources {
		openings, err := ScanSpanOpenings(src.Path, src.Src)
		if err != nil {
			return Report{}, err
		}
		rep.Census.Files++
		rep.Census.Lines += lineCount(src.Src)
		rep.Census.Openings += len(openings)
		rep.Openings = append(rep.Openings, openings...)
		for _, o := range openings {
			if reason := verdict(o, wrapper); reason != "" {
				rep.Findings = append(rep.Findings, Finding{Opening: o, Reason: reason})
			}
		}
	}
	if rep.Census.Openings == 0 {
		return Report{Census: rep.Census}, fmt.Errorf("%w (%s)", ErrNoOpenings, rep.Census)
	}
	return rep, nil
}

func verdict(o Opening, wrapper string) string {
	switch {
	case len(o.Closers) == 0:
		return "не закрыт: отложенного вызова End в области открытия нет"
	case len(o.Closers) > 1:
		lines := make([]string, len(o.Closers))
		for i, c := range o.Closers {
			lines[i] = strconv.Itoa(c.Line)
		}
		return fmt.Sprintf("закрыт %d отложенными вызовами End (строки %s), а обязан ровно один раз",
			len(o.Closers), strings.Join(lines, ", "))
	case o.Closers[0].Import == "":
		return fmt.Sprintf("закрыт методом %s.End (строка %d) мимо обёртки %s: исход операции на спане не отмечается",
			o.Closers[0].Receiver, o.Closers[0].Line, wrapper)
	case o.Closers[0].Import != wrapper:
		return fmt.Sprintf("закрыт через %s (строка %d), а обязан через %s: вырез телеметрии отменён",
			o.Closers[0].Import, o.Closers[0].Line, wrapper)
	case o.Name == "":
		return "имя спана не строковый литерал: поведенческая проба не сопоставит путь с составом"
	}
	return ""
}

func lineCount(src []byte) int {
	n := bytes.Count(src, []byte("\n"))
	if len(src) > 0 && src[len(src)-1] != '\n' {
		n++
	}
	return n
}
