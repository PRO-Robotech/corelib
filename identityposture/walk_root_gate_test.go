// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// walk_root_gate_test.go — ОБЛАСТЬ гейта F4d-03: обход судит ЭТОТ модуль и не
// выходит за него, а распознаватель «знает о посадке» опознаёт предмет в том
// написании, в котором он в этом модуле записан.
//
// ПОЧЕМУ ЭТО ОТДЕЛЬНЫЙ ПРЕДМЕТ, А НЕ ПОДРОБНОСТЬ СОСЕДНЕГО ГЕЙТА. Вердикт,
// снятый за пределами модуля, есть функция того, ЧТО ЛЕЖИТ РЯДОМ с рабочей
// копией, а не свойство коммита: на машине, где копии соседствуют, он красный
// всегда, а в конвейере — зелёный при пустом соседстве. Обе стороны читаются
// как вердикт о дереве и им не являются.
package identityposture_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestF4d03_TheWalkRootIsTheModuleThisPackageBelongsTo — корень обхода есть
// корень ЭТОГО модуля, ближайший каталог с go.mod над пакетом. Ни выше, ни ниже.
func TestF4d03_TheWalkRootIsTheModuleThisPackageBelongsTo(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("рабочий каталог не получен: %v", err)
	}

	want := nearestModuleRoot(t, cwd)

	got, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("корень обхода не разрешён: %v", err)
	}
	got = filepath.Clean(got)

	if got != want {
		outside := "внутри модуля"
		if !strings.HasPrefix(want+string(filepath.Separator), got+string(filepath.Separator)) {
			outside = "в стороне от модуля"
		} else if got != want {
			outside = "ВЫШЕ модуля: обход судит то, что лежит рядом с рабочей копией"
		}
		t.Fatalf("корень обхода %q — %s; корень модуля %q.\n"+
			"Вердикт, снятый за пределами модуля, есть свойство соседства, а не коммита: "+
			"рядом с рабочей копией лежат чужие копии этого же пакета, и каждая читается как "+
			"второе объявление словаря", got, outside, want)
	}

	t.Logf("перепись области: корень обхода %q; он же корень модуля (go.mod рядом)", got)
}

// TestF4d03Injection_AFileImportingThisPackageIsAware — ДЕФЕКТ, вносимый одним
// фактом: файл, импортирующий ЭТОТ пакет, обязан опознаваться знающим о
// посадке. Иначе второе перечисление у импортёра проходит мимо обхода.
func TestF4d03Injection_AFileImportingThisPackageIsAware(t *testing.T) {
	self := selfImportPath(t)

	src := "package consumer\n\nimport \"" + self + "\"\n\nvar _ = identityposture.Names\n"
	f, err := parser.ParseFile(token.NewFileSet(), "consumer.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}

	if !isPostureAware(f, src, self) {
		t.Fatalf("импортёр пакета %q не опознан знающим о посадке — "+
			"распознаватель называет предмет написанием, которого в этом модуле нет", self)
	}
}

// Законный близнец: импортёр СОСЕДНЕГО пакета фундамента о посадке не знает.
// Без этой половины распознаватель, расширенный до «любого импорта corelib»,
// объявил бы находкой каждый файл модуля.
func TestF4d03_AFileImportingASiblingPackageIsNotAware(t *testing.T) {
	self := selfImportPath(t)
	sibling := filepath.ToSlash(filepath.Join(filepath.Dir(self), "ids"))

	src := "package consumer\n\nimport \"" + sibling + "\"\n\nvar _ = ids.NewID\n"
	f, err := parser.ParseFile(token.NewFileSet(), "consumer.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}

	if isPostureAware(f, src, self) {
		t.Fatalf("импортёр соседнего пакета %q опознан знающим о посадке — "+
			"распознаватель ловит форму импорта, а не предмет", sibling)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Вспомогательное: обе величины ВЫВОДЯТСЯ из дерева, а не выписываются.
// Выписанный путь импорта пережил переезд пакета из платформы в фундамент —
// именно так распознаватель и перестал называть свой предмет.

// declaringPackageDir — каталог ЭТОГО пакета относительно корня модуля.
func declaringPackageDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("рабочий каталог не получен: %v", err)
	}
	rel, err := filepath.Rel(nearestModuleRoot(t, cwd), cwd)
	if err != nil {
		t.Fatalf("каталог пакета не соотнесён с корнем модуля: %v", err)
	}
	return rel
}

// nearestModuleRoot — ближайший каталог с go.mod над dir.
func nearestModuleRoot(t *testing.T, dir string) string {
	t.Helper()
	cur := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			t.Fatalf("go.mod не найден ни в одном каталоге над %q — предпосылка гейта неверна", dir)
		}
		cur = parent
	}
}

// selfImportPath — путь импорта ЭТОГО пакета: строка module из go.mod плюс путь
// каталога пакета относительно корня модуля.
func selfImportPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("рабочий каталог не получен: %v", err)
	}
	root := nearestModuleRoot(t, cwd)

	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("go.mod не прочитан: %v", err)
	}
	module := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			module = strings.TrimSpace(rest)
			break
		}
	}
	if module == "" {
		t.Fatal("строка module в go.mod не найдена — путь импорта не выводится")
	}

	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		t.Fatalf("каталог пакета не соотнесён с корнем модуля: %v", err)
	}
	if rel == "." {
		return module
	}
	return module + "/" + filepath.ToSlash(rel)
}
