// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// confinement_test.go — гейт F1-51 по ЭТОМУ дереву: стек JOSE движка живёт
// только в поддереве. Способность разбора упасть и смолчать доказана
// инъекциями в обе стороны у самого разбора
// (treehygiene/imports_injection_test.go); здесь — правило этого дерева и
// сверка переписи со знаменателем.
package engineconfinement_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/gitenv"
	"github.com/PRO-Robotech/corelib/treehygiene"
)

// subtreeRoot — корень внесённого поддерева движка.
const subtreeRoot = "internal/oauth2"

// engineStackRule — правило фундамента. Пути собраны из частей, чтобы строка
// правила не читалась поиском по образцу как импорт.
//
// Набор — КАЖДЫЙ опубликованный путь библиотеки, а не один нынешний: go-jose
// публиковалась под четырьмя (`github.com/square/go-jose` →
// `gopkg.in/square/go-jose.vN` → `github.com/go-jose/go-jose` и
// `gopkg.in/go-jose/go-jose.vN`), и подписант на любом из них вне поддерева —
// второй путь подписи. Перечень выведен опросом прокси модулей; предикат его
// полноты — forms_test.go.
func engineStackRule() treehygiene.ImportConfinement {
	return treehygiene.ImportConfinement{
		Name: "стек JOSE движка",
		Set: []string{
			"github.com/go-jose/" + "go-jose",
			"gopkg.in/go-jose/" + "go-jose",
			"gopkg.in/square/" + "go-jose",
			"github.com/square/" + "go-jose",
			"github.com/cristalhq/" + "jwt",
		},
		Homes:           []string{subtreeRoot},
		ForbiddenAtHome: []string{"github.com/golang-jwt/" + "jwt"},
	}
}

// repoRoot — корень дерева фундамента. Предпосылка проверяется, а не
// предполагается: переехавший пакет судил бы чужой каталог.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: корень дерева не вычислен: %v", err)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !bytes.Contains(mod, []byte("module github.com/PRO-Robotech/corelib\n")) {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %s — не корень модуля фундамента (go.mod: %v)", root, err)
	}
	return root
}

// trackedGoFiles — знаменатель ДРУГИМ выражением: отбор по образцу git, а не
// по суффиксу в разборе.
func trackedGoFiles(t *testing.T, root string) int {
	t.Helper()
	out, err := gitenv.Command(root, "ls-files", "-z", "--", "*.go").Output()
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: git ls-files: %v", err)
	}
	n := 0
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" {
			n++
		}
	}
	return n
}

// TestEngineJOSEStackIsConfinedToTheSubtree — гейт F1-51 по этому дереву.
func TestEngineJOSEStackIsConfinedToTheSubtree(t *testing.T) {
	root := repoRoot(t)

	findings, census, err := treehygiene.AuditImportConfinement(root, engineStackRule())
	if err != nil {
		t.Fatalf("НЕ ВЫПОЛНИЛОСЬ: %v", err)
	}
	t.Log(census)

	if want := trackedGoFiles(t, root); census.GoFiles != want {
		t.Errorf("обход прочитал файлов .go %d шт, а в индексе их %d шт — перепись разошлась со знаменателем",
			census.GoFiles, want)
	}
	if census.HomeImporters == 0 {
		t.Errorf("внутри %s ни один файл не импортирует стек JOSE движка — движок потерял стек, "+
			"либо обход слеп; «снаружи ноль» при этом ничего не значит", subtreeRoot)
	}
	for _, f := range findings {
		t.Errorf("%s", f)
	}
}
