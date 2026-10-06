// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/validate"
)

// Ядро ленты (замысел issue-2924 З28 п.1): у двух точек входа — сервера ленты
// (Server, по сети) и взятия в процессе (Local) — одно ядро взятия и записи
// исхода. SQL взятия есть только в claimTx, SQL записи исхода — только в
// recordOutcome; точки входа открывают транзакцию на своём пуле и зовут их.
// Поэтому условие головы нити, условие срока и наблюдатель исхода действуют
// на обеих точках сразу (CX5-54, CX5-55).

// noActiveLease — «у строки нет действующей аренды» по часам базы (замысел
// issue-2924 З8 п.5, M18): ОДНА формула ленты. Её подставляют взятие, голова
// нити, Supersede и DeleteUnleased константным выражением форматной строки;
// предиката «lease_until IS NULL» отдельно в пакете нет. Уборщик истечения
// несёт тот же текст в литерале оператора NTF-1 дословно (expireSQL): гейт
// NTF1-B25 читает литерал и судит в нём ровно эту формулу
// (TestNTF1B25_NoExpiryWithoutTheLeaseCondition). Столбец без псевдонима: в
// подзапросе он разрешается ближайшей таблицей FROM — строкой подзапроса.
const noActiveLease = "(lease_until IS NULL OR lease_until <= now())"

// TxDB — пул точки входа ленты: операторы и транзакции. Каждый Claim и Ack —
// одна короткая транзакция под storeCallTimeout; соединение не удерживается
// между вызовами (NTF1-B18).
type TxDB interface {
	DB
	Begin(ctx context.Context) (pgx.Tx, error)
}

// claimQuery — взятие: лента службы svc, классы словами колонки class, не
// больше limit строк.
type claimQuery struct {
	svc     string
	classes []string
	limit   int64
}

// claimedRow — строка, арендованная claimTx. expiresLeft — остаток срока;
// nil — у строки срока нет (класс obligation).
type claimedRow struct {
	id, token, template, address string
	class                        Class
	rev                          uint32
	attrs, secret                []byte
	threadKey                    *string
	enqueued                     time.Time
	leaseLeft                    time.Duration
	expiresLeft                  *time.Duration
}

// claimTx — оператор взятия (замысел NTF-1 З8, CX1-25): отбор под замком
// строки с SKIP LOCKED и перевод аренды в том же операторе. Условия отбора:
// pending, класс из запроса, срок не истёк либо его нет (замысел issue-2924
// З10 п.3 — пятый читатель срока), отсрочка прошла, нет действующей аренды и
// строка — голова своей нити (З12 п.1): в ленте нет другой pending-строки того
// же ключа, которая под действующей арендой либо стоит раньше в порядке
// взятия. Голова нити стоит в одном операторе, поэтому в полёте не больше
// одной строки нити при любой смеси точек входа. Остатки аренды и срока
// считает база (обе стороны — её часы, УК71), в микросекундах; у строки без
// срока остаток — NULL.
func claimTx(ctx context.Context, tx pgx.Tx, q claimQuery) ([]claimedRow, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(`WITH c AS (
  SELECT id FROM %[1]s t
   WHERE state = 'pending' AND class = ANY($1) AND (expires_at IS NULL OR expires_at > now())
     AND (not_before IS NULL OR not_before <= now())
     AND `+noActiveLease+`
     AND (t.thread_key IS NULL OR NOT EXISTS (
          SELECT 1 FROM %[1]s o
           WHERE o.thread_key = t.thread_key AND o.id <> t.id AND o.state = 'pending'
             AND (NOT `+noActiveLease+` OR (o.enqueued_at, o.id) < (t.enqueued_at, t.id))))
   ORDER BY enqueued_at, id
   LIMIT $2
     FOR UPDATE SKIP LOCKED)
UPDATE %[1]s t
   SET lease_token = gen_random_uuid(), lease_until = now() + make_interval(secs => $3),
       first_claimed_at = coalesce(t.first_claimed_at, now())
  FROM c
 WHERE t.id = c.id
RETURNING t.id, t.lease_token::text, t.template, t.schema_rev, t.class, t.recipient_address, t.attrs, t.secret_attrs,
          t.thread_key, t.enqueued_at,
          (extract(epoch FROM t.lease_until - now()) * 1000000)::bigint,
          (extract(epoch FROM t.expires_at - now()) * 1000000)::bigint`, tablename.Of(q.svc, tablename.Outbox)),
		q.classes, q.limit, LeaseTTL.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var got []claimedRow
	for rows.Next() {
		var (
			r         claimedRow
			class     string
			leaseUS   int64
			expiresUS *int64
		)
		if err := rows.Scan(&r.id, &r.token, &r.template, &r.rev, &class, &r.address, &r.attrs, &r.secret,
			&r.threadKey, &r.enqueued, &leaseUS, &expiresUS); err != nil {
			return nil, err
		}
		r.class = Class(class)
		r.leaseLeft = time.Duration(leaseUS) * time.Microsecond
		if expiresUS != nil {
			left := time.Duration(*expiresUS) * time.Microsecond
			r.expiresLeft = &left
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Порядок ответа — порядок взятия: момент постановки, затем id.
	sort.Slice(got, func(i, j int) bool {
		if !got[i].enqueued.Equal(got[j].enqueued) {
			return got[i].enqueued.Before(got[j].enqueued)
		}
		return got[i].id < got[j].id
	})
	return got, nil
}

// ackInput — запись исхода, разобранная точкой входа до SQL. deferSet —
// отсрочка передана; deferMalformed — передана, но не длительность.
type ackInput struct {
	id, token      string
	outcome        Outcome
	deferSet       bool
	deferMalformed bool
	deferFor       time.Duration
}

// ackViolation — нарушение границ записи исхода: поле и правило словами
// контракта. badID — id не той формы (отказ не поля, а id ресурса).
type ackViolation struct {
	field, rule string
	badID       bool
}

func (v *ackViolation) Error() string { return v.field + ": " + v.rule }

// wireKind, wireReason — имена вида и причины в написании контракта: ими
// называет нарушение текст отказа.
func wireKind(k Kind) string { return strings.ToUpper(string(k)) }

func wireReason(r Reason) string {
	if r == ReasonNone {
		return "OUTCOME_REASON_UNSPECIFIED"
	}
	return strings.ToUpper(string(r))
}

// validateAck — границы записи исхода в объявленном порядке, до SQL (замысел
// NTF-1 З9; NTF1-B14, B22, УК72, УК73, CX1-26 (г)): id, токен как UUID, вид,
// причина из таблицы ackReasons, defer_for в [MinDefer, MaxDefer] только при
// DEFER. Её зовут обе точки входа.
func validateAck(a ackInput) *ackViolation {
	if a.id == "" {
		return &ackViolation{field: "id", rule: "required"}
	}
	if err := validate.ResourceID("notification", ids.PrefixNotificationHyphen, a.id); err != nil {
		return &ackViolation{field: "id", rule: "invalid notification id", badID: true}
	}
	if a.token == "" {
		return &ackViolation{field: "lease_token", rule: "required"}
	}
	if !uuidForm.MatchString(a.token) {
		return &ackViolation{field: "lease_token", rule: "must be a UUID"}
	}
	if a.outcome.Kind == "" {
		return &ackViolation{field: "outcome", rule: "required"}
	}
	if a.outcome.Kind == KindExpired {
		return &ackViolation{field: "outcome.kind", rule: "EXPIRED is set by the source only"}
	}
	reasons, known := ackReasons()[a.outcome.Kind]
	if !known {
		return &ackViolation{field: "outcome.kind", rule: wireKind(a.outcome.Kind) + " is not an outcome kind"}
	}
	if !slices.Contains(reasons, a.outcome.Reason) {
		return &ackViolation{field: "outcome.reason",
			rule: fmt.Sprintf("%s is not allowed with %s", wireReason(a.outcome.Reason), wireKind(a.outcome.Kind))}
	}
	if a.outcome.Kind != KindDefer {
		if a.deferSet {
			return &ackViolation{field: "defer_for", rule: "must not be set unless outcome.kind is DEFER"}
		}
		return nil
	}
	if !a.deferSet {
		return &ackViolation{field: "defer_for", rule: "required"}
	}
	if a.deferMalformed || a.deferFor < MinDefer || a.deferFor > MaxDefer {
		return &ackViolation{field: "defer_for", rule: "must be in [1s..15m]"}
	}
	return nil
}

// ackVerdict — исход записи исхода.
type ackVerdict int

const (
	// ackRecorded — оператор изменил строку; наблюдатель позван.
	ackRecorded ackVerdict = iota + 1
	// ackRepeated — тот же токен и та же пара: успех без изменения строки.
	ackRepeated
	// ackAlreadyRecorded — тем же токеном записан другой исход.
	ackAlreadyRecorded
	// ackLeaseLost — аренда утрачена.
	ackLeaseLost
	// ackNotFound — строки нет.
	ackNotFound
)

// ackResult — исход recordOutcome; class — класс изменённой строки (только у
// ackRecorded).
type ackResult struct {
	verdict ackVerdict
	class   Class
}

// recordOutcome — запись исхода строки (замысел NTF-1 З9, CX1-26 (а), (б)):
// один условный оператор по первичному ключу и токену действующей аренды.
// Терминальный вид закрывает строку, стирает секрет и ставит момент исхода;
// DEFER оставляет строку pending с «не раньше» now() + defer_for. При любом
// виде аренда снимается, а записанная пара и токен сохраняются для
// классификации повтора. В ветке, изменившей строку, последним действием
// зовётся obs в той же транзакции. Ноль строк — одно чтение и классификация
// в объявленном порядке: строки нет; тот же токен и та же пара — повтор; тот
// же токен и иная пара; иначе аренда утрачена. Срок аренды в классификацию
// не входит (повтор после конца аренды — успех).
func recordOutcome(ctx context.Context, tx pgx.Tx, svc string, a ackInput, obs OutcomeObserver) (ackResult, error) {
	var (
		class, template string
		thread          *string
	)
	err := tx.QueryRow(ctx, fmt.Sprintf(`UPDATE %[1]s
   SET state = CASE WHEN $3 = 'defer' THEN state ELSE $3 END,
       outcome_reason = CASE WHEN $3 = 'defer' THEN outcome_reason ELSE nullif($4, '') END,
       outcome_at = CASE WHEN $3 = 'defer' THEN outcome_at ELSE now() END,
       secret_attrs = CASE WHEN $3 = 'defer' THEN secret_attrs END,
       last_defer_reason = CASE WHEN $3 = 'defer' THEN $4 ELSE last_defer_reason END,
       not_before = CASE WHEN $3 = 'defer' THEN now() + make_interval(secs => $5) ELSE not_before END,
       recorded_kind = $3, recorded_reason = nullif($4, ''), outcome_token = $2::uuid,
       lease_token = NULL, lease_until = NULL
 WHERE id = $1 AND lease_token = $2::uuid AND lease_until > now() AND state = 'pending'
RETURNING class, template, thread_key`, tablename.Of(svc, tablename.Outbox)),
		a.id, a.token, string(a.outcome.Kind), string(a.outcome.Reason), a.deferFor.Seconds()).Scan(&class, &template, &thread)
	switch {
	case err == nil:
		row := AckedRow{ID: a.id, Class: Class(class), Template: template, Outcome: a.outcome, ThreadKey: thread}
		if err := obs.ObserveOutcome(ctx, tx, row); err != nil {
			return ackResult{}, fmt.Errorf("feed: наблюдатель исхода строки %s: %w", a.id, err)
		}
		return ackResult{verdict: ackRecorded, class: Class(class)}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return ackResult{}, err
	}

	var token, kind, reason string
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT coalesce(outcome_token::text, ''), coalesce(recorded_kind, ''), coalesce(recorded_reason, '')
  FROM %s WHERE id = $1`, tablename.Of(svc, tablename.Outbox)), a.id).Scan(&token, &kind, &reason)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ackResult{verdict: ackNotFound}, nil
	case err != nil:
		return ackResult{}, err
	}
	if token != "" && strings.EqualFold(token, a.token) {
		if Kind(kind) == a.outcome.Kind && Reason(reason) == a.outcome.Reason {
			return ackResult{verdict: ackRepeated}, nil
		}
		return ackResult{verdict: ackAlreadyRecorded}, nil
	}
	return ackResult{verdict: ackLeaseLost}, nil
}

// entry — общее обеих точек входа: лента службы, пул, кольцо ключей,
// наблюдатель исхода, метрики, журнал. Точки входа — тонкие вызывающие её.
type entry struct {
	module, service string
	db              TxDB
	ring            *Keyring
	obs             OutcomeObserver
	metrics         *metrics
	log             *slog.Logger
}

// inTx исполняет fn в одной транзакции пула под storeCallTimeout: начало,
// операторы и фиксация — под одним сроком (arch-per-call-deadline). Отказ fn
// — откат.
func (e *entry) inTx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, storeCallTimeout)
	defer cancel()
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		// Откат отвязан от отмены вызывающего, но не от предела; его отказ
		// причины не меняет: незафиксированная транзакция не оставляет следа.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), savepointRollbackTimeout)
		defer rcancel()
		_ = tx.Rollback(rctx) // откат после отказа: исход вызова — отказ fn
		return err
	}
	return tx.Commit(ctx)
}

// claim — взятие одной транзакцией.
func (e *entry) claim(ctx context.Context, classes []string, limit int64) ([]claimedRow, error) {
	var got []claimedRow
	err := e.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		got, err = claimTx(ctx, tx, claimQuery{svc: e.service, classes: classes, limit: limit})
		return err
	})
	return got, err
}

// ack — запись исхода одной транзакцией; метрика исхода — после фиксации и
// только у изменённой строки (CX1-26 (д)).
func (e *entry) ack(ctx context.Context, a ackInput) (ackResult, error) {
	var res ackResult
	err := e.inTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		res, err = recordOutcome(ctx, tx, e.service, a, e.obs)
		return err
	})
	if err != nil {
		return ackResult{}, err
	}
	if res.verdict == ackRecorded {
		e.metrics.observeOutcome(res.class, a.outcome)
	}
	return res, nil
}

// openedRow — взятая строка с открытыми атрибутами.
type openedRow struct {
	claimedRow
	attrs map[string]string
}

// finishClaim — расшифровка после фиксации взятия (замысел NTF-1 З8):
// атрибуты строки — открытые плюс секретные, открытые кольцом. Строку, чей
// шифротекст не открывается, finishClaim закрывает вызовом recordOutcome с
// исходом INVALID(sealed_mismatch | key_unavailable) и токеном аренды взятия
// (замысел issue-2924 З28 п.2, CX5-61): наблюдатель получает и её. В ответ
// такая строка не попадает. Отказ закрытия строку тоже не выдаёт: она
// остаётся под арендой и после её конца выдаётся и закрывается снова. Ошибка
// — только порча открытой части (jsonb не карта строк).
func (e *entry) finishClaim(ctx context.Context, got []claimedRow) ([]openedRow, error) {
	out := make([]openedRow, 0, len(got))
	for _, r := range got {
		attrs, reason, err := e.open(r)
		if err != nil {
			return nil, err
		}
		if reason != ReasonNone {
			e.closeUnopened(ctx, r, reason)
			continue
		}
		out = append(out, openedRow{claimedRow: r, attrs: attrs})
	}
	return out, nil
}

// closeUnopened закрывает строку с неоткрывшимся секретом её токеном аренды
// через ядро записи исхода.
func (e *entry) closeUnopened(ctx context.Context, r claimedRow, reason Reason) {
	res, err := e.ack(ctx, ackInput{id: r.id, token: r.token, outcome: Outcome{Kind: KindInvalid, Reason: reason}})
	if err != nil || res.verdict != ackRecorded {
		attrs := []any{slog.String("module", e.module), slog.String("id", r.id), slog.String("reason", string(reason))}
		if err != nil {
			attrs = append(attrs, slog.String("err", err.Error()))
		}
		e.log.WarnContext(ctx, "notification feed row with an unopenable secret was not closed", attrs...)
	}
}

// open — значения атрибутов строки: открытые плюс секретные, расшифрованные
// кольцом. Неоткрывшийся секрет — причина закрытия строки, а не ошибка.
func (e *entry) open(r claimedRow) (map[string]string, Reason, error) {
	attrs := map[string]string{}
	if err := json.Unmarshal(r.attrs, &attrs); err != nil {
		return nil, ReasonNone, fmt.Errorf("строка %s: attrs: %w", r.id, err)
	}
	if r.secret == nil {
		return attrs, ReasonNone, nil
	}
	pt, err := e.ring.Open(e.service, r.id, r.template, r.secret)
	switch {
	case errors.Is(err, ErrKeyUnavailable):
		return nil, ReasonKeyUnavailable, nil
	case err != nil:
		return nil, ReasonSealedMismatch, nil
	}
	secret := map[string]string{}
	if err := json.Unmarshal(pt, &secret); err != nil {
		return nil, ReasonSealedMismatch, nil
	}
	for k, v := range secret {
		attrs[k] = v
	}
	return attrs, ReasonNone, nil
}
