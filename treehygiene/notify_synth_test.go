// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treehygiene_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/gitenv"
)

// corelibRoot — корень модуля corelib: пробы исполняются из каталога пакета.
func corelibRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: корня модуля corelib нет в %s: %v", abs, err)
	}
	return abs
}

// goSynth — синтетическое отслеживаемое дерево модуля module, собранное
// рабочим пространством вместе с настоящим corelib: пакеты дерева импортируют
// notify/feed, notify/address и notify/form по их настоящей идентичности.
func goSynth(t *testing.T, module string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: %v", err)
		}
	}
	write("go.mod", fmt.Sprintf("module %s\n\ngo 1.26.0\n", module))
	write("go.work", fmt.Sprintf("go 1.26.0\n\nuse .\nuse %s\n", corelibRoot(t)))
	write("README.md", "# синтетическое дерево пробы\n")
	for rel, body := range files {
		write(rel, strings.TrimLeft(body, "\n"))
	}
	for _, args := range [][]string{{"init", "--quiet", "-b", "main"}, {"add", "-A"}} {
		if out, err := gitenv.Command(root, args...).CombinedOutput(); err != nil {
			t.Fatalf("проба НЕ ИСПОЛНЯЛАСЬ: git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// kinds — виды находок по порядку.
func kindsOf[F interface{ String() string }](fs []F) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString(f.String())
		b.WriteByte('\n')
	}
	return b.String()
}
