// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// gitenv_test.go — проба СВОЙСТВА, ради которого пакет написан: фикстура,
// заводящая свой репозиторий, не трогает репозиторий прогона.
//
// # Пара обязательна
//
// Утверждение «индекс не изменился» само по себе зеленеет и тогда, когда фикстура
// вообще ничего не сделала. Поэтому рядом стоит КОНТРОЛЬ
// (TestRawExecCommandDoesWriteTheRepositoryOfTheRun): та же сцена, но вызов git —
// прямой, как было до починки. Он обязан показать урон. Если однажды покраснеет
// он, а не основная проба, значит предмет исчез — например, git перестал считать
// `GIT_DIR` сильнее рабочего каталога, — и снимать надо весь пакет вместе с
// гейтом, а не «починить» контроль.
//
// Прямой вызов git здесь законен ровно потому, что это дом предмета: гейт
// `internal/repohygiene` исключает каталог пакета поимённо.
//
// # Почему настоящий репозиторий, а не подставной
//
// Предмет проверки в том и состоит, что репозиторий выбирает git, а не наш код.
// Подменить git здесь нечем: подставной исполнитель принимал бы наши аргументы и
// не воспроизводил бы ровно то правило разрешения, которое и есть дефект.
// Репозиторий «прогона» здесь — свой, во временном каталоге; настоящую рабочую
// копию проба не трогает ни при каком исходе.
package gitenv

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo заводит репозиторий с count отслеживаемыми файлами и возвращает путь.
func newRepo(t *testing.T, count int) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		// Прямой вызов НАМЕРЕННО: сцену готовим до того, как выставим GIT_DIR.
		cmd := exec.Command("git", args...) // #nosec G204 -- фиксированный argv[0], аргументы из теста
		cmd.Dir = dir
		cmd.Env = append(Env(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("подготовка сцены, git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	var names []string
	for i := 0; i < count; i++ {
		name := "kept" + string(rune('a'+i)) + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	run(append([]string{"add", "--"}, names...)...)
	run("commit", "-qm", "scene")
	return dir
}

// indexDigest — отпечаток индекса репозитория. Сравнивается ОТПЕЧАТОК, а не
// число записей: подмена одного набора файлов другим той же длины прошла бы мимо
// счётчика.
func indexDigest(t *testing.T, repo string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatalf("чтение индекса %s: %v", repo, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func trackedCount(t *testing.T, repo string) int {
	t.Helper()
	out, err := Command(repo, "ls-files").Output()
	if err != nil {
		t.Fatalf("git ls-files в %s: %v", repo, err)
	}
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// buildFixture воспроизводит то, что делает добрая половина проб дерева:
// заводит синтетический репозиторий во временном каталоге и добавляет в него
// файл. gitFn — способ позвать git; в нём и вся разница между пробой и контролем.
func buildFixture(t *testing.T, gitFn func(dir string, args ...string) *exec.Cmd) string {
	t.Helper()
	fix := t.TempDir()
	if err := os.WriteFile(filepath.Join(fix, "synthetic.txt"), []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		cmd := gitFn(fix, args...)
		// Исход фикстуры здесь не утверждается: контроль обязан ДОЙТИ до
		// конца сцены, даже если git ругнулся, — предмет пробы в том, что
		// стало с ЧУЖИМ репозиторием, а не в том, удалась ли фикстура.
		_, _ = cmd.CombinedOutput()
	}
	return fix
}

// TestFixtureNeverWritesTheRepositoryOfTheRun — предикат снятия #468 в
// исполнимой форме: прогон с выставленным GIT_DIR не меняет ни индекс, ни состав
// репозитория, из которого он запущен.
func TestFixtureNeverWritesTheRepositoryOfTheRun(t *testing.T) {
	run := newRepo(t, 5)
	beforeDigest := indexDigest(t, run)
	beforeCount := trackedCount(t, run)
	if beforeCount != 5 {
		t.Fatalf("сцена собралась неверно: отслеживаемых файлов %d, ожидалось 5", beforeCount)
	}

	// Ровно то окружение, которое `git push` выставляет хуку и которое хук
	// наследует всему, что запускает.
	t.Setenv("GIT_DIR", filepath.Join(run, ".git"))

	fix := buildFixture(t, Command)

	if got := indexDigest(t, run); got != beforeDigest {
		t.Fatalf("индекс репозитория прогона ИЗМЕНЁН фикстурой пробы.\n"+
			"было %s, стало %s; отслеживаемых файлов было %d, стало %d.\n"+
			"Помощник перестал снимать окружение — сверь Vars() с тем, что\n"+
			"снимает scripts/hooks/pre-push.",
			beforeDigest[:12], got[:12], beforeCount, trackedCount(t, run))
	}

	// Положительная половина: фикстура не просто «ничего не сломала» — она
	// сделала свою работу в СВОЁМ репозитории. Без этого проба зеленела бы на
	// помощнике, который вообще не запускает git.
	out, err := Command(fix, "ls-files").Output()
	if err != nil {
		t.Fatalf("git ls-files в фикстуре: %v", err)
	}
	if !strings.Contains(string(out), "synthetic.txt") {
		t.Fatalf("фикстура не завела своего файла (вывод: %q) — проба «чужой "+
			"индекс цел» ничего не значит, если фикстура ничего не делала", out)
	}
}

// TestRawExecCommandDoesWriteTheRepositoryOfTheRun — КОНТРОЛЬ. Он утверждает, что
// предмет пробы выше существует: та же сцена с прямым вызовом git портит чужой
// репозиторий.
//
// Проба, требующая дефекта, законна ровно в одном случае — когда её предмет и
// есть дефект, и она уходит вместе с ним. Здесь это так: покрасневший контроль
// означает, что `GIT_DIR` перестал перебивать рабочий каталог, и тогда снимается
// весь пакет.
func TestRawExecCommandDoesWriteTheRepositoryOfTheRun(t *testing.T) {
	run := newRepo(t, 5)
	beforeDigest := indexDigest(t, run)

	t.Setenv("GIT_DIR", filepath.Join(run, ".git"))

	buildFixture(t, func(dir string, args ...string) *exec.Cmd {
		cmd := exec.Command("git", args...) // #nosec G204 -- воспроизведение дефекта, это и есть предмет
		cmd.Dir = dir
		cmd.Env = os.Environ() // ровно то наследование, от которого спасает пакет
		return cmd
	})

	if indexDigest(t, run) == beforeDigest {
		t.Fatal("прямой вызов git с выставленным GIT_DIR НЕ изменил чужой индекс.\n" +
			"Предмет пакета исчез: git больше не считает GIT_DIR сильнее рабочего\n" +
			"каталога. Тогда снимается ВЕСЬ пакет вместе с гейтом\n" +
			"TestGitCommandsRunWithScrubbedEnvironment, а не «чинится» этот контроль.")
	}
}

func TestScrubRemovesOnlyRepositorySelectingVars(t *testing.T) {
	in := []string{
		"GIT_DIR=/x", "GIT_WORK_TREE=/y", "GIT_INDEX_FILE=/z",
		"GIT_OBJECT_DIRECTORY=/o", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/a",
		"GIT_COMMON_DIR=/c", "GIT_PREFIX=p/",
		// Остаются: подпись фикстуры задают именно ими, и их снятие сломало бы
		// каждую пробу, которая коммитит.
		"GIT_AUTHOR_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_COUNT=0", "PATH=/usr/bin", "HOME=/home/x",
		// Ловушка на предикат по префиксу: имя НАЧИНАЕТСЯ с GIT_DIR.
		"GIT_DIRECTORY_HINT=keep",
	}
	got := strings.Join(Scrub(in), "\n")
	for _, v := range Vars() {
		if strings.Contains(got, v+"=") {
			t.Errorf("%s не снята", v)
		}
	}
	for _, keep := range []string{
		"GIT_AUTHOR_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_COUNT=0", "PATH=/usr/bin", "HOME=/home/x",
		"GIT_DIRECTORY_HINT=keep",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s снята, а не должна была", keep)
		}
	}
	if n := len(Scrub(in)); n != len(in)-len(Vars()) {
		t.Errorf("снято записей %d, ожидалось %d", len(in)-n, len(Vars()))
	}
}

func TestScrubDoesNotMutateItsInput(t *testing.T) {
	in := []string{"GIT_DIR=/x", "PATH=/usr/bin"}
	_ = Scrub(in)
	if in[0] != "GIT_DIR=/x" || len(in) != 2 {
		t.Fatalf("вход изменён: %v", in)
	}
}

func TestVarsIsACopy(t *testing.T) {
	v := Vars()
	if len(v) == 0 {
		t.Fatal("перечень снимаемых переменных пуст — пакет ничего не снимает")
	}
	v[0] = "ПОДМЕНА"
	if Vars()[0] == "ПОДМЕНА" {
		t.Fatal("Vars() отдаёт общий срез — вызывающий может изменить перечень")
	}
}
