// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	// toExpired — перевод строки ленты в EXPIRED: присваивание состояния в
	// операторе, а не упоминание слова.
	toExpired = regexp.MustCompile(`(?is)\bSET\s+state\s*=\s*'expired'`)
	// leaseFree — условие аренды того же оператора (NTF1-B25, CX1-16).
	leaseFree = regexp.MustCompile(`(?is)\(\s*lease_until\s+IS\s+NULL\s+OR\s+lease_until\s*<=\s*now\(\)\s*\)`)
)

// expiryWithoutLease — находки: строковый литерал не-тестового файла каталога,
// переводящий строку в EXPIRED, без условия аренды в том же литерале. Узел —
// литерал по разбору; комментарий узлом не является.
func expiryWithoutLease(t *testing.T, dir string) (found []string, expiries, files int) {
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
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil || !toExpired.MatchString(v) {
				return true
			}
			expiries++
			if !leaseFree.MatchString(v) {
				found = append(found, fset.Position(lit.Pos()).String())
			}
			return true
		})
	}
	return found, expiries, files
}

// NTF1-B25 (гейт): в feed нет перевода в EXPIRED без условия аренды в том же
// операторе.
func TestNTF1B25_NoExpiryWithoutTheLeaseCondition(t *testing.T) {
	found, expiries, files := expiryWithoutLease(t, ".")
	t.Logf("осмотрено не-тестовых файлов: %d, операторов перевода в EXPIRED: %d", files, expiries)
	require.NotZero(t, files, "пустой обход — не зелёный")
	require.Equal(t, 1, expiries, "переводов в EXPIRED не один — предмет гейта изменился")
	require.Empty(t, found)
}

// Инъекция: перевод без условия аренды — находка с координатой; близнец — то
// же с условием; упоминание в комментарии узлом не является.
func TestNTF1B25_InjectionExpiryWithoutLeaseIsFound(t *testing.T) {
	dir := t.TempDir()
	write := func(src string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600))
	}
	write("package x\n\n// SET state = 'expired' — комментарий.\nconst q = `UPDATE t SET state = 'expired' WHERE expires_at <= now()`\n")
	found, n, _ := expiryWithoutLease(t, dir)
	require.Equal(t, 1, n)
	require.Len(t, found, 1)
	require.Contains(t, found[0], "x.go:4")

	write("package x\n\nconst q = `UPDATE t SET state = 'expired' WHERE expires_at <= now() AND (lease_until IS NULL OR lease_until <= now())`\n")
	found, n, _ = expiryWithoutLease(t, dir)
	require.Equal(t, 1, n)
	require.Empty(t, found)
}
