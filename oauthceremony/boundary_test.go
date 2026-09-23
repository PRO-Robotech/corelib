// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// boundary_test.go — ПРЕДИКАТ ГЛАВНОГО ПРАВИЛА ПАКЕТА: ни один
// экспортированный элемент не называет чужого типа.
//
// Правило проверяется ДВАЖДЫ, двумя независимыми приборами, и оба обязаны
// сойтись:
//
//  1. По ИСХОДНИКУ (TestNoForeignTypeInExportedSurface). Дерево разбора
//     обходится целиком: подписи функций и методов, поля структур, методы
//     интерфейсов, объявления переменных и констант, и — по цепочке —
//     неэкспортированные типы, на которые они ссылаются. Прибор видит ВСЁ,
//     что объявлено, включая то, что не создать значением.
//
//  2. ПО ЗНАЧЕНИЮ (TestExportedTypesCarryNoForeignTypeAtRuntime). Отражение
//     обходит фактические типы: поля, подписи методов, элементы срезов и
//     карт. Прибор видит то, что исходник мог выразить неявно, — например
//     тип переменной, выведенный из вызова.
//
// Приборы связаны пробой полноты (TestRuntimeRosterCoversEveryExportedType):
// перечень типов второго прибора обязан совпадать с перечнем, найденным
// первым. Без неё второй прибор незаметно деградировал бы до проверки того
// подмножества, которое кто-то не забыл дописать.
package oauthceremony_test

import (
	"io/fs"

	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// ownPackagePath — единственный не-стандартный путь, дозволенный в
// экспортированной поверхности: наш собственный.
const ownPackagePath = "github.com/PRO-Robotech/corelib/oauthceremony"

// allowedForeignImports — ЧУЖИЕ пакеты, дозволенные в типах экспортированных
// элементов.
//
// Словарь ЗАКРЫТ и мал намеренно: это не «стандартная библиотека вообще», а
// поимённый перечень. `net/http` в нём НЕТ, хотя пакет его импортирует:
// церемония не владеет соединением, и тип соединения не должен доезжать до
// подписи. Расширение словаря — правка этой строки, то есть предмет обзора, а
// не молчаливое следствие правки кода.
var allowedForeignImports = map[string]bool{
	"context": true,
	"time":    true,
}

// TestNoForeignTypeInExportedSurface — предикат по исходнику.
func TestNoForeignTypeInExportedSurface(t *testing.T) {
	surface := parseSurface(t)

	if len(surface.problems) != 0 {
		sort.Strings(surface.problems)
		t.Fatalf("экспортированная поверхность называет чужие типы (%d шт):\n  %s",
			len(surface.problems), strings.Join(surface.problems, "\n  "))
	}

	// Проба обязана что-то проверять. Прибор, ничего не нашедший потому,
	// что ничего не читал, зелёный так же, как и прибор, читавший всё.
	if surface.checked == 0 {
		t.Fatalf("прибор не проверил ни одного объявления — читать было нечего")
	}
	t.Logf("проверено экспортированных объявлений: %d шт; из них типов: %d шт",
		surface.checked, len(surface.exportedTypes))
}

// TestExportedTypesCarryNoForeignTypeAtRuntime — предикат по значению.
func TestExportedTypesCarryNoForeignTypeAtRuntime(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var problems []string

	for name, typ := range runtimeRoster() {
		walkType(typ, name, seen, &problems)
	}

	if len(problems) != 0 {
		sort.Strings(problems)
		t.Fatalf("отражение нашло чужие типы в экспортированной поверхности (%d шт):\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
	if len(seen) == 0 {
		t.Fatalf("прибор не обошёл ни одного типа")
	}
	t.Logf("обойдено типов отражением: %d шт", len(seen))
}

// TestRuntimeRosterCoversEveryExportedType связывает два прибора.
func TestRuntimeRosterCoversEveryExportedType(t *testing.T) {
	surface := parseSurface(t)

	roster := map[string]bool{}
	for name := range runtimeRoster() {
		roster[name] = true
	}

	var missing []string
	for _, name := range surface.exportedTypes {
		if !roster[name] {
			missing = append(missing, name)
		}
	}
	var extra []string
	for name := range roster {
		if !contains(surface.exportedTypes, name) {
			extra = append(extra, name)
		}
	}

	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) != 0 {
		t.Errorf("перечень прибора по значению не покрывает объявленные типы (%d шт): %s",
			len(missing), strings.Join(missing, ", "))
	}
	if len(extra) != 0 {
		t.Errorf("перечень прибора по значению называет типы, которых в пакете нет (%d шт): %s",
			len(extra), strings.Join(extra, ", "))
	}
}

// runtimeRoster — экспортированные типы пакета, названные ЗНАЧЕНИЯМИ.
//
// Перечень ведётся руками намеренно: отражение не умеет перечислять типы
// пакета, а прибор, перечисляющий сам себя, проверял бы только то, что сам и
// знает. Полноту держит TestRuntimeRosterCoversEveryExportedType.
func runtimeRoster() map[string]reflect.Type {
	return map[string]reflect.Type{
		"Ceremony":             reflect.TypeOf(oauthceremony.Ceremony{}),
		"Config":               reflect.TypeOf(oauthceremony.Config{}),
		"Ports":                reflect.TypeOf(oauthceremony.Ports{}),
		"ProtocolError":        reflect.TypeOf(oauthceremony.ProtocolError{}),
		"FailureCode":          reflect.TypeOf(oauthceremony.FailureCode(0)),
		"StoreOutcome":         reflect.TypeOf(oauthceremony.StoreOutcome{}),
		"GrantRecord":          reflect.TypeOf(oauthceremony.GrantRecord{}),
		"ProofKeyMethod":       reflect.TypeOf(oauthceremony.ProofKeyMethod("")),
		"ProofKeyBinding":      reflect.TypeOf(oauthceremony.ProofKeyBinding{}),
		"SessionRecord":        reflect.TypeOf(oauthceremony.SessionRecord{}),
		"ClientRegistration":   reflect.TypeOf(oauthceremony.ClientRegistration{}),
		"AuthorizationRequest": reflect.TypeOf(oauthceremony.AuthorizationRequest{}),
		"AuthorizationIntent":  reflect.TypeOf(oauthceremony.AuthorizationIntent{}),
		"AuthorizationGrant":   reflect.TypeOf(oauthceremony.AuthorizationGrant{}),
		"AuthorizationResult":  reflect.TypeOf(oauthceremony.AuthorizationResult{}),
		"TokenRequest":         reflect.TypeOf(oauthceremony.TokenRequest{}),
		"TokenResult":          reflect.TypeOf(oauthceremony.TokenResult{}),
		"IntrospectionRequest": reflect.TypeOf(oauthceremony.IntrospectionRequest{}),
		"IntrospectionResult":  reflect.TypeOf(oauthceremony.IntrospectionResult{}),
		"RevocationRequest":    reflect.TypeOf(oauthceremony.RevocationRequest{}),
		"RevocationReason":     reflect.TypeOf(oauthceremony.RevocationReason("")),
		"GrantKind":            reflect.TypeOf(oauthceremony.GrantKind("")),
		"ResponseKind":         reflect.TypeOf(oauthceremony.ResponseKind("")),
		"ResponseDelivery":     reflect.TypeOf(oauthceremony.ResponseDelivery("")),
		"TokenKind":            reflect.TypeOf(oauthceremony.TokenKind("")),
		"ClientAuthMethod":     reflect.TypeOf(oauthceremony.ClientAuthMethod("")),
		"ScopeMatching":        reflect.TypeOf(oauthceremony.ScopeMatching(0)),
		"RefreshTokenIssuance": reflect.TypeOf(oauthceremony.RefreshTokenIssuance(0)),
		"ClientDirectory":      reflect.TypeOf((*oauthceremony.ClientDirectory)(nil)).Elem(),
		"AuthorizationCodeRecord": reflect.TypeOf(
			oauthceremony.AuthorizationCodeRecord{}),
		"AuthorizationCodeVault": reflect.TypeOf(
			(*oauthceremony.AuthorizationCodeVault)(nil)).Elem(),
		"AccessTokenVault":  reflect.TypeOf((*oauthceremony.AccessTokenVault)(nil)).Elem(),
		"RefreshTokenVault": reflect.TypeOf((*oauthceremony.RefreshTokenVault)(nil)).Elem(),
		"GrantRevoker":      reflect.TypeOf((*oauthceremony.GrantRevoker)(nil)).Elem(),
		"UnitOfWork":        reflect.TypeOf((*oauthceremony.UnitOfWork)(nil)).Elem(),
	}
}

// walkType обходит тип отражением, докладывая о чужих.
//
// Неэкспортированные поля НЕ обходятся: потребитель не может их назвать, и
// тип внутри них его поверхности не касается. Именно на этом держится
// AuthorizationIntent — он несёт разобранный запрос движка в закрытом поле.
func walkType(t reflect.Type, path string, seen map[reflect.Type]bool, problems *[]string) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true

	if pkg := t.PkgPath(); pkg != "" && pkg != ownPackagePath && !allowedForeignImports[pkg] {
		*problems = append(*problems, path+": тип из чужого пакета "+strconv.Quote(pkg)+" ("+t.String()+")")
		return
	}

	// Методы с указателем на тип видны только через указатель.
	if t.Kind() != reflect.Interface && t.Kind() != reflect.Pointer {
		walkMethods(reflect.PointerTo(t), path, seen, problems)
	}
	walkMethods(t, path, seen, problems)

	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		walkType(t.Elem(), path+".elem", seen, problems)
	case reflect.Map:
		walkType(t.Key(), path+".key", seen, problems)
		walkType(t.Elem(), path+".value", seen, problems)
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			walkType(f.Type, path+"."+f.Name, seen, problems)
		}
	case reflect.Func:
		for i := range t.NumIn() {
			walkType(t.In(i), path+".in", seen, problems)
		}
		for i := range t.NumOut() {
			walkType(t.Out(i), path+".out", seen, problems)
		}
	}
}

func walkMethods(t reflect.Type, path string, seen map[reflect.Type]bool, problems *[]string) {
	for i := range t.NumMethod() {
		m := t.Method(i)
		walkType(m.Type, path+"."+m.Name+"()", seen, problems)
	}
}

// ── Прибор по исходнику ─────────────────────────────────────────────────────

type surfaceReport struct {
	problems      []string
	exportedTypes []string
	checked       int
}

func parseSurface(t *testing.T) surfaceReport {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("разбор исходников пакета не состоялся: %v", err)
	}

	var files []*ast.File
	var fileImports []map[string]string
	for name, pkg := range pkgs {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		t.Fatalf("в каталоге пакета не найдено ни одного не-пробного файла")
	}

	report := surfaceReport{}

	// Объявленные в пакете типы и функции: нужны, чтобы пройти по цепочке
	// в неэкспортированный тип и чтобы вывести тип переменной из вызова.
	localTypes := map[string]ast.Expr{}
	funcResults := map[string]ast.Expr{}
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if ok {
						localTypes[ts.Name.Name] = ts.Type
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil && d.Type.Results != nil && len(d.Type.Results.List) > 0 {
					funcResults[d.Name.Name] = d.Type.Results.List[0].Type
				}
			}
		}
	}

	for _, f := range files {
		imports := map[string]string{}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				report.problems = append(report.problems, "неразбираемый путь импорта: "+imp.Path.Value)
				continue
			}
			local := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				if imp.Name.Name == "." {
					report.problems = append(report.problems,
						"точечный импорт "+strconv.Quote(path)+" делает поверхность непроверяемой")
					continue
				}
				local = imp.Name.Name
			}
			imports[local] = path
		}
		fileImports = append(fileImports, imports)

		checker := &surfaceChecker{
			imports:    imports,
			localTypes: localTypes,
			visited:    map[string]bool{},
		}

		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !exportedDecl(d) {
					continue
				}
				report.checked++
				checker.check(d.Type, declName(d))
			case *ast.GenDecl:
				switch d.Tok {
				case token.TYPE:
					for _, spec := range d.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok || !ts.Name.IsExported() {
							continue
						}
						report.checked++
						report.exportedTypes = append(report.exportedTypes, ts.Name.Name)
						checker.checkTypeDecl(ts.Type, ts.Name.Name)
					}
				case token.VAR, token.CONST:
					for _, spec := range d.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for i, name := range vs.Names {
							if !name.IsExported() {
								continue
							}
							report.checked++
							switch {
							case vs.Type != nil:
								checker.check(vs.Type, name.Name)
							case i < len(vs.Values):
								resolved := resolveValueType(vs.Values[i], funcResults)
								if resolved == nil {
									report.problems = append(report.problems,
										name.Name+": тип не выражен и не выводится из значения; назовите его явно")
									continue
								}
								checker.check(resolved, name.Name)
							default:
								// const в блоке iota: тип берётся
								// из первой строки блока, она уже
								// проверена выше.
							}
						}
					}
				}
			}
		}
		report.problems = append(report.problems, checker.problems...)
	}

	sort.Strings(report.exportedTypes)
	return report
}

type surfaceChecker struct {
	imports    map[string]string
	localTypes map[string]ast.Expr
	visited    map[string]bool
	problems   []string
}

// checkTypeDecl проверяет объявление типа, обходя только ту его часть,
// которая видна потребителю: экспортированные поля структуры и методы
// интерфейса.
func (c *surfaceChecker) checkTypeDecl(expr ast.Expr, path string) {
	switch t := expr.(type) {
	case *ast.StructType:
		for _, field := range t.Fields.List {
			if len(field.Names) == 0 {
				c.check(field.Type, path+".<встроенное>")
				continue
			}
			for _, name := range field.Names {
				if name.IsExported() {
					c.check(field.Type, path+"."+name.Name)
				}
			}
		}
	case *ast.InterfaceType:
		for _, field := range t.Methods.List {
			if len(field.Names) == 0 {
				c.check(field.Type, path+".<встроенное>")
				continue
			}
			for _, name := range field.Names {
				if name.IsExported() {
					c.check(field.Type, path+"."+name.Name)
				}
			}
		}
	default:
		c.check(expr, path)
	}
}

// check обходит выражение типа, докладывая о чужих пакетах и спускаясь в
// неэкспортированные типы пакета.
func (c *surfaceChecker) check(expr ast.Expr, path string) {
	ast.Inspect(expr, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			ident, ok := node.X.(*ast.Ident)
			if !ok {
				return true
			}
			importPath, isImport := c.imports[ident.Name]
			if !isImport {
				return true
			}
			if !allowedForeignImports[importPath] {
				c.problems = append(c.problems,
					path+": называет тип из "+strconv.Quote(importPath)+" ("+ident.Name+"."+node.Sel.Name+")")
			}
			return false
		case *ast.Ident:
			underlying, isLocal := c.localTypes[node.Name]
			if !isLocal || node.Name == "" {
				return true
			}
			if c.visited[node.Name] {
				return true
			}
			c.visited[node.Name] = true
			c.checkTypeDecl(underlying, path+"→"+node.Name)
			return true
		}
		return true
	})
}

func exportedDecl(d *ast.FuncDecl) bool {
	if d.Recv == nil {
		return d.Name.IsExported()
	}
	if !d.Name.IsExported() {
		return false
	}
	return receiverIsExported(d.Recv)
}

func receiverIsExported(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	expr := recv.List[0].Type
	if star, isPointer := expr.(*ast.StarExpr); isPointer {
		expr = star.X
	}
	if index, isGeneric := expr.(*ast.IndexExpr); isGeneric {
		expr = index.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.IsExported()
}

func declName(d *ast.FuncDecl) string {
	if d.Recv == nil {
		return d.Name.Name + "()"
	}
	return "(receiver)." + d.Name.Name + "()"
}

// resolveValueType выводит тип переменной из её значения — ровно в тех двух
// видах, которые пакет применяет. Всё прочее объявляется явно, и прибор этого
// требует.
func resolveValueType(value ast.Expr, funcResults map[string]ast.Expr) ast.Expr {
	switch v := value.(type) {
	case *ast.CompositeLit:
		return v.Type
	case *ast.UnaryExpr:
		if lit, ok := v.X.(*ast.CompositeLit); ok {
			return lit.Type
		}
	case *ast.CallExpr:
		if ident, ok := v.Fun.(*ast.Ident); ok {
			if result, known := funcResults[ident.Name]; known {
				return result
			}
		}
	case *ast.BasicLit:
		return ast.NewIdent("string")
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
