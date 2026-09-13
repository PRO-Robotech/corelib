// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// deferral.go — разбор отложенной работы: маркер, обещающий сделать позже.
//
// Почему разбор живёт в фундаменте, а виды вычитания — у потребителя, сказано в
// шапке пакета; здесь это не пересказывается.

package treehygiene

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PRO-Robotech/corelib/treecorpus"
)

// DeferralForm — одна форма отсрочки: затравка для дешёвого отсева и точный
// образец для решения.
//
// Затравка и образец лежат в ОДНОЙ записи намеренно. Отсев по подстроке («есть
// ли в файле хоть одна затравка») экономит порядок величины: образец с
// альтернацией по всему дереву стоил в монорепо 155 секунд из 438 у пакета
// проверок, то есть треть его бюджета и, в конечном счёте, предел времени
// конвейера. Но отсев, живущий ОТДЕЛЬНЫМ списком, — второе место об одном
// предмете: добавят форму в образец, забудут в затравки, и разбор молча
// перестанет её видеть. Здесь такое расхождение невозможно by construction — и
// предикат, и отсев выводятся из одного перечня.
type DeferralForm struct {
	// Seed — подстрока, обязательно присутствующая в любом совпадении.
	Seed string
	// Pattern — образец, решающий по существу.
	Pattern string
	// Example — строка, которую эта форма ОБЯЗАНА ловить. Проверяется пробой
	// СКВОЗНЫМ путём (через отсев), а не образцом в отрыве от сита.
	Example string
}

// deferralForms — формы, которыми в этих деревьях пишется «сделаю позже».
//
// Слова русского языка включены наравне с англоязычными: корпус двуязычен, и
// запрет, знающий только англоязычный маркер, обходится одним русским наречием
// без единой уловки — то есть был бы запретом НАПИСАНИЯ, а не отсрочки.
//
// Отсрочка отличается от УПОМИНАНИЯ тем, что автор её ДАЁТ, а не обсуждает.
// Англоязычная форма несёт этот признак в себе — форму обращения к читателю
// кода (имя маркера с двоеточием либо с тикетом в скобках перед ним); голое
// слово внутри предложения под неё не подпадает. У русских форм такого знака
// препинания в языке НЕТ, поэтому та же граница проводится с другой стороны:
// совпадение, целиком лежащее внутри кавычек, — цитата, а не обещание
// (см. deferralMentionQuotes).
//
// Различать обязательно: запрет, ловящий слово, заставляет переписывать
// собственную документацию о запрете — и первым делом снимают сам запрет.
//
// Порядок слов в русском свободен, поэтому перестановка объявляется ОТДЕЛЬНОЙ
// формой, а не подразумевается. Знать одну перестановку из двух — не строгость,
// а слепая зона: написанное во второй не находка и не чистота, а невидимость.
// Обе перестановки заведены там, где вторая идиоматична (наречие плюс глагол);
// у именных форм перестановки в этом корпусе не встречается, и заводить её
// вперёд предмета не надо.
//
// И образцы, и примеры собираются из ЧАСТЕЙ, а не пишутся целиком: иначе строка
// определения совпадает сама с собой, и разбор объявляет находкой собственный
// исходник. Заводить ради этого исключение по пути было бы хуже — перечень
// прощённых есть место, куда отсрочку вносят незамеченной.
var deferralForms = []DeferralForm{
	// Примеры англоязычных форм собираются из частей ровно по той же причине,
	// что и русские образцы: написанные целиком, они сделали бы ЭТОТ файл
	// нарушителем собственного запрета. Это не гипотеза — первая редакция
	// перечня в монорепо дала четыре находки, все в этих строках.
	// Класс границы `[\s;{(]` перед именем маркера — НЕСУЩИЙ, а не украшение:
	// он и есть то, чем вызов через точку (`context.TODO()`, `pkg.FIXME()`)
	// отличается от обращения к читателю кода. Прежде рядом стоял отдельный
	// вычет имени функции стандартной библиотеки; он снят, потому что ветвь
	// была НЕДОСТИЖИМА — измерено инъекцией: с отключённым вычетом вердикт
	// суиты не изменился ни на одной пробе, а прямой замер по объединённому
	// образцу дал «нет совпадения» на всех трёх написаниях вызова через точку,
	// включая то, за которым стоит двоеточие. Ослабишь класс границы —
	// свойство потеряется вместе с ним, и об этом скажет
	// TestDottedCallIsNotADeferral.
	{Seed: "TODO", Pattern: `(?m)(?:^|[\s;{(])TODO\s*(?:\([^)]*\))?\s*:`,
		Example: "\t// " + "TODO" + ": дочинить"},
	{Seed: "FIXME", Pattern: `(?m)(?:^|[\s;{(])FIXME\s*(?:\([^)]*\))?\s*:`,
		Example: "\t// " + "FIXME" + "(KAC-1): поправить"},
	{Seed: "XXX", Pattern: `(?m)(?:^|[\s;{(])XXX\s*(?:\([^)]*\))?\s*:`,
		Example: "\t// " + "XXX" + ": разобраться"},
	{Seed: "HACK", Pattern: `(?m)(?:^|[\s;{(])HACK\s*(?:\([^)]*\))?\s*:`,
		Example: "\t// " + "HACK" + ": обход"},
	{Seed: "позже", Pattern: "позже " + `допил`,
		Example: "\t// " + "позже " + "допилим" + " до конца"},
	{Seed: "потом", Pattern: "потом " + `додел`,
		Example: "\t// " + "потом " + "доделаем"},
	{Seed: "потом", Pattern: `додел` + `\S* ` + "потом",
		Example: "\t// " + "доделаем" + " потом"},
	{Seed: "позже", Pattern: `допил` + `\S* ` + "позже",
		Example: "\t// " + "допилим" + " позже"},
	{Seed: "пока", Pattern: "пока " + `заглушк`,
		Example: "\t// " + "пока " + "заглушка"},
	{Seed: "временное", Pattern: "временное " + `упрощени`,
		Example: "\t// " + "временное " + "упрощение"},
	{Seed: "надо", Pattern: "надо " + `допилить`,
		Example: "\t// " + "надо " + "допилить"},
	{Seed: "заглушка", Pattern: "заглушка " + `на время`,
		Example: "\t// " + "заглушка " + "на время" + " выпуска"},
}

// DeferralForms — объявленные формы. Отдаётся КОПИЯ: перечень есть предмет
// пробы сквозного пути, и проба не вправе его двигать.
//
// Копия при этом поверхностная, и этого достаточно: все поля формы — строки,
// то есть неизменяемы, и записать через неё в перечень нечего.
func DeferralForms() []DeferralForm {
	out := make([]DeferralForm, len(deferralForms))
	copy(out, deferralForms)
	return out
}

// deferralMarker — объединение всех образцов; собирается из deferralForms,
// поэтому форма, добавленная в перечень, не может остаться неучтённой.
var deferralMarker = regexp.MustCompile(func() string {
	pats := make([]string, 0, len(deferralForms))
	for _, f := range deferralForms {
		pats = append(pats, f.Pattern)
	}
	return strings.Join(pats, "|")
}())

// deferralSeeds — те же формы, но затравками: файл, не содержащий ни одной,
// разбору не подлежит.
var deferralSeeds = func() []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range deferralForms {
		if seen[f.Seed] {
			continue
		}
		seen[f.Seed] = true
		out = append(out, f.Seed)
	}
	return out
}()

// DeferralSeeds — затравки отсева. Нужны пробе сквозного пути: форма, чья
// затравка не встречается в её же примере, отсекается ДО образца и перестаёт
// ловиться, оставаясь на вид объявленной.
func DeferralSeeds() []string {
	out := make([]string, len(deferralSeeds))
	copy(out, deferralSeeds)
	return out
}

// HasDeferralSeed — дешёвый отсев перед разбором.
func HasDeferralSeed(body string) bool {
	for _, s := range deferralSeeds {
		if strings.Contains(body, s) {
			return true
		}
	}
	return false
}

// DeferralSkip — вид, вычитаемый из области, и предикат его узнавания.
//
// Вычитается ровно то, что судить нельзя ПО СУЩЕСТВУ, а не то, что неудобно:
// фикстура гейта обязана уметь написать форму дефекта, иначе гейт не проверить
// инъекцией, а маркер в сгенерированном принадлежит генератору.
//
// Перечень видов задаёт ПОТРЕБИТЕЛЬ и проверяет его сам: вид, которому больше
// нечего вычитать, — слепая зона, которую унаследует следующий (под его именем
// можно положить что угодно, и счёт не сдвинется). Поэтому обход считает
// вычтенное поштучно и отдаёт счёт переписью, а вердикт по нему выносит проба
// потребителя — она же знает состав своего дерева.
//
// Match получает путь, отсчитанный от корня обхода, с прямыми разделителями.
type DeferralSkip struct {
	// Name — имя вида; оно же ключ в переписи.
	Name string
	// Why — зачем вид заведён; печатается в отказе, когда предмет у вида
	// кончился, чтобы читатель не гадал, что именно снимать.
	Why string
	// Match — предикат узнавания по пути.
	Match func(slashed string) bool
}

// DeferralCensus — объём осмотренного. Печатается всегда: «ноль находок»
// обязано быть отличимо от «ноль прочитанного», а «область покрыта» — от
// «область выписана».
type DeferralCensus struct {
	Tracked int // элементов в индексе дерева
	Read    int // прочитано
	// Roots — верхний уровень индекса, ВЫВЕДЕННЫЙ из него же.
	Roots []string
	// ByRoot — корень, затем сколько прочитано под ним.
	ByRoot map[string]int
	// SkipNames — виды вычитания в том порядке, в каком их объявил вызывающий.
	// Держатся отдельно от счёта, потому что у карты порядка нет, а перепись
	// обязана быть детерминированной.
	SkipNames []string
	// Skipped — вид вычитания, затем сколько вычтено.
	Skipped map[string]int
	// Mentions — совпадений, отсечённых как цитата.
	Mentions int
}

// String — перепись одной строкой, включая раскладку по корням.
func (c DeferralCensus) String() string {
	byRoot := make([]string, 0, len(c.Roots))
	for _, r := range c.Roots {
		byRoot = append(byRoot, fmt.Sprintf("%s=%d", r, c.ByRoot[r]))
	}
	skipped := make([]string, 0, len(c.SkipNames))
	for _, name := range c.SkipNames {
		skipped = append(skipped, fmt.Sprintf("%s=%d", name, c.Skipped[name]))
	}
	subtracted := strings.Join(skipped, " ")
	if subtracted == "" {
		// Пустой перечень видов — законный вход, и он строже, а не мягче:
		// вычитать нечего, значит судится всё. Сказать это словом обязательно,
		// иначе пустое поле читается как «виды были, но не сработали».
		subtracted = "видов не объявлено"
	}
	return fmt.Sprintf(
		"перепись: в индексе %d, прочитано %d, корней (выведено из индекса) %d [%s]; "+
			"вычтено: %s; упоминаний (совпадение внутри кавычек — разговор о маркере, "+
			"а не обещание) %d; перечня прощённых путей у разбора нет",
		c.Tracked, c.Read, len(c.Roots), strings.Join(byRoot, " "), subtracted, c.Mentions)
}

// DeferralFinding — одна находка с координатой.
type DeferralFinding struct {
	Where string
	Line  string
}

func (f DeferralFinding) String() string { return f.Where + ": " + f.Line }

// deferralMentionQuotes — знаки, которыми корпус ЦИТИРУЕТ фразу, говоря о ней.
//
// Состав выбран замером, а не вкусом: живые упоминания отсрочки в обоих
// деревьях стоят в «ёлочках», они же сплошная норма корпуса. Что сюда
// СОЗНАТЕЛЬНО не вошло и почему — иначе следующий читатель впишет это как
// недосмотр:
//
//   - ASCII-кавычка. Она не цитирует прозу, а ограничивает строку кода и
//     значение в YAML/JSON. Признав её знаком упоминания, разбор замолчал бы на
//     значении-обещании в поле конфигурации — то есть на настоящей отсрочке,
//     написанной там, где эта кавычка обязательна по синтаксису;
//   - обратные кавычки. Ими корпус размечает КОД, а не цитирует обещание;
//   - ОТРИЦАНИЕ. Оно опаснее: фраза «не сейчас, потом доделаем» — законное
//     обещание, которое такой фильтр снял бы. Гипотеза проверена и отвергнута;
//     вернётся — пусть вернётся с предметом.
var deferralMentionQuotes = regexp.MustCompile("«[^»]*»|“[^”]*”|„[^“]*“")

// deferralTopLevel — верхний сегмент пути; для файла в корне это он сам.
func deferralTopLevel(slashed string) string {
	if i := strings.IndexByte(slashed, '/'); i >= 0 {
		return slashed[:i]
	}
	return "."
}

// deferralMarkedLine — строка тела, в которой сработал образец, с её номером.
type deferralMarkedLine struct {
	no   int
	text string
}

// deferralMarkedLines гонит образец по ВСЕМУ телу разом и возвращает только
// затронутые строки.
//
// Один проход по телу дешевле вызова образца на каждой строке дерева на
// порядок; номер строки восстанавливается по смещению совпадения, поэтому
// координата в находке та же самая.
//
// Совпадение, целиком лежащее внутри кавычек, отсекается ЗДЕСЬ — до дедупликации
// по строке. Порядок обязателен: строка, несущая и цитату, и обещание, при
// отсеве после дедупликации потеряла бы второе вместе с первым. Число
// отсечённого возвращается для переписи.
func deferralMarkedLines(body string) (out []deferralMarkedLine, mentions int) {
	locs := deferralMarker.FindAllStringIndex(body, -1)
	if len(locs) == 0 {
		return nil, 0
	}
	var (
		lineNo   = 1
		scanned  int
		lastLine = -1
	)
	for _, loc := range locs {
		lineNo += strings.Count(body[scanned:loc[0]], "\n")
		scanned = loc[0]

		start := strings.LastIndexByte(body[:loc[0]], '\n') + 1
		end := strings.IndexByte(body[loc[0]:], '\n')
		if end < 0 {
			end = len(body)
		} else {
			end += loc[0]
		}
		text := body[start:end]

		// Употребление или упоминание: цитата — разговор о маркере, а не обещание.
		if deferralIsMention(text, loc[0]-start, loc[1]-start) {
			mentions++
			continue
		}
		if lineNo == lastLine {
			continue // два обещания в одной строке — одна находка
		}
		lastLine = lineNo
		out = append(out, deferralMarkedLine{no: lineNo, text: text})
	}
	return out, mentions
}

// deferralIsMention — совпадение [from,to) строки line целиком внутри цитаты.
func deferralIsMention(line string, from, to int) bool {
	for _, q := range deferralMentionQuotes.FindAllStringIndex(line, -1) {
		if q[0] <= from && to <= q[1] {
			return true
		}
	}
	return false
}

// AuditDeferredWork обходит дерево под root и ищет маркеры отложенной работы.
//
// Область — ВЕСЬ индекс дерева: корни не выписываются, а выводятся из него,
// поэтому каталог, заведённый завтра, попадает под разбор в день появления, а
// не тогда, когда кто-нибудь снова пройдёт по дереву руками.
//
// skips — виды вычитания вызывающего (см. [DeferralSkip]). Пустой перечень —
// законный вход: он означает «не вычитать ничего» и делает разбор СТРОЖЕ, а не
// мягче, поэтому отказом не является. Счёт вычтенного по каждому виду уезжает в
// перепись, и вердикт «виду больше нечего вычитать» выносит вызывающий.
//
// Возвращает находки и перепись осмотренного: «ноль находок» обязано быть
// отличимо и от «ноль прочитанного», и от «прочитал не там».
func AuditDeferredWork(root string, skips []DeferralSkip) (
	findings []DeferralFinding, census DeferralCensus, err error) {
	census.ByRoot = map[string]int{}
	census.Skipped = map[string]int{}
	census.SkipNames = make([]string, 0, len(skips))
	for _, s := range skips {
		census.SkipNames = append(census.SkipNames, s.Name)
		census.Skipped[s.Name] = 0
	}

	tracked, terr := treecorpus.Under(root)
	if terr != nil {
		return nil, census, fmt.Errorf("состав дерева: %w", terr)
	}
	census.Tracked = len(tracked)

	roots := map[string]struct{}{}
	for _, abs := range tracked {
		rel, rerr := filepath.Rel(root, abs)
		if rerr != nil {
			return nil, census, fmt.Errorf("путь %s: %w", abs, rerr)
		}
		slashed := filepath.ToSlash(rel)
		roots[deferralTopLevel(slashed)] = struct{}{}

		if deferralSkipped(slashed, skips, census.Skipped) {
			continue
		}

		raw, berr := os.ReadFile(abs) // #nosec G304 -- путь из индекса git ЭТОГО дерева
		if berr != nil {
			return nil, census, fmt.Errorf("чтение %s: %w", slashed, berr)
		}
		body := string(raw)
		census.Read++
		census.ByRoot[deferralTopLevel(slashed)]++

		// Отсев: файл без единой затравки разбору не подлежит. Подавляющее
		// большинство дерева отсекается здесь, и именно поэтому разбор
		// укладывается в бюджет пакета проверок.
		if !HasDeferralSeed(body) {
			continue
		}
		lines, mentions := deferralMarkedLines(body)
		census.Mentions += mentions
		for _, line := range lines {
			findings = append(findings, DeferralFinding{
				Where: fmt.Sprintf("%s:%d", slashed, line.no),
				Line:  strings.TrimSpace(line.text),
			})
		}
	}

	for r := range roots {
		census.Roots = append(census.Roots, r)
	}
	sort.Strings(census.Roots)
	sort.Slice(findings, func(i, j int) bool { return findings[i].Where < findings[j].Where })
	return findings, census, nil
}

// deferralSkipped — подпадает ли путь под один из видов вычитания; счёт ведётся
// по ПЕРВОМУ совпавшему виду, иначе один файл увеличил бы сразу несколько
// счётчиков и перепись перестала бы складываться в прочитанное.
func deferralSkipped(slashed string, skips []DeferralSkip, counted map[string]int) bool {
	for _, s := range skips {
		if s.Match != nil && s.Match(slashed) {
			counted[s.Name]++
			return true
		}
	}
	return false
}
