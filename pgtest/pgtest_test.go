// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// pgtest_test.go — предпосылка, на которой стоит гейт «контейнер один на пакет».
//
// Гейт (internal/repohygiene/containerperpackage_test.go) не считает вызовы pgtest
// стартом контейнера. Это допущение, и проверяется оно ЗДЕСЬ и ПОВЕДЕНИЕМ, а не
// чтением кода: разойдётся — покраснеет тут, а не промолчит там.
//
// Пакет внешний (`pgtest_test`) намеренно: так вызовы идут через имя пакета, ровно
// как у настоящих потребителей, и гейт видит их той же формой.
package pgtest_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// schema — то, что попадает в шаблон. Достаточно одной таблицы: вопрос к шаблону
// один — доехал он до клона или нет.
const schema = `CREATE TABLE marker (id text primary key)`

func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{
		Name:    "pgtestself",
		Migrate: pgtest.SQL(schema),
	}))
}

// hostOf — host:port выданного DSN, то есть КАКОЙ контейнер его обслуживает.
func hostOf(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	return u.Host
}

// dbOf — имя базы в выданном DSN.
func dbOf(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	return strings.TrimPrefix(u.Path, "/")
}

// TestOneContainerManyDatabases — ровно то, что утверждает гейт: выдачи приходят с
// ОДНОГО контейнера и в РАЗНЫЕ базы.
//
// Обе половины нужны вместе. Один host без разных баз означал бы общую базу (потеря
// изоляции); разные базы без общего host означали бы, что контейнер всё-таки на
// вызов (потеря смысла всей правки).
func TestOneContainerManyDatabases(t *testing.T) {
	if testing.Short() {
		t.Skip("integration (testcontainers Postgres) — skipped with -short")
	}
	a, b := pgtest.NewDB(t), pgtest.NewDB(t)

	if hostOf(t, a) != hostOf(t, b) {
		t.Errorf("две выдачи пришли с РАЗНЫХ контейнеров (%s и %s) — pgtest поднимает "+
			"контейнер на вызов, и гейт, который на это допущение опирается, судит неверно",
			hostOf(t, a), hostOf(t, b))
	}
	if dbOf(t, a) == dbOf(t, b) {
		t.Errorf("две выдачи пришли в ОДНУ базу (%s) — тесты видели бы строки друг друга",
			dbOf(t, a))
	}
}

// TestRowsDoNotCrossBetweenDatabases — изоляция, проверенная данными, а не именами.
//
// Совпадение имён баз — свойство строки DSN; то, что тесты не видят строк друг друга,
// — свойство сервера. Утверждать надо второе.
func TestRowsDoNotCrossBetweenDatabases(t *testing.T) {
	if testing.Short() {
		t.Skip("integration (testcontainers Postgres) — skipped with -short")
	}
	ctx := context.Background()

	first, second := pgtest.NewDB(t), pgtest.NewDB(t)

	da, err := sql.Open("pgx", first)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = da.Close() }()
	db, err := sql.Open("pgx", second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	if _, err := da.ExecContext(ctx, `INSERT INTO marker(id) VALUES ('written-by-first')`); err != nil {
		t.Fatalf("insert into first: %v", err)
	}

	// Положительная половина: строка ВИДНА там, где записана. Без неё «не видно во
	// второй» осталось бы зелёным и при полностью нерабочей записи.
	var n int
	if err := da.QueryRowContext(ctx, `SELECT count(*) FROM marker`).Scan(&n); err != nil {
		t.Fatalf("count in first: %v", err)
	}
	if n != 1 {
		t.Fatalf("в своей базе строка не найдена (count=%d) — проба ничего не доказывает", n)
	}

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM marker`).Scan(&n); err != nil {
		t.Fatalf("count in second: %v", err)
	}
	if n != 0 {
		t.Errorf("во второй базе видно %d строк, записанных в первой — изоляция между "+
			"тестами потеряна", n)
	}
}

// TestTemplateReachesCloneAndEmptyStaysEmpty — NewDB отдаёт мигрированный шаблон,
// NewEmptyDB не отдаёт. Пара: без второй половины «таблица есть» было бы зелёным и
// в мире, где NewEmptyDB тоже клонирует шаблон, — а на нём стоят тесты, которые
// проходят цепочку миграций сами.
func TestTemplateReachesCloneAndEmptyStaysEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration (testcontainers Postgres) — skipped with -short")
	}
	ctx := context.Background()

	cloned, err := sql.Open("pgx", pgtest.NewDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cloned.Close() }()
	var one int
	if err := cloned.QueryRowContext(ctx, `SELECT 1 FROM marker LIMIT 1`).Scan(&one); err != nil && err != sql.ErrNoRows {
		t.Errorf("в клоне шаблона нет таблицы, которую создала миграция: %v", err)
	}

	empty, err := sql.Open("pgx", pgtest.NewEmptyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = empty.Close() }()
	if err := empty.QueryRowContext(ctx, `SELECT 1 FROM marker LIMIT 1`).Scan(&one); err == nil {
		t.Error("NewEmptyDB отдал базу со схемой шаблона — тест, который сам проходит " +
			"цепочку миграций, стартовал бы не с той точки")
	}
}
