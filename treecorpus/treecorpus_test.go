// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package treecorpus_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/treecorpus"

	"github.com/PRO-Robotech/corelib/gitenv"
)

// newRepo — настоящий репозиторий во временном каталоге: один отслеживаемый
// файл, одно правило игнорирования и один файл под ним.
//
// Дерево синтетическое, но git — настоящий: предмет проверки в том и состоит,
// что состав берётся у версионного контроля, поэтому подменять его нечем.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := gitenv.Command(dir, args...)
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write(".gitignore", "build/\n")
	write("sub/kept.go", "package sub\n")
	write("sub/build/generated.go", "package build\n")
	run("add", ".gitignore", "sub/kept.go")
	run("commit", "-qm", "seed")
	return dir
}

// TestUnderSeesTrackedAndNotIgnored — обе половины сразу, потому что порознь
// каждая зеленеет на сломанном: «игнорируемого нет» верно и когда корпус пуст,
// «отслеживаемое есть» верно и когда возвращено всё подряд.
func TestUnderSeesTrackedAndNotIgnored(t *testing.T) {
	repo := newRepo(t)

	files, err := treecorpus.Under(filepath.Join(repo, "sub"))
	if err != nil {
		t.Fatalf("Under: %v", err)
	}
	t.Logf("осмотрено: %d путей под sub/", len(files))

	var sawKept, sawIgnored bool
	for _, f := range files {
		switch filepath.Base(f) {
		case "kept.go":
			sawKept = true
		case "generated.go":
			sawIgnored = true
		}
	}
	if !sawKept {
		t.Errorf("отслеживаемый sub/kept.go в корпусе отсутствует — положительный "+
			"контроль провален, и «игнорируемого не видно» ничего не значит: пустой "+
			"корпус даёт тот же ответ. Получено: %v", files)
	}
	if sawIgnored {
		t.Errorf("sub/build/generated.go попал в корпус, хотя git его игнорирует "+
			"(.gitignore: build/). Такой файл не увидит ни свежий клон, ни CI: "+
			"вердикт проверки станет свойством рабочего каталога, а не коммита. "+
			"Получено: %v", files)
	}
}

// TestUnderRefusesOutsideARepository — недоступность индекса это ОТКАЗ.
//
// Молчаливый откат на обход диска вернул бы дефект, ради которого написан
// пакет, и сделал бы это невидимо: вызывающий получил бы непустой корпус и
// решил, что всё в порядке.
func TestUnderRefusesOutsideARepository(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := treecorpus.Under(outside)
	if err == nil {
		t.Fatalf("вне репозитория Under вернул %d путей и НЕ отказал: это тихий "+
			"откат на обход диска — ровно то, что пакет запрещает", len(files))
	}
	if len(files) != 0 {
		t.Errorf("при отказе корпус обязан быть пуст, получено %d", len(files))
	}
}

// TestUnderWithSuffixFiltersAndKeepsTheRefusal — отбор по суффиксу не отменяет
// ни одного из двух свойств выше.
func TestUnderWithSuffixFiltersAndKeepsTheRefusal(t *testing.T) {
	repo := newRepo(t)

	goFiles, err := treecorpus.UnderWithSuffix(repo, ".go")
	if err != nil {
		t.Fatalf("UnderWithSuffix: %v", err)
	}
	if len(goFiles) != 1 || filepath.Base(goFiles[0]) != "kept.go" {
		t.Errorf("ожидался ровно отслеживаемый kept.go, получено %v", goFiles)
	}

	all, err := treecorpus.UnderWithSuffix(repo)
	if err != nil {
		t.Fatalf("UnderWithSuffix без суффиксов: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("пустой набор суффиксов означает «все файлы»: ожидалось 2 "+
			"(.gitignore и sub/kept.go), получено %d — %v", len(all), all)
	}

	if _, err := treecorpus.UnderWithSuffix(t.TempDir(), ".go"); err == nil {
		t.Error("вне репозитория UnderWithSuffix обязан отказать так же, как Under")
	}
}

// TestUnder_RefusesAnEmptyCorpus — пустой корпус есть ОТКАЗ.
//
// Оба нынешних вызывающих несут собственную проверку на ноль, поэтому живого
// дефекта нет. Проба здесь не про них, а про СЛЕДУЮЩЕГО: текст отказа стража
// обходов отсылает к этому пакету всякий будущий гейт, и если пакет отвечает
// «ноль файлов, ошибки нет», то такой гейт унаследует ровно то, ради чего пакет
// написан, — «ноль находок», неотличимое от «ноль прочитанного».
//
// Вход не выдуман: `sub/build/` существует на диске и целиком игнорируется, то
// есть обход диска сказал бы «файлы есть», а индекс говорит «нет». Утверждаются
// обе стороны сразу, иначе отрицание зеленеет на всём сломанном.
func TestUnder_RefusesAnEmptyCorpus(t *testing.T) {
	repo := newRepo(t)

	// Положительный контроль: где отслеживаемое есть, пакет отвечает.
	got, err := treecorpus.Under(filepath.Join(repo, "sub"))
	if err != nil {
		t.Fatalf("предпосылка пробы сломана: отслеживаемый каталог не читается: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("предпосылка пробы сломана: в отслеживаемом каталоге ноль файлов")
	}

	// Отрицание — только в паре с ним.
	empty, err := treecorpus.Under(filepath.Join(repo, "sub", "build"))
	if err == nil {
		t.Fatalf("каталог без отслеживаемых файлов отдан пустым успехом (%d путей, ошибки нет).\n"+
			"  Тогда всякий гейт, взявший здесь состав, объявит «ноль находок» на «ноль "+
			"прочитанного» — тот самый класс, ради которого этот пакет и написан.", len(empty))
	}
	if !strings.Contains(err.Error(), "ни одного отслеживаемого файла") {
		t.Fatalf("отказ не называет причины: %v", err)
	}
}

// newGlobRepo — репозиторий, на котором различимы ВСЕ три свойства Glob сразу:
// отслеживаемое против игнорируемого, один уровень против рекурсии, и
// метасимвол в середине образца.
func newGlobRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := gitenv.Command(dir, args...)
		cmd.Env = append(cmd.Env,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write(".gitignore", "ignored/\n")
	write("mig/0001_a.sql", "-- a\n")
	write("mig/0002_b.sql", "-- b\n")
	write("mig/nested/0003_c.sql", "-- c\n") // глубже уровня — Glob его НЕ берёт
	write("mig/readme.md", "not sql\n")
	write("ignored/0009_x.sql", "-- ignored\n")
	write("svc/one/m/0001.sql", "-- one\n")
	write("svc/two/m/0001.sql", "-- two\n")
	run("add", ".gitignore", "mig", "svc")
	run("commit", "-qm", "seed")
	return dir
}

func globNames(t *testing.T, repo string, got []string) []string {
	t.Helper()
	out := make([]string, 0, len(got))
	for _, g := range got {
		rel, err := filepath.Rel(repo, g)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

// TestGlobTakesTrackedOneLevelAndSkipsIgnored — три утверждения в одной пробе,
// потому что порознь каждое зеленеет на сломанном: «игнорируемого нет» верно и
// для пустого ответа, «отслеживаемое есть» верно и для ответа со всем подряд, а
// «рекурсии нет» верно и когда не вернулось ничего.
func TestGlobTakesTrackedOneLevelAndSkipsIgnored(t *testing.T) {
	repo := newGlobRepo(t)

	// Неотслеживаемый файл нужной формы — тот самый вход, на котором обход диска
	// и обход индекса расходятся. Кладётся ПОСЛЕ коммита и в индекс не идёт.
	untracked := filepath.Join(repo, "mig", "0004_untracked.sql")
	if err := os.WriteFile(untracked, []byte("-- untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := treecorpus.Glob(filepath.Join(repo, "mig", "*.sql"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	names := globNames(t, repo, got)
	want := []string{"mig/0001_a.sql", "mig/0002_b.sql"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("Glob вернул %v, ожидалось %v", names, want)
	}

	// Контроль в обратную сторону: filepath.Glob на том же образце неотслеживаемый
	// файл ВИДИТ. Без этой половины проба зеленела бы и на дереве, где такого
	// файла нет вовсе, — то есть не различала бы предмет.
	disk, derr := filepath.Glob(filepath.Join(repo, "mig", "*.sql"))
	if derr != nil {
		t.Fatal(derr)
	}
	if len(disk) != len(got)+1 {
		t.Errorf("контроль негоден: обход диска дал %d, индекс %d — расхождения в один "+
			"неотслеживаемый файл нет, значит проба не различает предмет", len(disk), len(got))
	}
}

// TestGlobHonoursMetaInTheMiddle — метасимвол не только в последнем компоненте.
// Без этой формы перевод сайтов вида `ui-future/*/Dockerfile` был бы невозможен,
// и они остались бы на диске молча.
func TestGlobHonoursMetaInTheMiddle(t *testing.T) {
	repo := newGlobRepo(t)

	got, err := treecorpus.Glob(filepath.Join(repo, "svc", "*", "m", "*.sql"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	names := globNames(t, repo, got)
	want := []string{"svc/one/m/0001.sql", "svc/two/m/0001.sql"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("Glob вернул %v, ожидалось %v", names, want)
	}
}

// TestGlobRefusesAnAbsentBaseWithASentinel — отсутствующая база это ОТКАЗ, и
// отказ ОПОЗНАВАЕМЫЙ.
//
// Две половины: filepath.Glob здесь молча отдаёт (nil, nil), поэтому «каталог
// переехал» неотличимо от «ничего не подошло»; и вызывающий, для которого пустая
// база законна, обязан уметь отличить её от недоступного git — иначе он сведёт
// оба в `files = nil` и проглотит настоящий отказ.
func TestGlobRefusesAnAbsentBaseWithASentinel(t *testing.T) {
	repo := newGlobRepo(t)

	_, err := treecorpus.Glob(filepath.Join(repo, "nosuchdir", "*.sql"))
	if err == nil {
		t.Fatal("Glob по отсутствующей базе обязан отказать: пустой успех неотличим " +
			"от «ничего не подошло»")
	}
	if !errors.Is(err, treecorpus.ErrEmptyCorpus) {
		t.Errorf("отказ по отсутствующей базе обязан нести ErrEmptyCorpus, иначе вызывающий "+
			"не отличит его от недоступного git; получено: %v", err)
	}

	// Законный близнец: база ЕСТЬ, под образец ничего не подошло — это пустой
	// ответ, а не отказ. Без этой половины годился бы Glob, отказывающий всегда.
	got, err := treecorpus.Glob(filepath.Join(repo, "mig", "*.nomatch"))
	if err != nil {
		t.Errorf("непустая база без совпадений — законный пустой ответ, а не отказ: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ожидался пустой ответ, получено %v", got)
	}
}
