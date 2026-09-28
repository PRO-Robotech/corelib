// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package acrlevel_test

// transport_test.go — держатель п.1 предиката снятия corelib#49: пакет, где
// объявлено ранжирование уровня аутентификации, транспорта не тянет — и
// транзитивно тоже.
//
// Ранжирование читают не только точки принуждения на слушателе, но и слои без
// транспорта: разбор настроек посадки, use-case службы доступа. Пока таблица
// жила в пакете gRPC-сервера, каждый такой читатель получал ребро на сервер
// ради одной чистой функции. Проба обходит замыкание импортов пакета и
// краснеет на любом пути под google.golang.org/grpc, называя файл и строку.
//
// # Почему обход объявлений, а не `go list -deps`
//
// Граф сборки видит только файлы текущего контекста сборки: файл под
// ограничением сборки из него выпадает. Разбор объявлений импорта
// (`parser.ImportsOnly`) ограничений не исполняет и читает шире — для запрета
// шире значит строже. Файлы проб (`_test.go`) не читаются: их импорты до
// импортёра пакета не доезжают.
//
// # Чего обход не судит — и говорит об этом
//
// Пакеты модуля он читает сам, стандартная библиотека — лист. Импорт чужого
// модуля вне транспорта — ОТКАЗ судить, а не зелёный: замыкание за ним обходу
// не видно, и «находок ноль» там означало бы «не прочитано».

import (
	"bufio"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	// corelibModule — путь модуля, в котором судится пакет; сверяется с go.mod
	// (проверка предпосылки), а не принимается на веру.
	corelibModule = "github.com/PRO-Robotech/corelib"
	// transportRoot — корень транспорта, которого пакет не тянет.
	transportRoot = "google.golang.org/grpc"
)

// closureWalk — исход обхода: перепись и находки раздельно, чтобы «находок
// ноль» было отличимо от «прочитано ноль».
type closureWalk struct {
	// files — прочитано файлов исходника (без проб).
	files int
	// packages — пакеты модуля в замыкании, в порядке обхода.
	packages []string
	// findings — импорты транспорта: «файл:строка: путь», файл от корня модуля.
	findings []string
	// unseen — импорты чужих модулей вне транспорта: «пакет → путь».
	unseen []string
}

// walkImportClosure обходит замыкание импортов пакета start в модуле modulePath,
// лежащем в moduleRoot. Ошибка — отказ судить: каталог не читается, исходник не
// разбирается, у пакета в замыкании нет ни одного исходника.
func walkImportClosure(moduleRoot, modulePath, start string) (closureWalk, error) {
	var w closureWalk
	seen := map[string]bool{start: true}
	queue := []string{start}
	fset := token.NewFileSet()
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		rel := strings.TrimPrefix(strings.TrimPrefix(pkg, modulePath), "/")
		dir := filepath.Join(moduleRoot, filepath.FromSlash(rel))
		entries, err := os.ReadDir(dir)
		if err != nil {
			return w, fmt.Errorf("пакет %s: каталог не прочитан: %w", pkg, err)
		}
		read := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
			if err != nil {
				return w, fmt.Errorf("пакет %s: %w", pkg, err)
			}
			read++
			for _, spec := range f.Imports {
				imp, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return w, fmt.Errorf("пакет %s: путь импорта %s не разобран: %w", pkg, spec.Path.Value, err)
				}
				switch {
				case imp == transportRoot || strings.HasPrefix(imp, transportRoot+"/"):
					pos := fset.Position(spec.Pos())
					file, err := filepath.Rel(moduleRoot, pos.Filename)
					if err != nil {
						return w, err
					}
					w.findings = append(w.findings, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(file), pos.Line, imp))
				case imp == modulePath || strings.HasPrefix(imp, modulePath+"/"):
					if !seen[imp] {
						seen[imp] = true
						queue = append(queue, imp)
					}
				case isStandardLibrary(imp):
				default:
					if edge := pkg + " → " + imp; !seen[edge] {
						seen[edge] = true
						w.unseen = append(w.unseen, edge)
					}
				}
			}
		}
		if read == 0 {
			return w, fmt.Errorf("пакет %s: исходников нет — судить нечего", pkg)
		}
		w.files += read
		w.packages = append(w.packages, pkg)
	}
	return w, nil
}

// isStandardLibrary — путь стандартной библиотеки: в первом элементе нет точки.
func isStandardLibrary(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

// liveModuleRoot — корень модуля этого дерева. Проба исполняется в каталоге
// пакета; путь модуля сверяется с go.mod — это предпосылка обхода, и её отказ
// роняет пробу, а не даёт «находок ноль» про чужой модуль.
func liveModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("предпосылка: go.mod модуля не открыт: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if mod, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			if got := strings.TrimSpace(mod); got != corelibModule {
				t.Fatalf("предпосылка: модуль %q, проба судит %q", got, corelibModule)
			}
			return root
		}
	}
	t.Fatalf("предпосылка: в %s нет строки module", f.Name())
	return ""
}

// TestRankingPackagePullsNoTransport — живое дерево: замыкание пакета
// ранжирования без единого импорта транспорта и без непрочитанного.
func TestRankingPackagePullsNoTransport(t *testing.T) {
	root := liveModuleRoot(t)
	w, err := walkImportClosure(root, corelibModule, corelibModule+"/acrlevel")
	if err != nil {
		t.Fatalf("не выполнилось: %v", err)
	}
	t.Logf("перепись: пакетов модуля %d %v · файлов исходника %d · импортов транспорта %d · непрочитанных чужих %d",
		len(w.packages), w.packages, w.files, len(w.findings), len(w.unseen))
	// Найденный транспорт — вердикт при любом непрочитанном; непрочитанное без
	// находок — отказ судить, а не зелёный.
	if len(w.findings) > 0 {
		t.Fatalf("пакет ранжирования тянет транспорт: %v", w.findings)
	}
	if len(w.unseen) > 0 {
		t.Fatalf("не выполнилось: замыкание через чужой модуль не прочитано: %v", w.unseen)
	}
}

// TestTransportClosureSeesGRPCInTheServerPackage — положительный контроль на
// НАСТОЯЩЕМ входе: тот же обход по пакету gRPC-сервера, прежнему дому таблицы,
// находит транспорт. Иначе молчание пробы выше неотличимо от слепоты обхода.
func TestTransportClosureSeesGRPCInTheServerPackage(t *testing.T) {
	root := liveModuleRoot(t)
	w, err := walkImportClosure(root, corelibModule, corelibModule+"/grpcsrv")
	if err != nil {
		t.Fatalf("не выполнилось: %v", err)
	}
	t.Logf("перепись: пакетов модуля %d · файлов исходника %d · импортов транспорта %d",
		len(w.packages), w.files, len(w.findings))
	if len(w.findings) == 0 {
		t.Fatal("обход не увидел транспорта в пакете gRPC-сервера — он слеп, и молчание по пакету ранжирования ничего не значит")
	}
}

// synthModulePath — путь синтетического модуля проб обхода.
const synthModulePath = "example.test/m"

// synthModule — синтетический модуль в t.TempDir(): путь файла → содержимое.
func synthModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module " + synthModulePath + "\n\ngo 1.26\n"
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func mustWalk(t *testing.T, root, start string) closureWalk {
	t.Helper()
	w, err := walkImportClosure(root, synthModulePath, start)
	if err != nil {
		t.Fatalf("обход отказал: %v", err)
	}
	return w
}

func requireStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\n  получено %q\n  ожидалось %q", what, got, want)
	}
}

func TestTransportClosureFindsADirectImport(t *testing.T) {
	root := synthModule(t, map[string]string{
		"a/a.go": "package a\n\nimport _ \"google.golang.org/grpc\"\n",
	})
	w := mustWalk(t, root, synthModulePath+"/a")
	requireStrings(t, "находки", w.findings, []string{"a/a.go:3: google.golang.org/grpc"})
}

// Транзитивная инъекция и её законный близнец различаются РОВНО одним фактом:
// импортом пакета b.
func TestTransportClosureFindsATransitiveImport(t *testing.T) {
	root := synthModule(t, map[string]string{
		"a/a.go": "package a\n\nimport (\n\t_ \"time\"\n\t_ \"example.test/m/b\"\n)\n",
		"b/b.go": "package b\n\nimport _ \"google.golang.org/grpc/metadata\"\n",
	})
	w := mustWalk(t, root, synthModulePath+"/a")
	requireStrings(t, "находки", w.findings, []string{"b/b.go:3: google.golang.org/grpc/metadata"})
	requireStrings(t, "пакеты", w.packages, []string{"example.test/m/a", "example.test/m/b"})
}

func TestTransportClosureLawfulTwinIsSilent(t *testing.T) {
	root := synthModule(t, map[string]string{
		"a/a.go": "package a\n\nimport (\n\t_ \"time\"\n\t_ \"example.test/m/b\"\n)\n",
		"b/b.go": "package b\n\nimport _ \"strings\"\n",
	})
	w := mustWalk(t, root, synthModulePath+"/a")
	requireStrings(t, "находки", w.findings, nil)
	requireStrings(t, "непрочитанное", w.unseen, nil)
	requireStrings(t, "пакеты", w.packages, []string{"example.test/m/a", "example.test/m/b"})
	if w.files != 2 {
		t.Fatalf("файлов прочитано %d, ожидалось 2", w.files)
	}
}

// Импорт пробы до импортёра пакета не доезжает — и не судится.
func TestTransportClosureDoesNotReadProbes(t *testing.T) {
	root := synthModule(t, map[string]string{
		"a/a.go":      "package a\n\nimport _ \"time\"\n",
		"a/a_test.go": "package a\n\nimport _ \"google.golang.org/grpc\"\n",
	})
	w := mustWalk(t, root, synthModulePath+"/a")
	requireStrings(t, "находки", w.findings, nil)
	if w.files != 1 {
		t.Fatalf("файлов прочитано %d, ожидалось 1 (проба не читается)", w.files)
	}
}

// Файл под ограничением сборки выпал бы из графа сборки — обход его читает.
func TestTransportClosureReadsBuildConstrainedFiles(t *testing.T) {
	root := synthModule(t, map[string]string{
		"a/a.go":     "package a\n",
		"a/extra.go": "//go:build integration\n\npackage a\n\nimport _ \"google.golang.org/grpc\"\n",
	})
	w := mustWalk(t, root, synthModulePath+"/a")
	requireStrings(t, "находки", w.findings, []string{"a/extra.go:5: google.golang.org/grpc"})
}

// Чужой модуль вне транспорта — отказ судить, а не молчание.
func TestTransportClosureRefusesAForeignModuleItCannotRead(t *testing.T) {
	root := synthModule(t, map[string]string{
		"a/a.go": "package a\n\nimport _ \"github.com/other/lib\"\n",
	})
	w := mustWalk(t, root, synthModulePath+"/a")
	requireStrings(t, "находки", w.findings, nil)
	requireStrings(t, "непрочитанное", w.unseen, []string{"example.test/m/a → github.com/other/lib"})
}

// Пакет без исходников — отказ обхода, а не «находок ноль».
func TestTransportClosureRefusesAPackageWithoutSources(t *testing.T) {
	root := synthModule(t, map[string]string{
		"a/a_test.go": "package a\n",
	})
	_, err := walkImportClosure(root, synthModulePath, synthModulePath+"/a")
	if err == nil || !strings.Contains(err.Error(), "исходников нет") {
		t.Fatalf("ожидался отказ «исходников нет», получено %v", err)
	}
}
