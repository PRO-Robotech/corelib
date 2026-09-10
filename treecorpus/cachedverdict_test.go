// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// cachedverdict_test.go — страж кешированного вердикта СПОСОБЕН отказать и
// СПОСОБЕН пропустить.
//
// Инъекция идёт НАСТОЯЩИМ вводом: командные строки ниже сняты с работающего
// `go test` (семь форм вызова), а не выдуманы. Рядом с каждой стоит фактическое
// поведение кеша, измеренное на изолированном модуле, — то есть проба утверждает
// не «предикат отвечает так», а «предикат отвечает ТО ЖЕ, что делает инструмент».

package treecorpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// observedRuns — формы вызова, снятые с `go test` 2026-08-25 (go 1.25.13).
//
// wantCached — не ожидание автора, а НАБЛЮДЕНИЕ: применил ли инструмент кеш на
// этой форме. Обе стороны представлены, иначе проба зеленела бы и на предикате,
// отвечающем «да» всегда, и на отвечающем «нет» всегда.
var observedRuns = []struct {
	name       string
	argv       []string
	wantCached bool
}{
	{
		name: "go test ./пакет/ — кеш применяется",
		argv: []string{"/tmp/go-build/b001/probe.test",
			"-test.testlogfile=/tmp/go-build1679090454/b001/testlog.txt",
			"-test.paniconexit0", "-test.timeout=10m0s", "-test.run=TestX$", "-test.v=true"},
		wantCached: true,
	},
	{
		name: "go test ./пакет/ -count=1 — кеш не применяется",
		argv: []string{"/tmp/go-build/b001/probe.test",
			"-test.paniconexit0", "-test.timeout=10m0s", "-test.run=TestX$", "-test.v=true",
			"-test.count=1"},
		wantCached: false,
	},
	{
		name: "go test ./пакет/ -count=2 — кеш не применяется",
		argv: []string{"/tmp/go-build/b001/probe.test",
			"-test.paniconexit0", "-test.timeout=10m0s", "-test.count=2"},
		wantCached: false,
	},
	{
		name: "go test ./пакет/ -race — кеш ПРИМЕНЯЕТСЯ (сборка другая, кеш тот же)",
		argv: []string{"/tmp/go-build/b001/probe.test",
			"-test.testlogfile=/tmp/go-build/b001/testlog.txt",
			"-test.paniconexit0", "-test.timeout=10m0s"},
		wantCached: true,
	},
	{
		name: "go test ./пакет/ -timeout 5m — кеш применяется (флаг кешируемый)",
		argv: []string{"/tmp/go-build/b001/probe.test",
			"-test.testlogfile=/tmp/go-build/b001/testlog.txt",
			"-test.paniconexit0", "-test.timeout=5m0s"},
		wantCached: true,
	},
	{
		name: "go test ./пакет/ -bench=. — кеш не применяется",
		argv: []string{"/tmp/go-build/b001/probe.test",
			"-test.paniconexit0", "-test.timeout=10m0s", "-test.bench=."},
		wantCached: false,
	},
	{
		// Форма, ради которой дискриминатор выбран именно такой. Восстановление
		// правил кеширования по перечню «кешируемых» флагов дало бы здесь ЛОЖНУЮ
		// находку: заданных флагов нет ни одного, а кеш инструмент не применяет.
		name: "go test в режиме текущего каталога — кеш не применяется",
		argv: []string{"/tmp/go-build/b001/probe.test",
			"-test.paniconexit0", "-test.timeout=10m0s", "-test.run=TestX$", "-test.v=true"},
		wantCached: false,
	},
}

func TestCachedVerdictDiscriminatorAgreesWithTheToolOnEveryObservedForm(t *testing.T) {
	seenCached, seenPlain := 0, 0
	for _, r := range observedRuns {
		if got := resultWillBeCached(r.argv); got != r.wantCached {
			t.Errorf("%s: предикат сказал %v, инструмент повёл себя как %v",
				r.name, got, r.wantCached)
		}
		if r.wantCached {
			seenCached++
		} else {
			seenPlain++
		}
	}
	t.Logf("перепись: форм вызова осмотрено %d (кеш применяется %d, не применяется %d)",
		len(observedRuns), seenCached, seenPlain)
	// Односторонний набор — вакуумное утверждение: предикат-константа прошёл бы
	// его целиком.
	if seenCached == 0 || seenPlain == 0 {
		t.Fatalf("набор односторонний (кешируемых %d, некешируемых %d) — он ничего не различает",
			seenCached, seenPlain)
	}
}

func TestCachedVerdictRefusalNamesTheFix(t *testing.T) {
	withRunArgs(t, observedRuns[0].argv)
	msg := CachedVerdictRefusal()
	if msg == "" {
		t.Fatal("на кешируемом прогоне отказа нет")
	}
	// Сообщение отказа — рантайм-диагностика оператору, и оно обязано называть
	// то, что чинит: без этого следующий читатель снимет стража как непонятный.
	if !strings.Contains(msg, "-count=1") {
		t.Fatalf("отказ не называет починку:\n%s", msg)
	}
}

// TestRealIndexConstructorsRefuseACacheableRun — страж провязан в ОБА
// конструктора настоящего индекса, а не объявлен рядом с ними.
//
// Обе стороны на каждом конструкторе: на кешируемой командной строке — отказ, на
// той же с отключённым кешем — состав дерева. Без второй половины проба зеленела
// бы на конструкторе, отказывающем всегда.
func TestRealIndexConstructorsRefuseACacheableRun(t *testing.T) {
	root := repoRootForProbe(t)

	withRunArgs(t, observedRuns[0].argv) // кеш применяется
	if _, err := Under(root); err == nil || !strings.Contains(err.Error(), "-count=1") {
		t.Errorf("Under пропустил кешируемый прогон: %v", err)
	}
	if _, err := NewTree(root); err == nil || !strings.Contains(err.Error(), "-count=1") {
		t.Errorf("NewTree пропустил кешируемый прогон: %v", err)
	}

	withRunArgs(t, observedRuns[1].argv) // кеш отключён
	files, err := Under(root)
	if err != nil || len(files) == 0 {
		t.Fatalf("Under отказал на прогоне с отключённым кешем: файлов %d, %v", len(files), err)
	}
	tree, err := NewTree(root)
	if err != nil || tree.Count() == 0 {
		t.Fatalf("NewTree отказал на прогоне с отключённым кешем: файлов %d, %v",
			treeCount(tree), err)
	}
	t.Logf("перепись: на прогоне с отключённым кешем состав прочитан — файлов %d", len(files))
}

// TestSyntheticTreeIsNotRefusedOnACacheableRun — законный близнец.
//
// Синтетическое дерево репозиторием не является: его состав целиком определяется
// пакетом пробы, поэтому вердикт о нём кешируется ЗАКОННО. Страж, накрывший бы и
// его, был бы платой без выигрыша — и первым же ложным отказом себя отключил.
func TestSyntheticTreeIsNotRefusedOnACacheableRun(t *testing.T) {
	withRunArgs(t, observedRuns[0].argv) // кеш применяется

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("подготовка синтетики: %v", err)
	}
	tree, err := SyntheticTree(dir)
	if err != nil {
		t.Fatalf("SyntheticTree отказал на кешируемом прогоне — страж накрыл лишнее: %v", err)
	}
	if tree.Count() != 1 {
		t.Fatalf("синтетическое дерево прочитано неверно: файлов %d", tree.Count())
	}
}

// withRunArgs подменяет командную строку прогона на время пробы и возвращает её
// на место. Проба, оставившая подмену, испортила бы соседние by construction,
// поэтому возврат — не вежливость, а условие.
func withRunArgs(t *testing.T, argv []string) {
	t.Helper()
	saved := runArgs
	runArgs = argv
	t.Cleanup(func() { runArgs = saved })
}

func repoRootForProbe(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("рабочий каталог: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("корень репозитория не найден: go.mod отсутствует во всех каталогах вверх")
		}
		dir = parent
	}
}

func treeCount(t *Tree) int {
	if t == nil {
		return 0
	}
	return t.Count()
}
