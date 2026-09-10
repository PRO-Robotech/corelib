// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package listcursorplan доказывает ПОВЕДЕНИЕМ, что страница курсорного списка
// берёт свой порядок из индекса, а не достраивает его сортировкой.
//
// # Зачем отдельная проба, если гейт уже зелёный
//
// Гейт `internal/repohygiene` сводит текст миграции с текстом запроса. Это
// утверждение о СХЕМЕ: «объявлен индекс такой-то формы». Утверждение о
// ПОВЕДЕНИИ — другое: «планировщик Postgres берёт порядок отсюда». Между ними
// умещается всё, что гейт по построению не знает: тип индекса, класс операторов,
// сортировка колонки, версия сервера, порядок колонок, который человек прочитал
// иначе, чем читает база. Поэтому форма проверяется разбором, а действие —
// планом настоящего Postgres на настоящей схеме сервиса.
//
// # Что именно утверждается и почему без данных
//
// Проба спрашивает у базы ПЛАН страницы:
//
//	EXPLAIN SELECT … FROM <схема>.<таблица> [WHERE <равенство>]
//	        ORDER BY <ключи курсора> LIMIT <размер+1>
//
// и требует двух вещей сразу: план ИМЕНУЕТ ожидаемый индекс и НЕ содержит узла
// сортировки.
//
// Выбор плана зависит от статистики, а статистика — от числа строк, которое
// проба сама и насыпала бы. Такая проба меряла бы не свойство схемы, а удачность
// подобранного объёма: на трёхстах строках вердикт один, на трёх тысячах другой,
// и оба «настоящие». Поэтому основной вопрос ставится ДЕТЕРМИНИРОВАННО —
// последовательное чтение и сортировка выключаются на время одного запроса
// (`SET LOCAL enable_seqscan/enable_sort = off`). Обе ручки МЯГКИЕ: они не
// запрещают план, а делают его дорогим. Значит база возьмёт упорядоченный путь
// по индексу тогда и только тогда, когда он СУЩЕСТВУЕТ, — а когда его нет,
// честно построит сортировку, и проба это увидит.
//
// Один случай на сервис проверяется ВДОБАВОК реалистично — на насыпанных
// строках, с `ANALYZE` и БЕЗ единой ручки: там план выбирает сам планировщик по
// своей стоимости. Реалистичным сделан общий список операций: у его таблицы нет
// ни внешних ключей, ни триггеров учёта, поэтому строки насыпаются одним
// оператором и не тянут за собой фикстуру половины сервиса.
//
// # Контроль в обратную сторону обязателен
//
// Утверждение «сортировки в плане нет» само по себе зеленело бы на пробе,
// которая ничего не читает. Поэтому каждый прогон обязан нести КОНТРОЛЬ: тот же
// запрос к той же таблице, упорядоченный по колонке, под которую индекса нет.
// Его план обязан содержать сортировку. Без контроля проба доказывает только то,
// что она выполнилась.
package listcursorplan

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер database/sql для goose и EXPLAIN
	"github.com/pressly/goose/v3"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// Case — один курсорный обход, чей план проверяется.
type Case struct {
	// Table — таблица без схемы.
	Table string
	// Index — индекс, который план ОБЯЗАН назвать.
	Index string
	// Order — ключи курсора дословно, как их пишет репозиторий:
	// `created_at ASC, id ASC`.
	Order string
	// Where — равенство, которое несёт настоящий обход (`project_id = 'prj-plan'`).
	// Пусто, если у обхода обязательного равенства нет.
	Where string
	// Seed — оператор, насыпающий строки. Непуст ⇒ случай проверяется ВДОБАВОК
	// реалистично: `ANALYZE`, затем план БЕЗ единой ручки.
	Seed string
}

// Control — обратная половина: обход, под который индекса нет и быть не должно.
// Его план обязан содержать сортировку, иначе проба не доказывает ничего.
type Control struct {
	Table string
	Order string
}

// Options — что проверять у одного сервиса.
type Options struct {
	Service string
	Schema  string
	FS      fs.FS
	Cases   []Case
	Control Control
}

// planNodeSortRe — узел сортировки в плане. Проверяется НАЧАЛО строки узла
// (`->  Sort` либо корневой `Sort`), а не вхождение слова: `Sort Key:` —
// свойство узла, а `Sort Method:` печатает только `EXPLAIN ANALYZE`.
var planNodeSortRe = regexp.MustCompile(`(?m)^\s*(?:->\s+)?(?:Incremental\s+)?Sort\b`)

// Run применяет цепочку миграций сервиса в пустую базу и проверяет план каждого
// случая.
func Run(t *testing.T, opts Options) {
	t.Helper()
	if opts.Service == "" || opts.Schema == "" || opts.FS == nil {
		t.Fatalf("listcursorplan: Options неполны (сервис %q, схема %q, FS=%v)",
			opts.Service, opts.Schema, opts.FS != nil)
	}
	if len(opts.Cases) == 0 {
		t.Fatalf("listcursorplan: %s — ни одного случая; проба утверждала бы о пустоте", opts.Service)
	}
	if opts.Control.Table == "" || opts.Control.Order == "" {
		t.Fatalf("listcursorplan: %s — не назван контроль; без него «сортировки в плане нет» "+
			"зеленело бы на пробе, которая ничего не читает", opts.Service)
	}
	if testing.Short() {
		// «Не выполнилось» никогда не читается как «выполнилось и чисто»: объём
		// НЕпроверенного называется числом.
		t.Skipf("listcursorplan: %s — -short оставляет %d план(ов) непрочитанными; "+
			"форма индексов проверена гейтом, действие — нет", opts.Service, len(opts.Cases))
	}

	ctx := context.Background()
	db, err := sql.Open("pgx", pgtest.NewEmptyDB(t))
	if err != nil {
		t.Fatalf("listcursorplan: подключение: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(opts.FS)
	if serr := goose.SetDialect("postgres"); serr != nil {
		t.Fatalf("listcursorplan: диалект goose: %v", serr)
	}
	goose.SetLogger(goose.NopLogger())
	if uerr := goose.Up(db, "."); uerr != nil {
		t.Fatalf("listcursorplan: %s — цепочка миграций не доходит до головы: %v", opts.Service, uerr)
	}

	forced, realistic := 0, 0

	// Контроль ПЕРВЫМ: если он не даёт сортировки, все прочие утверждения этого
	// прогона недействительны, и продолжать нечего.
	controlPlan := explain(ctx, t, db, true, fmt.Sprintf(
		`SELECT * FROM %s.%s ORDER BY %s LIMIT 51`,
		quote(opts.Schema), quote(opts.Control.Table), opts.Control.Order))
	if !planNodeSortRe.MatchString(controlPlan) {
		t.Fatalf("listcursorplan: %s — КОНТРОЛЬ не сработал: обход %s.%s по %s обязан достраивать "+
			"порядок сортировкой (индекса под него нет), а план сортировки не содержит. "+
			"Значит проверка «сортировки нет» ничего не различает.\nплан:\n%s",
			opts.Service, opts.Schema, opts.Control.Table, opts.Control.Order, controlPlan)
	}

	for _, c := range opts.Cases {
		where := ""
		if c.Where != "" {
			where = " WHERE " + c.Where
		}
		query := fmt.Sprintf(`SELECT * FROM %s.%s%s ORDER BY %s LIMIT 51`,
			quote(opts.Schema), quote(c.Table), where, c.Order)

		plan := explain(ctx, t, db, true, query)
		assertOrderedIndexPath(t, opts, c, "детерминированный", plan)
		forced++

		if c.Seed == "" {
			continue
		}
		if _, serr := db.ExecContext(ctx, c.Seed); serr != nil {
			t.Fatalf("listcursorplan: %s.%s — посев строк: %v", opts.Service, c.Table, serr)
		}
		if _, aerr := db.ExecContext(ctx,
			fmt.Sprintf("ANALYZE %s.%s", quote(opts.Schema), quote(c.Table))); aerr != nil {
			t.Fatalf("listcursorplan: %s.%s — ANALYZE: %v", opts.Service, c.Table, aerr)
		}
		plan = explain(ctx, t, db, false, query)
		assertOrderedIndexPath(t, opts, c, "реалистичный (строки насыпаны, ручек нет)", plan)
		realistic++
	}

	t.Logf("listcursorplan: %s — планов прочитано: детерминированных %d, реалистичных %d, контроль 1",
		opts.Service, forced, realistic)
}

// assertOrderedIndexPath — обе половины утверждения сразу: план ИМЕНУЕТ индекс И
// не содержит узла сортировки.
//
// Одной первой мало: индекс может быть назван внутри плана, порядок которого
// достраивает сортировка сверху (`Sort -> Index Scan`), — а это ровно та цена,
// ради снятия которой индекс и заводится.
func assertOrderedIndexPath(t *testing.T, opts Options, c Case, mode, plan string) {
	t.Helper()
	if !strings.Contains(plan, "using "+c.Index) {
		t.Errorf("%s.%s (%s): план не называет %s — порядок берётся не из него.\nплан:\n%s",
			opts.Service, c.Table, mode, c.Index, plan)
	}
	if planNodeSortRe.MatchString(plan) {
		t.Errorf("%s.%s (%s): в плане есть узел сортировки — страница платит за порядок, "+
			"а не получает его из индекса %s.\nплан:\n%s",
			opts.Service, c.Table, mode, c.Index, plan)
	}
}

// explain читает план. forced=true выключает последовательное чтение и
// сортировку НА ВРЕМЯ ОДНОГО запроса: обе ручки мягкие, поэтому упорядоченный
// путь берётся тогда и только тогда, когда он существует.
func explain(ctx context.Context, t *testing.T, db *sql.DB, forced bool, query string) string {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("listcursorplan: транзакция: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if forced {
		for _, knob := range []string{
			"SET LOCAL enable_seqscan = off",
			"SET LOCAL enable_sort = off",
		} {
			if _, kerr := tx.ExecContext(ctx, knob); kerr != nil {
				t.Fatalf("listcursorplan: %s: %v", knob, kerr)
			}
		}
	}

	rows, err := tx.QueryContext(ctx, "EXPLAIN "+query)
	if err != nil {
		t.Fatalf("listcursorplan: EXPLAIN %s: %v", query, err)
	}
	defer func() { _ = rows.Close() }()

	var b strings.Builder
	for rows.Next() {
		var line string
		if serr := rows.Scan(&line); serr != nil {
			t.Fatalf("listcursorplan: чтение плана: %v", serr)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if rerr := rows.Err(); rerr != nil {
		t.Fatalf("listcursorplan: чтение плана: %v", rerr)
	}
	if b.Len() == 0 {
		t.Fatalf("listcursorplan: пустой план на %s — читать было нечего", query)
	}
	return b.String()
}

// SeedOperations — 5000 строк общей таблицы операций одним оператором.
//
// Живёт здесь, а не у каждого сервиса: семь копий одного посева разошлись бы
// молча, и разошлись бы на составе колонок. Отметки времени расходятся, поэтому
// порядок по (created_at, id) не вырожден и ничьи не решают всё.
func SeedOperations(schema string) string {
	return `INSERT INTO ` + schema + `.operations (id, description, created_at)
	        SELECT 'op-' || lpad(g::text, 14, '0'), 'listcursorplan seed',
	               now() - (g || ' seconds')::interval
	          FROM generate_series(1, 5000) AS g`
}

// quote — идентификатор в кавычках. Имена приходят из этого же дерева, но
// собирать SQL конкатенацией без кавычек значит завести привычку, которая
// однажды встретит имя с заглавной буквой.
func quote(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}
