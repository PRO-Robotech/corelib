// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// provider_gate_test.go — сценарий F4d-03 приёмки Ф4д: перечень законных
// значений посадки личности ВЫВОДИТСЯ из типа и объявлен ровно один раз на всё
// дерево.
//
// ПОЧЕМУ ГЕЙТ ЗДЕСЬ. Словарь читают два процесса — служба прав и край. Второе
// перечисление у одного из них разошлось бы с первым на первом же новом
// значении, и разошлось бы молча: обе стороны компилируются, обе выглядят
// исправными, и различает их только значение, которого пока нет.
//
// ПРЕДИКАТ И ЕГО ГРАНИЦА, НАЗВАННАЯ ВСЛУХ. Гейт судит РАЗОБРАННЫЙ исходник, а
// не текст: канонические имена стоят и в комментариях, и в текстах отказов, и
// проверка по подстроке краснела бы на собственном объяснении. Обход сужен до
// файлов, ЗНАЮЩИХ о посадке, — тех, что импортируют этот пакет либо называют
// поле его каноническим именем. Сужение обязательно: `own` и `external` —
// обычные английские слова, и обход всего дерева по ним считал бы находкой
// каждое поле «владелец» и каждый «внешний адрес».
//
// Чего гейт НЕ даёт: он не поймает третьего перечисления в файле, который о
// посадке нигде не упоминает и этот пакет не импортирует. Такой файл значений
// поля и не разбирает — разбирать их нечем; свойство держится тем, что разбор
// один, и обзором.
package identityposture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/identityposture"
)

// repoRoot — корень дерева относительно каталога этого пакета.
const repoRoot = "../.."

// Канонические имена значений обязаны стоять строковыми литералами РОВНО в
// одном объявлении на всё дерево.
func TestF4d03_ValueNamesAreDeclaredOnceInTheWholeTree(t *testing.T) {
	names := identityposture.Names()
	if len(names) == 0 {
		t.Fatal("словарь пуст — обходить нечего, и гейт судил бы о непрочитанном")
	}

	files, aware := postureAwareFiles(t, repoRoot)
	if files == 0 {
		t.Fatal("обход пуст: непроверочных файлов Go не найдено")
	}
	if len(aware) == 0 {
		t.Fatal("ни одного файла, знающего о посадке, — предикат перестал опознавать свой предмет")
	}

	found := CountCanonicalNames(t, aware, names)

	t.Logf("перепись: непроверочных файлов Go осмотрено %d; знающих о посадке %d; канонических имён %d (%s)",
		files, len(aware), len(names), strings.Join(names, ", "))

	for _, name := range names {
		places := found[name]
		sort.Strings(places)
		switch len(places) {
		case 0:
			t.Errorf("каноническое имя %q не найдено ни в одном литерале — словарь не выводится из типа", name)
		case 1:
			t.Logf("  %q объявлено один раз: %s", name, places[0])
		default:
			t.Errorf("каноническое имя %q объявлено %d раз (%s) — перечень обязан быть один: "+
				"второй разойдётся с первым на первом же новом значении, и разойдётся молча",
				name, len(places), strings.Join(places, ", "))
		}
	}
}

// Законный близнец: файл, о посадке не знающий, перечисляет слово `own` в
// своём смысле — гейт молчит. Без этой половины он краснел бы на каждом поле
// «владелец».
func TestF4d03_AFileThatDoesNotKnowAboutThePostureIsNotAFinding(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "unrelated.go", `package other
// Соседний смысл того же слова: перечень видов владения ресурсом.
var ownership = []string{"own", "shared", "external"}
`, parser.ParseComments)
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}
	if isPostureAware(f, "unrelated.go", `package other
var ownership = []string{"own", "shared", "external"}
`) {
		t.Fatal("файл, не знающий о посадке, опознан как знающий — гейт краснел бы на чужом словаре")
	}
}

// Дефект: ВТОРОЕ перечисление в файле, который о посадке знает. Обязано
// находиться.
func TestF4d03Injection_ASecondEnumerationIsFound(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for name, src := range map[string]string{
		"declaring.go": `package identityposture
var providerNames = []struct{ name string }{{"external"}, {"own"}}
`,
		"elsewhere.go": `package edge
// Знает о посадке: называет поле по имени identity-provider.
var legal = []string{"external", "own"}
`,
	} {
		f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("фикстура %s не разобрана: %v", name, err)
		}
		files[name] = f
	}
	found := CountCanonicalNames(t, files, identityposture.Names())
	for _, name := range identityposture.Names() {
		if len(found[name]) < 2 {
			t.Fatalf("второе объявление имени %q не найдено — гейт не способен упасть", name)
		}
	}
}

// Законный близнец второго рода: ПРОЗА, называющая оба значения в комментарии.
// Комментарий литералом не является — иначе гейт краснел бы на шапке, которая
// его же и объясняет.
func TestF4d03Injection_ProseNamingBothValuesIsSilent(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "doc.go", `package edge
// Поле identity-provider принимает external либо own; ниже — только разбор.
func nothing() {}
`, parser.ParseComments)
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}
	found := CountCanonicalNames(t, map[string]*ast.File{"doc.go": f}, identityposture.Names())
	for _, name := range identityposture.Names() {
		if len(found[name]) != 0 {
			t.Fatalf("проза объявлена находкой: %q найдено %d раз", name, len(found[name]))
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Тело гейта. Вынесено, чтобы инъекция звала ТО ЖЕ, что исполняется на дереве.

// CountCanonicalNames — где в файлах объявлены канонические имена значений.
func CountCanonicalNames(t *testing.T, files map[string]*ast.File, names []string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	for file, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, name := range names {
				if v == name {
					found[name] = append(found[name], file)
				}
			}
			return true
		})
	}
	return found
}

// postureAwareFiles обходит дерево и возвращает число осмотренных файлов и те
// из них, что о посадке ЗНАЮТ.
func postureAwareFiles(t *testing.T, root string) (int, map[string]*ast.File) {
	t.Helper()
	total := 0
	aware := map[string]*ast.File{}
	fset := token.NewFileSet()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		total++
		f, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
		if perr != nil {
			return nil
		}
		if isPostureAware(f, path, string(src)) {
			rel, _ := filepath.Rel(root, path)
			aware[rel] = f
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход дерева не выполнен: %v", err)
	}
	return total, aware
}

// isPostureAware — знает ли файл о посадке личности: импортирует этот пакет
// либо называет поле его каноническим именем.
func isPostureAware(f *ast.File, path, src string) bool {
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		if p, err := strconv.Unquote(imp.Path.Value); err == nil &&
			strings.HasSuffix(p, "pkg/identityposture") {
			return true
		}
	}
	if strings.HasSuffix(path, filepath.Join("pkg", "identityposture", "provider.go")) {
		return true
	}
	return strings.Contains(src, identityposture.FieldName)
}
