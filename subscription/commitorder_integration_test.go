// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/kacho/cloud/subscription"
)

// =============================================================================
// WATCH-1-31 — НЕСУЩИЙ сценарий: возобновление не пропускает строку,
// закоммиченную ПОСЛЕ выдачи позиции.
// =============================================================================
//
// Номер выдаётся счётчиком на вставке, а строка становится ВИДИМОЙ на фиксации,
// значит порядок номеров и порядок фиксаций независимы. Подписчик возвращает
// позицию и перечитывает строго «дальше неё», поэтому строка, закоммиченная
// после строки с бо́льшим номером, обязана быть отдана ДО того, как позиция ушла
// за неё. Иначе событие теряется молча: ни возобновление, ни перепрос его не
// воспроизводят, и пропуска в нумерации клиент не видит.
//
// Подслучая три, и третий — тот, ради которого горизонт строится на блокировке
// таблицы, а не на снимке транзакций:
//
//  1. инверсный порядок фиксаций;
//  2. писатель ОТКАТИЛСЯ — номер потерян навсегда, поток обязан пойти дальше,
//     а не залипнуть на дыре (предмет — ЖИВОСТЬ, а не потеря);
//  3. писатель ЕЩЁ БЕЗ идентификатора транзакции: номер уже выдан, вставка не
//     началась. Такого писателя снимок транзакций не видит ВОВСЕ.
//
// Проба перенесена из `services/nlb/internal/repo/kacho/pg/lifecycle_feed_commit_order_integration_test.go`
// вместе с самой техникой. Она — единственный написанный в дереве ответ на этот
// класс, и перенос был условием фазы: снятие того файла до переноса уничтожило
// бы её.

// gateLockID — замок, на котором тест-триггер держит писателя ровно в состоянии
// «номер выдан, идентификатора транзакции ещё нет».
const gateLockID = 4243

const probeInsertSQL = `
	INSERT INTO probe_outbox (resource_kind, resource_id, event_type, payload)
	VALUES ('Network', $1, 'CREATED', '{"projectId":"prj-a"}'::jsonb)
	RETURNING sequence_no`

func TestResumeNeverSkipsARowCommittedAfterThePositionWasIssued(t *testing.T) {
	s := newStand(t, standOpts{})
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	admin := mustConnect(t, ctx, s.dsn)

	t.Run("инверсный порядок фиксаций отдаёт обе строки", func(t *testing.T) {
		sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{})
		position := sb.opened.GetPosition()

		// A берёт номер первым и остаётся в полёте.
		connA := mustConnect(t, ctx, s.dsn)
		txA, err := connA.Begin(ctx)
		mustNoErr(t, err)
		seqA := insertInTx(t, ctx, txA, "netA0000000000000001")

		// B берёт СЛЕДУЮЩИЙ номер и коммитится ПЕРВЫМ.
		connB := mustConnect(t, ctx, s.dsn)
		txB, err := connB.Begin(ctx)
		mustNoErr(t, err)
		seqB := insertInTx(t, ctx, txB, "netB0000000000000001")
		mustNoErr(t, txB.Commit(ctx))
		if seqB <= seqA {
			t.Fatalf("сценарий не воспроизведён: seqB=%d не больше seqA=%d", seqB, seqA)
		}

		// Пока A в полёте, поток НЕ ВПРАВЕ отдать B: за ним ещё может появиться
		// меньший номер. Молчание здесь — правильный исход, и оно проверяется.
		requireQuiet(t, sb)

		// A коммитится — в бою это будит поток его же уведомлением.
		mustNoErr(t, txA.Commit(ctx))

		got := recvEvents(t, sb, 2)
		if got[0].GetResourceId() != "netA0000000000000001" ||
			got[1].GetResourceId() != "netB0000000000000001" {
			t.Fatalf("отдано %q, %q — ожидались A и B по возрастанию номера",
				got[0].GetResourceId(), got[1].GetResourceId())
		}
		// И возобновление с ВЫДАННОЙ позиции даёт ровно то же: строка A не
		// пропущена, хотя её номер меньше номера B, а закоммичена она позже.
		sb2 := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{
			Start: &subscriptionv1.SubscriptionRequest_Position{Position: position},
		})
		again := recvEvents(t, sb2, 2)
		if again[0].GetResourceId() != "netA0000000000000001" {
			t.Fatalf("возобновление с выданной позиции потеряло A: пришло %q",
				again[0].GetResourceId())
		}
	})

	t.Run("откатившийся писатель не заклинивает поток", func(t *testing.T) {
		sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{})

		connA := mustConnect(t, ctx, s.dsn)
		txA, err := connA.Begin(ctx)
		mustNoErr(t, err)
		seqA := insertInTx(t, ctx, txA, "netR0000000000000001")

		connB := mustConnect(t, ctx, s.dsn)
		txB, err := connB.Begin(ctx)
		mustNoErr(t, err)
		seqB := insertInTx(t, ctx, txB, "netS0000000000000001")
		mustNoErr(t, txB.Commit(ctx))
		if seqB <= seqA {
			t.Fatalf("сценарий не воспроизведён: seqB=%d не больше seqA=%d", seqB, seqA)
		}

		// A откатывается: номера seqA не будет НИКОГДА. Граница обязана
		// перенестись ЗА дыру — иначе поток залипнет на номере, которого не
		// появится, и молчание станет вечным.
		mustNoErr(t, txA.Rollback(ctx))

		got := recvEvents(t, sb, 1)
		if got[0].GetResourceId() != "netS0000000000000001" {
			t.Fatalf("после отката отдано %q, ожидалась только строка B",
				got[0].GetResourceId())
		}
		requireQuiet(t, sb)
	})

	t.Run("писатель без идентификатора транзакции не пропускается", func(t *testing.T) {
		installGateTrigger(t, ctx, admin)

		gate := mustConnect(t, ctx, s.dsn)
		if _, err := gate.Exec(ctx, "SELECT pg_advisory_lock($1)", gateLockID); err != nil {
			t.Fatalf("замок не взят: %v", err)
		}
		released := false
		release := func() {
			if released {
				return
			}
			released = true
			if _, err := gate.Exec(ctx, "SELECT pg_advisory_unlock($1)", gateLockID); err != nil {
				t.Fatalf("замок не снят: %v", err)
			}
		}
		defer release()

		sb := s.open(t, ctx, &subscriptionv1.SubscriptionRequest{})

		connA := mustConnect(t, ctx, s.dsn)
		type result struct {
			seq int64
			err error
		}
		done := make(chan result, 1)
		go func() {
			tx, berr := connA.Begin(ctx)
			if berr != nil {
				done <- result{err: berr}
				return
			}
			var seq int64
			if qerr := tx.QueryRow(ctx, probeInsertSQL, "netG0000000000000001").Scan(&seq); qerr != nil {
				_ = tx.Rollback(ctx)
				done <- result{err: qerr}
				return
			}
			done <- result{seq: seq, err: tx.Commit(ctx)}
		}()

		// Барьер на НАБЛЮДАЕМОМ состоянии, а не на паузе: писатель уже держит
		// блокировку журнала и ещё НЕ имеет идентификатора транзакции.
		waitFor(t, 20*time.Second, "писатель обязан застрять с выданным номером и без идентификатора транзакции",
			func() bool {
				var lockHeld, xidHeld bool
				err := admin.QueryRow(ctx, `
					SELECT EXISTS (SELECT 1 FROM pg_locks
					                WHERE relation = 'probe_outbox'::regclass
					                  AND mode = 'RowExclusiveLock' AND granted
					                  AND pid <> pg_backend_pid()),
					       EXISTS (SELECT 1 FROM pg_locks x
					                JOIN pg_locks r ON r.pid = x.pid
					               WHERE x.locktype = 'transactionid' AND x.granted
					                 AND r.relation = 'probe_outbox'::regclass
					                 AND r.mode = 'RowExclusiveLock' AND r.granted)`).
					Scan(&lockHeld, &xidHeld)
				return err == nil && lockHeld && !xidHeld
			})

		// B берёт больший номер и коммитится, пока A висит без идентификатора.
		connB := mustConnect(t, ctx, s.dsn)
		txB, err := connB.Begin(ctx)
		mustNoErr(t, err)
		seqB := insertInTx(t, ctx, txB, "netH0000000000000001")
		mustNoErr(t, txB.Commit(ctx))

		// Поток не вправе отдать B: горизонт видит писателя ПО БЛОКИРОВКЕ, хотя
		// снимок транзакций его не видит вовсе.
		requireQuiet(t, sb)

		release()
		res := <-done
		mustNoErr(t, res.err)
		if seqB <= res.seq {
			t.Fatalf("сценарий не воспроизведён: seqB=%d не больше seqA=%d", seqB, res.seq)
		}

		got := recvEvents(t, sb, 2)
		if got[0].GetResourceId() != "netG0000000000000001" ||
			got[1].GetResourceId() != "netH0000000000000001" {
			t.Fatalf("отдано %q, %q — событие писателя без идентификатора транзакции пропущено",
				got[0].GetResourceId(), got[1].GetResourceId())
		}
	})
}

func mustConnect(t testing.TB, ctx context.Context, dsn string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("соединение: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	})
	return conn
}

func mustNoErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("неожиданный отказ: %v", err)
	}
}

func insertInTx(t testing.TB, ctx context.Context, tx pgx.Tx, id string) int64 {
	t.Helper()
	var seq int64
	if err := tx.QueryRow(ctx, probeInsertSQL, id).Scan(&seq); err != nil {
		t.Fatalf("вставка: %v", err)
	}
	return seq
}

func waitFor(t testing.TB, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("не дождались: %s", what)
}

// installGateTrigger ставит ТЕСТОВЫЙ триггер: строка с названным
// идентификатором виснет на замке ПОСЛЕ того, как счётчик выдал ей номер, и ДО
// вставки — то есть до назначения идентификатора транзакции.
func installGateTrigger(t testing.TB, ctx context.Context, admin *pgx.Conn) {
	t.Helper()
	_, err := admin.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION test_probe_gate() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.resource_id = 'netG0000000000000001' THEN
				PERFORM pg_advisory_xact_lock(%d);
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER test_probe_gate_trg BEFORE INSERT ON probe_outbox
			FOR EACH ROW EXECUTE FUNCTION test_probe_gate();`, gateLockID))
	if err != nil {
		t.Fatalf("тест-триггер не поставлен: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = admin.Exec(dropCtx, `
			DROP TRIGGER IF EXISTS test_probe_gate_trg ON probe_outbox;
			DROP FUNCTION IF EXISTS test_probe_gate();`)
	})
}
