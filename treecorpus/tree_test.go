// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treecorpus_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treecorpus"

	"github.com/PRO-Robotech/corelib/gitenv"
)

// TestTreeExcludesIgnoredAndKeepsTracked — состав обязан краснеть на внесённом
// дефекте и МОЛЧАТЬ на законной конструкции той же формы.
//
// Дефект вносится настоящим: во временном репозитории заводится игнорируемый
// каталог с файлом внутри — та самая форма, что живёт в рабочих деревьях
// агентов. Законная конструкция — отслеживаемый файл в каталоге с ТЕМ ЖЕ
// необычным именем-префиксом, чтобы отсев не мог оказаться грубым запретом по
// имени: убери опору на индекс — покраснеет первая половина; замени её на
// «отбрасывать всё, что начинается с .claude» — покраснеет вторая.
//
// Проба переехала сюда вместе со своим предметом: раскладка состава жила в
// тестовом файле пакета гейтов и была доступна одному пакету, из-за чего
// расщепление того пакета упиралось в место объявления помощника, а не в предмет
// обхода.
func TestTreeExcludesIgnoredAndKeepsTracked(t *testing.T) {
	root := t.TempDir()
	mustRun := func(args ...string) {
		t.Helper()
		cmd := gitenv.Command(root, args...)
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	mustRun("init", "-q")
	write(".gitignore", ".claude/worktrees/\n")
	write("services/x/real.go", "package x\n")
	// Законная конструкция ТОЙ ЖЕ ФОРМЫ: каталог `.claude`, но НЕ `worktrees`,
	// и файл в индексе. Он обязан остаться виден.
	write(".claude/agents/kept.go", "package agents\n")
	mustRun("add", ".gitignore", "services/x/real.go", ".claude/agents/kept.go")
	mustRun("-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"commit", "-q", "-m", "fixture")
	// ДЕФЕКТ: копия дерева в игнорируемом каталоге.
	write(".claude/worktrees/copy/services/x/real.go", "package x\n")
	write(".claude/worktrees/copy/ghost.go", "package ghost\n")

	tree, err := treecorpus.NewTree(root)
	if err != nil {
		t.Fatalf("NewTree: %v", err)
	}

	if got := tree.Count(); got != 3 {
		names := tree.SortedFiles()
		sort.Strings(names)
		t.Fatalf("перепись: прочитано %d файлов индекса, ожидалось 3 (%v)", got, names)
	}
	// (а) КРАСНОЕ НАПРАВЛЕНИЕ: привнесённое из игнорируемого каталога не видно.
	for _, rel := range []string{
		".claude/worktrees/copy/ghost.go",
		".claude/worktrees/copy/services/x/real.go",
	} {
		if tree.HasFile(rel) {
			t.Errorf("%s принят за часть дерева — состав взят с диска, а не из индекса", rel)
		}
	}
	if tree.HasDir(".claude/worktrees") || tree.HasDir(".claude/worktrees/copy") {
		t.Error(".claude/worktrees/ не отсечён как каталог — поддерево будет прочитано целиком")
	}
	// (б) МОЛЧАЛИВОЕ НАПРАВЛЕНИЕ: законное той же формы остаётся видимым.
	if !tree.HasFile(".claude/agents/kept.go") {
		t.Error(".claude/agents/kept.go потерян — отсев грубее своего предмета: " +
			"он запрещает по имени каталога вместо того, чтобы спрашивать индекс")
	}
	if !tree.HasDir(".claude/agents") || !tree.HasDir("services/x") {
		t.Error("каталог с отслеживаемым содержимым объявлен ненужным")
	}
	if !tree.HasFile("services/x/real.go") {
		t.Error("обычный отслеживаемый файл потерян")
	}
}

// TestSyntheticTreeReadsTheDiskAndSaysSo — синтетическое дерево репозиторием не
// является, и состав ему даёт обход. Отдельный конструктор — чтобы этот выбор
// делал вызывающий, а не молчаливый откат внутри NewTree.
func TestSyntheticTreeReadsTheDiskAndSaysSo(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a", "b", "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	tree, err := treecorpus.SyntheticTree(root)
	if err != nil {
		t.Fatalf("SyntheticTree: %v", err)
	}
	if !tree.HasFile("a/b/f.txt") || !tree.HasDir("a/b") || tree.Count() != 1 {
		t.Fatalf("синтетический состав не собран: файлы %v", tree.SortedFiles())
	}

	// Тот же каталог репозиторием не является — NewTree обязан ОТКАЗАТЬ, а не
	// вернуть пустой состав: пустой успех неотличим от чистого дерева.
	if _, err := treecorpus.NewTree(root); err == nil {
		t.Error("NewTree на не-репозитории вернул состав вместо отказа — молчаливый " +
			"откат на обход диска вернул бы ровно тот дефект, ради которого пакет написан")
	}
}
