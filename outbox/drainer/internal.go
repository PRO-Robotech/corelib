// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// shutdownCtx returns a bounded context that survives parent ctx cancellation
// (for finalising in-flight work during graceful shutdown) but won't hang
// forever if the DB is unreachable. ApplyTimeout (default 5s) is the bound.
//
// Use for tx.Commit / tx.Rollback / conn.Close — operations that must run to
// completion even after parent ctx cancel, but whose synchronous calls on a
// dead Postgres backend would otherwise block forever and defeat graceful
// shutdown.
func (d *Drainer[T]) shutdownCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), d.cfg.ApplyTimeout)
}

// listenLoop держит dedicated LISTEN-connection (hijacked из pool),
// слушает NOTIFY и сигналит wakeup-channel на каждое сообщение.
// При conn-drop — переоткрывает с exp-backoff (1s → 30s cap).
// Сразу после reconnect — non-blocking signal на wakeup, чтобы processLoop
// сделал catch-up (NOTIFY мог быть потерян во время disconnect-window).
func (d *Drainer[T]) listenLoop(ctx context.Context, wakeup chan<- struct{}) {
	d.reconnectLoop(ctx, d.listenOnce, wakeup, sleepFor)
}

const (
	initialListenBackoff = 1 * time.Second
	maxListenBackoff     = 30 * time.Second
)

// reconnectLoop drives session (one connect+LISTEN+serve cycle) with exponential
// reconnect backoff. session reports whether it established the subscription
// (connected) before returning. A connected session that later dropped resets
// the backoff to initialListenBackoff so a long-lived healthy subscription
// reconnects promptly instead of at the ratcheted maxListenBackoff cap
// (otherwise a handful of transient drops over the process lifetime pin the
// backoff at the cap and every subsequent single-conn hiccup re-establishes
// LISTEN up to maxListenBackoff late). sleep is injected so tests observe the
// requested wait deterministically; it returns false if ctx ended during the wait.
func (d *Drainer[T]) reconnectLoop(
	ctx context.Context,
	session func(context.Context, chan<- struct{}) (bool, error),
	wakeup chan<- struct{},
	sleep func(context.Context, time.Duration) bool,
) {
	backoff := initialListenBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		connected, err := session(ctx, wakeup)
		if ctx.Err() != nil {
			return
		}
		if connected {
			// Healthy session held the subscription then dropped — reset the cycle
			// so the next reconnect is prompt, not the ratcheted cap.
			backoff = initialListenBackoff
		}
		if err != nil {
			d.logger.Warn("listen_conn_drop",
				slog.String("err", err.Error()),
				slog.Duration("backoff", backoff))
			// Pool-conns to the same Postgres backend могут также быть мертвы
			// (FATAL admin-shutdown часто бьет по всем conn'ам этого процесса).
			// pgxpool's ShouldPing-default ждет IdleDuration > 1s, что слишком
			// долго — Reset() форсит destroy всех idle conn'ов сейчас, fresh
			// conn'ы будут созданы на следующем Acquire.
			d.pool.Reset()
		}
		if !sleep(ctx, backoff) {
			return
		}
		backoff *= 2
		if backoff > maxListenBackoff {
			backoff = maxListenBackoff
		}
		// После reconnect — сигналим wakeup, чтобы processLoop сделал
		// catch-up (мог пропустить NOTIFY-и во время disconnect).
		signalWakeup(wakeup)
	}
}

// sleepFor waits for d or until ctx is done. Returns false if ctx ended first.
func sleepFor(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// listenOnce — один цикл «подключиться, LISTEN, обрабатывать NOTIFY до conn-drop».
// Возвращает err при потере connection.
// The bool reports whether the subscription was established (LISTEN succeeded)
// before the returned error — reconnectLoop uses it to reset the backoff after a
// healthy session.
//
// РЕПЛИКИ: на-реплику — подписка будит дренаж СВОЕГО процесса и ничего не пишет. Развод здесь
// не нужен и вреден: неразбуженная реплика перестала бы дренажить вовсе,
// а разводит очередь клейм (см. Drainer.Run).
func (d *Drainer[T]) listenOnce(ctx context.Context, wakeup chan<- struct{}) (bool, error) {
	// Hijack — берем conn из pool и забираем владение (pool больше его не recycle'ит).
	// Это нужно потому, что LISTEN живет на одном connection и его нельзя
	// возвращать в pool (idle-connection-reset уничтожит state LISTEN-а).
	pconn, err := d.pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("pool.Acquire: %w", err)
	}
	conn := pconn.Hijack()
	defer func() {
		// parent is context.Background() (not the live ctx) because Run's ctx
		// may already be Done when this defer fires — we still want the close
		// attempt to have a real deadline, not block forever on a dead backend.
		closeCtx, cancel := d.shutdownCtx(context.Background())
		defer cancel()
		if err := conn.Close(closeCtx); err != nil {
			// Close-failure on a hijacked conn means the backend didn't
			// terminate cleanly — may leak a PG backend until
			// idle_in_transaction_session_timeout fires server-side.
			d.logger.Debug("listen_conn_close_failed", slog.String("err", err.Error()))
		}
	}()

	if _, err := conn.Exec(ctx, "LISTEN "+d.cfg.Channel); err != nil {
		return false, fmt.Errorf("LISTEN: %w", err)
	}
	d.logger.Debug("listen_connected")

	// После каждого свежего connect — будим processLoop (catch-up safety).
	signalWakeup(wakeup)

	for {
		notif, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return true, nil
			}
			// Wrap the leaf error to preserve the call-site (like the sibling
			// pool.Acquire / LISTEN paths above): listenLoop logs this to trigger
			// reconnect, and a bare pgx/network error is indistinguishable from an
			// Acquire/Exec failure when triaging reconnect churn from logs.
			return true, fmt.Errorf("WaitForNotification: %w", err)
		}
		if notif == nil {
			continue
		}
		// Payload игнорируем (id-конкретного row) — processLoop сделает
		// атомарный claim по всему батчу. Мы только сигналим «есть работа».
		signalWakeup(wakeup)
	}
}

// signalWakeup — non-blocking send на wakeup-channel.
// Если канал уже имеет один pending signal — игнорируем (processLoop еще не
// проснулся, все равно увидит «есть работа»).
func signalWakeup(c chan<- struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// claimedRow — то, что drainer получил из атомарного claim'а.
// Поле tx — открытая транзакция, которая держит row-lock (`FOR UPDATE SKIP LOCKED`)
// на время apply. После apply + mark — tx коммитится; на crash drainer'а —
// rollback и row становится доступной для re-claim другой репликой.
type claimedRow struct {
	id           int64
	eventType    string
	payload      []byte
	attemptCount int
	// Note: the pgx.Tx that holds the row-lock is returned by claimRows
	// alongside the []claimedRow batch (whole batch shares one tx). We don't
	// store tx per-row because that would suggest per-row independence.
}

// buildClaimQuery renders the claim SQL for the outbox table.
//
// ORDER BY (attempt_count, id) — fairness against transient head-of-line
// starvation. The transient handling caps a persistently-transient-failing row's
// attempt_count at MaxAttempts-1 so it stays claimable forever — under a plain
// `ORDER BY id` a backlog of such stuck low-id rows would permanently shadow a
// freshly enqueued higher-id intent (the small per-claim LIMIT never advances
// past them) and the new intent would never be delivered, breaking at-least-once
// under a sustained outage. Ordering by attempt_count first makes a fresh row
// (attempt_count=0) sort ahead of capped rows, so new intents are claimed
// promptly. FIFO for the happy path is preserved: equal attempt_count → id order.
//
// partitionExpr == "" (default) → the historical query, BYTE-for-byte unchanged
// (every consumer except iam fga_outbox keeps its exact prior behaviour).
//
// partitionExpr != "" → partition-head-only claim: a row t is claimable only if
// its partition (partitionExpr) has NO DELIVERABLE (sent_at IS NULL AND
// attempt_count < MaxAttempts) predecessor with a smaller id. This makes a
// successor never claimable ahead of an unsent predecessor of the same partition
// — cross-batch AND cross-replica (a peer's uncommitted claim is still
// sent_at IS NULL in this tx's snapshot, so the successor stays blocked until the
// peer commits the mark). Per-partition FIFO therefore holds by construction, and
// at most ONE row per partition is claimable in any snapshot (its head), so one
// claim batch never carries two rows of one partition → no intra-batch concurrent
// reorder within a partition. A POISONED predecessor (attempt_count = MaxAttempts)
// is excluded from the blocking set (`p.attempt_count < $1`): it will never apply,
// so it must not wedge its partition forever; a poisoned write creates no tuple,
// so skipping it introduces no over-grant. partitionExpr must begin with a column
// identifier so `<alias>.<partitionExpr>` is valid SQL (see Config.PartitionColumn).
func buildClaimQuery(table, partitionExpr string) string {
	if partitionExpr == "" {
		return fmt.Sprintf(`
			UPDATE %s
			   SET attempt_count = attempt_count + 1
			 WHERE id IN (
			     SELECT id FROM %s
			      WHERE sent_at IS NULL AND attempt_count < $1
			      ORDER BY attempt_count, id
			      FOR UPDATE SKIP LOCKED
			      LIMIT $2
			 )
			RETURNING id, event_type, payload, attempt_count
		`, table, table)
	}
	return fmt.Sprintf(`
		UPDATE %[1]s
		   SET attempt_count = attempt_count + 1
		 WHERE id IN (
		     SELECT t.id FROM %[1]s t
		      WHERE t.sent_at IS NULL AND t.attempt_count < $1
		        AND NOT EXISTS (
		            SELECT 1 FROM %[1]s p
		             WHERE p.sent_at IS NULL
		               AND p.attempt_count < $1
		               AND p.id < t.id
		               AND p.%[2]s = t.%[2]s
		        )
		      ORDER BY t.attempt_count, t.id
		      FOR UPDATE OF t SKIP LOCKED
		      LIMIT $2
		 )
		RETURNING id, event_type, payload, attempt_count
	`, table, partitionExpr)
}

// claimRows — атомарный pre-claim до `limit` pending rows.
// Открывает одну транзакцию, в ней `SELECT … FOR UPDATE SKIP LOCKED`,
// `UPDATE attempt_count` через RETURNING. Транзакция возвращается caller'у —
// он держит row-lock на время apply и КОММИТИТ/ROLLBACK'ит по результату.
//
// FOR UPDATE SKIP LOCKED + транзакция-держит-lock-на-время-apply — обеспечивает
// exactly-once across concurrent drainer replicas без race-window «claim →
// apply, но еще не markSuccess».
//
// На crash drainer'а conn drops → PG автоматически rollback'ит tx → row снова
// доступна для другого drainer'а. attempt_count инкрементнут в этом claim
// останется (увидится в логах), но это OK — attempt_count это «попытки», не
// «успехи».
//
// CAS-условие: sent_at IS NULL AND attempt_count < MaxAttempts (poisoned-skip).
//
// Возвращает (rows, tx, err). На err == nil И len(rows) > 0 — caller ОБЯЗАН
// finishClaim(tx, ...) после apply.
func (d *Drainer[T]) claimRows(ctx context.Context, limit int) ([]claimedRow, pgx.Tx, error) {
	if d.onClaim != nil {
		d.onClaim()
	}
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("begin claim tx: %w", err)
	}

	// rollback closure — bounded shutdown-tolerant ctx so a dead Postgres
	// backend cannot hang error paths indefinitely (graceful-shutdown safety).
	rollback := func() {
		rbCtx, cancel := d.shutdownCtx(ctx)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}

	q := buildClaimQuery(d.cfg.Table, d.cfg.PartitionColumn)

	rows, err := tx.Query(ctx, q, d.cfg.MaxAttempts, limit)
	if err != nil {
		rollback()
		return nil, nil, fmt.Errorf("claim query: %w", err)
	}

	var out []claimedRow
	for rows.Next() {
		var r claimedRow
		if err := rows.Scan(&r.id, &r.eventType, &r.payload, &r.attemptCount); err != nil {
			rows.Close()
			rollback()
			return nil, nil, fmt.Errorf("claim scan: %w", err)
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		rollback()
		return nil, nil, fmt.Errorf("claim rows: %w", err)
	}

	if len(out) == 0 {
		// Пусто — закрываем tx сразу, никакого row-lock не держим.
		rollback()
		return nil, nil, nil
	}
	return out, tx, nil
}

// markSuccess отмечает row как успешно примененную (sent_at = now(), last_error = NULL)
// в переданной транзакции (которая держит row-lock с момента claim'а).
func (d *Drainer[T]) markSuccess(ctx context.Context, tx pgx.Tx, id int64) error {
	q := fmt.Sprintf(
		`UPDATE %s SET sent_at = now(), last_error = NULL WHERE id = $1`,
		d.cfg.Table,
	)
	_, err := tx.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("mark success id=%d: %w", id, err)
	}
	return nil
}

// markPoisoned форсит attempt_count = MaxAttempts (drainer больше не переклеймит)
// и сохраняет last_error. Используется для permanent-errors (ErrPermanent) и
// decoder-fail. Выполняется в переданной транзакции.
func (d *Drainer[T]) markPoisoned(ctx context.Context, tx pgx.Tx, id int64, errMsg string) error {
	q := fmt.Sprintf(
		`UPDATE %s SET last_error = $1, attempt_count = $2 WHERE id = $3`,
		d.cfg.Table,
	)
	_, err := tx.Exec(ctx, q, truncErr(errMsg), d.cfg.MaxAttempts, id)
	if err != nil {
		return fmt.Errorf("mark poisoned id=%d: %w", id, err)
	}
	return nil
}

// markTransientFailure сохраняет last_error И гарантирует, что transient-класс
// НИКОГДА не отравит строку: attempt_count кап'ится ниже
// MaxAttempts (MaxAttempts-1), поэтому CAS-claim-условие `attempt_count <
// MaxAttempts` остается истинным сколько угодно долго — затяжной IAM-outage
// (Unavailable/timeout) ретраится unbounded с backoff, intent не теряется.
// attempt_count при этом продолжает расти до cap'а (питает exp-backoff и
// остается видимым в логах как «число попыток»). Выполняется в переданной tx.
func (d *Drainer[T]) markTransientFailure(ctx context.Context, tx pgx.Tx, id int64, errMsg string) error {
	// Cap at MaxAttempts-1 so the poison-gate (attempt_count < MaxAttempts) never
	// trips for a transient error. LEAST(...) keeps the existing increment for
	// rows still below the cap (backoff continues to grow), and clamps any row
	// that the claim pushed up to / past the cap back to MaxAttempts-1.
	capAttempt := d.cfg.MaxAttempts - 1
	if capAttempt < 0 {
		capAttempt = 0
	}
	q := fmt.Sprintf(
		`UPDATE %s SET last_error = $1, attempt_count = LEAST(attempt_count, $2) WHERE id = $3`,
		d.cfg.Table,
	)
	_, err := tx.Exec(ctx, q, truncErr(errMsg), capAttempt, id)
	if err != nil {
		return fmt.Errorf("mark transient id=%d: %w", id, err)
	}
	return nil
}

// truncErr ограничивает длину error-сообщения (хранить простыни stack-trace в
// last_error смысла нет; для debugging есть logger).
func truncErr(s string) string {
	const max = 2000
	if len(s) > max {
		return s[:max] + "...(truncated)"
	}
	return s
}

// drainBatch — один цикл «получить batch → обработать → отдать управление».
//
// Алгоритм:
//  1. Per-claim limit = claimLimit(): random [1, min(4, BatchSize)] для HA-fairness
//     (две реплики не сгребают одну волну за шот) ЛИБО ровно ApplyConcurrency, если
//     он >1 (полная параллельная apply-волна за один claim).
//  2. Обрабатываем claim'd rows в транзакции, которая держит row-lock на
//     время apply (FOR UPDATE SKIP LOCKED) — exactly-once guarantee
//     (другой drainer SKIP'нет lock'нутые rows до commit'а). При ApplyConcurrency>1
//     apply-вызовы батча идут ПАРАЛЛЕЛЬНО (внешние, без tx), mark'и — последовательно.
//  3. После commit — короткий jitter (0-10ms) перед следующим claim,
//     дает другому drainer'у шанс на следующую волну.
//  4. Если claim вернул 0 rows — выходим (вернемся в select main-loop ждать
//     NOTIFY/tick).
//  5. На ctx.Done() — корректно прерываемся, но in-flight Apply защищен
//     ApplyTimeout-grace (см. processRowInTx).
//
// Все ошибки логируются; функция не возвращает err — drainer-loop устойчив.
//
// РЕПЛИКИ: клейм — партия уже взята клеймом с пропуском занятых, поэтому её строки
// принадлежат только этой реплике; петля отсрочки внутри партии их не делит.
func (d *Drainer[T]) drainBatch(ctx context.Context) {
	d.maybeReportWedge(ctx)
	iter := 0
	for {
		if ctx.Err() != nil {
			return
		}

		rows, tx, err := d.claimRows(ctx, d.claimLimit())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.logger.Warn("claim_batch_failed", slog.String("err", err.Error()))
			return
		}
		if len(rows) == 0 {
			return // tx уже rollback'нут в claimRows
		}

		// Обрабатываем batch внутри одной транзакции (держит row-lock).
		// exactly-once инвариант ОДИНАКОВ для обоих путей: claim-tx держит
		// FOR UPDATE SKIP LOCKED lock КАЖДОЙ строки батча до commit'а; apply не
		// трогает DB. ApplyConcurrency>1 лишь распараллеливает ВНЕШНИЕ apply-вызовы
		// (mark'и всегда последовательны на единственной tx — pgx.Tx не
		// конкурентно-безопасна).
		retry := make([]bool, len(rows))
		needsRetryAfter := false
		if d.cfg.ApplyConcurrency > 1 {
			// Phase 1: параллельный apply (только внешние вызовы, БЕЗ доступа к tx;
			// батч сайзнут ≤ ApplyConcurrency, поэтому fan-out ограничен). Никаких
			// доп. conn'ов пула: apply — gRPC/HTTP к target, не запрос к своей БД.
			outcomes := make([]applyOutcome, len(rows))
			var wg sync.WaitGroup
			for i := range rows {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					outcomes[i] = d.applyRow(ctx, rows[i])
				}(i)
			}
			wg.Wait()
			// Phase 2: последовательный mark на общей claim-tx.
			for i := range rows {
				retry[i] = d.markRow(ctx, tx, rows[i], outcomes[i])
				if retry[i] {
					needsRetryAfter = true
				}
			}
		} else {
			for i, r := range rows {
				retry[i] = d.processRowInTx(ctx, tx, r)
				if retry[i] {
					needsRetryAfter = true
				}
			}
		}
		// Bounded shutdown-tolerant ctx for commit: survives parent ctx cancel
		// (we must finalise the in-flight batch) but won't hang forever if the
		// Postgres backend is dead.
		commitCtx, commitCancel := d.shutdownCtx(ctx)
		if err := tx.Commit(commitCtx); err != nil {
			// Commit fail — все sent_at-mark'и потеряны → row'ы остаются
			// pending (attempt_count уже инкрементнут). Будут переклейм'ены.
			d.logger.Warn("commit_batch_failed", slog.String("err", err.Error()))
		}
		commitCancel()

		// Если транзитные ошибки — sleep backoff чтобы не загрузить FGA
		// серией мгновенных retry-ев. Размер backoff — по attempt_count
		// САМОЙ-ретраенной transient-row батча (не rows[0], который под
		// `ORDER BY attempt_count` — наименее-ретраенная row, часто свежий
		// успех), иначе застрявшая tail-row переклеймится с BackoffMin-каденсом
		// именно во время partial-outage, ради которого backoff и существует.
		if needsRetryAfter {
			sleep := expBackoff(backoffAttemptCount(rows, retry), d.cfg.BackoffMin, d.cfg.BackoffMax)
			select {
			case <-time.After(sleep):
			case <-ctx.Done():
				return
			}
		} else if iter > 0 {
			// Tiny inter-batch jitter (0-10ms) дает другой реплике шанс
			// claim'нуть следующий batch. Применяется только после первой
			// итерации чтобы не задерживать первую обработку single-drainer.
			yieldJitter := interBatchJitter()
			if yieldJitter > 0 {
				select {
				case <-time.After(yieldJitter):
				case <-ctx.Done():
					return
				}
			}
		}
		iter++
	}
}

// wedgeReportLimit caps how many wedged partitions ONE scan reports.
//
// The observer's job is ATTRIBUTION — "which partition is stuck" — and the answer
// is the oldest few heads; "how much is stuck" is already answered, without
// cardinality, by the table-wide backlog-depth and oldest-pending-age gauges. The
// cap matters because the partition key is deliberately the NARROWEST
// non-commutative key (see Config.PartitionColumn), so partition count scales with
// distinct in-flight keys, not with resources: on a live stand 8 643 pending rows
// spanned 8 641 distinct tuples. During a peer outage every one of them is past the
// threshold, and an uncapped report would emit thousands of WARN lines per scan —
// burying the very attribution it exists to provide. Ten oldest is enough to name
// the head that is blocking, and it bounds the scan's output as the key narrows
// further.
const wedgeReportLimit = 10

// maybeReportWedge surfaces partitions whose oldest unsent row is older than
// Config.WedgeWarnAfter (head-of-line wedge attribution), oldest first and at most
// wedgeReportLimit of them. No-op unless an onWedge observer is registered AND
// PartitionColumn + WedgeWarnAfter>0 are set. Rate-limited to at most once per
// WedgeWarnAfter (called from the single drainBatch goroutine, so lastWedgeCheck
// needs no synchronisation). Best-effort: a failed scan is logged at Debug and
// never disturbs the drain.
func (d *Drainer[T]) maybeReportWedge(ctx context.Context) {
	if d.onWedge == nil || d.cfg.PartitionColumn == "" || d.cfg.WedgeWarnAfter <= 0 {
		return
	}
	if !d.lastWedgeCheck.IsZero() && time.Since(d.lastWedgeCheck) < d.cfg.WedgeWarnAfter {
		return
	}
	d.lastWedgeCheck = time.Now()

	// Oldest unsent row per partition, filtered to those exceeding the threshold and
	// ordered oldest-first so the cap keeps the WORST offenders rather than an
	// arbitrary sample. Uses the same partial index as the claim
	// ((<PartitionColumn>), id) WHERE sent_at IS NULL. NULL partition keys are
	// excluded (they carry no ordering). The key is rendered as text so any
	// partition expression — not just a text column — scans into a string.
	q := fmt.Sprintf(`
		SELECT (%[2]s)::text AS part,
		       EXTRACT(EPOCH FROM (now() - min(created_at)))::float8 AS age_s
		  FROM %[1]s
		 WHERE sent_at IS NULL AND (%[2]s) IS NOT NULL
		 GROUP BY %[2]s
		HAVING min(created_at) < now() - make_interval(secs => $1)
		 ORDER BY min(created_at)
		 LIMIT %[3]d
	`, d.cfg.Table, d.cfg.PartitionColumn, wedgeReportLimit)

	rows, err := d.pool.Query(ctx, q, d.cfg.WedgeWarnAfter.Seconds())
	if err != nil {
		d.logger.Debug("wedge_scan_failed", slog.String("err", err.Error()))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var part string
		var ageS float64
		if err := rows.Scan(&part, &ageS); err != nil {
			d.logger.Debug("wedge_scan_row_failed", slog.String("err", err.Error()))
			return
		}
		d.onWedge(part, time.Duration(ageS*float64(time.Second)))
	}
	if err := rows.Err(); err != nil {
		d.logger.Debug("wedge_scan_iter_failed", slog.String("err", err.Error()))
	}
}

// interBatchJitter returns a random inter-batch yield window in [0,10ms],
// matching drainBatch step-3 godoc. Applied between claim iterations to give
// another HA replica a chance to claim the next batch before this drainer loops.
func interBatchJitter() time.Duration {
	// math/rand достаточно: джиттер планирования, не крипто (см. drainBatch).
	return time.Duration(rand.IntN(11)) * time.Millisecond // #nosec G404 -- джиттер HA-справедливости
}

// claimLimit returns the per-claim row LIMIT.
//   - ApplyConcurrency>1: the batch is sized EXACTLY to the concurrency so one
//     claim feeds a full parallel apply-wave (len(rows) ≤ ApplyConcurrency bounds
//     the goroutine fan-out with no separate semaphore).
//   - otherwise (default): a random small batch [1, min(4, BatchSize)] preserving
//     HA-fairness (two replicas don't grab one wave in a single shot — see
//     drainBatch godoc). math/rand is fine here: HA-fairness, not crypto — a
//     predictable batch size grants an attacker nothing (it gates neither access
//     nor content), and crypto/rand would cost a syscall per claim iteration.
func (d *Drainer[T]) claimLimit() int {
	if d.cfg.ApplyConcurrency > 1 {
		return d.cfg.ApplyConcurrency
	}
	return 1 + rand.IntN(haFairnessLimit(d.cfg.BatchSize)) // #nosec G404 -- HA-fairness, не крипто
}

// haFairnessLimit returns max(1, min(4, batchSize)) — upper bound для random
// per-claim LIMIT. Меньше = лучше HA-fairness; больше = выше catch-up throughput.
// 4 — компромисс: 32 BatchSize / 4 = 8 claim-итераций для дренажа полной волны,
// при HA две реплики получают шанс взять каждый claim.
func haFairnessLimit(batchSize int) int {
	if batchSize <= 1 {
		return 1
	}
	if batchSize < 4 {
		return batchSize
	}
	return 4
}

// applyOutcome carries the result of the apply-phase (decode + Apply) of one row
// to the mark-phase. It holds NO DB handle, so the apply-phase can run
// concurrently across rows of one claim-batch (ApplyConcurrency>1) while the
// mark-phase stays sequential on the single claim-tx.
type applyOutcome struct {
	decodeErr error // non-nil → decode failed (permanent poison), applyErr unused
	applyErr  error // classify with Classify(); nil = success
}

// applyRow runs the APPLY phase of one row: decode → Apply. It touches NO
// transaction / DB state, only the external target via d.applier, so it is safe
// to invoke concurrently for distinct rows of one claim-batch. Apply uses a
// detached (WithoutCancel) ApplyTimeout budget for graceful-shutdown grace: an
// in-flight apply finishes within ApplyTimeout even if parentCtx is cancelled.
func (d *Drainer[T]) applyRow(parentCtx context.Context, r claimedRow) applyOutcome {
	payload, derr := d.decoder(r.payload)
	if derr != nil {
		return applyOutcome{decodeErr: derr}
	}
	applyCtx, applyCancel := context.WithTimeout(
		context.WithoutCancel(parentCtx),
		d.cfg.ApplyTimeout,
	)
	defer applyCancel()
	return applyOutcome{applyErr: d.applier(applyCtx, r.eventType, payload)}
}

// markRow runs the MARK phase of one row on the claim-batch tx (which holds the
// row's FOR UPDATE SKIP LOCKED lock since claim). It is invoked SEQUENTIALLY per
// batch (a pgx.Tx is not safe for concurrent use), after that row's applyRow has
// returned. Returns true if the row needs retry-backoff (transient).
//
// The mark budget (dbCtx) is minted FRESH here — after apply returned —
// deliberately NOT sharing a deadline with the apply. Both are detached
// (WithoutCancel) for graceful-shutdown grace, but they must NOT share a
// deadline: an apply that consumes the WHOLE ApplyTimeout (a hung peer that only
// errors at its own deadline) would otherwise leave the mark's ctx already
// expired ("context already done"). Then markTransientFailure never lands its
// attempt_count cap, the claim's attempt_count++ commits unbounded, and a
// merely-slow *transient* row climbs into the poison gate — false-poisoned, its
// intent lost forever (violating the transient no-poison rule). A fresh budget
// per mark guarantees the outcome is always recorded.
func (d *Drainer[T]) markRow(parentCtx context.Context, tx pgx.Tx, r claimedRow, out applyOutcome) bool {
	dbCtx, dbCancel := context.WithTimeout(context.WithoutCancel(parentCtx), d.cfg.ApplyTimeout)
	defer dbCancel()

	// Единственная точка принятия решения — DecideOutcome: она читает ОБА исхода
	// (разбор и применение) и политику очереди. Ветвление здесь только исполняет
	// принятое: временные отказы (Unavailable/timeout/conn) НИКОГДА не травят
	// (markTransientFailure держит attempt_count ниже порога), а постоянный отказ
	// применения травит либо повторяется — по политике (см. PermanentPolicy).
	failErr := out.decodeErr
	if failErr == nil {
		failErr = out.applyErr
	}
	switch DecideOutcome(out.decodeErr, out.applyErr, d.cfg.PermanentPolicy) {
	case DispositionDeliver:
		if Classify(out.applyErr) == ClassAlreadyApplied {
			d.logger.Debug("target_already_applied",
				slog.Int64("id", r.id), slog.String("event_type", r.eventType))
		}
		if err := d.markSuccess(dbCtx, tx, r.id); err != nil {
			d.logger.Error("mark_success_failed",
				slog.Int64("id", r.id), slog.String("err", err.Error()))
		}
		// Доставка наблюдается ЗДЕСЬ, а не сканом живых строк: скан считает то,
		// что ЛЕЖИТ, а величина объявлена «за всё время» (#1714). Наблюдатель
		// зовётся и тогда, когда пометка не удалась: строка применена у
		// адресата, и это факт о доставке, а не о нашей записи о ней, —
		// непомеченная строка приедет повторно и будет отброшена как
		// уже-применённая, но событие доставки уже состоялось.
		if d.onDeliver != nil {
			d.onDeliver(r.eventType)
		}
		return false
	case DispositionPoison:
		event := "apply_permanent_poison"
		if out.decodeErr != nil {
			event = "decode_failed_poison"
		}
		d.logger.Warn(event,
			slog.Int64("id", r.id),
			slog.String("event_type", r.eventType),
			slog.String("err", failErr.Error()))
		d.poison(dbCtx, tx, r.id, failErr.Error())
		return false
	default: // DispositionRetry — не травим; повтор с отступом.
		// Постоянный отказ у очереди с политикой повтора обязан быть ГРОМЧЕ
		// временного: он и есть та настройка, которую чинят руками, а частота
		// повторов сама по себе о нём не сообщает (security.md §8 — «мягкий
		// проход обязан отличать настройку от сбоя»).
		if Classify(out.applyErr) == ClassPermanent {
			d.logger.Warn("apply_permanent_retry",
				slog.Int64("id", r.id),
				slog.String("event_type", r.eventType),
				slog.Int("attempt", r.attemptCount),
				slog.String("err", failErr.Error()))
		} else {
			d.logger.Debug("apply_transient_retry",
				slog.Int64("id", r.id),
				slog.Int("attempt", r.attemptCount),
				slog.String("err", failErr.Error()))
		}
		if err := d.markTransientFailure(dbCtx, tx, r.id, failErr.Error()); err != nil {
			d.logger.Error("mark_transient_failed",
				slog.Int64("id", r.id), slog.String("err", err.Error()))
		}
		return true
	}
}

// processRowInTx — sequential apply+mark of one row (the ApplyConcurrency<=1 /
// default path). Behaviour is byte-equivalent to the historical inlined version:
// applyRow (decode+Apply) then markRow (classify+mark on tx).
func (d *Drainer[T]) processRowInTx(parentCtx context.Context, tx pgx.Tx, r claimedRow) bool {
	return d.markRow(parentCtx, tx, r, d.applyRow(parentCtx, r))
}

// poison marks the row poisoned (attempt_count = MaxAttempts) and notifies the
// optional poison observer (metrics: outbox_poisoned_total). Used for permanent
// applier errors and decoder failures only — never for transient errors.
func (d *Drainer[T]) poison(ctx context.Context, tx pgx.Tx, id int64, errMsg string) {
	if err := d.markPoisoned(ctx, tx, id, errMsg); err != nil {
		d.logger.Error("mark_poisoned_failed",
			slog.Int64("id", id), slog.String("err", err.Error()))
		return
	}
	if d.onPoison != nil {
		d.onPoison()
	}
}

// backoffAttemptCount returns the largest attempt_count among the batch rows
// flagged transient (retry[i]==true) — the pacing input for the post-batch retry
// backoff. It must NOT be sized off rows[0]: claimRows orders pending rows
// `ORDER BY attempt_count, id`, so rows[0] is the LEAST-attempted row of the
// batch (often a fresh CREATED intent that applied successfully), and pacing a
// mixed batch off it would re-claim a persistently-failing tail row at BackoffMin
// cadence during a partial outage — exactly when the exponential backoff is meant
// to cushion the target. Returns 0 when no row is transient.
func backoffAttemptCount(rows []claimedRow, retry []bool) int {
	attempt := 0
	for i, r := range rows {
		if retry[i] && r.attemptCount > attempt {
			attempt = r.attemptCount
		}
	}
	return attempt
}

// expBackoff = min(base * 2^(attempt-1), max). attempt — 1-based.
// attempt=1 → base; attempt=2 → 2*base; attempt=3 → 4*base; clamp на max.
func expBackoff(attempt int, base, max time.Duration) time.Duration {
	if attempt <= 1 {
		return base
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return d
}
