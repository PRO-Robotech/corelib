// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package nameformdb — общий двигатель пробы «ограничение формы имени ДЕЙСТВУЕТ на
// живой базе».
//
// # Предмет
//
// Ограничение формы имени стоит в схемах, принявших канон #715. «Миграция
// применилась» и «ограничение отвергает негодную строку» — РАЗНЫЕ утверждения, и
// до задачи #721 проверялось только первое: перепись пробы, доказывающей второе,
// давала один сервис из пяти.
//
// Ни число схем, ни имя миграции здесь НЕ выписываются, и это не небрежность.
// Прежняя редакция называла `715001_resource_name_single_form.sql` и «пять схем»:
// первое перестало быть верным, когда цепь iam была сведена в одну первичную
// миграцию (форма стоит в ней), второе — потому что состав пятёрки сменился, а
// её размер совпал случайно, geo форму снял, iam её принял. Число, пережившее
// свой предикат, читается как измеренное. Действующий состав печатает переписью
// гейт `TestNameFormConstraintIsProvenWhereItIsDeclared` на каждом прогоне.
//
// Ограничение базы — последний рубеж канона: код может смениться, вызывающий
// может пойти мимо слоя домена, но оператор базы отвергнет негодную строку
// всегда. Ровно поэтому его действие обязано быть ДОКАЗАНО вставкой, а не
// выведено из того, что файл миграции лежит в дереве.
//
// # Почему двигатель общий, а не скопирован в четыре пакета
//
// Копия расходится с оригиналом молча и расходится там, где расхождение не
// видно, — на составе утверждений. Своего у сервиса ровно одно: как выглядит
// МИНИМАЛЬНАЯ ЗАКОННАЯ СТРОКА его таблицы. Перечень проверяемых значений,
// чтение отказа и перепись — одно на всех.
//
// # Что проба утверждает
//
//  1. ПЕРЕПИСЬ. Перечень таблиц схемы, несущих форму имени, совпадает с
//     перечнем, который проба обходит. Таблица, получившая форму позже, и
//     таблица, её потерявшая, — обе находка, а не тишина.
//  2. ОТРИЦАНИЕ. Имя вне канона отвергается — и отвергается ИМЕННО формой:
//     сверяется не только SQLSTATE 23514, но и имя ограничения, назвавшего
//     отказ. Без второго условия проба зеленела бы на отказе любого соседнего
//     ограничения той же таблицы.
//  3. ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ. Каноничное имя вставляется УСПЕШНО. Без него
//     отрицание зеленеет на любой поломке фикстуры: строка, отвергнутая по
//     совершенно другой причине, выглядит как сработавшая защита.
//
// Пункт 3 несёт отдельную ценность, названную самой миграцией: прежние формы
// требовали БУКВУ первым символом, канон допускает цифру. Поэтому среди
// принимаемых значений стоит имя, начинающееся с цифры: возврат к прежней форме
// покраснит пробу, тогда как одно отрицание его бы не заметило.
//
// # Почему исход, а не сразу падение
//
// [Probe.Check] возвращает ОТЧЁТ, а падение производит [Probe.Run]. Так
// способность пробы упасть доказуема инъекцией: проба сноса ограничения зовёт
// Check и утверждает, что находка появилась и назвала координату, — а рядом
// законный близнец той же формы, на котором находок ноль. Проверка, которая
// умеет только звать t.Fatal, о себе самой ничего доказать не может.
package nameformdb

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/corelib/db/pgfault"
	"github.com/jackc/pgx/v5"

	canon "github.com/PRO-Robotech/corelib/validate/nameform"
)

// Execer — та часть пула, которой пользуется двигатель. Интерфейс, а не
// *pgxpool.Pool, чтобы пакет не тянул за собой конструкцию пула вызывающего.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Table — таблица под пробой и способ собрать для неё МИНИМАЛЬНУЮ ЗАКОННУЮ
// строку с заданным именем.
//
// Row обязан вернуть INSERT, отвергаемый ТОЛЬКО формой имени: все прочие
// ограничения таблицы (перечисления состояний, диапазоны, внешние ключи,
// уникальность, учёт числа ресурсов) он обязан удовлетворять. Иначе
// положительный контроль перестанет быть контролем — он будет падать по чужой
// причине.
//
// seq различает строки одного прогона: у большинства таблиц уникальны и ключ, и
// пара «проект, имя», а у слушателя nlb — ещё и тройка «балансировщик, порт,
// протокол».
type Table struct {
	Name string
	Row  func(name string, seq int) (sql string, args []any)
}

// Probe — то, что объявляет вызывающий: схема, таблицы и намеренные исключения.
//
// Excluded — таблицы схемы, которые несут столбец имени и которым форма НЕ
// ставится ОСОЗНАННО; значение — причина. Перечень проверяется в обе стороны:
// исключение, которому больше нечего исключать (форму поставили), — находка, а
// не молчание. Так послабление истекает само, а не переживает свой предмет.
type Probe struct {
	Schema   string
	Tables   []Table
	Excluded map[string]string
	// OtherForm — таблицы схемы, которые несут форму имени НАМЕРЕННО ДРУГУЮ,
	// чем канон; значение — причина. Отличается от Excluded по существу:
	// исключённая таблица формы не несёт ВОВСЕ, а эта несёт СВОЮ, и та форма
	// действует.
	//
	// Категория заведена схемой iam, где рядом с шестью именуемыми ресурсами
	// живёт идентификатор роли (`roles/vpc.admin`): он не косметическая метка,
	// а то, на что ссылаются привязки, и формой имени не судится — записанное
	// решение владельца (#715). Без третьей категории такую таблицу пришлось бы
	// либо привести к канону (сломав ссылки), либо объявить исключением
	// (соврав: форму она несёт).
	//
	// Перечень проверяется в ОБЕ стороны: запись, чья таблица формы не несёт
	// вовсе, и запись, чья таблица пришла К КАНОНУ, — обе находка. Так
	// послабление истекает само, а не переживает свой предмет.
	OtherForm map[string]string
}

// Report — исход прогона: объём осмотренного и перечень находок.
//
// Объём печатается ВСЕГДА, в том числе при нуле находок: иначе «находок нет»
// неотличимо от «ничего не прочитано».
type Report struct {
	Schema string
	// Probed — таблицы, которые обошла проба.
	Probed []string
	// CarriedInDB — таблицы схемы, реально несущие форму имени.
	CarriedInDB []string
	// BareInDB — таблицы схемы со столбцом имени и БЕЗ формы.
	BareInDB []string
	// Excluded — объявленные намеренные исключения.
	Excluded []string
	// OtherForm — таблицы, объявленные несущими намеренно иную форму.
	OtherForm []string
	// OtherFormWhy — причина по каждой такой таблице.
	OtherFormWhy map[string]string
	// ExcludedWhy — причина по каждому исключению. Поле читается переписью и
	// печатается: причина, которую никто не читает, — то же «принято и
	// проигнорировано», только в оснастке.
	ExcludedWhy map[string]string
	// RejectedPerTable / AcceptedPerTable — сколько значений проверено на таблицу.
	RejectedPerTable int
	AcceptedPerTable int
	// Findings — находки. Пусто = ограничение действует всюду, где объявлено.
	Findings []string
}

// Census — строка переписи для печати в лог прогона.
func (r Report) Census() string {
	why := make([]string, 0, len(r.Excluded))
	for _, tbl := range r.Excluded {
		why = append(why, fmt.Sprintf("%s — %s", tbl, r.ExcludedWhy[tbl]))
	}
	other := make([]string, 0, len(r.OtherForm))
	for _, tbl := range r.OtherForm {
		other = append(other, fmt.Sprintf("%s — %s", tbl, r.OtherFormWhy[tbl]))
	}
	return fmt.Sprintf(
		"схема %s: осмотрено таблиц с формой имени — %d (%v); на каждой отвергнуто значений — %d, "+
			"принято — %d; намеренных исключений — %d [%s]; таблиц с намеренно иной формой — %d [%s]; "+
			"находок — %d",
		r.Schema, len(r.Probed), r.Probed, r.RejectedPerTable, r.AcceptedPerTable,
		len(r.Excluded), strings.Join(why, "; "),
		len(r.OtherForm), strings.Join(other, "; "), len(r.Findings))
}

// rejected — имена ВНЕ канона. Каждое отвергается формой, и каждое покрывает
// свою сторону расхождения, названную миграцией 715001:
//
//   - пустая строка — прежние формы её принимали, канон не принимает;
//   - подчёркивание и заглавные — прежние формы шире канона;
//   - дефис по краям и длина 64 — границы, на которых форма обязана отказать.
var rejected = []struct{ label, value string }{
	{"пустое имя", ""},
	{"подчёркивание", "bad_name"},
	{"заглавные буквы", "Bad-Name"},
	{"дефис первым символом", "-lead"},
	{"дефис последним символом", "trail-"},
	{"длиннее 63 символов", strings.Repeat("a", 64)},
}

// accepted — имена ПО канону. Помимо обычного, здесь две границы и одно
// значение, которое прежняя форма отвергала (цифра первым символом): без него
// возврат к «первым символом обязана быть буква» прошёл бы незамеченным, потому
// что от такого СУЖЕНИЯ отрицание не краснеет.
var accepted = []struct{ label, value string }{
	{"обычное имя", "probe-name"},
	{"цифра первым символом", "9lives"},
	{"один символ", "a"},
	{"ровно 63 символа", strings.Repeat("b", 63)},
}

// canonSourcePkg — где живёт единственное объявление формы. Строкой, а не
// импортом пути: нужна она только в тексте находки.
//
// Записана ПУТЁМ ИМПОРТА, а не путём в дереве (задача #2532, класс 3). Прежде
// здесь стояло `pkg/validate/nameform` — координата дерева, которой после
// разъезда фундамента не станет: пакеты переезжают из `pkg/X` в `X`. Путь
// импорта живёт в том же пространстве имён, что и сам импорт строкой выше, и
// потому правится ТЕМ ЖЕ заходом, каким правятся импорты; путь дерева не правится
// ничем и молча начинает указывать в никуда.
const canonSourcePkg = "github.com/PRO-Robotech/corelib/validate/nameform"

// formCheck — то, что перепись нашла у таблицы: имена и определения ограничений
// формы имени.
type formCheck struct {
	names []string
	defs  []string
}

// censusSQL перечисляет БАЗОВЫЕ таблицы схемы, несущие столбец `name`, и по
// каждой собирает ограничения-проверки, чьё определение ограничивает САМО имя
// регулярным выражением.
//
// Отбор идёт по подстроке `(name ~ ` в ОПРЕДЕЛЕНИИ, а не по имени ограничения:
// имя — соглашение, которое переживёт смену смысла, а определение — то, что
// исполняет сервер. Проверки вида `length(name) <= …` под отбор не подпадают:
// это ограничение ДЛИНЫ, а не формы, и считать его формой значило бы объявить
// покрытой таблицу, которая имя произвольного вида принимает.
const censusSQL = `
SELECT c.table_name,
       coalesce(array_agg(k.conname) FILTER (WHERE k.conname IS NOT NULL), '{}') AS names,
       coalesce(array_agg(k.def)     FILTER (WHERE k.def     IS NOT NULL), '{}') AS defs
  FROM information_schema.columns c
  JOIN information_schema.tables tb
    ON tb.table_schema = c.table_schema
   AND tb.table_name   = c.table_name
   AND tb.table_type   = 'BASE TABLE'
  LEFT JOIN LATERAL (
       SELECT con.conname AS conname, pg_get_constraintdef(con.oid) AS def
         FROM pg_constraint con
         JOIN pg_class     rel ON rel.oid = con.conrelid
         JOIN pg_namespace n   ON n.oid   = rel.relnamespace
        WHERE n.nspname = c.table_schema
          AND rel.relname = c.table_name
          AND con.contype = 'c'
          AND pg_get_constraintdef(con.oid) LIKE '%(name ~ %'
  ) k ON true
 WHERE c.table_schema = $1
   AND c.column_name  = 'name'
 GROUP BY c.table_name
 ORDER BY c.table_name`

// Run исполняет пробу и роняет прогон на каждой находке, печатая перепись.
//
// Перепись и уже найденное печатаются ДО вердикта и на ЛЮБОМ исходе — включая
// отказ чтения схемы. Прежняя редакция роняла прогон сразу и теряла оба:
// расхождения образцов с каноном находятся ДО обращения к базе, и молчание о
// них посылало читателя чинить соединение там, где разошёлся перечень значений.
// Ложного зелёного здесь не было — терялась диагностика, а она и есть то, ради
// чего пробу читают.
func (p Probe) Run(ctx context.Context, t *testing.T, db Execer) {
	t.Helper()

	rep, err := p.Check(ctx, db)
	t.Log(rep.Census())
	for _, f := range rep.Findings {
		t.Error(f)
	}
	if err != nil {
		t.Fatalf("проба формы имени не смогла прочитать схему %s: %v — "+
			"перепись выше относится к тому, что успело прочитаться, и полной не является",
			p.Schema, err)
	}
}

// Check исполняет пробу и возвращает отчёт. Ошибку возвращает только тогда,
// когда прочитать схему не удалось вовсе, — то есть когда отчёт был бы не
// «находок нет», а «ничего не прочитано».
func (p Probe) Check(ctx context.Context, db Execer) (Report, error) {
	rep := Report{
		Schema:           p.Schema,
		Probed:           []string{},
		CarriedInDB:      []string{},
		BareInDB:         []string{},
		Excluded:         []string{},
		ExcludedWhy:      map[string]string{},
		OtherForm:        []string{},
		OtherFormWhy:     map[string]string{},
		RejectedPerTable: len(rejected),
		AcceptedPerTable: len(accepted),
		Findings:         []string{},
	}

	// Предпосылка. Пустой перечень таблиц сделал бы всё нижеследующее вакуумным:
	// ни одного утверждения не исполнилось бы, а отчёт остался бы пустым — то
	// есть «находок нет» стало бы неотличимо от «ничего не проверено».
	if len(p.Tables) == 0 {
		return rep, fmt.Errorf("схема %s: проба не назвала ни одной таблицы — исполнять нечего", p.Schema)
	}
	for _, tb := range p.Tables {
		rep.Probed = append(rep.Probed, tb.Name)
	}
	sort.Strings(rep.Probed)
	for tbl, why := range p.Excluded {
		rep.Excluded = append(rep.Excluded, tbl)
		rep.ExcludedWhy[tbl] = why
	}
	sort.Strings(rep.Excluded)
	for tbl, why := range p.OtherForm {
		rep.OtherForm = append(rep.OtherForm, tbl)
		rep.OtherFormWhy[tbl] = why
	}
	sort.Strings(rep.OtherForm)
	for _, tbl := range rep.OtherForm {
		if _, dup := p.Excluded[tbl]; dup {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"схема %s, таблица %s: объявлена И исключением (формы нет), И носителем иной формы — "+
					"две записи об одном предмете, из которых верна одна", p.Schema, tbl))
		}
	}

	// Образцы сверяются с ЕДИНСТВЕННЫМ объявлением формы, а не с представлением
	// автора о ней. Иначе перечень значений стал бы вторым местом об одном
	// предмете: канон сменился бы, а проба продолжила бы утверждать про
	// прежний — и утверждала бы уверенно, потому что база сменилась вместе с
	// каноном, а образцы нет.
	for _, bad := range rejected {
		if canon.OK(bad.value) {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"образец %s (%q) объявлен здесь негодным, но канон (%s) его ПРИНИМАЕТ — "+
					"перечень значений разошёлся с единственным объявлением формы",
				bad.label, bad.value, canonSourcePkg))
		}
	}
	for _, good := range accepted {
		if !canon.OK(good.value) {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"образец %s (%q) объявлен здесь каноничным, но канон (%s) его ОТВЕРГАЕТ — "+
					"перечень значений разошёлся с единственным объявлением формы",
				good.label, good.value, canonSourcePkg))
		}
	}

	found, err := census(ctx, db, p.Schema)
	if err != nil {
		return rep, err
	}

	for tbl, fc := range found {
		if len(fc.names) > 0 {
			rep.CarriedInDB = append(rep.CarriedInDB, tbl)
			continue
		}
		rep.BareInDB = append(rep.BareInDB, tbl)
	}
	sort.Strings(rep.CarriedInDB)
	sort.Strings(rep.BareInDB)

	// ── 1. Перепись: что несёт форму — и что её не несёт ────────────────────
	// Форму в базе несут ДВА объявленных множества: те, что проба обходит, и
	// те, что несут намеренно иную форму. Сверять надо их объединение — иначе
	// вторая категория читалась бы как «таблица получила форму, а проба о ней
	// не знает».
	declaredCarriers := append(append([]string{}, rep.Probed...), rep.OtherForm...)
	sort.Strings(declaredCarriers)
	if !equalSets(declaredCarriers, rep.CarriedInDB) {
		rep.Findings = append(rep.Findings, fmt.Sprintf(
			"схема %s: перечень таблиц с формой имени в БАЗЕ разошёлся с объявленным. "+
				"Таблица, получившая форму позже, осталась бы недоказанной; таблица, её потерявшая, — незамеченной.\n"+
				"  объявлено (проба + иная форма): %v\n  база несёт:                     %v",
			p.Schema, declaredCarriers, rep.CarriedInDB))
	}
	if !equalSets(rep.Excluded, rep.BareInDB) {
		rep.Findings = append(rep.Findings, fmt.Sprintf(
			"схема %s: перечень намеренных исключений разошёлся с базой. "+
				"Исключение, которому больше нечего исключать, — находка: оно переживает свой предмет.\n"+
				"  объявлено исключений: %v\n  без формы в базе:     %v", p.Schema, rep.Excluded, rep.BareInDB))
	}

	// ── 2. Форма одна на схему ──────────────────────────────────────────────
	//
	// Две разные формы у соседних таблиц одного сервиса — ровно то состояние,
	// которое снимала #715. Сверяется определение, а не имя ограничения.
	sample, sampleFrom := "", ""
	// Образец формы берётся у таблицы, которую проба ОБХОДИТ: она и есть носитель
	// канона. Возьми его у первой попавшейся из CarriedInDB — и на схеме, где
	// первой по алфавиту стоит таблица с намеренно иной формой, канон сравнивался
	// бы с не-каноном, а находка пришлась бы на все остальные разом.
	for _, tbl := range rep.Probed {
		fc, ok := found[tbl]
		if ok && len(fc.defs) == 1 {
			sample, sampleFrom = fc.defs[0], tbl
			break
		}
	}
	for _, tbl := range rep.OtherForm {
		fc, ok := found[tbl]
		if !ok || len(fc.names) == 0 {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"схема %s, таблица %s: объявлена носителем намеренно иной формы (%s), но формы имени "+
					"НЕ НЕСЁТ вовсе — запись потеряла предмет: либо таблица его лишилась, либо ей место "+
					"в перечне исключений", p.Schema, tbl, rep.OtherFormWhy[tbl]))
			continue
		}
		if sample != "" && len(fc.defs) == 1 && fc.defs[0] == sample {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"схема %s, таблица %s: объявлена носителем намеренно иной формы (%s), но несёт ТУ ЖЕ, что %s — "+
					"запись пережила свой предмет и должна быть снята, а таблица переведена под пробу",
				p.Schema, tbl, rep.OtherFormWhy[tbl], sampleFrom))
		}
	}
	for _, tbl := range rep.CarriedInDB {
		if _, other := p.OtherForm[tbl]; other {
			continue
		}
		fc := found[tbl]
		if len(fc.names) > 1 {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"схема %s, таблица %s: форму имени объявляют %d ограничения (%v) — у одного поля два "+
					"правила, и какое из них действует, читатель не выведет", p.Schema, tbl, len(fc.names), fc.names))
			continue
		}
		if tbl == sampleFrom {
			continue
		}
		if sample != "" && fc.defs[0] != sample {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"схема %s: таблица %s несёт ФОРМУ, отличную от %s.\n  %s: %s\n  %s: %s",
				p.Schema, tbl, sampleFrom, tbl, fc.defs[0], sampleFrom, sample))
		}
	}

	// ── 3. Действие ограничения ─────────────────────────────────────────────
	seq := 0
	for _, tbl := range p.Tables {
		fc, ok := found[tbl.Name]
		if !ok || len(fc.names) == 0 {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"схема %s, таблица %s: формы имени НЕТ — проверять действие нечего", p.Schema, tbl.Name))
			continue
		}
		conname := fc.names[0]

		for _, bad := range rejected {
			seq++
			if f := rejectedByForm(ctx, db, tbl, conname, bad.label, bad.value, seq); f != "" {
				rep.Findings = append(rep.Findings, f)
			}
		}
		for _, good := range accepted {
			seq++
			if f := acceptedAsIs(ctx, db, tbl, good.label, good.value, seq); f != "" {
				rep.Findings = append(rep.Findings, f)
			}
		}
	}

	return rep, nil
}

// rejectedByForm требует отказа ИМЕННО от формы имени.
//
// Сверяется имя ограничения, а не только SQLSTATE: у каждой из этих таблиц есть
// соседние проверки (состояние, диапазон, метки), и отказ любой из них — тоже
// 23514. Проба, довольствующаяся кодом, осталась бы зелёной при снятой форме,
// если бы строка не проходила по какой-нибудь другой причине.
func rejectedByForm(ctx context.Context, db Execer, tbl Table, conname, label, name string, seq int) string {
	sql, args := tbl.Row(name, seq)
	_, err := db.Exec(ctx, sql, args...)
	if err == nil {
		return fmt.Sprintf("%s: %s (%q) — строка ВСТАВЛЕНА, форма имени не действует", tbl.Name, label, name)
	}

	f := pgfault.Classify(err)
	if !f.FromDatabase() {
		return fmt.Sprintf("%s: %s (%q) — отказ пришёл не от сервера: %v", tbl.Name, label, name, err)
	}
	if !f.Is(pgfault.Check) {
		return fmt.Sprintf("%s: %s (%q) — ожидался отказ проверки, получен %s: %s",
			tbl.Name, label, name, f.SQLState, f.Message)
	}
	if f.Constraint != conname {
		return fmt.Sprintf("%s: %s (%q) — отвергло ДРУГОЕ ограничение (%s), а не форма имени (%s): "+
			"строка не дошла до проверки формы, и её действие этим не доказано",
			tbl.Name, label, name, f.Constraint, conname)
	}
	return ""
}

// acceptedAsIs — положительный контроль: каноничное имя вставляется.
func acceptedAsIs(ctx context.Context, db Execer, tbl Table, label, name string, seq int) string {
	sql, args := tbl.Row(name, seq)
	if _, err := db.Exec(ctx, sql, args...); err != nil {
		return fmt.Sprintf("%s: %s (%q) — каноничное имя ОТВЕРГНУТО (%v). Без успеха на этом значении "+
			"отрицание ничего не значит: оно зеленело бы и на строке, отвергаемой по любой другой причине",
			tbl.Name, label, name, err)
	}
	return ""
}

func census(ctx context.Context, db Execer, schema string) (map[string]formCheck, error) {
	rows, err := db.Query(ctx, censusSQL, schema)
	if err != nil {
		return nil, fmt.Errorf("перепись схемы %s: %w", schema, err)
	}
	defer rows.Close()

	out := map[string]formCheck{}
	for rows.Next() {
		var (
			table string
			names []string
			defs  []string
		)
		if err := rows.Scan(&table, &names, &defs); err != nil {
			return nil, fmt.Errorf("перепись схемы %s: %w", schema, err)
		}
		out[table] = formCheck{names: names, defs: defs}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("перепись схемы %s: %w", schema, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("перепись схемы %s не нашла НИ ОДНОЙ таблицы со столбцом имени — "+
			"молчание пробы означало бы «ничего не прочитано», а не «находок нет»", schema)
	}
	return out, nil
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
