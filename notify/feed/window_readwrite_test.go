// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// windowSite — место оператора над таблицей окна: вызов fmt.Sprintf, среди
// аргументов которого есть tablename.Of(…, tablename.Window).
type windowSite struct {
	pos    string
	format string
}

// windowSites находит места окна в не-тестовых файлах каталога. Узел — вызов
// по разбору, а не образец текста.
func windowSites(t *testing.T, dir string) (sites []windowSite, files int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isSel(call.Fun, "fmt", "Sprintf") || len(call.Args) == 0 {
				return true
			}
			window := false
			for _, a := range call.Args[1:] {
				if c, ok := a.(*ast.CallExpr); ok && isSel(c.Fun, "tablename", "Of") && len(c.Args) == 2 && isSel(c.Args[1], "tablename", "Window") {
					window = true
				}
			}
			if !window {
				return true
			}
			format := ""
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				format, _ = strconv.Unquote(lit.Value)
			}
			sites = append(sites, windowSite{pos: fset.Position(call.Pos()).String(), format: format})
			return true
		})
	}
	return sites, files
}

func isSel(e ast.Expr, pkg, name string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != name {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// readThenWrite — находки: оператор над окном, читающий счётчик отдельно от
// записи (SELECT … count без записи в том же операторе), либо форматная
// строка, которую разбор не читает.
func readThenWrite(sites []windowSite) []string {
	var out []string
	for _, s := range sites {
		up := strings.ToUpper(strings.TrimSpace(s.format))
		switch {
		case up == "":
			out = append(out, s.pos+": форматная строка не литерал — разбор её не читает")
		case strings.HasPrefix(up, "SELECT") && strings.Contains(up, "COUNT"):
			out = append(out, s.pos+": чтение счётчика окна отдельным оператором")
		}
	}
	return out
}

// NTF1-B09 (гейт): в feed нет чтения счётчика окна с последующей записью —
// лимит держит условный оператор базы (CAS), а не проверка в Go.
func TestNTF1B09_FeedHasNoReadThenWriteOfTheWindowCounter(t *testing.T) {
	sites, files := windowSites(t, ".")
	t.Logf("осмотрено не-тестовых файлов: %d, мест окна: %d", files, len(sites))
	require.NotZero(t, files, "пустой обход — не зелёный")
	require.NotEmpty(t, sites, "мест окна 0 — предмет не найден, проверять нечего")
	require.Empty(t, readThenWrite(sites))
}

// Инъекция: синтетический пакет с чтением счётчика и записью отдельными
// операторами — находка с координатой; близнец — условный INSERT … ON CONFLICT.
func TestNTF1B09_InjectionReadThenWriteIsFound(t *testing.T) {
	dir := t.TempDir()
	write := func(src string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "w.go"), []byte(src), 0o600))
	}
	write(`package w
import ("fmt"; "x/tablename")
func put() {
	_ = fmt.Sprintf("SELECT count FROM %s WHERE key = $1", tablename.Of("s", tablename.Window))
	_ = fmt.Sprintf("UPDATE %s SET count = $2 WHERE key = $1", tablename.Of("s", tablename.Window))
}`)
	sites, _ := windowSites(t, dir)
	found := readThenWrite(sites)
	require.Len(t, found, 1)
	require.Contains(t, found[0], "w.go:4")

	write(`package w
import ("fmt"; "x/tablename")
func put() {
	_ = fmt.Sprintf("INSERT INTO %s AS w (key, count) VALUES ($1, 1) ON CONFLICT (key) DO UPDATE SET count = w.count + 1 WHERE w.count < $2 RETURNING count", tablename.Of("s", tablename.Window))
}`)
	sites, _ = windowSites(t, dir)
	require.Len(t, sites, 1)
	require.Empty(t, readThenWrite(sites))
}
