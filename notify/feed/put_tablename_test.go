// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// tableSuffix — суффикс таблицы ленты; имя метрики с «_notification_» им не
// является.
var tableSuffix = regexp.MustCompile(`_notification_(outbox|window|contrib)\b`)

// suffixLiterals — строковые литералы с суффиксом таблицы ленты в не-тестовых
// файлах поддерева вне internal/tablename.
func suffixLiterals(t *testing.T, root string) (found []string, files int) {
	t.Helper()
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if filepath.Base(p) == "tablename" || filepath.Base(p) == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		files++
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err == nil && tableSuffix.MatchString(v) {
				found = append(found, fset.Position(lit.Pos()).String())
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	return found, files
}

// УК89 (б): имя таблицы ленты — только tablename.Of; суффикс литералом живёт
// только в internal/tablename.
func TestUK89_FeedTableSuffixLivesOnlyInTablename(t *testing.T) {
	found, files := suffixLiterals(t, ".")
	t.Logf("осмотрено не-тестовых файлов notify/feed: %d", files)
	require.NotZero(t, files)
	require.Empty(t, found)
}

// Инъекция: литерал с суффиксом вне tablename — находка с координатой;
// близнец — тот же литерал в tablename молчит.
func TestUK89_InjectionSuffixLiteralIsFound(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "internal", "tablename"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "tablename", "t.go"),
		[]byte("package tablename\nconst s = \"_notification_window\"\n"), 0o600))
	found, _ := suffixLiterals(t, root)
	require.Empty(t, found, "близнец: суффикс в tablename")
	require.NoError(t, os.WriteFile(filepath.Join(root, "put.go"),
		[]byte("package feed\nconst q = \"UPDATE vpc_notification_window SET count = 0\"\n"), 0o600))
	found, _ = suffixLiterals(t, root)
	require.Len(t, found, 1)
	require.Contains(t, found[0], "put.go:2")
	// близнец: имя метрики с «_notification_» — не суффикс таблицы
	require.NoError(t, os.WriteFile(filepath.Join(root, "put.go"),
		[]byte("package feed\nconst m = \"kacho_notification_feed_put_defects_total\"\n"), 0o600))
	found, _ = suffixLiterals(t, root)
	require.Empty(t, found)
}
