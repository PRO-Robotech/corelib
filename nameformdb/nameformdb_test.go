// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// nameformdb_test.go — шов, на который опирается диагностика `Run`.
//
// `Run` печатает перепись и уже найденное ДО того, как уронить прогон отказом
// чтения схемы. Это осмысленно ровно постольку, поскольку `Check` возвращает
// ПРИГОДНЫЙ отчёт ВМЕСТЕ с ошибкой. Утверждение проверяемо без базы и без
// подставного `*testing.T`, поэтому проверяется здесь, а не разглядыванием кода.
//
// Замечание, из которого выведено (рецензия #721, 2026-08-19): при отказе
// переписи `Run` ронял прогон сразу и терял оба — и перепись, и расхождения
// образцов с каноном, которые находятся ДО обращения к базе. Ложного зелёного
// не было; терялась диагностика, а она и есть то, ради чего пробу читают.
package nameformdb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	canon "github.com/PRO-Robotech/corelib/validate/nameform"
)

// failingExecer — база, которая не отвечает. Отказ отдаётся на ЧТЕНИИ (Query),
// потому что перепись идёт именно им; Exec на этом пути не зовётся, и его
// молчаливый успех выдал бы ложный вывод о том, какой путь исполнился.
type failingExecer struct{ err error }

func (f failingExecer) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("Exec не должен зваться до успешной переписи — путь исполнился не тот, который проверяется")
}

func (f failingExecer) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, f.err
}

func TestCheck_ReportSurvivesTheError(t *testing.T) {
	ctx := context.Background()

	t.Run("перепись НЕ прочиталась — отчёт всё равно пригоден", func(t *testing.T) {
		boom := errors.New("соединение закрыто")
		p := Probe{
			Schema: "kacho_probe_seam",
			Tables: []Table{
				{Name: "widgets", Row: func(string, int) (string, []any) { return "", nil }},
			},
		}

		rep, err := p.Check(ctx, failingExecer{err: boom})
		if !errors.Is(err, boom) {
			t.Fatalf("отказ чтения обязан дойти до вызывающего, получено %v", err)
		}
		census := rep.Census()
		if !strings.Contains(census, "kacho_probe_seam") || !strings.Contains(census, "widgets") {
			t.Errorf("отчёт при отказе не несёт переписи — `Run` печатал бы пустоту: %q", census)
		}
		if rep.RejectedPerTable == 0 || rep.AcceptedPerTable == 0 {
			t.Errorf("отчёт при отказе не несёт объёма утверждений: отвергаемых %d, принимаемых %d",
				rep.RejectedPerTable, rep.AcceptedPerTable)
		}
	})

	t.Run("законный близнец: перечень таблиц ПУСТ — отказ, и он про другое", func(t *testing.T) {
		// Другой вход, а не копия предыдущего: здесь до базы дело не доходит
		// вовсе, поэтому исправная база подставляется намеренно — отказ обязан
		// прийти от предпосылки пробы, а не от чтения. Без этого подслучая
		// «отчёт пригоден при отказе» доказывалось бы на одном-единственном
		// виде отказа.
		p := Probe{Schema: "kacho_probe_seam"}

		rep, err := p.Check(ctx, failingExecer{err: errors.New("этой ошибки не должно быть видно")})
		if err == nil {
			t.Fatal("проба без таблиц обязана отказать: исполнять нечего, а пустой отчёт " +
				"читался бы как «находок нет»")
		}
		if strings.Contains(err.Error(), "этой ошибки не должно быть видно") {
			t.Errorf("отказ пришёл от чтения базы, хотя до неё дойти не должно было: %v", err)
		}
		if !strings.Contains(rep.Census(), "kacho_probe_seam") {
			t.Errorf("отчёт при отказе предпосылки не называет схему: %q", rep.Census())
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Ось «намеренно ИНАЯ форма» (задача #1279) — доказательство инъекцией.
//
// Категория заведена схемой iam, где рядом с шестью именуемыми ресурсами живёт
// идентификатор роли: он формой имени не судится, но форму НЕСЁТ — свою. Без
// третьей категории такую таблицу пришлось бы либо привести к канону (сломав
// ссылки), либо объявить исключением (соврав: форму она несёт).
//
// Проверка, которую нельзя навести на подложенный дефект, о своей способности
// упасть не свидетельствует, поэтому здесь — ЧЕТЫРЕ прогона: законный близнец
// (находок ноль) и три инъекции, каждая по своей оси. Инъекция снимает РОВНО
// проверяемое свойство: остальные объявления при этом целы, поэтому красное
// приходит от новой проверки, а не от соседней.

// censusRow — строка переписи схемы: таблица, имена и определения её ограничений
// формы имени.
type censusRow struct {
	table string
	names []string
	defs  []string
}

// fakeRows — минимальный `pgx.Rows` над заранее заданной переписью. Нужен, чтобы
// ось проверялась БЕЗ контейнера: свойство здесь про разбор переписи, а не про
// поведение сервера, и поднимать ради него базу значило бы мерить не то.
type fakeRows struct {
	rows []censusRow
	i    int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Values() ([]any, error)                       { return nil, nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }

func (r *fakeRows) Next() bool {
	if r.i >= len(r.rows) {
		return false
	}
	r.i++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	cur := r.rows[r.i-1]
	*(dest[0].(*string)) = cur.table
	*(dest[1].(*[]string)) = cur.names
	*(dest[2].(*[]string)) = cur.defs
	return nil
}

// canonDef / otherDef — определения ограничения, какими их отдаёт
// `pg_get_constraintdef`. Каноничное собирается ИЗ САМОГО КАНОНА, а не
// выписывается: выписанное разошлось бы с ним молча — ровно тот класс, ради
// которого этот двигатель и написан.
var (
	canonDef = "CHECK ((name ~ '" + canon.Form + "'::text))"
	otherDef = "CHECK ((name ~ '^[a-z][a-z0-9_]{0,40}$'::text))"
)

// censusExecer — база, отвечающая заданной переписью. `Exec` судит вход тем же
// каноном, что и сервер: негодное имя отвергается ограничением названной
// таблицы, каноничное вставляется. Дублёр, принимающий БОЛЬШЕ настоящего, сделал
// бы невидимым ровно тот дефект, ради которого его подставляют.
type censusExecer struct{ rows []censusRow }

func (e censusExecer) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return &fakeRows{rows: e.rows}, nil
}

func (e censusExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	name, _ := args[0].(string)
	if canon.OK(name) {
		return pgconn.CommandTag{}, nil
	}
	return pgconn.CommandTag{}, &pgconn.PgError{
		Code:           "23514",
		TableName:      sql, // Row ниже кладёт в sql имя таблицы.
		ConstraintName: sql + canon.ConstraintSuffix,
	}
}

// probeOver собирает пробу над заданной переписью: таблицы под пробой, исключения
// и носители намеренно иной формы.
func probeOver(rows []censusRow, probed []string, excluded, other map[string]string) (Probe, Execer) {
	tables := make([]Table, 0, len(probed))
	for _, t := range probed {
		tables = append(tables, Table{
			Name: t,
			Row:  func(name string, _ int) (string, []any) { return t, []any{name} },
		})
	}
	return Probe{Schema: "kacho_inj", Tables: tables, Excluded: excluded, OtherForm: other},
		censusExecer{rows: rows}
}

// legitCensus — перепись, на которой ВСЁ объявлено верно: одна таблица под
// пробой несёт канон, одна несёт намеренно иную форму, одна формы не несёт.
func legitCensus() []censusRow {
	return []censusRow{
		{table: "accounts", names: []string{"accounts_name_check"}, defs: []string{canonDef}},
		{table: "roles", names: []string{"roles_custom_name_check"}, defs: []string{otherDef}},
		{table: "archive", names: []string{}, defs: []string{}},
	}
}

func findingsOf(t *testing.T, p Probe, db Execer) []string {
	t.Helper()
	rep, err := p.Check(context.Background(), db)
	if err != nil {
		t.Fatalf("перепись не прочиталась: %v", err)
	}
	return rep.Findings
}

// TestOtherForm_LegitimateTwin_IsSilent — законный близнец: находок ноль.
//
// Он стоит ПЕРВЫМ намеренно. Без него три инъекции ниже зеленели бы и на
// проверке, краснеющей всегда, — а такую снимут первым же ложным срабатыванием.
func TestOtherForm_LegitimateTwin_IsSilent(t *testing.T) {
	p, db := probeOver(legitCensus(),
		[]string{"accounts"},
		map[string]string{"archive": "архив: писателя нет"},
		map[string]string{"roles": "идентификатор роли, а не косметическая метка"})
	if got := findingsOf(t, p, db); len(got) != 0 {
		t.Fatalf("законная раскладка дала находки (%d): %v", len(got), got)
	}
	t.Log("перепись: таблиц под пробой 1, исключений 1, носителей иной формы 1; находок 0")
}

// TestOtherForm_LostItsSubject_IsFound — ось 1: запись есть, а формы у таблицы
// НЕТ. Послабление обязано истекать само, а не переживать свой предмет.
func TestOtherForm_LostItsSubject_IsFound(t *testing.T) {
	rows := legitCensus()
	rows[1] = censusRow{table: "roles", names: []string{}, defs: []string{}} // форму сняли
	p, db := probeOver(rows,
		[]string{"accounts"},
		map[string]string{"archive": "архив: писателя нет"},
		map[string]string{"roles": "идентификатор роли, а не косметическая метка"})

	got := findingsOf(t, p, db)
	if !containsSubstr(got, "roles") || !containsSubstr(got, "НЕ НЕСЁТ") {
		t.Fatalf("снятая форма у объявленного носителя не найдена, находки: %v", got)
	}
}

// TestOtherForm_CameToCanon_IsFound — ось 2: таблица пришла К КАНОНУ, а запись
// продолжает объявлять её особой. Такая запись — тоже пережившее свой предмет
// послабление, только в другую сторону: она прячет таблицу от проверки действия.
func TestOtherForm_CameToCanon_IsFound(t *testing.T) {
	rows := legitCensus()
	rows[1].defs = []string{canonDef} // форма стала каноничной
	p, db := probeOver(rows,
		[]string{"accounts"},
		map[string]string{"archive": "архив: писателя нет"},
		map[string]string{"roles": "идентификатор роли, а не косметическая метка"})

	got := findingsOf(t, p, db)
	if !containsSubstr(got, "несёт ТУ ЖЕ") {
		t.Fatalf("приход к канону у объявленного носителя не найден, находки: %v", got)
	}
}

// TestOtherForm_UndeclaredCarrier_IsFound — ось 3: перепись. Таблица несёт
// форму, но не объявлена НИ пробой, НИ носителем иной формы.
//
// Ось проверяет именно ОБЪЕДИНЕНИЕ двух объявленных множеств: до этой правки
// сверялся только перечень пробы, и всякий носитель иной формы читался бы как
// «таблица получила форму, а проба о ней не знает».
func TestOtherForm_UndeclaredCarrier_IsFound(t *testing.T) {
	rows := append(legitCensus(),
		censusRow{table: "clusters", names: []string{"clusters_name_check"}, defs: []string{otherDef}})
	p, db := probeOver(rows,
		[]string{"accounts"},
		map[string]string{"archive": "архив: писателя нет"},
		map[string]string{"roles": "идентификатор роли, а не косметическая метка"})

	got := findingsOf(t, p, db)
	if !containsSubstr(got, "clusters") {
		t.Fatalf("необъявленный носитель формы не найден, находки: %v", got)
	}
}

// TestOtherForm_DoubleDeclared_IsFound — таблица объявлена И исключением, И
// носителем иной формы: два места об одном предмете, из которых верно одно.
func TestOtherForm_DoubleDeclared_IsFound(t *testing.T) {
	p, db := probeOver(legitCensus(),
		[]string{"accounts"},
		map[string]string{"archive": "архив: писателя нет", "roles": "и тут тоже"},
		map[string]string{"roles": "идентификатор роли, а не косметическая метка"})

	got := findingsOf(t, p, db)
	if !containsSubstr(got, "объявлена И исключением") {
		t.Fatalf("двойное объявление не найдено, находки: %v", got)
	}
}

func containsSubstr(all []string, want string) bool {
	for _, s := range all {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}
