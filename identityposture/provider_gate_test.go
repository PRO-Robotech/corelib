// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// provider_gate_test.go — сценарий F4d-03 приёмки Ф4д: перечень законных
// значений посадки личности ВЫВОДИТСЯ из типа и объявлен ровно один раз на всё
// дерево, а снятое со словаря имя не объявлено ни разу.
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
// поле его каноническим именем. Сужение обязательно: `own` — имя словаря — и
// `external` — снятое имя, которое гейт считает тоже, — обычные английские
// слова, и обход всего дерева по ним считал бы находкой каждое поле «владелец»
// и каждый «внешний адрес».
//
// Литерал судится в трёх формах: точный; в другом регистре (сверка без учёта
// регистра с `External` принимает снятое значение так же, как точная с
// `external`); собранный сложением литералов (`"ex" + "ternal"`). Форма и
// порядок разбора — у CountCanonicalNames.
//
// СНЯТОЕ ИМЯ СЧИТАЕТСЯ ТОЖЕ, и законное число у него — ноль. Посадка
// `external` снята со словаря (corelib#30), и Names() её больше не производит.
// Гейт, считающий только выведенные имена, вернувшейся строки словаря не видит:
// имя снова попадает в Names(), находится один раз и проходит законным. Так и
// было с прежней редакцией: строка `{External, "external"}`, возвращённая в
// provider.go, оставляла её зелёной с «канонических имён 2 (external, own)».
// Поэтому снятые имена выписаны перечнем withdrawnNames, и у каждого две
// находки: словарь снова его производит; его литерал — в любой из трёх форм —
// стоит в файле, знающем о посадке: вернувшаяся строка словаря либо разбор
// значения мимо Parse, сверяющий с этим литералом.
//
// ОБЛАСТЬ ГЕЙТА — ЭТОТ МОДУЛЬ, и она названа числом. Обход идёт от корня
// модуля (каталог с go.mod), а не выше: вердикт, снятый за его пределами, есть
// свойство того, ЧТО ЛЕЖИТ РЯДОМ с рабочей копией, а не свойство коммита.
// Замер 2026-09-16 из рабочей копии под `kacho-workspace/tmp`: корень `"../.."`
// дал бы обход 619 413 непроверочных файлов Go и 187 объявлений словаря вместо
// одного — 186 чужих копий этого же пакета. Корень модуля даёт 181 файл.
// Держит это `walk_root_gate_test.go`.
//
// Чего гейт НЕ даёт — три границы, все названы:
//
//   - он не поймает третьего перечисления в файле, который о посадке нигде не
//     упоминает и этот пакет не импортирует. Такой файл значений поля и не
//     разбирает — разбирать их нечем; свойство держится тем, что разбор один,
//     и обзором;
//   - в файле, ЗНАЮЩЕМ о посадке, он не видит разбора мимо Parse, который
//     снятого имени константой из литералов не несёт: строки, вычисленной во
//     время исполнения или собранной из идентификаторов, сравнения по части
//     строки (префикс, регулярное выражение) и значения, записанного в поле
//     ЧИСЛОМ, — преобразованием типа или декодером настройки, кладущим число
//     прямо. Последнее строки не несёт вовсе, и держать его может не гейт, а
//     только проверка старта, зовущая Provider.Validate: метод отвергает любое
//     число вне словаря, и это закреплено поведением в
//     withdrawn_external_test.go. Своей проверки старта у модуля нет, а у
//     потребителей она этого метода пока не зовёт — ниже. Молчание гейта на
//     вычисленной строке закреплено здесь же пробой
//     TestF4d03_ARuntimeComputedNameIsTheNamedBoundary;
//   - он не видит ПОТРЕБИТЕЛЕЙ. Словарь читают два процесса — служба прав и
//     край, — и после выноса фундамента отдельным модулем оба живут в ДРУГИХ
//     репозиториях: обход модуля до них не доходит by construction, а внутри
//     модуля он находит одно объявление, и перепись гейта печатает, сколько
//     файлов о посадке знает.
//
// СТОРОНА ПОТРЕБИТЕЛЕЙ ДЕРЖИТСЯ ОБЗОРОМ — это решение, механизма у неё нет.
// Решено 2026-09-25 исполнителем задачи PRO-Robotech/corelib#15 по поручению
// владельца решать самому; гейта того же класса в деревьях потребителей не
// заводится. Основание — замер тем же распознавателем и тем же счётом
// литералов, что ниже, приложенными к деревьям потребителей: непроверочных
// файлов, знающих о посадке, у kacho main@1d42a6728b — 8, 2564@d68ef02a00 —
// 6; у kaname main@cbbac984b7 — 10, 357@fc9f5aff19 — 11. Счёт литералов
// перемерен на тех же ревизиях после того, как распознаватель стал читать три
// формы (corelib#29): литералов `own` и `external` — точных, в другом
// регистре, собранных сложением — в этих файлах 0. Оба имени даны замеру
// явно, поэтому он покрывает и нынешнее имя, и снятое. Оба потребителя
// называют словарь только выведенным: константами пакета (служба прав — их
// псевдонимами), Parse, Names, Values, NotDeclared. Положительный контроль
// того же замера — этот модуль на ревизии до #30 (227ed2b) — нашёл оба
// тогдашних имени в provider.go; на нынешней ревизии гейт находит там `own`
// один раз, а литерала снятого имени — ни одного.
//
// Замер литералов говорит о ТЕКСТЕ и молчит о ПУТИ значения: проходит ли
// значение поля у потребителя через Parse, он не показывает — это вторая
// граница выше. Механизм для этого пути фундамент даёт — Provider.Validate
// (corelib#29), — но у потребителей он пока не стоит. Перевод проверок старта
// службы прав и края на Validate ведут задачи PRO-Robotech/kaname#424 и
// PRO-Robotech/kacho#2862; пока они открыты, потребители Validate при старте не
// зовут и судят только IsSet, и путь значения у них не держит ни гейт, ни
// проверка старта. Поэтому он стоит в условии пересмотра рядом с литералом.
//
// Решение пересматривается, как только в непроверочном файле потребителя,
// знающем о посадке, появится литерал имени словаря или снятого имени в любой
// из трёх форм либо разбор значения мимо Parse: тогда гейт того же класса
// заводится в ЕГО дереве.
package identityposture_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/identityposture"
)

// repoRoot — корень МОДУЛЯ относительно каталога этого пакета. Пробы Go
// исполняются с рабочим каталогом пакета, поэтому `..` — корень модуля, а
// `../..` — то, что лежит СНАРУЖИ него. Сверяет `walk_root_gate_test.go`.
const repoRoot = ".."

// withdrawnNames — канонические имена, СНЯТЫЕ со словаря. Тип их не печатает и
// Names() их не производит, поэтому вывести их неоткуда: перечень выписан здесь,
// в одном месте, и снимается вместе с устаревшей константой — то есть только
// мажорным выпуском модуля. Литерал в проверочном файле обходу не виден: обход
// читает только непроверочные.
var withdrawnNames = []string{"external"} // corelib#30

// Канонические имена значений обязаны стоять строковыми литералами РОВНО в
// одном объявлении на всё дерево, а снятые — ни в одном.
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
	// Предпосылка названа СОДЕРЖИМЫМ, а не числом: внутри фундамента знающий
	// файл ровно один — само объявление, — поэтому порог `len(aware) > 0` стоит
	// на границе беспредметности и сполз бы к нулю незамеченным. Требуем
	// присутствия объявляющего пакета поимённо.
	if _, ok := aware[filepath.Join(declaringPackageDir(t), "provider.go")]; !ok {
		seen := make([]string, 0, len(aware))
		for k := range aware {
			seen = append(seen, k)
		}
		sort.Strings(seen)
		t.Fatalf("объявляющий файл не опознан знающим о посадке (осмотрено %d, знают %d: %s) — "+
			"обход читает не то дерево либо распознаватель перестал называть свой предмет",
			files, len(aware), strings.Join(seen, ", "))
	}

	v := judgeDictionary(t, aware, names, withdrawnNames)

	t.Logf("перепись: непроверочных файлов Go осмотрено %d; знающих о посадке %d; "+
		"канонических имён %d (%s); снятых имён %d (%s); форм литерала 3 "+
		"(точный, другой регистр, сложение литералов)",
		files, len(aware), len(names), strings.Join(names, ", "),
		len(withdrawnNames), strings.Join(withdrawnNames, ", "))
	for _, name := range names {
		if places := v.declared[name]; len(places) == 1 {
			t.Logf("  %q объявлено один раз: %s", name, places[0])
		}
	}
	for _, name := range withdrawnNames {
		if len(v.withdrawn[name]) == 0 {
			t.Logf("  снятое %q не объявлено ни разу", name)
		}
	}
	for _, f := range v.findings {
		t.Error(f)
	}
}

// Законный близнец: файл, о посадке не знающий, перечисляет слова `own` и
// `external` в своём смысле — гейт молчит. Без этой половины он краснел бы на
// каждом поле «владелец» и каждом «внешнем адресе».
func TestF4d03_AFileThatDoesNotKnowAboutThePostureIsNotAFinding(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "unrelated.go", `package other
// Соседний смысл того же слова: перечень видов владения ресурсом.
var ownership = []string{"own", "shared", "external"}
`, parser.ParseComments)
	if err != nil {
		t.Fatalf("фикстура не разобрана: %v", err)
	}
	if isPostureAware(f, `package other
var ownership = []string{"own", "shared", "external"}
`, selfImportPath(t)) {
		t.Fatal("файл, не знающий о посадке, опознан как знающий — гейт краснел бы на чужом словаре")
	}
}

// Дефект: ВТОРОЕ перечисление в файле, который о посадке знает. Обязано
// находиться, и находка называет имя и оба места.
func TestF4d03Injection_ASecondEnumerationIsFound(t *testing.T) {
	names := identityposture.Names()
	v := judgeDictionary(t, parseFixtures(t, map[string]string{
		"declaring.go": declaringFixture(names),
		"elsewhere.go": "package edge\n// Знает о посадке: называет поле по имени identity-provider.\n" +
			"var legal = []string{" + strings.Join(quoteAll(names), ", ") + "}\n",
	}), names, withdrawnNames)
	for _, name := range names {
		mustFind(t, v.findings, fmt.Sprintf("каноническое имя %q объявлено 2 раз (declaring.go, elsewhere.go)", name))
	}
	if len(v.findings) != len(names) {
		t.Errorf("находок %d при %d именах: %q", len(v.findings), len(names), v.findings)
	}
}

// Снятое имя — в обе стороны. Законный близнец — словарь, каким его производит
// тип: находок нет. Каждый дефект отличается от него одним фактом: вернувшейся
// строкой словаря либо одним лишним файлом, разбирающим снятое значение сам.
func TestF4d03Injection_AWithdrawnNameIsFoundAndTheLawfulDictionaryIsSilent(t *testing.T) {
	names := identityposture.Names()

	lawful := judgeDictionary(t, parseFixtures(t, map[string]string{
		"declaring.go": declaringFixture(names),
	}), names, withdrawnNames)
	if len(lawful.findings) != 0 {
		t.Fatalf("законный словарь объявлен находкой: %q — отказ ниже ничего бы не значил", lawful.findings)
	}

	for _, w := range withdrawnNames {
		// Вернувшаяся строка меняет и объявление, и то, что словарь производит:
		// это одна правка в provider.go, и обе её стороны моделируются вместе.
		t.Run("строка "+w+" вернулась в словарь", func(t *testing.T) {
			returned := append(slices.Clone(names), w)
			v := judgeDictionary(t, parseFixtures(t, map[string]string{
				"declaring.go": declaringFixture(returned),
			}), returned, withdrawnNames)
			mustFind(t, v.findings, fmt.Sprintf("снятое имя %q снова производится словарём", w))
			mustFind(t, v.findings, fmt.Sprintf("снятое имя %q стоит литералом 1 раз (declaring.go)", w))
		})
		// Разбор мимо Parse — в каждой форме, которую распознаватель читает.
		// Законный близнец каждой формы — та же строка кода с посторонним словом
		// вместо снятого имени: молчит. Дефект отличается от него одним фактом.
		for _, form := range bypassForms(w) {
			t.Run("снятое "+w+" разобрано мимо Parse: "+form.name, func(t *testing.T) {
				twin := judgeDictionary(t, parseFixtures(t, map[string]string{
					"declaring.go": declaringFixture(names),
					"elsewhere.go": bypassFixture(form.twin),
				}), names, withdrawnNames)
				if len(twin.findings) != 0 {
					t.Fatalf("законный близнец формы «%s» объявлен находкой: %q — отказ ниже ничего бы не значил",
						form.name, twin.findings)
				}
				v := judgeDictionary(t, parseFixtures(t, map[string]string{
					"declaring.go": declaringFixture(names),
					"elsewhere.go": bypassFixture(form.defect),
				}), names, withdrawnNames)
				mustFind(t, v.findings, fmt.Sprintf("снятое имя %q стоит литералом 1 раз (elsewhere.go)", w))
				if len(v.findings) != 1 {
					t.Errorf("находок %d, want 1: %q", len(v.findings), v.findings)
				}
			})
		}
	}
}

// Второе перечисление имени словаря в ДРУГОМ регистре — тоже второе
// перечисление: сверка без учёта регистра принимает `own` так же, как точная.
func TestF4d03Injection_ASecondEnumerationInAnotherCaseIsFound(t *testing.T) {
	names := identityposture.Names()
	upper := make([]string, 0, len(names))
	for _, n := range names {
		upper = append(upper, strings.ToUpper(n))
	}
	v := judgeDictionary(t, parseFixtures(t, map[string]string{
		"declaring.go": declaringFixture(names),
		"elsewhere.go": "package edge\n// Знает о посадке: называет поле по имени identity-provider.\n" +
			"var legal = []string{" + strings.Join(quoteAll(upper), ", ") + "}\n",
	}), names, withdrawnNames)
	for _, name := range names {
		mustFind(t, v.findings, fmt.Sprintf("каноническое имя %q объявлено 2 раз (declaring.go, elsewhere.go)", name))
	}
	if len(v.findings) != len(names) {
		t.Errorf("находок %d при %d именах: %q", len(v.findings), len(names), v.findings)
	}
}

// Граница, названная в шапке, — в обе стороны. Строка, вычисленная во время
// исполнения, литерала снятого имени не несёт, и гейт её НЕ видит: эта проба
// утверждает молчание, чтобы шапка, называющая границу, не разошлась с тем,
// что гейт делает. Литерал внутри того же вычисления гейт по-прежнему видит.
func TestF4d03_ARuntimeComputedNameIsTheNamedBoundary(t *testing.T) {
	names := identityposture.Names()
	for _, w := range withdrawnNames {
		half := len(w) / 2
		unseen := judgeDictionary(t, parseFixtures(t, map[string]string{
			"declaring.go": declaringFixture(names),
			"elsewhere.go": bypassFixture(`strings.Join([]string{` + strconv.Quote(w[:half]) + `, ` +
				strconv.Quote(w[half:]) + `}, "") == s`),
		}), names, withdrawnNames)
		if len(unseen.findings) != 0 {
			t.Errorf("строка, собранная во время исполнения, найдена: %q — гейт видит больше, чем говорит "+
				"его шапка; поправь шапку вместе с распознавателем", unseen.findings)
		}
		seen := judgeDictionary(t, parseFixtures(t, map[string]string{
			"declaring.go": declaringFixture(names),
			"elsewhere.go": bypassFixture(`strings.Join([]string{` + strconv.Quote(w) + `}, "") == s`),
		}), names, withdrawnNames)
		mustFind(t, seen.findings, fmt.Sprintf("снятое имя %q стоит литералом 1 раз (elsewhere.go)", w))
	}
}

// bypassForm — одна форма разбора мимо Parse: дефект со снятым именем и
// законный близнец той же формы с посторонним словом.
type bypassForm struct{ name, defect, twin string }

// bypassForms — формы, которые распознаватель обязан читать: точный литерал,
// литерал в другом регистре под сверкой без учёта регистра, строка, собранная
// сложением литералов.
func bypassForms(w string) []bypassForm {
	const other = "shared"
	title := func(s string) string { return strings.ToUpper(s[:1]) + s[1:] }
	half := len(w) / 2
	return []bypassForm{
		{"точный литерал", `s == ` + strconv.Quote(w), `s == ` + strconv.Quote(other)},
		{"другой регистр", `strings.EqualFold(s, ` + strconv.Quote(title(w)) + `)`,
			`strings.EqualFold(s, ` + strconv.Quote(title(other)) + `)`},
		{"сложение литералов", `s == ` + strconv.Quote(w[:half]) + ` + ` + strconv.Quote(w[half:]),
			`s == ` + strconv.Quote(other[:3]) + ` + ` + strconv.Quote(other[3:])},
	}
}

// bypassFixture — файл, знающий о посадке, с одним сравнением cond.
func bypassFixture(cond string) string {
	return "package edge\n\nimport \"strings\"\n\n// Знает о посадке: называет поле по имени identity-provider.\n" +
		"var _ = strings.EqualFold\n\nfunc legacy(s string) bool { return " + cond + " }\n"
}

// Законный близнец второго рода: ПРОЗА, называющая и значение словаря, и
// снятое имя. Комментарий литералом не является — иначе гейт краснел бы на
// шапке, которая его же и объясняет.
func TestF4d03Injection_ProseNamingTheValueAndTheWithdrawnNameIsSilent(t *testing.T) {
	names := identityposture.Names()
	v := judgeDictionary(t, parseFixtures(t, map[string]string{
		"declaring.go": declaringFixture(names),
		"doc.go": "package edge\n// Поле identity-provider принимает " + strings.Join(names, " либо ") +
			"; снятое " + strings.Join(withdrawnNames, ", ") + " — только проза.\nfunc nothing() {}\n",
	}), names, withdrawnNames)
	if len(v.findings) != 0 {
		t.Fatalf("проза объявлена находкой: %q", v.findings)
	}
}

// parseFixtures — синтетические файлы; путь им дан именем, обхода нет.
func parseFixtures(t *testing.T, srcs map[string]string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := make(map[string]*ast.File, len(srcs))
	for name, src := range srcs {
		f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("фикстура %s не разобрана: %v", name, err)
		}
		files[name] = f
	}
	return files
}

// declaringFixture — синтетическое объявление словаря с данными строками.
func declaringFixture(names []string) string {
	rows := make([]string, 0, len(names))
	for _, n := range quoteAll(names) {
		rows = append(rows, "{"+n+"}")
	}
	return "package identityposture\nvar providerNames = []struct{ name string }{" + strings.Join(rows, ", ") + "}\n"
}

func quoteAll(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strconv.Quote(n))
	}
	return out
}

// mustFind — находка с данным началом есть. Сверяется ТЕКСТ, а не число
// находок: покраснеть по чужой причине гейт мог бы и без своей.
func mustFind(t *testing.T, findings []string, prefix string) {
	t.Helper()
	for _, f := range findings {
		if strings.HasPrefix(f, prefix) {
			return
		}
	}
	t.Errorf("находки «%s…» нет; находки: %q — гейт не способен упасть на этом дефекте", prefix, findings)
}

// ─────────────────────────────────────────────────────────────────────────────
// Тело гейта. Вынесено, чтобы инъекция звала ТО ЖЕ, что исполняется на дереве.

// dictionaryVerdict — вердикт гейта по разобранным файлам.
type dictionaryVerdict struct {
	declared  map[string][]string // имя словаря → файлы, где стоит его литерал
	withdrawn map[string][]string // снятое имя → файлы, где стоит его литерал
	findings  []string
}

// judgeDictionary — каждое имя словаря объявлено литералом ровно один раз;
// снятое имя словарём не производится и литералом не стоит ни в одном файле.
func judgeDictionary(t *testing.T, files map[string]*ast.File, names, withdrawn []string) dictionaryVerdict {
	t.Helper()
	v := dictionaryVerdict{
		declared:  CountCanonicalNames(t, files, names),
		withdrawn: CountCanonicalNames(t, files, withdrawn),
	}
	for _, name := range names {
		places := v.declared[name]
		sort.Strings(places)
		switch len(places) {
		case 0:
			v.findings = append(v.findings, fmt.Sprintf(
				"каноническое имя %q не найдено ни в одном литерале — словарь не выводится из типа", name))
		case 1:
		default:
			v.findings = append(v.findings, fmt.Sprintf(
				"каноническое имя %q объявлено %d раз (%s) — перечень обязан быть один: "+
					"второй разойдётся с первым на первом же новом значении, и разойдётся молча",
				name, len(places), strings.Join(places, ", ")))
		}
	}
	for _, name := range withdrawn {
		if slices.Contains(names, name) {
			v.findings = append(v.findings, fmt.Sprintf(
				"снятое имя %q снова производится словарём — строка снятой посадки вернулась "+
					"в providerNames (corelib#30)", name))
		}
		if places := v.withdrawn[name]; len(places) > 0 {
			sort.Strings(places)
			v.findings = append(v.findings, fmt.Sprintf(
				"снятое имя %q стоит литералом %d раз (%s) — точным, в другом регистре либо сложением "+
					"литералов: снятая посадка вернулась в словарь либо её значение разбирается мимо Parse "+
					"(corelib#30)",
				name, len(places), strings.Join(places, ", ")))
		}
	}
	return v
}

// CountCanonicalNames — где в файлах объявлены канонические имена значений.
//
// Имя считается объявленным строковой константой, собранной из ЛИТЕРАЛОВ:
// одним литералом либо их сложением (скобки допустимы). Сверка идёт без учёта
// регистра: сравнение `strings.EqualFold(s, "External")` принимает снятое
// значение так же, как точный литерал, и читается тем же предметом. Сложение
// сперва судится целиком — `"ex" + "ternal"` есть одно объявление снятого
// имени, — а если целое не совпало ни с одним именем, судится каждый его
// литерал, как у одиночного литерала: `"own" + "," + "external"` — два
// объявления, а не ноль.
//
// Чего разбор не читает, названо в шапке файла третьей границей: строка,
// вычисленная во время исполнения или собранная из идентификаторов, сравнение
// по части строки и значение, записанное в поле числом.
func CountCanonicalNames(t *testing.T, files map[string]*ast.File, names []string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	record := func(file, v string) bool {
		hit := false
		for _, name := range names {
			if strings.EqualFold(v, name) {
				found[name] = append(found[name], file)
				hit = true
			}
		}
		return hit
	}
	for file, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			leaves, whole, ok := literalString(e)
			if !ok {
				return true
			}
			if !record(file, whole) {
				for _, leaf := range leaves {
					record(file, leaf)
				}
			}
			return false
		})
	}
	return found
}

// literalString — значение строковой константы, собранной из литералов
// сложением, и значения её литералов по порядку. Любой иной узел — не такая
// константа (ok == false), и обход спускается в него за литералами.
func literalString(e ast.Expr) (leaves []string, whole string, ok bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return nil, "", false
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			return nil, "", false
		}
		return []string{v}, v, true
	case *ast.ParenExpr:
		return literalString(x.X)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return nil, "", false
		}
		l, lw, lok := literalString(x.X)
		if !lok {
			return nil, "", false
		}
		r, rw, rok := literalString(x.Y)
		if !rok {
			return nil, "", false
		}
		return append(l, r...), lw + rw, true
	}
	return nil, "", false
}

// postureAwareFiles обходит дерево и возвращает число осмотренных файлов и те
// из них, что о посадке ЗНАЮТ.
func postureAwareFiles(t *testing.T, root string) (int, map[string]*ast.File) {
	t.Helper()
	total := 0
	aware := map[string]*ast.File{}
	fset := token.NewFileSet()
	self := selfImportPath(t)
	declDir := declaringPackageDir(t)

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
		rel, rerr2 := filepath.Rel(root, path)
		if rerr2 != nil {
			return nil
		}
		// Принадлежность объявляющему пакету судится по ПУТИ ОБХОДА, а не внутри
		// распознавателя содержимого: у синтетической фикстуры пути нет, и общая
		// ветвь объявила бы знающим каждого законного близнеца.
		if filepath.Dir(rel) == declDir || isPostureAware(f, string(src), self) {
			aware[rel] = f
		}
		return nil
	})
	if err != nil {
		t.Fatalf("обход дерева не выполнен: %v", err)
	}
	return total, aware
}

// isPostureAware — знает ли СОДЕРЖИМОЕ файла о посадке личности: импортирует
// этот пакет либо называет поле его каноническим именем.
//
// Путь импорта ПЕРЕДАЁТСЯ, а не выписывается здесь, и сверяется ТОЧНО. Прежняя
// редакция искала суффикс `pkg/identityposture` — раскладку платформы, из
// которой пакет переехал в фундамент; в этом модуле такого пути нет ни у одного
// файла, и ветвь была мёртвой. Суффиксное сравнение вдобавок опознало бы
// `.../internal/pkg/identityposture` чужого модуля.
func isPostureAware(f *ast.File, src, selfImport string) bool {
	if selfImport != "" {
		for _, imp := range f.Imports {
			if imp.Path == nil {
				continue
			}
			if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == selfImport {
				return true
			}
		}
	}
	return strings.Contains(src, identityposture.FieldName)
}
