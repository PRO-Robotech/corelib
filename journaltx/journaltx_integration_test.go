// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package journaltx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/corelib/auth"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// singleConnPool — пул из ОДНОГО соединения: следующая транзакция идёт по тому
// же соединению, что предыдущая, и «настройка видна следующей транзакции»
// становится наблюдаемой, а не зависящей от того, какое соединение выдал пул.
func singleConnPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(pgtest.NewDB(t))
	if err != nil {
		t.Fatalf("разбор DSN: %v", err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("пул: %v", err)
	}
	pgtest.ClosePoolAtEnd(t, pool)
	return pool
}

func setting(t *testing.T, ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, name string) (string, int32) {
	t.Helper()
	var v *string
	var pid int32
	if err := q.QueryRow(ctx, `SELECT current_setting($1, true), pg_backend_pid()`, name).Scan(&v, &pid); err != nil {
		t.Fatalf("чтение настройки %s: %v", name, err)
	}
	if v == nil {
		return "", pid
	}
	return *v, pid
}

// TestBegin_SettingsAreLocalToTheTransaction — помощник выставляет инициатора и
// флаг ленты ЛОКАЛЬНО к транзакции: внутри они видны, `Tx.Initiator()` равен
// выставленному, а следующая транзакция ТОГО ЖЕ соединения их не видит
// (CX3C-01 (а)).
func TestBegin_SettingsAreLocalToTheTransaction(t *testing.T) {
	ctx := context.Background()
	pool := singleConnPool(t)
	usr := ids.NewID(ids.PrefixUser)
	uctx := operations.WithPrincipal(ctx, operations.Principal{Type: "user", ID: usr})

	for _, enabled := range []bool{false, true} {
		tx, err := journaltx.Begin(uctx, pool, journaltx.NewOptions(enabled))
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		gotInit, pid := setting(t, ctx, tx, journaltx.SettingInitiator)
		if gotInit != "user:"+usr {
			t.Errorf("внутри транзакции %s = %q, ожидалось %q", journaltx.SettingInitiator, gotInit, "user:"+usr)
		}
		if tx.Initiator().String() != gotInit {
			t.Errorf("Tx.Initiator() = %q, в базу ушло %q", tx.Initiator().String(), gotInit)
		}
		gotFeed, _ := setting(t, ctx, tx, journaltx.SettingFeedEnabled)
		want := "false"
		if enabled {
			want = "true"
		}
		if gotFeed != want {
			t.Errorf("внутри транзакции %s = %q, ожидалось %q", journaltx.SettingFeedEnabled, gotFeed, want)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		// Следующая транзакция того же соединения.
		plain, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin пула: %v", err)
		}
		afterInit, pid2 := setting(t, ctx, plain, journaltx.SettingInitiator)
		afterFeed, _ := setting(t, ctx, plain, journaltx.SettingFeedEnabled)
		_ = plain.Rollback(ctx)
		if pid2 != pid {
			t.Fatalf("соединение сменилось (%d → %d) — проба не наблюдает локальность", pid, pid2)
		}
		if afterInit != "" || afterFeed != "" {
			t.Errorf("следующая транзакция соединения видит настройки: %s=%q, %s=%q",
				journaltx.SettingInitiator, afterInit, journaltx.SettingFeedEnabled, afterFeed)
		}
	}
}

// TestBegin_JournalRowTakesTheInitiatorFromTheDefault — строка журнала,
// вставленная в транзакции помощника без колонки инициатора, несёт его из
// умолчания; близнец — та же вставка на том же соединении без помощника
// отвергнута 23502 с именем колонки.
func TestBegin_JournalRowTakesTheInitiatorFromTheDefault(t *testing.T) {
	ctx := context.Background()
	pool := singleConnPool(t)
	sva := ids.NewID(ids.PrefixServiceAccount)
	sctx := operations.WithPrincipal(ctx, operations.Principal{Type: "service_account", ID: sva})

	tx, err := journaltx.Begin(sctx, pool, journaltx.NewOptions(true))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	var got string
	if err := tx.QueryRow(ctx, `INSERT INTO probe_journal (resource_id) VALUES ('a') RETURNING initiator`).Scan(&got); err != nil {
		t.Fatalf("вставка в транзакции помощника: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got != "service_account:"+sva {
		t.Errorf("инициатор строки %q, ожидался %q", got, "service_account:"+sva)
	}

	_, err = pool.Exec(ctx, `INSERT INTO probe_journal (resource_id) VALUES ('b')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23502" || pgErr.ColumnName != "initiator" {
		t.Fatalf("вставка без помощника на переиспользованном соединении: %v, ожидался 23502 по колонке initiator", err)
	}
}

// TestAfterCommit_RunsOnlyAfterASuccessfulCommit — хук исполняется после
// успешного Commit и видит зафиксированное; откат и неудавшийся Commit хука не
// исполняют (УК3-04, половина фундамента: счётчик не утверждает того, чего не
// было).
func TestAfterCommit_RunsOnlyAfterASuccessfulCommit(t *testing.T) {
	ctx := context.Background()
	dsn := pgtest.NewDB(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("пул: %v", err)
	}
	pgtest.ClosePoolAtEnd(t, pool)
	uctx := operations.WithPrincipal(ctx, operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)})

	t.Run("коммит", func(t *testing.T) {
		tx, err := journaltx.Begin(uctx, pool, journaltx.NewOptions(true))
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO probe_journal (resource_id) VALUES ('committed')`); err != nil {
			t.Fatalf("вставка: %v", err)
		}
		var runs, visible int
		if err := tx.AfterCommit(func() {
			runs++
			// Видимость из ДРУГОГО соединения доказывает порядок: хук идёт после
			// фиксации, а не перед ней.
			vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			conn, err := pgx.Connect(vctx, dsn)
			if err != nil {
				t.Errorf("соединение из хука: %v", err)
				return
			}
			defer func() { _ = conn.Close(vctx) }()
			if err := conn.QueryRow(vctx, `SELECT count(*) FROM probe_journal WHERE resource_id = 'committed'`).Scan(&visible); err != nil {
				t.Errorf("чтение из хука: %v", err)
			}
		}); err != nil {
			t.Fatalf("AfterCommit на открытой транзакции: %v", err)
		}
		if runs != 0 {
			t.Fatal("хук исполнен при регистрации")
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if runs != 1 {
			t.Errorf("хук исполнен %d раз, ожидался 1", runs)
		}
		if visible != 1 {
			t.Errorf("из хука зафиксированных строк видно %d, ожидалась 1", visible)
		}
	})

	t.Run("откат", func(t *testing.T) {
		tx, err := journaltx.Begin(uctx, pool, journaltx.NewOptions(true))
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		runs := 0
		if err := tx.AfterCommit(func() { runs++ }); err != nil {
			t.Fatalf("AfterCommit на открытой транзакции: %v", err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if err := tx.Commit(ctx); err == nil {
			t.Error("Commit после отката успешен")
		}
		if runs != 0 {
			t.Errorf("хук исполнен %d раз после отката", runs)
		}
	})

	t.Run("неудавшийся коммит", func(t *testing.T) {
		tx, err := journaltx.Begin(uctx, pool, journaltx.NewOptions(true))
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		runs := 0
		if err := tx.AfterCommit(func() { runs++ }); err != nil {
			t.Fatalf("AfterCommit на открытой транзакции: %v", err)
		}
		// Ошибка оператора переводит транзакцию в прерванное состояние: Commit
		// базы на ней — откат.
		_, _ = tx.Exec(ctx, `INSERT INTO probe_journal (sequence_no, resource_id) VALUES (NULL, 'x')`)
		if err := tx.Commit(ctx); err == nil {
			t.Fatal("Commit прерванной транзакции успешен")
		}
		if runs != 0 {
			t.Errorf("хук исполнен %d раз после неудавшегося коммита", runs)
		}
	})
}

// TestBegin_InitiatorComesFromTheComponentContext — контекст AsComponent даёт
// транзакции инициатора пары; второго источника у Begin нет.
func TestBegin_InitiatorComesFromTheComponentContext(t *testing.T) {
	ctx := context.Background()
	pool := singleConnPool(t)
	cctx, err := journaltx.AsComponent(ctx, "storage", "reconciler")
	if err != nil {
		t.Fatalf("AsComponent: %v", err)
	}
	tx, err := journaltx.Begin(cctx, pool, journaltx.NewOptions(false))
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	want, err := auth.InitiatorOf(auth.SystemPrincipalFor("storage", "reconciler"))
	if err != nil {
		t.Fatalf("InitiatorOf: %v", err)
	}
	if tx.Initiator() != want || want.String() != "system:storage-reconciler" {
		t.Errorf("Tx.Initiator() = %q, ожидалось %q", tx.Initiator().String(), want.String())
	}
}

// TestAfterCommit_OnAFinishedTransactionIsRefused — регистрация хука на
// завершённой транзакции (после Commit и после Rollback) — отказ
// [journaltx.ErrTxFinished], а не паника: хуки регистрируются по ходу запроса,
// и паника стояла бы в рабочем пути. Хук, поданный с отказом, не исполняется
// никогда. Близнец — регистрация на открытой транзакции той же пробы — отказа не
// даёт.
func TestAfterCommit_OnAFinishedTransactionIsRefused(t *testing.T) {
	ctx := context.Background()
	pool := singleConnPool(t)
	uctx := operations.WithPrincipal(ctx, operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)})

	finishers := []struct {
		name   string
		finish func(*journaltx.Tx) error
	}{
		{"после коммита", func(tx *journaltx.Tx) error { return tx.Commit(ctx) }},
		{"после отката", func(tx *journaltx.Tx) error { return tx.Rollback(ctx) }},
	}
	for _, f := range finishers {
		t.Run(f.name, func(t *testing.T) {
			tx, err := journaltx.Begin(uctx, pool, journaltx.NewOptions(true))
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			before := 0
			if err := tx.AfterCommit(func() { before++ }); err != nil {
				t.Fatalf("близнец: AfterCommit на открытой транзакции отвергнут: %v", err)
			}
			if err := f.finish(tx); err != nil {
				t.Fatalf("завершение: %v", err)
			}

			late := 0
			err = tx.AfterCommit(func() { late++ })
			if !errors.Is(err, journaltx.ErrTxFinished) {
				t.Fatalf("AfterCommit на завершённой транзакции: отказ %v, ожидался ErrTxFinished", err)
			}
			_ = tx.Commit(ctx)
			if late != 0 {
				t.Errorf("хук, поданный с отказом, исполнен %d раз", late)
			}
		})
	}
}
