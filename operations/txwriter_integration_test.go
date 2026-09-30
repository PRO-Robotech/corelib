// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/corelib/operations"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// Проба NTF5-122 — контракт функций записи операции в транзакции вызывающего
// (Ф1 CreatePendingTx, Ф2 CreateDoneTx, Ф3 MarkDoneTx, Ф4 MarkErrorTx).
//
// Обвязка — не шаблон пакета (там только таблица operations), а пустая база, в
// которую накатываются ВСЕ миграции corelib из каталога `migrations/` модуля, и
// две таблицы вызывающего: T1 с ограничением, которое сценарий (в) нарушает
// намеренно, и T2. Хранилище собрано единственным конструктором
// NewRepo(pool, schema) — другой конфигурации у него нет, и «строка, которую
// функция поставила бы только при настройке», в этой обвязке невыразима.
//
// «Перепись строк» — число строк КАЖДОЙ таблицы базы по каталогу, а не только
// таблицы операций: функция, поставившая строку любой таблицы обвязки,
// роняет (з), а (и) — положительный контроль того, что перепись такую строку
// вообще видит.

// principalP — принципал P из «Дано» NTF5-122.
var principalP = operations.Principal{Type: "user", ID: "usr-op", DisplayName: "op"}

// ntf5Harness — база обвязки NTF5-122.
type ntf5Harness struct {
	pool *pgxpool.Pool
	repo operations.TxRepo
}

// newNTF5Harness поднимает пустую базу, накатывает на неё все миграции corelib и
// таблицы вызывающего. Печатает число применённых файлов миграций и число таблиц
// каталога: «миграций 0» — не обвязка, а её отсутствие, и это отказ.
func newNTF5Harness(t *testing.T) *ntf5Harness {
	t.Helper()
	ctx := context.Background()
	dsn := pgtest.NewEmptyDB(t)

	dirs, files := corelibMigrationDirs(t)
	require.NotZero(t, files, "обвязка NTF5-122: в каталоге migrations/ модуля не найдено ни одного файла миграции")
	for _, dir := range dirs {
		require.NoError(t, pgtest.Goose(os.DirFS(dir))(ctx, dsn), "миграции %s", dir)
	}

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)

	_, err = pool.Exec(ctx, `
		CREATE TABLE t1 (id TEXT PRIMARY KEY, v INT NOT NULL CONSTRAINT t1_v_positive CHECK (v > 0));
		CREATE TABLE t2 (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	tables := len(rowCensus(t, pool))
	t.Logf("обвязка NTF5-122: каталогов миграций %d, применено файлов миграций %d, таблиц каталога %d", len(dirs), files, tables)
	require.Contains(t, rowCensus(t, pool), "public.operations", "миграции corelib не создали таблицу операций")

	return &ntf5Harness{pool: pool, repo: operations.NewRepo(pool, "public")}
}

// corelibMigrationDirs находит каждый каталог модуля под migrations/, где есть
// *.sql, и число таких файлов. Перечень выводится обходом, а не выписан: новая
// миграция corelib попадает в обвязку без правки пробы.
func corelibMigrationDirs(t *testing.T) ([]string, int) {
	t.Helper()
	root := filepath.Join("..", "migrations")
	seen := map[string]bool{}
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".sql" {
			return nil
		}
		files++
		seen[filepath.Dir(path)] = true
		return nil
	})
	require.NoError(t, err)
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs, files
}

// rowCensus — число строк каждой таблицы базы по каталогу (все схемы, кроме
// системных). Ключ — «схема.таблица».
func rowCensus(t *testing.T, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `
		SELECT n.nspname, c.relname
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r', 'p')
		   AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		   AND n.nspname NOT LIKE 'pg_toast%'
		 ORDER BY 1, 2`)
	require.NoError(t, err)
	var names [][2]string
	for rows.Next() {
		var s, r string
		require.NoError(t, rows.Scan(&s, &r))
		names = append(names, [2]string{s, r})
	}
	require.NoError(t, rows.Err())
	rows.Close()

	out := make(map[string]int64, len(names))
	for _, n := range names {
		var c int64
		require.NoError(t, pool.QueryRow(ctx,
			fmt.Sprintf(`SELECT count(*) FROM %s`, pgx.Identifier{n[0], n[1]}.Sanitize())).Scan(&c))
		out[n[0]+"."+n[1]] = c
	}
	return out
}

// censusDelta — изменения переписи: только таблицы, где число строк изменилось.
func censusDelta(before, after map[string]int64) map[string]int64 {
	d := map[string]int64{}
	for k, v := range after {
		if v != before[k] {
			d[k] = v - before[k]
		}
	}
	for k, v := range before {
		if _, ok := after[k]; !ok {
			d[k] = -v
		}
	}
	return d
}

func newOp(t *testing.T) operations.Operation {
	t.Helper()
	meta, err := anypb.New(wrapperspb.String("meta"))
	require.NoError(t, err)
	op, err := operations.New("ntf", "ntf5-122", nil)
	require.NoError(t, err)
	op.Metadata = meta
	return op
}

func response(t *testing.T, v string) *anypb.Any {
	t.Helper()
	a, err := anypb.New(wrapperspb.String(v))
	require.NoError(t, err)
	return a
}

// inTx исполняет fn в транзакции вызывающего на READ COMMITTED; ошибка fn —
// откат, иначе фиксация.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return errors.Join(err, rbErr)
		}
		return err
	}
	return tx.Commit(ctx)
}

func t1Rows(t *testing.T, pool *pgxpool.Pool, id string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM t1 WHERE id = $1`, id).Scan(&n))
	return n
}

func opRows(t *testing.T, pool *pgxpool.Pool, id string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM operations WHERE id = $1`, id).Scan(&n))
	return n
}

// seedPending кладёт незавершённую операцию с принципалом P через Ф1 в
// зафиксированной транзакции.
func seedPending(t *testing.T, h *ntf5Harness) operations.Operation {
	t.Helper()
	op := newOp(t)
	require.NoError(t, inTx(context.Background(), h.pool, func(tx pgx.Tx) error {
		return h.repo.CreatePendingTx(context.Background(), tx, op, principalP)
	}))
	return op
}

// (а) — (б): Ф1 и строка вызывающего в одной транзакции; меняется один факт —
// откат / фиксация.
func TestNTF5_122_a_b_PendingInCallerTx_RollbackVsCommit(t *testing.T) {
	h := newNTF5Harness(t)
	ctx := context.Background()
	errRollback := errors.New("caller rolls back")

	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit=%v", commit), func(t *testing.T) {
			e1 := newOp(t)
			err := inTx(ctx, h.pool, func(tx pgx.Tx) error {
				if err := h.repo.CreatePendingTx(ctx, tx, e1, principalP); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO t1 (id, v) VALUES ($1, 1)`, e1.ID); err != nil {
					return err
				}
				if !commit {
					return errRollback
				}
				return nil
			})
			got, gErr := h.repo.Get(ctx, e1.ID)
			if !commit {
				require.ErrorIs(t, err, errRollback)
				require.ErrorIs(t, gErr, operations.ErrNotFound, "(а) после отката операции E1 нет")
				assert.Zero(t, t1Rows(t, h.pool, e1.ID), "(а) строки своей таблицы нет")
				return
			}
			require.NoError(t, err)
			require.NoError(t, gErr, "(б) после фиксации операция E1 есть")
			assert.False(t, got.Done)
			assert.Equal(t, principalP, got.Principal)
			assert.Equal(t, 1, t1Rows(t, h.pool, e1.ID), "(б) строка своей таблицы есть")
		})
	}
}

// (в) — (г): Ф2 и оператор своей таблицы; меняется один факт — нарушение
// ограничения в той же транзакции есть / нет.
func TestNTF5_122_v_g_DoneInCallerTx_ConstraintViolationVsNone(t *testing.T) {
	h := newNTF5Harness(t)
	ctx := context.Background()

	for _, violate := range []bool{true, false} {
		t.Run(fmt.Sprintf("violate=%v", violate), func(t *testing.T) {
			e2 := newOp(t)
			resp := response(t, "e2-response")
			v := 1
			if violate {
				v = 0 // нарушает t1_v_positive
			}
			err := inTx(ctx, h.pool, func(tx pgx.Tx) error {
				if err := h.repo.CreateDoneTx(ctx, tx, e2, principalP, resp); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO t1 (id, v) VALUES ($1, $2)`, e2.ID, v)
				return err
			})
			got, gErr := h.repo.Get(ctx, e2.ID)
			if violate {
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr)
				require.Equal(t, "t1_v_positive", pgErr.ConstraintName)
				require.ErrorIs(t, gErr, operations.ErrNotFound, "(в) операции E2 нет")
				return
			}
			require.NoError(t, err)
			require.NoError(t, gErr, "(г) операция E2 есть")
			assert.True(t, got.Done)
			assert.Nil(t, got.Error)
			require.NotNil(t, got.Response)
			assert.Equal(t, resp.GetTypeUrl(), got.Response.GetTypeUrl())
			assert.Equal(t, resp.GetValue(), got.Response.GetValue())
			assert.Equal(t, principalP, got.Principal)
		})
	}
}

// (д1) — (е1), (д2) — (е2) и буквы CX5-53: обе функции создания отвергают
// любой принципал, который IsAnonymous, ошибкой ErrEmptyPrincipal и строки не
// пишут; запасного SystemPrincipal нет. Близнец — полная пара P, строка есть.
func TestNTF5_122_d_e_EmptyPrincipalIsRefusedByBothCreateFunctions(t *testing.T) {
	h := newNTF5Harness(t)
	ctx := context.Background()

	type createFn func(ctx context.Context, tx pgx.Tx, op operations.Operation, p operations.Principal) error
	fns := []struct {
		name     string
		wantDone bool
		fn       createFn
	}{
		{"CreatePendingTx(d1,e1)", false, h.repo.CreatePendingTx},
		{"CreateDoneTx(d2,e2)", true, func(ctx context.Context, tx pgx.Tx, op operations.Operation, p operations.Principal) error {
			return h.repo.CreateDoneTx(ctx, tx, op, p, response(t, "done"))
		}},
	}
	refused := []struct {
		name string
		p    operations.Principal
	}{
		{"empty", operations.Principal{}},
		{"user-without-id", operations.Principal{Type: "user", ID: ""}},
		{"display-name-only", operations.Principal{DisplayName: "someone"}},
		{"reserved-anonymous", operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID}},
	}

	for _, f := range fns {
		for _, r := range refused {
			t.Run(f.name+"/"+r.name, func(t *testing.T) {
				op := newOp(t)
				err := inTx(ctx, h.pool, func(tx pgx.Tx) error { return f.fn(ctx, tx, op, r.p) })
				require.ErrorIs(t, err, operations.ErrEmptyPrincipal)
				assert.Zero(t, opRows(t, h.pool, op.ID), "строки операции нет")
			})
		}
		t.Run(f.name+"/twin-full-pair", func(t *testing.T) {
			op := newOp(t)
			require.NoError(t, inTx(ctx, h.pool, func(tx pgx.Tx) error { return f.fn(ctx, tx, op, principalP) }))
			got, err := h.repo.Get(ctx, op.ID)
			require.NoError(t, err)
			assert.Equal(t, f.wantDone, got.Done)
			assert.Equal(t, principalP, got.Principal)
		})
	}

	var system int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT count(*) FROM operations WHERE principal_type = 'system'`).Scan(&system))
	assert.Zero(t, system, "принципала system в таблице нет: запасной SystemPrincipal не подставлен")
}

// Буква «nil вместо транзакции»: каждая из Ф1…Ф4 отказывает ErrNilTx до первого
// запроса, строк 0. Близнец — та же функция на настоящей транзакции проходит.
func TestNTF5_122_NilTxIsRefusedBeforeAnyQuery(t *testing.T) {
	h := newNTF5Harness(t)
	ctx := context.Background()
	pending := seedPending(t, h)

	before := rowCensus(t, h.pool)
	calls := map[string]func(tx pgx.Tx) error{
		"CreatePendingTx": func(tx pgx.Tx) error { return h.repo.CreatePendingTx(ctx, tx, newOp(t), principalP) },
		"CreateDoneTx": func(tx pgx.Tx) error {
			return h.repo.CreateDoneTx(ctx, tx, newOp(t), principalP, response(t, "r"))
		},
		"MarkDoneTx": func(tx pgx.Tx) error { return h.repo.MarkDoneTx(ctx, tx, pending.ID, response(t, "r")) },
		"MarkErrorTx": func(tx pgx.Tx) error {
			return h.repo.MarkErrorTx(ctx, tx, pending.ID, &status.Status{Code: 9, Message: "m"})
		},
	}
	for name, call := range calls {
		t.Run(name+"/nil", func(t *testing.T) {
			require.ErrorIs(t, call(nil), operations.ErrNilTx)
		})
	}
	assert.Empty(t, censusDelta(before, rowCensus(t, h.pool)), "nil вместо транзакции не пишет ни одной строки")

	// Близнец: та же функция на настоящей транзакции проходит.
	for _, name := range []string{"CreatePendingTx", "CreateDoneTx", "MarkDoneTx"} {
		t.Run(name+"/twin-real-tx", func(t *testing.T) {
			require.NoError(t, inTx(ctx, h.pool, calls[name]))
		})
	}
	t.Run("MarkErrorTx/twin-real-tx", func(t *testing.T) {
		other := seedPending(t, h)
		require.NoError(t, inTx(ctx, h.pool, func(tx pgx.Tx) error {
			return h.repo.MarkErrorTx(ctx, tx, other.ID, &status.Status{Code: 9, Message: "m"})
		}))
	})
}

// Классификация нуля строк у Ф3 и Ф4: строки нет — ErrNotFound; строка уже
// завершена — ErrAlreadyDone. Вызывающий по любой из них откатывается.
func TestNTF5_122_TerminalWritesClassifyZeroRows(t *testing.T) {
	h := newNTF5Harness(t)
	ctx := context.Background()

	missing := "ntf-missing-" + fmt.Sprint(time.Now().UnixNano())
	require.ErrorIs(t, inTx(ctx, h.pool, func(tx pgx.Tx) error {
		return h.repo.MarkDoneTx(ctx, tx, missing, response(t, "r"))
	}), operations.ErrNotFound)
	require.ErrorIs(t, inTx(ctx, h.pool, func(tx pgx.Tx) error {
		return h.repo.MarkErrorTx(ctx, tx, missing, &status.Status{Code: 9})
	}), operations.ErrNotFound)

	done := seedPending(t, h)
	require.NoError(t, inTx(ctx, h.pool, func(tx pgx.Tx) error {
		return h.repo.MarkDoneTx(ctx, tx, done.ID, response(t, "first"))
	}))
	require.ErrorIs(t, inTx(ctx, h.pool, func(tx pgx.Tx) error {
		return h.repo.MarkDoneTx(ctx, tx, done.ID, response(t, "second"))
	}), operations.ErrAlreadyDone)
	require.ErrorIs(t, inTx(ctx, h.pool, func(tx pgx.Tx) error {
		return h.repo.MarkErrorTx(ctx, tx, done.ID, &status.Status{Code: 9})
	}), operations.ErrAlreadyDone)
	got, err := h.repo.Get(ctx, done.ID)
	require.NoError(t, err)
	assert.Equal(t, "first", mustString(t, got.Response), "терминальная строка не перезаписана")
}

func mustString(t *testing.T, a *anypb.Any) string {
	t.Helper()
	require.NotNil(t, a)
	var s wrapperspb.StringValue
	require.NoError(t, a.UnmarshalTo(&s))
	return s.GetValue()
}

// raceOutcome — исход одного повтора гонки «терминальная запись в транзакции
// вызывающего против CancelOwned».
type raceOutcome struct {
	cancelErr error
	writeErr  error
}

// raceAgainstCancelOwned исполняет одновременно CancelOwned(op, owner(P)) и
// транзакцию вызывающего «terminal по op плюс строка T1, откат при нуле строк».
func raceAgainstCancelOwned(t *testing.T, h *ntf5Harness, op operations.Operation,
	terminal func(ctx context.Context, tx pgx.Tx, id string) error) raceOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var (
		out   raceOutcome
		wg    sync.WaitGroup
		start = make(chan struct{})
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, out.cancelErr = h.repo.CancelOwned(ctx, op.ID, operations.OwnerFromPrincipal(principalP))
	}()
	go func() {
		defer wg.Done()
		<-start
		out.writeErr = inTx(ctx, h.pool, func(tx pgx.Tx) error {
			if err := terminal(ctx, tx, op.ID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO t1 (id, v) VALUES ($1, 1)`, op.ID)
			return err
		})
	}()
	close(start)
	wg.Wait()
	return out
}

const raceRepeats = 20

// (ж) Ф3 против CancelOwned, 20 повторов: в каждом ровно один исход, ни одной
// ошибки сериализации, ни отменённой операции со строкой T1, ни успешной без неё.
func TestNTF5_122_zh_MarkDoneTxRacesCancelOwned(t *testing.T) {
	h := newNTF5Harness(t)
	ctx := context.Background()
	cancelled, written := 0, 0

	for i := 0; i < raceRepeats; i++ {
		e4 := seedPending(t, h)
		resp := response(t, "e4") // собран до гонки: require не зовётся из горутины
		out := raceAgainstCancelOwned(t, h, e4, func(ctx context.Context, tx pgx.Tx, id string) error {
			return h.repo.MarkDoneTx(ctx, tx, id, resp)
		})
		got, err := h.repo.Get(ctx, e4.ID)
		require.NoError(t, err)
		require.True(t, got.Done, "повтор %d: операция терминальна", i)

		switch {
		case out.cancelErr == nil && errors.Is(out.writeErr, operations.ErrAlreadyDone):
			cancelled++
			require.NotNil(t, got.Error, "повтор %d", i)
			assert.EqualValues(t, 1, got.Error.GetCode(), "повтор %d: отмена применена", i)
			assert.Nil(t, got.Response, "повтор %d", i)
			assert.Zero(t, t1Rows(t, h.pool, e4.ID), "повтор %d: отменённая операция без строки T1", i)
		case out.writeErr == nil && errors.Is(out.cancelErr, operations.ErrAlreadyDone):
			written++
			assert.Nil(t, got.Error, "повтор %d", i)
			assert.Equal(t, "e4", mustString(t, got.Response), "повтор %d", i)
			assert.Equal(t, 1, t1Rows(t, h.pool, e4.ID), "повтор %d: успешная операция со строкой T1", i)
		default:
			t.Fatalf("повтор %d: исход не из двух допустимых: CancelOwned=%v, транзакция=%v", i, out.cancelErr, out.writeErr)
		}
	}
	t.Logf("(ж) повторов %d: отмена применена %d, Ф3 применена %d", raceRepeats, cancelled, written)
}

// (к) Ф4 против CancelOwned, 20 повторов — то же для терминальной записи ошибки.
func TestNTF5_122_k_MarkErrorTxRacesCancelOwned(t *testing.T) {
	h := newNTF5Harness(t)
	ctx := context.Background()
	cancelled, written := 0, 0

	for i := 0; i < raceRepeats; i++ {
		e9 := seedPending(t, h)
		out := raceAgainstCancelOwned(t, h, e9, func(ctx context.Context, tx pgx.Tx, id string) error {
			return h.repo.MarkErrorTx(ctx, tx, id, &status.Status{Code: 9, Message: "m"})
		})
		got, err := h.repo.Get(ctx, e9.ID)
		require.NoError(t, err)
		require.True(t, got.Done, "повтор %d: операция терминальна", i)
		require.NotNil(t, got.Error, "повтор %d", i)

		switch {
		case out.cancelErr == nil && errors.Is(out.writeErr, operations.ErrAlreadyDone):
			cancelled++
			assert.EqualValues(t, 1, got.Error.GetCode(), "повтор %d: отмена применена", i)
			assert.Zero(t, t1Rows(t, h.pool, e9.ID), "повтор %d: строки T1 нет", i)
		case out.writeErr == nil && errors.Is(out.cancelErr, operations.ErrAlreadyDone):
			written++
			assert.EqualValues(t, 9, got.Error.GetCode(), "повтор %d: Ф4 применена", i)
			assert.Equal(t, 1, t1Rows(t, h.pool, e9.ID), "повтор %d: строка T1 есть", i)
		default:
			t.Fatalf("повтор %d: исход не из двух допустимых: CancelOwned=%v, транзакция=%v", i, out.cancelErr, out.writeErr)
		}
	}
	t.Logf("(к) повторов %d: отмена применена %d, Ф4 применена %d", raceRepeats, cancelled, written)
}

// (з) — (и): перепись строк всех таблиц базы обвязки до и после каждой из четырёх
// транзакций. (з) — меняется только таблица операций (Ф1, Ф2 — на одну строку,
// Ф3, Ф4 — ни на одну). (и) — вызывающий сам пишет строку T2, и перепись её
// видит: положительный контроль. Меняется один факт — вторая строка есть / нет.
func TestNTF5_122_z_i_EachFunctionWritesOnlyTheOperationRow(t *testing.T) {
	for _, callerWritesT2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("callerWritesT2=%v", callerWritesT2), func(t *testing.T) {
			h := newNTF5Harness(t)
			ctx := context.Background()
			e8 := seedPending(t, h)
			e6, e7 := newOp(t), newOp(t)

			steps := []struct {
				name    string
				opDelta int64
				call    func(tx pgx.Tx) error
			}{
				{"CreatePendingTx(E6)", 1, func(tx pgx.Tx) error { return h.repo.CreatePendingTx(ctx, tx, e6, principalP) }},
				{"CreateDoneTx(E7)", 1, func(tx pgx.Tx) error { return h.repo.CreateDoneTx(ctx, tx, e7, principalP, response(t, "e7")) }},
				{"MarkDoneTx(E6)", 0, func(tx pgx.Tx) error { return h.repo.MarkDoneTx(ctx, tx, e6.ID, response(t, "e6")) }},
				{"MarkErrorTx(E8)", 0, func(tx pgx.Tx) error {
					return h.repo.MarkErrorTx(ctx, tx, e8.ID, &status.Status{Code: 9, Message: "m"})
				}},
			}
			for i, s := range steps {
				before := rowCensus(t, h.pool)
				require.NoError(t, inTx(ctx, h.pool, func(tx pgx.Tx) error {
					if err := s.call(tx); err != nil {
						return err
					}
					if callerWritesT2 {
						_, err := tx.Exec(ctx, `INSERT INTO t2 (id) VALUES ($1)`, fmt.Sprintf("t2-%d", i))
						return err
					}
					return nil
				}), s.name)
				after := rowCensus(t, h.pool)

				want := map[string]int64{}
				if s.opDelta != 0 {
					want["public.operations"] = s.opDelta
				}
				if callerWritesT2 {
					want["public.t2"] = 1
				}
				assert.Equal(t, want, censusDelta(before, after),
					"%s: перепись строк по %d таблицам каталога", s.name, len(after))
			}

			got6, err := h.repo.Get(ctx, e6.ID)
			require.NoError(t, err)
			assert.True(t, got6.Done)
			assert.Equal(t, "e6", mustString(t, got6.Response))
			got7, err := h.repo.Get(ctx, e7.ID)
			require.NoError(t, err)
			assert.True(t, got7.Done)
			assert.Equal(t, "e7", mustString(t, got7.Response))
			got8, err := h.repo.Get(ctx, e8.ID)
			require.NoError(t, err)
			assert.True(t, got8.Done)
			require.NotNil(t, got8.Error)
			assert.EqualValues(t, 9, got8.Error.GetCode())
		})
	}
}
