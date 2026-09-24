// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// issued_surface_test.go — поверхность выпуска объявляет ровно то, что
// церемония выпускает.
//
// Элемент, у которого нет исполнителя, — вид артефакта, который ни один путь
// не выпускает, поле ответа, которое ни один путь не заполняет, граница
// срока под видом без выпуска, — обещает поведение, которого нет: служба
// строит на нём свою схему и свою выдачу, и ни одна проба не краснеет. Так
// жили вид `id_token`, поле токена личности в ответе обмена и его ветка
// перевода: обработчика OpenID Connect церемония не провязывает (doc.go).
//
// Перечни здесь выводятся из дерева, а не выписаны: виды — разбором
// исходников пакета, поля — отражением. Вид или поле, добавленные без
// исполнителя, судятся без правки проб.
package oauthceremony_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// tokenKindConstants — константы типа TokenKind, объявленные в файлах files:
// имя → значение. Законных форм записи две — `X TokenKind = "…"` и
// `X = TokenKind("…")`; спецификация, которая называет TokenKind иначе
// (значение не строковый литерал), отдаётся в odd, чтобы распознаватель не
// молчал о форме, которой не знает.
func tokenKindConstants(files []*ast.File) (kinds map[string]string, odd []string) {
	kinds = map[string]string{}
	isTokenKind := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "TokenKind"
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					value := vs.Values[i]
					switch {
					case vs.Type != nil && isTokenKind(vs.Type):
					case vs.Type == nil:
						call, isCall := value.(*ast.CallExpr)
						if !isCall || !isTokenKind(call.Fun) || len(call.Args) != 1 {
							continue
						}
						value = call.Args[0]
					default:
						continue
					}
					lit, isLit := value.(*ast.BasicLit)
					if !isLit || lit.Kind != token.STRING {
						odd = append(odd, name.Name)
						continue
					}
					unquoted, err := strconv.Unquote(lit.Value)
					if err != nil {
						odd = append(odd, name.Name)
						continue
					}
					kinds[name.Name] = unquoted
				}
			}
		}
	}
	return kinds, odd
}

// packageSources — не-пробные файлы пакета.
func packageSources(t *testing.T) []*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: разбор исходников пакета: %v", err)
	}
	var files []*ast.File
	for name, pkg := range pkgs {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: в каталоге пакета нет ни одного не-пробного файла")
	}
	return files
}

// TestTokenKindRecognizerKnowsBothLawfulForms — распознаватель видов находит
// обе законные формы записи и молчит о константе другого типа; значение не
// литералом он называет, а не пропускает.
func TestTokenKindRecognizerKnowsBothLawfulForms(t *testing.T) {
	const src = `package p
const (
	Typed TokenKind = "typed"
	Converted = TokenKind("converted")
	OtherType GrantKind = "other"
	Untyped = "untyped"
)
const Computed TokenKind = prefix + "x"
`
	f, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: синтетический исходник не разобран: %v", err)
	}
	kinds, odd := tokenKindConstants([]*ast.File{f})
	want := map[string]string{"Typed": "typed", "Converted": "converted"}
	if !maps.Equal(kinds, want) {
		t.Errorf("найдены виды %v, ожидались %v", kinds, want)
	}
	if !slices.Equal(odd, []string{"Computed"}) {
		t.Errorf("неразобранные формы %v, ожидалась [Computed]", odd)
	}
}

// issuedKindsOf — виды, сроки которых лежат в записях хранилища: вид
// попадает туда только выпуском своего артефакта.
func issuedKindsOf(store *memoryPorts) map[oauthceremony.TokenKind]bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	issued := map[oauthceremony.TokenKind]bool{}
	note := func(rec oauthceremony.GrantRecord) {
		for kind := range rec.Session.ExpiresAt {
			issued[kind] = true
		}
	}
	for _, row := range store.codes {
		note(row.grant)
	}
	for _, rec := range store.access {
		note(rec)
	}
	for _, row := range store.refresh {
		note(row.grant)
	}
	return issued
}

// TestEveryDeclaredTokenKindIsIssued — каждый вид, объявленный константой
// пакета, есть в словаре TokenKinds, и каждый вид словаря церемония выпускает:
// его срок лежит в записи хранилища после выдачи кода, обмена и оборота.
// Вид без выпуска — обещание без исполнителя.
func TestEveryDeclaredTokenKindIsIssued(t *testing.T) {
	files := packageSources(t)
	constants, odd := tokenKindConstants(files)
	if len(odd) != 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: константы TokenKind в форме, которой распознаватель не знает: %v", odd)
	}

	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())
	first := exchangeCode(t, ceremony)
	if _, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken)); err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: оборот отказал: %v", err)
	}
	issued := issuedKindsOf(store)
	dictionary := oauthceremony.TokenKinds()

	var declared int
	for _, name := range slices.Sorted(maps.Keys(constants)) {
		kind := oauthceremony.TokenKind(constants[name])
		if kind == oauthceremony.TokenKindUnspecified {
			continue
		}
		declared++
		if !slices.Contains(dictionary, kind) {
			t.Errorf("константа %s объявляет вид %q, которого нет в словаре TokenKinds %v", name, kind, dictionary)
		}
		if !issued[kind] {
			t.Errorf("константа %s объявляет вид %q, который ни один путь церемонии не выпускает (выпущены %v)",
				name, kind, slices.Sorted(maps.Keys(issued)))
		}
	}
	for _, kind := range dictionary {
		if !issued[kind] {
			t.Errorf("словарь TokenKinds называет вид %q, который ни один путь церемонии не выпускает", kind)
		}
	}
	t.Logf("перепись: файлов %d · констант вида %d (кроме неназванного — %d) · в словаре %d · выпущено видов %d",
		len(files), len(constants), declared, len(dictionary), len(issued))
	if declared == 0 || len(issued) == 0 {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: объявлено видов %d, выпущено %d", declared, len(issued))
	}
}

// TestEveryTokenResultFieldIsFilledByAPath — каждое поле ответа обмена
// заполняет хотя бы один путь церемонии: обмен кода либо оборот токена
// обновления. Поле, которое не заполняет ни один, — обещание без исполнителя.
//
// Заполненность — ненулевое значение отражения; карта заполнена, когда она не
// nil. Additional судится так намеренно: это корзина для полей движка без
// имени, и то, что сегодня движок таких не кладёт, — не дефект поля.
func TestEveryTokenResultFieldIsFilledByAPath(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())
	first := exchangeCode(t, ceremony)
	rotated, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: оборот отказал: %v", err)
	}

	typ := reflect.TypeFor[oauthceremony.TokenResult]()
	var fields, filled int
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		fields++
		byPath := map[string]bool{
			"обмен кода": !reflect.ValueOf(first).Field(i).IsZero(),
			"оборот":     !reflect.ValueOf(rotated).Field(i).IsZero(),
		}
		if !byPath["обмен кода"] && !byPath["оборот"] {
			t.Errorf("поле TokenResult.%s не заполняет ни один путь церемонии: %v", field.Name, byPath)
			continue
		}
		filled++
	}
	t.Logf("перепись: полей TokenResult %d · заполнено путями %d", fields, filled)
	if fields == 0 {
		t.Fatal("НЕ ВЫПОЛНИЛОСЬ: у TokenResult не найдено ни одного экспортированного поля")
	}
}

// TestGrantBoundOfAKindThatIsNotIssuedIsRefusedByName — граница срока под
// видом, которого церемония не выпускает, отвергается при выдаче по имени вида
// (ErrCeremonyMisuse), и кода в хранилище нет: такая граница ничего бы не
// ограничила, а служба считала бы её действующей. Близнец — граница каждого
// вида словаря: выдача принята.
func TestGrantBoundOfAKindThatIsNotIssuedIsRefusedByName(t *testing.T) {
	bound := time.Now().UTC().Add(time.Minute)
	for _, kind := range oauthceremony.TokenKinds() {
		t.Run("близнец: "+string(kind), func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			grant := grantOfScopes("openid", "offline")
			grant.ExpiresAt = map[oauthceremony.TokenKind]time.Time{kind: bound}
			if _, err := completeWith(t, ceremony, authorizeRequest(), grant); err != nil {
				t.Fatalf("граница вида %q отвергнута: %v", kind, err)
			}
		})
	}
	for _, kind := range []oauthceremony.TokenKind{"id_token", "device_code"} {
		t.Run(string(kind), func(t *testing.T) {
			store := newMemoryPorts()
			registerTestClient(t, store)
			ceremony := newTestCeremony(t, store.ports())
			grant := grantOfScopes("openid", "offline")
			grant.ExpiresAt = map[oauthceremony.TokenKind]time.Time{kind: bound}
			result, err := completeWith(t, ceremony, authorizeRequest(), grant)
			if err == nil {
				t.Fatalf("граница вида %q, которого церемония не выпускает, принята: параметры %v", kind, result.Parameters)
			}
			if !errors.Is(err, oauthceremony.ErrCeremonyMisuse) {
				t.Errorf("граница вида %q отвергнута случаем %v, ожидался %v", kind, oauthceremony.CodeOf(err), oauthceremony.CodeCeremonyMisuse)
			}
			for _, want := range []string{"AuthorizationGrant.ExpiresAt", strconv.Quote(string(kind))} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("отказ не называет %s: %q", want, err)
				}
			}
			if codes := storedCodes(store); codes != 0 {
				t.Errorf("при отказе в хранилище кодов %d", codes)
			}
		})
	}
}
