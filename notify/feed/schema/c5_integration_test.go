// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// c5_integration_test.go — лента действующей версии несёт то, что вносит C5
// (приёмка NTF-5 kacho#2924, §3 З19, Р13, Р5; замысел issue-2924 З10 п.2,
// З12 п.1, З8 п.5; маршрут C5):
//
//   - expires_at допускает NULL, и CHECK ((expires_at IS NULL) = (class =
//     'obligation')): строка obligation срока не имеет, строка
//     security/notice без срока невыразима (CX5-31 (б));
//   - класс obligation в CHECK класса;
//   - thread_key NULL с CHECK «NULL либо непустая строка»:
//     отсутствие ключа — одно написание (CX5-52 (а));
//   - состояние superseded с причиной — новой версией схемы поверх V2, и
//     переход V2 → действующая даёт те же ограничения, что лента с нуля.
//
// Пробы судят базу по коду SQLSTATE и имени ограничения, а не по тексту.
// Каждая проба начинает с положительного контроля той же формы вставки,
// который проходит и на базе полосы: отказ контроля — сломанная фикстура, а
// не отсутствующая возможность.
package schema_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// TestMain выдаёт пакету один Postgres; шаблонная база пуста — каждая проба
// строит ленту сама нужной версией.
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Config{Name: "feedschema"}))
}

// emptyPool — своя пустая база пробы.
func emptyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), pgtest.NewEmptyDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	return pool
}

func execOK(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

// requireCheckViolation — отказ CHECK (23514); имя ограничения печатается.
func requireCheckViolation(t *testing.T, err error, what string) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "%s: ждали отказ CHECK (23514), получили %v", what, err)
	require.Equal(t, "23514", pgErr.Code, "%s: ждали 23514, получили %s %s (%s)", what, pgErr.Code, pgErr.Message, pgErr.ConstraintName)
	t.Logf("%s: 23514 ограничение %s", what, pgErr.ConstraintName)
}

const c5Svc = "cfive"

// insertRow — вставка строки ленты c5Svc; cols — дополнительные столбцы со
// значениями (имя → значение SQL-литералом).
func insertRow(pool *pgxpool.Pool, id, class, expires string, extra map[string]string) error {
	cols := []string{"id", "template", "schema_rev", "class", "recipient_address", "attrs", "state", "expires_at"}
	vals := []string{"'" + id + "'", "'t'", "1", "'" + class + "'", "'a@example.invalid'", "'{}'", "'pending'", expires}
	for c, v := range extra {
		cols = append(cols, c)
		vals = append(vals, v)
	}
	_, err := pool.Exec(context.Background(), `INSERT INTO "`+c5Svc+`_notification_outbox" (`+
		strings.Join(cols, ", ")+`) VALUES (`+strings.Join(vals, ", ")+`)`)
	return err
}

// currentFeed — лента c5Svc действующей версией с нуля и положительный
// контроль формы вставки: строка notice со сроком принята.
func currentFeed(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := emptyPool(t)
	up, _, err := schema.DDL(c5Svc, schema.Current())
	require.NoError(t, err)
	execOK(t, pool, up)
	require.NoError(t, insertRow(pool, "ctl-notice", "notice", "now() + interval '1 hour'", nil),
		"ФИКСТУРА: строка notice со сроком не вставилась — форма вставки сломана")
	return pool
}

// З10 п.2, §3 З19: строка obligation без срока выразима.
func TestC5Schema_ObligationRowHasNoExpiry(t *testing.T) {
	pool := currentFeed(t)
	require.NoError(t, insertRow(pool, "ob-1", "obligation", "NULL", nil),
		"строка obligation без срока (expires_at NULL) не вставилась")
}

// З10 п.2 (CX5-31 (б)): строка notice без срока невыразима — 23514; близнец
// — та же строка со сроком (контроль currentFeed); факт один — срок.
func TestC5Schema_NoticeRowWithoutExpiryIsInexpressible(t *testing.T) {
	pool := currentFeed(t)
	requireCheckViolation(t, insertRow(pool, "nt-1", "notice", "NULL", nil), "notice без срока")
	requireCheckViolation(t, insertRow(pool, "sc-1", "security", "NULL", nil), "security без срока")
}

// З10 п.2: строка obligation со сроком невыразима — CHECK равенства, а не
// одной стороны. Близнец — TestC5Schema_ObligationRowHasNoExpiry.
func TestC5Schema_ObligationRowWithExpiryIsInexpressible(t *testing.T) {
	pool := currentFeed(t)
	require.NoError(t, insertRow(pool, "ob-twin", "obligation", "NULL", nil),
		"близнец: строка obligation без срока не вставилась")
	requireCheckViolation(t, insertRow(pool, "ob-2", "obligation", "now() + interval '1 hour'", nil), "obligation со сроком")
}

// З12 п.1 (CX5-52 (а)): thread_key — NULL либо непустая строка; пустая строка — 23514.
// Близнец — та же строка с ключом 'ntc-1/usr-1'; факт один — значение ключа.
func TestC5Schema_ThreadKeyAbsenceHasOneSpelling(t *testing.T) {
	pool := currentFeed(t)
	require.NoError(t, insertRow(pool, "th-1", "notice", "now() + interval '1 hour'",
		map[string]string{"thread_key": "'ntc-1/usr-1'"}), "строка с ключом нити не вставилась")
	require.NoError(t, insertRow(pool, "th-2", "notice", "now() + interval '1 hour'",
		map[string]string{"thread_key": "NULL"}), "строка без ключа нити (NULL) не вставилась")
	requireCheckViolation(t, insertRow(pool, "th-3", "notice", "now() + interval '1 hour'",
		map[string]string{"thread_key": "''"}), "thread_key = ''")
}

// З8 п.5: состояние superseded с причиной клетки SUPERSEDED принято CHECK
// состояния и outcome_pair; без причины — отказ outcome_pair. Причина берётся
// из ЕДИНСТВЕННОЙ таблицы «состояние × причина» (schema.OutcomePairs), а не
// выписывается пробой.
func TestC5Schema_SupersededIsAClosedState(t *testing.T) {
	pool := currentFeed(t)
	reasons := schema.OutcomePairs()["superseded"]
	require.Len(t, reasons, 1, "у состояния superseded в таблице «состояние × причина» ждали одну причину, есть %v", reasons)
	require.NoError(t, insertRow(pool, "sp-1", "notice", "now() + interval '1 hour'", map[string]string{
		"state": "'superseded'", "outcome_reason": "'" + reasons[0] + "'", "outcome_at": "now()",
	}), "строка superseded с причиной не вставилась")
	err := insertRow(pool, "sp-2", "notice", "now() + interval '1 hour'", map[string]string{
		"state": "'superseded'", "outcome_at": "now()",
	})
	requireCheckViolation(t, err, "superseded без причины")
}

// Значение superseded — новой версией схемы: лента службы на V2 после
// перехода на действующую несёт те же ограничения, что лента действующей
// версии с нуля; переход вниз возвращает V2.
func TestC5Schema_UpgradeFromV2EqualsTheCurrentSchema(t *testing.T) {
	require.Greater(t, int(schema.Current()), int(schema.V2), "действующая версия схемы — не новее V2")
	pool := emptyPool(t)
	ddl := func(svc string, v schema.Version) {
		t.Helper()
		up, _, err := schema.DDL(svc, v)
		require.NoError(t, err)
		execOK(t, pool, up)
	}
	ddl("cfold", schema.V2)
	ddl("cfnew", schema.Current())
	ddl("cfrel", schema.V2)
	v2 := constraintDefs(t, pool, "cfrel")
	require.Equal(t, v2, constraintDefs(t, pool, "cfold"), "фикстура: обе ленты V2 одинаковы")

	up, down, err := schema.UpgradeDDL("cfold", schema.V2, schema.Current())
	require.NoError(t, err, "переход V2 → действующая не выпущен")
	execOK(t, pool, up)
	require.Equal(t, constraintDefs(t, pool, "cfnew"), constraintDefs(t, pool, "cfold"), "переход V2 → действующая")
	execOK(t, pool, down)
	require.Equal(t, v2, constraintDefs(t, pool, "cfold"), "переход вниз возвращает V2")
}

// constraintDefs — ограничения ленты svc (имя без префикса → определение) и
// определения её столбцов expires_at и thread_key.
func constraintDefs(t *testing.T, pool *pgxpool.Pool, svc string) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	rows, err := pool.Query(ctx, `
SELECT c.conname, pg_get_constraintdef(c.oid)
  FROM pg_constraint c JOIN pg_class r ON r.oid = c.conrelid
 WHERE r.relname = $1`, svc+"_notification_outbox")
	require.NoError(t, err)
	for rows.Next() {
		var name, def string
		require.NoError(t, rows.Scan(&name, &def))
		out["constraint "+strings.TrimPrefix(name, svc)] = def
	}
	rows.Close()
	require.NoError(t, rows.Err())
	rows, err = pool.Query(ctx, `
SELECT column_name, data_type || ' ' || is_nullable
  FROM information_schema.columns
 WHERE table_name = $1 AND column_name IN ('expires_at', 'thread_key')`, svc+"_notification_outbox")
	require.NoError(t, err)
	for rows.Next() {
		var name, def string
		require.NoError(t, rows.Scan(&name, &def))
		out["column "+name] = def
	}
	rows.Close()
	require.NoError(t, rows.Err())
	rows, err = pool.Query(ctx, `SELECT indexname, indexdef FROM pg_indexes WHERE tablename = $1`, svc+"_notification_outbox")
	require.NoError(t, err)
	for rows.Next() {
		var name, def string
		require.NoError(t, rows.Scan(&name, &def))
		out["index "+strings.TrimPrefix(name, svc)] = strings.ReplaceAll(def, svc, "<svc>")
	}
	rows.Close()
	require.NoError(t, rows.Err())
	require.NotEmpty(t, out, "у ленты %s не прочитано ничего", svc)
	return out
}
