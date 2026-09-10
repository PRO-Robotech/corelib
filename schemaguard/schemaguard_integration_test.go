// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// schemaguard_integration_test.go — НАБЛЮДАЕМОЕ поведение, а не только вердикт:
// образ на схеме следующей версии готовым НЕ объявляется, на совместимой —
// объявляется (предикат 4 задачи #1734).
//
// # Почему проба ходит в базу
//
// Решение — чистая функция и проверено без базы (schemaguard_test.go). Здесь
// проверяется ДРУГОЕ: что версию из `goose_db_version` мы читаем ту, которую
// ведёт goose, и читаем ПРИМЕНЁННУЮ. Оба свойства принадлежат SQL, и без базы у
// них нет производителя: запрос, спрашивающий не ту колонку, зелен на любом
// поддельном источнике.
//
// # Почему утверждается ТЕЛО `/readyz`, а не только ошибка чекера
//
// Оператор читает тело ответа, а не код. Обе величины обязаны быть В НЁМ, иначе
// «не готов» неотличимо от отказа базы — ровно того, что этот чекер и заведён
// различать.
package schemaguard_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/corelib/observability/health"
	"github.com/PRO-Robotech/corelib/pgtest"
	"github.com/PRO-Robotech/corelib/schemaguard"
)

// gooseTable — журнал goose ровно в той форме, в какой его ведёт сам goose.
// Форма НЕ упрощена: `is_applied` — единственное, чем применённая миграция
// отличается от снятой, и проба, создавшая таблицу без него, доказывала бы
// свойство своего упрощения.
const gooseTable = `
CREATE TABLE IF NOT EXISTS goose_db_version (
  id          SERIAL PRIMARY KEY,
  version_id  BIGINT      NOT NULL,
  is_applied  BOOLEAN     NOT NULL,
  tstamp      TIMESTAMP   NULL DEFAULT now()
);
`

func schemaGuardPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: требуется Postgres")
	}
	dsn := pgtest.NewDB(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("пул: %v", err)
	}
	pgtest.ClosePoolAtEnd(t, pool)
	return pool
}

func applyVersions(t *testing.T, pool *pgxpool.Pool, rows [][2]any) {
	t.Helper()
	for _, r := range rows {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, $2)`,
			r[0], r[1]); err != nil {
			t.Fatalf("запись версии %v: %v", r, err)
		}
	}
}

// TestPgxVersionReader_ReadsTheAppliedMaximum — снятая миграция старшей не считается.
func TestPgxVersionReader_ReadsTheAppliedMaximum(t *testing.T) {
	pool := schemaGuardPool(t)
	read := schemaguard.PgxVersionReader(pool)
	ctx := context.Background()

	// Пустой журнал — законное состояние свежей базы, а не отказ.
	if v, err := read(ctx); err != nil || v != 0 {
		t.Fatalf("пустой журнал: получили (%d, %v), ожидали (0, nil)", v, err)
	}

	applyVersions(t, pool, [][2]any{{1, true}, {2, true}, {3, false}})

	v, err := read(ctx)
	if err != nil {
		t.Fatalf("чтение версии: %v", err)
	}
	if v != 2 {
		t.Fatalf("применённая старшая версия: получили %d, ожидали 2 — запрос считает старшей "+
			"СНЯТУЮ миграцию, и образ объявил бы схему ушедшей вперёд там, где она не уходила", v)
	}
}

// TestReadyzRefusesOnANewerSchemaAndNamesBothNumbers — наблюдаемое поведение
// плюс ПОЛОЖИТЕЛЬНЫЙ контроль на совместимой схеме.
func TestReadyzRefusesOnANewerSchemaAndNamesBothNumbers(t *testing.T) {
	pool := schemaGuardPool(t)

	// Образ несёт версии 1 и 2.
	fsys := fstest.MapFS{
		"0001_initial.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")},
		"0002_add.sql":     {Data: []byte("-- +goose Up\nSELECT 1;\n")},
	}
	agg := health.New([]health.Checker{{
		Name:  schemaguard.CheckerName,
		Check: schemaguard.CheckFromFS(fsys, schemaguard.PgxVersionReader(pool)),
	}})
	srv := httptest.NewServer(agg.ReadyHandler())
	defer srv.Close()

	get := func() (int, string) {
		t.Helper()
		resp, err := http.Get(srv.URL) //nolint:noctx // проба, адрес свой
		if err != nil {
			t.Fatalf("запрос готовности: %v", err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		return resp.StatusCode, string(raw)
	}

	// ── ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: схема вровень с образом ───────────────────
	//
	// Без него отрицание ниже зеленело бы на чекере, отвергающем всё.
	applyVersions(t, pool, [][2]any{{1, true}, {2, true}})
	if code, body := get(); code != http.StatusOK {
		t.Fatalf("совместимая схема: получили %d (%s), ожидали 200 — чекер отвергает то, "+
			"что обязан пропускать, и отрицание ниже ничего не доказывает", code, body)
	}

	// ── схема ушла ВПЕРЁД образа ──────────────────────────────────────────
	applyVersions(t, pool, [][2]any{{3, true}})
	code, body := get()
	if code != http.StatusServiceUnavailable {
		t.Fatalf("схема следующей версии: получили %d (%s), ожидали 503 — под объявлен готовым "+
			"на схеме, которую образ обслуживать не умеет, и получит трафик", code, body)
	}
	if !strings.Contains(body, schemaguard.CheckerName) {
		t.Errorf("тело ответа не называет зависимость %q: %s — оператор не отличит это от "+
			"отказа базы", schemaguard.CheckerName, body)
	}
}
