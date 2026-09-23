// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// engineconfig_static_internal_test.go — методы настроек движка не пишут в
// СОДЕРЖИМОЕ полей: статическая проба по исходнику.
//
// Перепись записей (engineconfig_internal_test.go) сверяет каждое поле
// настроек до и после вызова метода по ТОЖДЕСТВУ, а тождество ссылочного поля
// — адрес: у указателя и карты только он, у среза ещё длина и ёмкость, у
// интерфейса — тождество лежащего в нём. Запись в содержимое такого поля —
// элемент карты или среза, поле значения под указателем — адреса не меняет, и
// перепись её не видит, даже если само поле названо в New. Эту половину держит
// разбор исходника: в каждом методе типа настроек движка ищется запись, корень
// которой — приёмник, а глубина больше поля. `c.F = …` — глубина 1 (предмет
// переписи); `c.F[k] = …`, `c.F.X = …`, `*c.F = …` — глубина 2 и больше
// (предмет этой пробы).
//
// Обход читает те файлы, которые компилятор собирает в пакет движка: все
// не-тестовые .go каталога пакета. Методы типа объявимы только в нём. Что
// прочитан тот самый пакет, проверяется, а не предполагается: поля и
// экспортированные методы типа по разбору обязаны совпасть с отражением
// собранного типа.
//
// Формы записи, которые разбор узнаёт (каждая доказана инъекцией ниже —
// TestContentWriteInjectionIsFoundWithItsCoordinate):
//   - присваивание, составное присваивание, ++/--, присваивание в range;
//   - встроенные append, copy, delete, clear — по первому аргументу;
//   - мутаторы стандартной библиотеки из закрытого перечня contentMutators —
//     по первому аргументу, под любым именем импорта;
//   - вызов метода с приёмником-указателем, объявленного в пакете движка, на
//     поле этого типа (`c.AuthorizeEndpointHandlers.Append(h)`);
//   - всё перечисленное через локальный псевдоним содержимого: переменную,
//     получившую его присваиванием, объявлением, range, переключателем типа
//     или преобразованием, и результат вызова метода настроек
//     (`c.GetX(ctx)[0] = …`);
//   - внутри вложенных функциональных литералов и у приёмника-значения.
//
// Чего разбор без типов судить не умеет, он не пропускает, а называет
// НЕРАЗОБРАННЫМ — и проба краснеет так же, как на находке
// (TestUnjudgedFormInjectionIsRedNotSilent): вызов на содержимом поля, если это
// не метод с приёмником-указателем пакета движка, — в том числе через
// значение-метод, взятое у содержимого; передача содержимого или самого
// приёмника аргументом функции, которая не встроенная и не из перечня (копия
// поля-значения — не передача содержимого); взятие адреса содержимого.
// Законные близнецы тех же форм — запись глубины 1 и чтения — молчат
// (TestLawfulTwinOfAContentWriteIsSilent).
//
// Псевдоним прослеживается без учёта порядка: переменная, хоть раз получившая
// содержимое, считается им до конца метода. Цена упрощения — лишнее красное, а
// не молчание.
//
// Чего проба НЕ судит (слепая зона, названная): что делает со ссылочным
// содержимым тот, кому метод его ОТДАЛ, — вызывающий геттер код движка или
// значение, в составной литерал которого оно легло. Это чужой код, а не метод
// настроек, и ни одна проба пакета его не судит.
//
// Глубина 1 здесь только считается и сверяется с тем, что перепись находит на
// пустых настройках (TestSourceAndReflectiveCensusesNameTheSameLazyWrites):
// разбор и отражение — два независимых прибора одного утверждения, и
// расхождение значит, что один из них не видит формы.
//
// Файл внутренний: сверка с переписью зовёт её внутренние функции.
package oauthceremony

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	engine "github.com/PRO-Robotech/corelib/internal/oauth2"
)

// contentMutators — функции стандартной библиотеки, пишущие в содержимое
// ПЕРВОГО аргумента, по пути импорта. Перечень закрыт: функция вне его,
// получившая содержимое настроек, — неразобранная форма, а не молчание.
var contentMutators = map[string][]string{
	"maps":   {"Copy", "DeleteFunc"},
	"slices": {"Compact", "CompactFunc", "Delete", "DeleteFunc", "Insert", "Replace", "Reverse", "Sort", "SortFunc", "SortStableFunc"},
	"sort":   {"Float64s", "Ints", "Slice", "SliceStable", "Sort", "Stable", "Strings"},
}

// builtinContentWriters — встроенные функции, пишущие в содержимое первого
// аргумента. append пишет в общий массив, если у среза есть запас ёмкости.
var builtinContentWriters = map[string]bool{"append": true, "clear": true, "copy": true, "delete": true}

// builtinReaders — встроенные функции, которые аргументы только читают.
var builtinReaders = map[string]bool{
	"cap": true, "complex": true, "imag": true, "len": true, "make": true, "max": true,
	"min": true, "new": true, "panic": true, "print": true, "println": true, "real": true,
}

// basicTypes — предобъявленные типы: вызов с таким именем — преобразование, а
// поле такого типа — значение, копия которого содержимого не несёт.
var basicTypes = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true, "float32": true,
	"float64": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true, "uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true,
}

// foreignValueTypes — типы чужих пакетов с семантикой значения, встречающиеся
// в полях настроек. Перечень закрыт и мал: поле неизвестного чужого типа
// считается ссылочным.
var foreignValueTypes = map[string]bool{"time.Duration": true}

// Формы записи — текст находки называет, КАК записано.
const (
	formAssign   = "присваивание"
	formOpAssign = "составное присваивание"
	formIncDec   = "++/--"
	formRange    = "присваивание в range"
)

// contentWrite — запись с корнем в приёмнике метода настроек.
type contentWrite struct {
	at     string // файл:строка
	method string
	field  string // первое поле пути; «M()» — результат метода настроек; «*» — сами настройки
	depth  int
	form   string
	expr   string
	// valueReceiver — метод с приёмником-значением: запись глубины 1 пишет
	// копию и настроек не касается.
	valueReceiver bool
}

// unjudgedForm — место, о котором разбор без типов не может сказать, пишет ли
// оно в настройки.
type unjudgedForm struct {
	at, method, what, expr string
}

// sourceCensus — перепись обхода исходника: сколько осмотрено и что найдено.
// Ноль находок отличим от нуля осмотренного.
type sourceCensus struct {
	dir          string
	files, lines int
	typeDecls    int      // объявлений типа настроек — ровно одно
	structFields []string // поля типа настроек в порядке объявления
	methods      []string // методы типа настроек в порядке обхода
	rootUses     int      // обращений к приёмнику и его псевдонимам
	writes       []contentWrite
	unjudged     []unjudgedForm
}

// emptyWalk — причина, по которой обход не вердикт; пусто — осмотрено непустое.
func (c sourceCensus) emptyWalk() string {
	if c.files == 0 || len(c.methods) == 0 || c.rootUses == 0 {
		return fmt.Sprintf("НЕ ВЫПОЛНИЛОСЬ: обход %s прочитал файлов %d, методов типа настроек %d, "+
			"обращений к приёмнику %d — судить нечего", c.dir, c.files, len(c.methods), c.rootUses)
	}
	return ""
}

// deeper — записи глубже поля: предмет этой пробы.
func (c sourceCensus) deeper() []contentWrite {
	var out []contentWrite
	for _, w := range c.writes {
		if w.depth >= 2 {
			out = append(out, w)
		}
	}
	return out
}

// sharedFieldWrites — записи глубины 1 в поле настроек (не в копию приёмника):
// предмет переписи, в той же единице, что она.
func (c sourceCensus) sharedFieldWrites() []getterWrite {
	var out []getterWrite
	for _, w := range c.writes {
		if w.depth == 1 && !w.valueReceiver && w.field != "*" && !strings.HasSuffix(w.field, "()") {
			out = append(out, getterWrite{method: w.method, field: w.field})
		}
	}
	sortGetterWrites(out)
	return slices.Compact(out)
}

func (c sourceCensus) summary() string {
	depths := map[int]int{}
	for _, w := range c.writes {
		depths[w.depth]++
	}
	keys := make([]int, 0, len(depths))
	for d := range depths {
		keys = append(keys, d)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, d := range keys {
		parts = append(parts, fmt.Sprintf("глубина %d: %d", d, depths[d]))
	}
	byDepth := "записей нет"
	if len(parts) != 0 {
		byDepth = strings.Join(parts, " · ")
	}
	return fmt.Sprintf("перепись исходника %s: файлов %d · строк %d · методов типа настроек %d · "+
		"обращений к приёмнику %d · записей %d (%s) · глубже поля %d · неразобранных форм %d",
		c.dir, c.files, c.lines, len(c.methods), c.rootUses, len(c.writes), byDepth,
		len(c.deeper()), len(c.unjudged))
}

// contentFindingText — текст находки. Он часть свойства: называет, что
// записано, почему это гонка и почему прежнее средство здесь не помогает.
func contentFindingText(w contentWrite) string {
	target := "поля " + w.field
	if strings.HasSuffix(w.field, "()") {
		target = "того, что отдаёт метод " + w.field
	}
	return fmt.Sprintf("%s: метод %s пишет в СОДЕРЖИМОЕ %s — `%s` (%s, глубина %d). Настройки одни на все "+
		"одновременные запросы, и эта запись гоняется с чтением из другого запроса. Адреса поля она не "+
		"меняет, поэтому перепись по тождеству её не видит; назвать поле в New её НЕ снимает — снимает "+
		"только отсутствие записи в методе", w.at, w.method, target, w.expr, w.form, w.depth)
}

// unjudgedText — текст неразобранной формы.
func unjudgedText(u unjudgedForm) string {
	return fmt.Sprintf("%s: метод %s — %s: `%s`. Разбор без типов не знает, пишет ли эта форма в "+
		"настройки, и потому не молчит: научи разбор этой форме (перечень форм — шапка "+
		"engineconfig_static_internal_test.go) либо убери её из метода", u.at, u.method, u.what, u.expr)
}

// ── Разбор ──────────────────────────────────────────────────────────────────

// packageIndex — то, что разбор знает о пакете движка без типов.
type packageIndex struct {
	typeName     string
	localTypes   map[string]bool
	methodsOf    map[string]map[string]bool // тип → метод → приёмник-указатель
	fieldTypeOf  map[string]string          // поле настроек → локальный именованный тип
	valueField   map[string]bool            // поле настроек со значением без содержимого
	configMethod map[string]bool
}

func (p *packageIndex) isMethod(name string) bool { return p.configMethod[name] }

// pointerMethodOnField — вызов name на поле field есть вызов метода с
// приёмником-указателем, объявленного в пакете движка.
func (p *packageIndex) pointerMethodOnField(field, name string) bool {
	typ, ok := p.fieldTypeOf[field]
	if !ok {
		return false
	}
	return p.methodsOf[typ][name]
}

// origin — откуда выражение: глубина от приёмника и первое поле пути.
type origin struct {
	depth int
	field string
}

// maxAliasDepth ограничивает перепривязку псевдонима вглубь (`h = h.Next`):
// без предела неподвижная точка не наступила бы.
const maxAliasDepth = 8

type methodWalk struct {
	census        *sourceCensus
	pkg           *packageIndex
	fset          *token.FileSet
	imports       map[string]string // имя импорта в файле → путь
	method        string
	valueReceiver bool
	aliases       map[string]origin
}

// walkEngineSource разбирает не-тестовые .go каталога dir и судит методы типа
// typeName пакета pkgName.
func walkEngineSource(dir, pkgName, typeName string) (sourceCensus, error) {
	c := sourceCensus{dir: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return c, fmt.Errorf("каталог пакета движка не прочитан: %w", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return c, fmt.Errorf("%s не прочитан: %w", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return c, fmt.Errorf("%s не разобран: %w", name, err)
		}
		if f.Name.Name != pkgName {
			return c, fmt.Errorf("%s — пакет %q, а не %q: обход читал бы не тот пакет", name, f.Name.Name, pkgName)
		}
		c.files++
		c.lines += bytes.Count(src, []byte("\n"))
		files = append(files, f)
	}

	pkg := indexPackage(files, typeName, &c)
	for _, f := range files {
		imports := importNames(f)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := fn.Recv.List[0]
			base, pointer := receiverType(recv.Type)
			if base != typeName {
				continue
			}
			c.methods = append(c.methods, fn.Name.Name)
			if fn.Body == nil || len(recv.Names) == 0 || recv.Names[0].Name == "_" {
				continue
			}
			w := &methodWalk{
				census: &c, pkg: pkg, fset: fset, imports: imports,
				method: fn.Name.Name, valueReceiver: !pointer,
				aliases: map[string]origin{recv.Names[0].Name: {}},
			}
			w.collectAliases(fn.Body, recv.Names[0].Name)
			w.judge(fn.Body)
		}
	}
	return c, nil
}

func indexPackage(files []*ast.File, typeName string, c *sourceCensus) *packageIndex {
	p := &packageIndex{
		typeName:     typeName,
		localTypes:   map[string]bool{},
		methodsOf:    map[string]map[string]bool{},
		fieldTypeOf:  map[string]string{},
		valueField:   map[string]bool{},
		configMethod: map[string]bool{},
	}
	basicSpecs := map[string]bool{}
	var configStruct *ast.StructType
	var configFile *ast.File
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					p.localTypes[ts.Name.Name] = true
					if id, ok := ts.Type.(*ast.Ident); ok && basicTypes[id.Name] {
						basicSpecs[ts.Name.Name] = true
					}
					if st, ok := ts.Type.(*ast.StructType); ok && ts.Name.Name == typeName {
						c.typeDecls++
						configStruct, configFile = st, f
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil || len(d.Recv.List) != 1 {
					continue
				}
				base, pointer := receiverType(d.Recv.List[0].Type)
				if p.methodsOf[base] == nil {
					p.methodsOf[base] = map[string]bool{}
				}
				p.methodsOf[base][d.Name.Name] = pointer
				if base == typeName {
					p.configMethod[d.Name.Name] = true
				}
			}
		}
	}
	if configStruct == nil {
		return p
	}
	imports := importNames(configFile)
	for _, field := range configStruct.Fields.List {
		names := field.Names
		if len(names) == 0 { // встроенное поле называется своим типом
			if base, _ := receiverType(field.Type); base != "" {
				names = []*ast.Ident{ast.NewIdent(base)}
			}
		}
		for _, n := range names {
			c.structFields = append(c.structFields, n.Name)
			typ := field.Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if id, ok := typ.(*ast.Ident); ok && p.localTypes[id.Name] {
				p.fieldTypeOf[n.Name] = id.Name
			}
			switch t := field.Type.(type) {
			case *ast.Ident:
				p.valueField[n.Name] = basicTypes[t.Name] || basicSpecs[t.Name]
			case *ast.SelectorExpr:
				if pkg, ok := t.X.(*ast.Ident); ok {
					p.valueField[n.Name] = foreignValueTypes[imports[pkg.Name]+"."+t.Sel.Name]
				}
			}
		}
	}
	return p
}

// receiverType — имя базового типа приёмника и то, указатель ли он.
func receiverType(expr ast.Expr) (string, bool) {
	pointer := false
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			pointer = true
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name, pointer
		default:
			return "", pointer
		}
	}
}

// importNames — имя, под которым файл видит каждый импорт, → путь импорта.
func importNames(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = path
	}
	return out
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// root — корень выражения в приёмнике: глубина пути и первое поле.
func (w *methodWalk) root(expr ast.Expr) (origin, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		o, ok := w.aliases[e.Name]
		return o, ok
	case *ast.ParenExpr:
		return w.root(e.X)
	case *ast.SelectorExpr:
		o, ok := w.root(e.X)
		if !ok {
			return origin{}, false
		}
		if o.depth == 0 {
			if w.pkg.isMethod(e.Sel.Name) {
				return origin{}, false // значение-метод, а не содержимое
			}
			return origin{depth: 1, field: e.Sel.Name}, true
		}
		return origin{depth: o.depth + 1, field: o.field}, true
	case *ast.IndexExpr:
		o, ok := w.root(e.X)
		if ok {
			o.depth++
		}
		return o, ok
	case *ast.StarExpr:
		o, ok := w.root(e.X)
		if ok {
			if o.depth == 0 {
				o.field = "*"
			}
			o.depth++
		}
		return o, ok
	case *ast.TypeAssertExpr:
		return w.root(e.X)
	case *ast.SliceExpr:
		return w.root(e.X)
	case *ast.CallExpr:
		// Преобразование несёт то же содержимое, что его аргумент.
		if w.isConversion(e.Fun) && len(e.Args) == 1 {
			return w.root(e.Args[0])
		}
		// Результат метода настроек — содержимое самих настроек.
		if sel, ok := unparen(e.Fun).(*ast.SelectorExpr); ok {
			if o, ok := w.root(sel.X); ok && o.depth == 0 && w.pkg.isMethod(sel.Sel.Name) {
				return origin{depth: 1, field: sel.Sel.Name + "()"}, true
			}
		}
	}
	return origin{}, false
}

// isConversion — вызов есть преобразование к типу, названному предобъявленным
// или локальным именем либо составным выражением типа. Тип чужого пакета
// (`pkg.T(x)`) от функции без типов не отличим и преобразованием не считается.
func (w *methodWalk) isConversion(fun ast.Expr) bool {
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		_, alias := w.aliases[f.Name]
		return !alias && (basicTypes[f.Name] || w.pkg.localTypes[f.Name])
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.InterfaceType, *ast.StructType, *ast.StarExpr:
		return true
	}
	return false
}

// bind делает lhs псевдонимом содержимого rhs со сдвигом extra.
func (w *methodWalk) bind(lhs ast.Expr, rhs ast.Expr, extra int, receiver string) bool {
	id, ok := unparen(lhs).(*ast.Ident)
	if !ok || id.Name == "_" || id.Name == receiver {
		return false
	}
	o, ok := w.root(rhs)
	if !ok {
		return false
	}
	o.depth += extra
	if old, bound := w.aliases[id.Name]; bound && (old.depth >= o.depth || o.depth > maxAliasDepth) {
		return false
	}
	w.aliases[id.Name] = o
	return true
}

// collectAliases — псевдонимы содержимого, до неподвижной точки и без учёта
// порядка.
func (w *methodWalk) collectAliases(body *ast.BlockStmt, receiver string) {
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.AssignStmt:
				if len(s.Lhs) == len(s.Rhs) {
					for i := range s.Lhs {
						changed = w.bind(s.Lhs[i], s.Rhs[i], 0, receiver) || changed
					}
				} else if len(s.Rhs) == 1 {
					changed = w.bind(s.Lhs[0], s.Rhs[0], 0, receiver) || changed
				}
			case *ast.ValueSpec:
				if len(s.Names) == len(s.Values) {
					for i := range s.Names {
						changed = w.bind(s.Names[i], s.Values[i], 0, receiver) || changed
					}
				} else if len(s.Values) == 1 {
					changed = w.bind(s.Names[0], s.Values[0], 0, receiver) || changed
				}
			case *ast.RangeStmt:
				for _, v := range []ast.Expr{s.Key, s.Value} {
					if v != nil {
						changed = w.bind(v, s.X, 1, receiver) || changed
					}
				}
			}
			return true
		})
	}
}

func (w *methodWalk) at(n ast.Node) string {
	p := w.fset.Position(n.Pos())
	return fmt.Sprintf("%s:%d", p.Filename, p.Line)
}

func (w *methodWalk) record(n ast.Node, o origin, form string, expr ast.Expr) {
	w.census.writes = append(w.census.writes, contentWrite{
		at: w.at(n), method: w.method, field: o.field, depth: o.depth, form: form,
		expr: types.ExprString(expr), valueReceiver: w.valueReceiver,
	})
}

func (w *methodWalk) unjudged(n ast.Node, what string, expr ast.Expr) {
	w.census.unjudged = append(w.census.unjudged, unjudgedForm{
		at: w.at(n), method: w.method, what: what, expr: types.ExprString(expr),
	})
}

// judgeTarget — цель присваивания. Переназначение самой локальной переменной
// (приёмника или псевдонима) — не запись в настройки.
func (w *methodWalk) judgeTarget(target ast.Expr, form string) {
	if target == nil {
		return
	}
	if _, ok := unparen(target).(*ast.Ident); ok {
		return
	}
	if o, ok := w.root(target); ok {
		w.record(target, o, form, target)
	}
}

// passesContent — аргумент несёт содержимое настроек, а не копию значения.
func (w *methodWalk) passesContent(arg ast.Expr) bool {
	o, ok := w.root(arg)
	if !ok {
		return false
	}
	return o.depth != 1 || !w.pkg.valueField[o.field]
}

// contentArgsUnjudged — вызов, чей получатель не разобран, получил содержимое.
func (w *methodWalk) contentArgsUnjudged(call *ast.CallExpr, callee string) {
	for _, arg := range call.Args {
		if w.passesContent(arg) {
			w.unjudged(call, callee+" получает содержимое настроек аргументом ("+types.ExprString(arg)+
				"), а запись через параметр разбор не прослеживает", call)
			return
		}
	}
}

func (w *methodWalk) judgeCall(call *ast.CallExpr) {
	fun := unparen(call.Fun)
	if w.isConversion(fun) {
		return // преобразование не пишет; псевдоним результата прослеживает root
	}

	// Встроенные функции и функции пакета движка.
	if id, ok := fun.(*ast.Ident); ok {
		if _, alias := w.aliases[id.Name]; !alias {
			switch {
			case builtinContentWriters[id.Name]:
				if len(call.Args) > 0 {
					if o, ok := w.root(call.Args[0]); ok {
						o.depth++
						w.record(call, o, "встроенная "+id.Name, call)
					}
				}
			case builtinReaders[id.Name]:
			default:
				w.contentArgsUnjudged(call, "функция "+id.Name)
			}
			return
		}
	}

	if sel, ok := fun.(*ast.SelectorExpr); ok {
		// Функция чужого пакета.
		if x, ok := sel.X.(*ast.Ident); ok {
			if path, imported := w.imports[x.Name]; imported {
				if _, alias := w.aliases[x.Name]; !alias {
					if slices.Contains(contentMutators[path], sel.Sel.Name) {
						if len(call.Args) > 0 {
							if o, ok := w.root(call.Args[0]); ok {
								o.depth++
								w.record(call, o, "мутатор "+path+"."+sel.Sel.Name, call)
							}
						}
						return
					}
					w.contentArgsUnjudged(call, "функция "+path+"."+sel.Sel.Name)
					return
				}
			}
		}
		// Вызов, достигнутый через приёмник.
		if o, ok := w.root(sel.X); ok {
			switch {
			case o.depth == 0 && w.pkg.isMethod(sel.Sel.Name):
				w.contentArgsUnjudged(call, "метод настроек "+sel.Sel.Name)
			case o.depth == 0:
				w.contentArgsUnjudged(call, "функция из поля "+sel.Sel.Name)
			case o.depth == 1 && w.pkg.pointerMethodOnField(o.field, sel.Sel.Name):
				w.record(call, origin{depth: 2, field: o.field}, "метод "+sel.Sel.Name+" с приёмником-указателем", call)
			default:
				w.unjudged(call, "вызов "+sel.Sel.Name+" на содержимом поля "+o.field+
					": пишет ли он, разбор без типов не знает", call)
			}
			return
		}
	}

	// Вызов значения-функции, достигнутого через приёмник (псевдоним поля).
	if o, ok := w.root(fun); ok {
		if o.depth <= 1 {
			w.contentArgsUnjudged(call, "функция из поля "+o.field)
		} else {
			w.unjudged(call, "вызов значения, достигнутого через содержимое поля "+o.field, call)
		}
		return
	}
	w.contentArgsUnjudged(call, "вызов "+types.ExprString(fun))
}

// judge обходит тело метода, включая вложенные функциональные литералы.
func (w *methodWalk) judge(body *ast.BlockStmt) {
	notUses := map[*ast.Ident]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.SelectorExpr:
			notUses[s.Sel] = true
		case *ast.KeyValueExpr:
			if id, ok := s.Key.(*ast.Ident); ok {
				notUses[id] = true
			}
		case *ast.Ident:
			if _, ok := w.aliases[s.Name]; ok && !notUses[s] {
				w.census.rootUses++
			}
		case *ast.AssignStmt:
			form := formAssign
			if s.Tok != token.ASSIGN && s.Tok != token.DEFINE {
				form = formOpAssign
			}
			for _, lhs := range s.Lhs {
				w.judgeTarget(lhs, form)
			}
		case *ast.IncDecStmt:
			w.judgeTarget(s.X, formIncDec)
		case *ast.RangeStmt:
			if s.Tok == token.ASSIGN {
				w.judgeTarget(s.Key, formRange)
				w.judgeTarget(s.Value, formRange)
			}
		case *ast.CallExpr:
			w.judgeCall(s)
		case *ast.UnaryExpr:
			if s.Op == token.AND {
				if _, ok := w.root(s.X); ok {
					w.unjudged(s, "адрес содержимого настроек уходит из выражения", s)
				}
			}
		}
		return true
	})
}

// ── Предпосылка: разобран тот пакет, что собран ────────────────────────────

// engineSource — каталог, имя пакета и имя типа настроек движка, выведенные из
// СОБРАННОГО типа, а не выписанные: путь пакета из отражения, корень модуля — из
// ближайшего go.mod.
func engineSource(t *testing.T) (dir, pkgName, typeName string) {
	t.Helper()
	typ := reflect.TypeFor[engine.Config]()
	typeName = typ.Name()
	pkgName = strings.TrimSuffix(typ.String(), "."+typeName)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("рабочий каталог не получен: %v", err)
	}
	root := cwd
	var modFile []byte
	for {
		if modFile, err = os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: над %s нет go.mod", cwd)
		}
		root = parent
	}
	modPath := ""
	for _, line := range strings.Split(string(modFile), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			modPath = f[1]
			break
		}
	}
	rel, ok := strings.CutPrefix(typ.PkgPath(), modPath+"/")
	if modPath == "" || !ok {
		t.Fatalf("ПРЕДПОСЫЛКА: пакет движка %q вне модуля %q (%s)", typ.PkgPath(), modPath, root)
	}
	return filepath.Join(root, filepath.FromSlash(rel)), pkgName, typeName
}

// requireSameTypeAsCompiled — поля и экспортированные методы типа по разбору
// совпадают с отражением собранного типа: иначе обход судил бы не тот тип или
// не все его методы.
func requireSameTypeAsCompiled(t *testing.T, c sourceCensus) {
	t.Helper()
	typ := reflect.TypeFor[engine.Config]()

	if c.typeDecls != 1 {
		t.Fatalf("ПРЕДПОСЫЛКА: объявлений типа %s в %s — %d, а не одно", typ.Name(), c.dir, c.typeDecls)
	}
	wantFields := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		wantFields = append(wantFields, typ.Field(i).Name)
	}
	if !slices.Equal(c.structFields, wantFields) {
		t.Fatalf("ПРЕДПОСЫЛКА: поля %s по разбору %v, у собранного типа %v", typ.Name(), c.structFields, wantFields)
	}
	ptr := reflect.PointerTo(typ)
	wantMethods := make([]string, 0, ptr.NumMethod())
	for i := range ptr.NumMethod() {
		wantMethods = append(wantMethods, ptr.Method(i).Name)
	}
	var gotExported []string
	for _, m := range c.methods {
		if token.IsExported(m) {
			gotExported = append(gotExported, m)
		}
	}
	sort.Strings(gotExported)
	sort.Strings(wantMethods)
	if !slices.Equal(gotExported, wantMethods) {
		t.Fatalf("ПРЕДПОСЫЛКА: экспортированные методы *%s по разбору (%d) %v, у собранного типа (%d) %v",
			typ.Name(), len(gotExported), gotExported, len(wantMethods), wantMethods)
	}
}

func sortGetterWrites(ws []getterWrite) {
	slices.SortFunc(ws, func(a, b getterWrite) int {
		if c := strings.Compare(a.method, b.method); c != 0 {
			return c
		}
		return strings.Compare(a.field, b.field)
	})
}

func walkRealEngineSource(t *testing.T) sourceCensus {
	t.Helper()
	dir, pkgName, typeName := engineSource(t)
	c, err := walkEngineSource(dir, pkgName, typeName)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
	}
	t.Log(c.summary())
	if reason := c.emptyWalk(); reason != "" {
		t.Fatal(reason)
	}
	requireSameTypeAsCompiled(t, c)
	return c
}

// ── Пробы по дереву ─────────────────────────────────────────────────────────

// TestEngineSettingsMethodsWriteNoFieldContent — ни один метод настроек движка
// не пишет в содержимое их полей, и ни одной формы, которую разбор судить не
// умеет, в них нет.
func TestEngineSettingsMethodsWriteNoFieldContent(t *testing.T) {
	c := walkRealEngineSource(t)
	for _, w := range c.deeper() {
		t.Error(contentFindingText(w))
	}
	for _, u := range c.unjudged {
		t.Error(unjudgedText(u))
	}
}

// TestSourceAndReflectiveCensusesNameTheSameLazyWrites — записи глубины 1,
// найденные разбором, — ровно те, что перепись отражением находит на пустых
// настройках. Положительный контроль разбора на живом дереве: обход, не
// видящий записей вовсе, здесь краснеет, а не зеленеет.
func TestSourceAndReflectiveCensusesNameTheSameLazyWrites(t *testing.T) {
	c := walkRealEngineSource(t)
	fromSource := c.sharedFieldWrites()

	reflective := censusGetterWrites(&engine.Config{})
	requireCensusCovered(t, reflective)
	fromReflection := slices.Clone(reflective.writes)
	sortGetterWrites(fromReflection)

	t.Logf("записи глубины 1: разбором %v · отражением %v", fromSource, fromReflection)
	if len(fromSource) == 0 {
		t.Fatalf("разбор не нашёл ни одной записи глубины 1, а отражение нашло %v — разбор не видит записей", fromReflection)
	}
	if !slices.Equal(fromSource, fromReflection) {
		t.Fatalf("приборы разошлись: разбором %v, отражением %v — один из них не видит формы записи; "+
			"выясни, какой, прежде чем править перечень", fromSource, fromReflection)
	}
}

// TestEmptySourceWalkIsNotAVerdict — обход, не прочитавший ничего, и обход,
// не нашедший типа настроек, — «не выполнилось», а не «находок ноль».
func TestEmptySourceWalkIsNotAVerdict(t *testing.T) {
	_, pkgName, typeName := engineSource(t)

	empty, err := walkEngineSource(t.TempDir(), pkgName, typeName)
	if err != nil {
		t.Fatalf("обход пустого каталога: %v", err)
	}
	if empty.emptyWalk() == "" {
		t.Errorf("обход пустого каталога назван вердиктом: %s", empty.summary())
	}

	noType := t.TempDir()
	writeFile(t, noType, "other.go", "package "+pkgName+"\n\ntype Other struct{ F map[string]string }\n\n"+
		"func (o *Other) Set() { o.F[\"k\"] = \"v\" }\n")
	c, err := walkEngineSource(noType, pkgName, typeName)
	if err != nil {
		t.Fatalf("обход каталога без типа настроек: %v", err)
	}
	if c.emptyWalk() == "" {
		t.Errorf("обход без единого метода типа настроек назван вердиктом: %s", c.summary())
	}
	if len(c.writes) != 0 {
		t.Errorf("обход засчитал запись в метод чужого типа: %v", c.writes)
	}
}

// ── Инъекции в копии поддерева ─────────────────────────────────────────────

// marker отмечает в инъекции строку, которую находка обязана назвать.
const marker = "// ← предмет"

// injectedMapField — поле-карта, которое копия добавляет в тип настроек: у
// настоящего типа ссылочных полей вида карты нет, а инъекция записи в
// содержимое карты должна быть записью в НАСТОЯЩЕЕ поле-карту.
const injectedMapField = "\n\t// InjectedMap — поле-карта копии для инъекций.\n\tInjectedMap map[string]string\n"

// engineCopy копирует не-тестовые .go пакета движка в свой каталог и
// добавляет в тип настроек поле-карту.
func engineCopy(t *testing.T) (dir, pkgName, typeName string) {
	t.Helper()
	src, pkgName, typeName := engineSource(t)
	dir = t.TempDir()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("ФИКСТУРА: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("ФИКСТУРА: %v", err)
		}
		writeFile(t, dir, name, string(b))
	}
	replaceOnce(t, dir, "config_default.go", "\tIsPushedAuthorizeEnforced bool\n}",
		"\tIsPushedAuthorizeEnforced bool\n"+injectedMapField+"}")
	return dir, pkgName, typeName
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("ФИКСТУРА: %v", err)
	}
}

// replaceOnce — правка копии по якорю, который обязан встречаться ровно раз:
// иначе инъекция попала бы не туда или никуда.
func replaceOnce(t *testing.T, dir, name, anchor, with string) {
	t.Helper()
	path := filepath.Join(dir, name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ФИКСТУРА: %v", err)
	}
	if n := strings.Count(string(b), anchor); n != 1 {
		t.Fatalf("ФИКСТУРА: якорь инъекции встречается в %s %d раз, а не один: %q", name, n, anchor)
	}
	writeFile(t, dir, name, strings.Replace(string(b), anchor, with, 1))
}

// markedLine — координата строки с маркером.
func markedLine(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("ФИКСТУРА: %v", err)
	}
	var at []string
	for i, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, marker) {
			at = append(at, fmt.Sprintf("%s:%d", name, i+1))
		}
	}
	if len(at) != 1 {
		t.Fatalf("ФИКСТУРА: строк с маркером в %s — %v, а не одна", name, at)
	}
	return at[0]
}

// injection — один факт, вносимый в копию: либо новый файл с методом
// настроек, либо правка существующего метода по якорю.
type injection struct {
	name string
	// file — файл копии: для нового — его имя, для правки — правимый файл.
	file string
	// anchor — якорь правки; пусто — file создаётся с содержимым src.
	anchor string
	src    string
}

func (in injection) apply(t *testing.T) (census sourceCensus, at string) {
	t.Helper()
	dir, pkgName, typeName := engineCopy(t)
	if in.anchor == "" {
		writeFile(t, dir, in.file, "package "+pkgName+"\n\n"+in.src)
	} else {
		replaceOnce(t, dir, in.file, in.anchor, in.src)
	}
	at = markedLine(t, dir, in.file)
	c, err := walkEngineSource(dir, pkgName, typeName)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: обход копии: %v", err)
	}
	if reason := c.emptyWalk(); reason != "" {
		t.Fatal(reason)
	}
	// Положительный контроль разбора на копии: записи глубины 1 живого дерева
	// видны и в ней. Без него молчание близнеца было бы неотличимо от слепоты.
	if got := c.sharedFieldWrites(); len(got) == 0 {
		t.Errorf("положительный контроль: разбор не видит в копии ни одной записи глубины 1 живого дерева "+
			"— молчание ниже было бы слепотой: %s", c.summary())
	}
	t.Log(c.summary())
	return c, at
}

func newMethod(imports, body string) string {
	head := ""
	if imports != "" {
		head = "import (\n" + imports + "\n)\n\n"
	}
	return head + body
}

// TestContentWriteInjectionIsFoundWithItsCoordinate — запись в содержимое поля
// в каждой узнаваемой форме краснеет и называет свою строку; больше в копии
// не краснеет ничего, то есть красное приходит от внесённого факта.
func TestContentWriteInjectionIsFoundWithItsCoordinate(t *testing.T) {
	cases := []struct {
		injection
		form  string
		depth int
	}{
		{injection{name: "элемент карты, названной полем", file: "injected.go", src: newMethod(`"context"`, `
func (c *Config) GetInjected(_ context.Context) map[string]string {
	if c.InjectedMap != nil {
		c.InjectedMap["seen"] = "yes" `+marker+`
	}
	return c.InjectedMap
}
`)}, formAssign, 2},
		{injection{name: "поле значения под указателем", file: "injected.go", src: newMethod(`"context"`, `
func (c *Config) TuneHTTPClient(_ context.Context) {
	c.HTTPClient.RetryMax = 7 `+marker+`
}
`)}, formAssign, 2},
		{injection{name: "указатель в интерфейсе, названном в New, — в существующем геттере",
			file: "config_default.go", anchor: "\t\tc.ClientSecretsHasher = &BCrypt{Config: c}\n\t}\n",
			src: "\t\tc.ClientSecretsHasher = &BCrypt{Config: c}\n\t}\n" +
				"\tc.ClientSecretsHasher.(*BCrypt).Config = &Config{HashCost: 4} " + marker + "\n"}, formAssign, 2},
		{injection{name: "элемент среза, названного в New, — в существующем геттере",
			file: "config_default.go", anchor: "\treturn c.SanitationWhiteList\n",
			src: "\tc.SanitationWhiteList[0] = \"injected\" " + marker + "\n\treturn c.SanitationWhiteList\n"}, formAssign, 2},
		{injection{name: "разыменование поля-указателя", file: "injected.go", src: newMethod("", `
func (c *Config) ResetTemplate() {
	*c.FormPostHTMLTemplate = *c.FormPostHTMLTemplate `+marker+`
}
`)}, formAssign, 2},
		{injection{name: "составное присваивание", file: "injected.go", src: newMethod("", `
func (c *Config) Mark() {
	c.InjectedMap["k"] += "x" `+marker+`
}
`)}, formOpAssign, 2},
		{injection{name: "инкремент элемента", file: "injected.go", src: newMethod("", `
func (c *Config) Bump() {
	c.GlobalSecret[0]++ `+marker+`
}
`)}, formIncDec, 2},
		{injection{name: "присваивание в range", file: "injected.go", src: newMethod("", `
func (c *Config) Fill(xs []string) {
	for _, c.InjectedMap["last"] = range xs { `+marker+`
	}
}
`)}, formRange, 2},
		{injection{name: "append в поле", file: "injected.go", src: newMethod("", `
func (c *Config) Scopes() []string {
	return append(c.RefreshTokenScopes, "extra") `+marker+`
}
`)}, "встроенная append", 2},
		{injection{name: "delete из поля-карты", file: "injected.go", src: newMethod("", `
func (c *Config) Forget() {
	delete(c.InjectedMap, "k") `+marker+`
}
`)}, "встроенная delete", 2},
		{injection{name: "clear поля", file: "injected.go", src: newMethod("", `
func (c *Config) Wipe() {
	clear(c.GlobalSecret) `+marker+`
}
`)}, "встроенная clear", 2},
		{injection{name: "copy в поле", file: "injected.go", src: newMethod("", `
func (c *Config) Overwrite(b []byte) {
	copy(c.GlobalSecret, b) `+marker+`
}
`)}, "встроенная copy", 2},
		{injection{name: "мутатор slices", file: "injected.go", src: newMethod(`"slices"`, `
func (c *Config) SortPrompts() {
	slices.Sort(c.AllowedPromptValues) `+marker+`
}
`)}, "мутатор slices.Sort", 2},
		{injection{name: "мутатор под другим именем импорта", file: "injected.go", src: newMethod(`srt "sort"`, `
func (c *Config) SortScopes() {
	srt.Strings(c.RefreshTokenScopes) `+marker+`
}
`)}, "мутатор sort.Strings", 2},
		{injection{name: "метод с приёмником-указателем на поле", file: "injected.go", src: newMethod("", `
func (c *Config) AddHandler(h AuthorizeEndpointHandler) {
	c.AuthorizeEndpointHandlers.Append(h) `+marker+`
}
`)}, "метод Append с приёмником-указателем", 2},
		{injection{name: "псевдоним поля-карты", file: "injected.go", src: newMethod("", `
func (c *Config) ViaAlias() {
	h := c.InjectedMap
	h["k"] = "v" `+marker+`
}
`)}, formAssign, 2},
		{injection{name: "псевдоним объявлением var", file: "injected.go", src: newMethod("", `
func (c *Config) ViaVar() {
	var s = c.SanitationWhiteList
	s[0] = "v" `+marker+`
}
`)}, formAssign, 2},
		{injection{name: "псевдоним через утверждение типа", file: "injected.go", src: newMethod("", `
func (c *Config) Rehash() {
	if b, ok := c.ClientSecretsHasher.(*BCrypt); ok {
		b.Config = c `+marker+`
	}
}
`)}, formAssign, 2},
		{injection{name: "псевдоним переключателем типа", file: "injected.go", src: newMethod("", `
func (c *Config) Retype() {
	switch b := c.ClientSecretsHasher.(type) {
	case *BCrypt:
		b.Config = c `+marker+`
	}
}
`)}, formAssign, 2},
		{injection{name: "псевдоним элемента range", file: "injected.go", src: newMethod("", `
func (c *Config) ZeroRotated() {
	for _, key := range c.RotatedGlobalSecrets {
		key[0] = 0 `+marker+`
	}
}
`)}, formAssign, 3},
		{injection{name: "псевдоним через преобразование", file: "injected.go", src: newMethod("", `
func (c *Config) ViaConversion() {
	h := AuthorizeEndpointHandlers(c.AuthorizeEndpointHandlers)
	h[0] = nil `+marker+`
}
`)}, formAssign, 2},
		{injection{name: "результат метода настроек", file: "injected.go", src: newMethod(`"context"`, `
func (c *Config) Poison(ctx context.Context) {
	c.GetSanitationWhiteList(ctx)[0] = "x" `+marker+`
}
`)}, formAssign, 2},
		{injection{name: "функциональный литерал", file: "injected.go", src: newMethod("", `
func (c *Config) Later() func() {
	return func() {
		c.InjectedMap["k"] = "v" `+marker+`
	}
}
`)}, formAssign, 2},
		{injection{name: "приёмник-значение", file: "injected.go", src: newMethod("", `
func (c Config) ByValue() {
	c.InjectedMap["k"] = "v" `+marker+`
}
`)}, formAssign, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, at := tc.apply(t)
			found := c.deeper()
			if len(found) != 1 || len(c.unjudged) != 0 {
				t.Fatalf("ожидалась ровно одна находка на %s и ноль неразобранных; находок %d: %v · неразобранных %d: %v",
					at, len(found), found, len(c.unjudged), c.unjudged)
			}
			w := found[0]
			if w.at != at || w.form != tc.form || w.depth != tc.depth {
				t.Fatalf("находка %v, а ожидалась на %s формы %q глубины %d", w, at, tc.form, tc.depth)
			}
			text := contentFindingText(w)
			for _, part := range []string{at, w.method, "СОДЕРЖИМОЕ", "назвать поле в New её НЕ снимает"} {
				if !strings.Contains(text, part) {
					t.Fatalf("текст находки не называет %q: %s", part, text)
				}
			}
			t.Log(text)
		})
	}
}

// TestUnjudgedFormInjectionIsRedNotSilent — форма, о которой разбор без типов
// не может сказать, пишет ли она, краснеет и называет строку, а не молчит.
func TestUnjudgedFormInjectionIsRedNotSilent(t *testing.T) {
	cases := []struct {
		injection
		what string
	}{
		{injection{name: "метод чужого типа на поле-указателе", file: "injected.go", src: newMethod("", `
func (c *Config) Standard() {
	_ = c.HTTPClient.StandardClient() `+marker+`
}
`)}, "вызов StandardClient на содержимом поля HTTPClient"},
		{injection{name: "функция пакета получает поле-карту", file: "injected.go", src: newMethod("", `
func (c *Config) Hand() {
	mutateInjected(c.InjectedMap) `+marker+`
}

func mutateInjected(m map[string]string) { m["k"] = "v" }
`)}, "функция mutateInjected получает содержимое настроек"},
		{injection{name: "функция чужого пакета получает сам приёмник", file: "injected.go", src: newMethod(`"encoding/json"`, `
func (c *Config) Load(b []byte) error {
	return json.Unmarshal(b, c) `+marker+`
}
`)}, "функция encoding/json.Unmarshal получает содержимое настроек"},
		{injection{name: "взятие адреса поля", file: "injected.go", src: newMethod("", `
func (c *Config) Addr() *[]string {
	return &c.RefreshTokenScopes `+marker+`
}
`)}, "адрес содержимого настроек"},
		{injection{name: "метод настроек получает содержимое аргументом", file: "injected.go", src: newMethod("", `
func (c *Config) Outer() {
	c.inner(c.InjectedMap) `+marker+`
}

func (c *Config) inner(m map[string]string) { m["k"] = "v" }
`)}, "метод настроек inner получает содержимое настроек"},
		{injection{name: "значение-метод, взятое у содержимого", file: "injected.go", src: newMethod("", `
func (c *Config) Deferred() {
	f := c.HTTPClient.StandardClient
	_ = f() `+marker+`
}
`)}, "вызов значения, достигнутого через содержимое поля HTTPClient"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, at := tc.apply(t)
			if len(c.deeper()) != 0 || len(c.unjudged) != 1 {
				t.Fatalf("ожидалась ровно одна неразобранная форма на %s и ноль находок; находок %v · неразобранных %v",
					at, c.deeper(), c.unjudged)
			}
			u := c.unjudged[0]
			if u.at != at || !strings.Contains(u.what, tc.what) {
				t.Fatalf("неразобранная форма %+v, а ожидалась на %s: %q", u, at, tc.what)
			}
			text := unjudgedText(u)
			if !strings.Contains(text, at) || !strings.Contains(text, "не молчит") {
				t.Fatalf("текст неразобранной формы не называет координату или исход: %s", text)
			}
			t.Log(text)
		})
	}
}

// TestLawfulTwinOfAContentWriteIsSilent — законные близнецы той же формы:
// запись глубины 1 в то же поле (предмет переписи, а не этой пробы) и чтения
// содержимого. Находок и неразобранных нет; запись глубины 1 при этом ВИДНА —
// молчание не от слепоты.
func TestLawfulTwinOfAContentWriteIsSilent(t *testing.T) {
	cases := []struct {
		injection
		// depthOne — близнец пишет глубину 1, и она обязана быть сосчитана.
		depthOne bool
		// copyOnly — запись глубины 1 пишет копию приёмника-значения и в
		// сверку с переписью не входит.
		copyOnly bool
	}{
		{injection{name: "поле-карта заменено целиком", file: "injected.go", src: newMethod("", `
func (c *Config) Replace() {
	c.InjectedMap = map[string]string{"seen": "yes"} `+marker+`
}
`)}, true, false},
		{injection{name: "поле-указатель заменено целиком", file: "injected.go", src: newMethod(`retryablehttp "github.com/hashicorp/go-retryablehttp"`, `
func (c *Config) NewHTTPClient() {
	c.HTTPClient = &retryablehttp.Client{RetryMax: 7} `+marker+`
}
`)}, true, false},
		{injection{name: "поле, названное в New, заполнено лениво", file: "injected.go", src: newMethod("", `
func (c *Config) LazyScope() ScopeStrategy {
	if c.ScopeStrategy == nil {
		c.ScopeStrategy = ExactScopeStrategy `+marker+`
	}
	return c.ScopeStrategy
}
`)}, true, false},
		{injection{name: "приёмник-значение пишет свою копию", file: "injected.go", src: newMethod("", `
func (c Config) Local() {
	c.HashCost = 1 `+marker+`
}
`)}, true, true},
		{injection{name: "чтения содержимого", file: "injected.go", src: newMethod(`"strconv"`, `
func (c *Config) Reads(dst []byte) (int, string) {
	v := c.InjectedMap["k"]
	v = "rebound"
	h := c.InjectedMap
	h = nil
	_ = h
	_ = append([]string{}, c.RefreshTokenScopes...)
	copy(dst, c.GlobalSecret)
	_ = c.ScopeStrategy(nil, "x")
	_ = []string{c.TokenURL}
	_ = strconv.Itoa(c.HashCost)
	_ = string(c.GlobalSecret)
	return len(c.SanitationWhiteList), v `+marker+`
}
`)}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, at := tc.apply(t)
			if len(c.deeper()) != 0 || len(c.unjudged) != 0 {
				t.Fatalf("близнец назван: находок %v · неразобранных %v", c.deeper(), c.unjudged)
			}
			if !tc.depthOne {
				return
			}
			i := slices.IndexFunc(c.writes, func(w contentWrite) bool { return w.at == at && w.depth == 1 })
			if i < 0 {
				t.Fatalf("запись глубины 1 на %s не сосчитана — молчание от слепоты, а не от законности: %v", at, c.writes)
			}
			w := c.writes[i]
			shared := slices.Contains(c.sharedFieldWrites(), getterWrite{method: w.method, field: w.field})
			if shared == tc.copyOnly {
				t.Fatalf("запись %v: в сверке с переписью — %v, а копия ли она приёмника-значения — %v",
					w, shared, tc.copyOnly)
			}
		})
	}
}
