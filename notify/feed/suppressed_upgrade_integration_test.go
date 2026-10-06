// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed/schema"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// constraintsOf — ограничения ленты службы svc: имя без префикса службы →
// определение, как его отдаёт сервер.
func constraintsOf(t *testing.T, pool *pgxpool.Pool, svc string) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
SELECT c.conname, pg_get_constraintdef(c.oid)
  FROM pg_constraint c JOIN pg_class r ON r.oid = c.conrelid
 WHERE r.relname = $1`, svc+"_notification_outbox")
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, def string
		require.NoError(t, rows.Scan(&name, &def))
		out[strings.TrimPrefix(name, svc)] = def
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, out, "у ленты %s не прочитано ни одного ограничения", svc)
	return out
}

// Х3 NTF-4, NTF1-D04: служба со схемой V1 после перехода несёт те же
// ограничения ленты, что служба, чья лента создана действующей версией с
// нуля; состояние suppressed с причиной Р17 выразимо. Переход вниз
// возвращает ограничения V1. Префикс «старой» службы — на границе длины
// (Valid, граница выводится у самого Valid через schema.DDL): имя CHECK
// состояния, которое снимает переход, — то, что сервер дал ему по умолчанию,
// без усечения.
//
// Состояние, у которого в таблице есть причины, без причины невыразимо в
// действующей версии: CHECK над NULL не ложен, и outcome_pair V1 такую строку
// пропускал (V1 применена и не правится; действующая — закрывает).
func TestNTF4X3_UpgradedV1EqualsTheCurrentSchema(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.NewDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	exec := func(sql string) {
		t.Helper()
		_, err := pool.Exec(ctx, sql)
		require.NoError(t, err)
	}
	apply := func(svc string, v schema.Version) {
		t.Helper()
		up, _, err := schema.DDL(svc, v)
		require.NoError(t, err)
		exec(up)
	}

	// Три префикса одной длины: имена ограничений, которые сервер усекает
	// по умолчанию (not_before, schema_rev у префикса на границе), у них
	// совпадают после снятия префикса.
	tail := ""
	for n := 1; n <= 63; n++ {
		p := strings.Repeat("x", n)
		if _, _, err := schema.DDL("o"+p, schema.V1); err != nil {
			break
		}
		tail = p
	}
	require.NotEmpty(t, tail, "граница длины префикса не найдена")
	old, fresh, released := "o"+tail, "f"+tail, "r"+tail
	t.Logf("префикс на границе Valid: %d байт", len(old))
	apply(old, schema.V1)
	apply(fresh, schema.Current())
	apply(released, schema.V1)
	v1 := constraintsOf(t, pool, released)
	require.Equal(t, v1, constraintsOf(t, pool, old), "фикстура: обе ленты V1 одинаковы")
	require.NotEqual(t, v1, constraintsOf(t, pool, fresh), "фикстура: действующая версия отличается от V1")

	up, down, err := schema.UpgradeDDL(old, schema.V1, schema.Current())
	require.NoError(t, err)
	exec(up)
	require.Equal(t, constraintsOf(t, pool, fresh), constraintsOf(t, pool, old), "переход V1 → действующая")
	exec(`INSERT INTO "` + old + `_notification_outbox" (id, template, schema_rev, class, recipient_address, attrs, state, outcome_reason, outcome_at, expires_at)
VALUES ('n1', 't', 1, 'notice', 'a@example.invalid', '{}', 'suppressed', 'hard_bounce', now(), now() + interval '1 hour')`)
	exec(`DELETE FROM "` + old + `_notification_outbox"`)

	for _, st := range []string{"denied", "invalid", "dropped", "expired", "suppressed"} {
		_, err := pool.Exec(ctx, `INSERT INTO "`+old+`_notification_outbox" (id, template, schema_rev, class, recipient_address, attrs, state, outcome_at, expires_at)
VALUES ('n2', 't', 1, 'notice', 'a@example.invalid', '{}', $1, now(), now() + interval '1 hour')`, st)
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), "%s без причины: ждали отказ outcome_pair, получили %v", st, err)
		require.Equal(t, "outcome_pair", pgErr.ConstraintName, "%s: %v", st, err)
	}
	exec(`INSERT INTO "` + old + `_notification_outbox" (id, template, schema_rev, class, recipient_address, attrs, state, outcome_at, expires_at)
VALUES ('n3', 't', 1, 'notice', 'a@example.invalid', '{}', 'sent', now(), now() + interval '1 hour')`)
	exec(`DELETE FROM "` + old + `_notification_outbox"`)

	exec(down)
	require.Equal(t, v1, constraintsOf(t, pool, old), "переход вниз возвращает V1")
}
