// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Class is the outcome of classifying an applier (or decoder) error. It is the
// single, testable decision point that drives whether the drainer marks a row
// success, poisons it (no further retry) or retries it unbounded with backoff.
//
// Transient-class no-poison rule: a long-but-transient IAM outage (gRPC
// Unavailable / DeadlineExceeded / connection-refused / timeout) — and a
// concurrency conflict (409) — must NEVER poison a row: it retries forever (with
// backoff) so the owner-tuple is never lost to a temporary peer outage or a
// racing writer. Only permanent errors (4xx other than the conflict class,
// decode-failure, malformed) poison.
type Class int

const (
	// ClassSuccess — nil error; the row is delivered.
	ClassSuccess Class = iota
	// ClassAlreadyApplied — the target reports already-applied; idempotent success,
	// the row is marked sent. What decides the class is not the wire code but what
	// the target DID: "it is already there, nothing left to do" is success, while an
	// abort that applied NOTHING is ClassTransient — marking such a row sent would
	// silently drop the intent. Only the applier can tell the two apart, because
	// only it knows its target's vocabulary.
	ClassAlreadyApplied
	// ClassPermanent — retry is pointless (ErrPermanent, gRPC InvalidArgument /
	// 4xx-non-409, decode-failure). The row is poisoned (attempt_count forced to
	// MaxAttempts) and surfaced for an operator.
	ClassPermanent
	// ClassTransient — a temporary failure (peer Unavailable / DeadlineExceeded /
	// connection-refused / timeout / any unclassified error). The row is retried
	// unbounded with backoff and is NEVER driven into the poison gate.
	ClassTransient
)

// String renders the class for logs/metrics labels.
func (c Class) String() string {
	switch c {
	case ClassSuccess:
		return "success"
	case ClassAlreadyApplied:
		return "already_applied"
	case ClassPermanent:
		return "permanent"
	case ClassTransient:
		return "transient"
	default:
		return "unknown"
	}
}

// Classify maps an applier error to a Class.
//
// Decision order (most-specific first):
//  1. nil                                   → ClassSuccess
//  2. errors.Is(err, ErrAlreadyApplied)     → ClassAlreadyApplied
//  3. errors.Is(err, ErrPermanent)          → ClassPermanent
//  4. gRPC InvalidArgument / PermissionDenied → ClassPermanent
//  5. everything else (Unavailable,
//     DeadlineExceeded, NotFound,
//     FailedPrecondition,
//     connection-refused/timeout, raw)      → ClassTransient
//
// Rationale for step 4: both codes describe a decision about the REQUEST — its
// content, or the caller's authority to make it — and an identical retry changes
// neither. See isPermanentGRPC for why calling a refusal "transient" produces a
// permanently wedged partition rather than an eventual success.
//
// Rationale for step 5: the remaining codes describe peer STATE, which a retry can
// genuinely find changed. A raw, un-wrapped error of unknown shape is likewise
// treated as transient — fail-SAFE for delivery (retry rather than lose the
// tuple). Appliers that KNOW an error is permanent must wrap it in ErrPermanent.
func Classify(err error) Class {
	if err == nil {
		return ClassSuccess
	}
	if errors.Is(err, ErrAlreadyApplied) {
		return ClassAlreadyApplied
	}
	if errors.Is(err, ErrPermanent) {
		return ClassPermanent
	}
	if isPermanentGRPC(err) {
		return ClassPermanent
	}
	// Unavailable / DeadlineExceeded / NotFound / FailedPrecondition / connection
	// errors / any unclassified error → transient (never poison).
	return ClassTransient
}

// isPermanentGRPC reports whether err carries a gRPC status code that is
// permanent on the applier side.
//
// InvalidArgument is permanent for the obvious reason: the peer rejected the
// content, and re-sending the same content cannot change that.
//
// PermissionDenied is permanent for the same reason, arrived at less obviously.
// An authorization decision is a function of (caller, relation, object); a retry
// alters none of the three, so repeating an identical refused request cannot start
// succeeding. Calling it transient was justified as "the peer may not be
// provisioned yet, it will heal" — but that is not what the retry buys. A
// transient row is deliberately held one attempt BELOW the poison gate
// (markTransientFailure), so it never leaves the claim query's blocking set, and
// with PartitionColumn set every later row of its partition is never claimed. What
// the old classification actually produced was a partition wedged forever. It was
// observed in production shape: a register queue in which no row had ever been
// delivered, all refused on authorization grounds — and because a resource's
// unregistration is queued behind its registration, deleted resources kept their
// grants. A grant outliving the thing it grants is over-grant.
//
// Poisoning is the safe direction by comparison. The refused write never happened,
// so nothing was granted (under-grant is fail-closed), and the partition unblocks,
// so the intents queued behind it — including the revocations — apply.
//
// It is NOT self-healing on its own, and that matters: a poisoned registration means
// kaname never got the resource's mirror row, and every candidate query the
// binding reconciler runs reads that mirror, so the resource has no owner tuple and
// no materialized verbs until the row is delivered. Poisoning is therefore only
// correct in a service that also re-drives poisoned rows
// (reconciler.RedrivePoisoned). WHEN it re-drives is the service's choice: the
// register-outboxes run the pass on a timer. A second shape stood beside it — an
// EVENT-driven pass over iam's tuple journal, woken by the rights model changing —
// and it went away with that journal's drainer: those rows are no longer applied to
// anything, so nothing can poison them. What survives is the rule rather than the
// pair of examples — a queue that poisons owes a backstop — and that this is a
// property of the TREE rather than of anyone's memory is held by
// internal/repohygiene TestEveryPoisoningOutboxHasARedrive.
// With that backstop the outcome is a bounded pause:
// a cause that was temporary succeeds on a later pass, and a cause that is genuinely
// permanent keeps poisoning — visibly, via the poison counter — instead of silently
// wedging every intent behind it. Without it, poisoning loses the intent for good.
//
// The other codes (NotFound, FailedPrecondition, Unavailable, DeadlineExceeded)
// stay transient: they describe peer STATE, which a retry genuinely can find
// changed. Appliers wrap additional permanent cases in ErrPermanent.
func isPermanentGRPC(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	return st.Code() == codes.InvalidArgument || st.Code() == codes.PermissionDenied
}

// PermanentPolicy — что делать с ПОСТОЯННЫМ отказом ПРИМЕНЕНИЯ.
//
// # Почему у этого вопроса вообще появился второй ответ
//
// Травление покупает РОВНО ОДНО: отравленная строка выбывает из блокирующего
// набора заявки, и партиция, стоявшая за ней, разблокируется. Комментарий выше
// это и обосновывает — и обосновывает целиком через партицию: «with
// PartitionColumn set every later row of its partition is never claimed».
//
// У КОММУТАТИВНОГО потока партиции нет. Блокировать нечего, разблокировать
// нечего — травление не покупает ничего, а платит полной ценой: намерение
// выбывает навсегда. Тот же комментарий называет и условие правильности —
// «poisoning is therefore only correct in a service that also re-drives poisoned
// rows», — а возврат отравленных строк строится только вокруг ключа порядка,
// которого у коммутативной очереди нет by construction. То есть для такой
// очереди травление одновременно бесполезно и невосполнимо.
//
// Отсюда второй ответ, и он не «мягче», а уместнее: повторять с отступом. Цена
// названа честно — вечный повтор заведомо безнадёжного вызова с частотой не
// чаще BackoffMax, видимый счётчиком незавершённых строк. Цена альтернативы —
// молча потерянное намерение, и наблюдать её нечем: «доставлено» и «потеряно»
// снаружи выглядят одинаково.
//
// Выведено 2026-08-16 по kacho#455 из двух очередей kaname, у которых
// травление работало, а возврата не было ни у одной.
type PermanentPolicy int

const (
	// PoisonPermanent — отравить строку. Умолчание и сегодняшнее поведение:
	// нулевое значение обязано означать то, что уже провязанные очереди делают
	// сейчас, иначе введение поля сменило бы их поведение молча.
	//
	// Требует возврата отравленных строк. Что это свойство ДЕРЕВА, а не чьей-то
	// памяти, держит internal/repohygiene TestEveryPoisoningOutboxHasARedrive.
	PoisonPermanent PermanentPolicy = iota

	// RetryPermanent — повторять, как временный отказ.
	//
	// Законно ТОЛЬКО у коммутативной очереди (PartitionColumn пуст): пара с
	// ключом порядка отвергается Config.Validate, потому что там постоянный
	// отказ заклинил бы свою партицию навсегда.
	RetryPermanent
)

// String — для журналов и меток.
func (p PermanentPolicy) String() string {
	switch p {
	case PoisonPermanent:
		return "poison"
	case RetryPermanent:
		return "retry"
	default:
		return "unknown"
	}
}

// Disposition — что полагается сделать со строкой.
//
// Отделено от места, где строка помечается: пометка требует транзакции и
// проверяется интеграционно, а решение обязано быть проверяемо без базы. Иначе
// единственная точка принятия решения снова размазалась бы по ветвям, которые
// поодиночке защитимы.
type Disposition int

const (
	// DispositionDeliver — пометить доставленной.
	DispositionDeliver Disposition = iota
	// DispositionPoison — отравить: повтор не имеет шанса на успех, и партиция
	// обязана разблокироваться.
	DispositionPoison
	// DispositionRetry — повторить с отступом, не доводя до порога отравления.
	DispositionRetry
)

// String — для журналов и текстов отказа проб.
func (d Disposition) String() string {
	switch d {
	case DispositionDeliver:
		return "deliver"
	case DispositionPoison:
		return "poison"
	case DispositionRetry:
		return "retry"
	default:
		return "unknown"
	}
}

// Decide — единственная точка, отвечающая «что сделать со строкой» по классу
// отказа применения и политике очереди.
func Decide(cls Class, policy PermanentPolicy) Disposition {
	switch cls {
	case ClassSuccess, ClassAlreadyApplied:
		return DispositionDeliver
	case ClassPermanent:
		if policy == RetryPermanent {
			return DispositionRetry
		}
		return DispositionPoison
	default: // ClassTransient
		return DispositionRetry
	}
}

// DecideOutcome — ЕДИНСТВЕННАЯ точка, отвечающая «что сделать со строкой» по
// обоим исходам её обработки и политике очереди. Ровно её зовёт путь пометки,
// поэтому здесь нет ветви, которой нет в работе.
//
// # Почему отказ РАЗБОРА не читает политику
//
// Политика повтора относится к отказу ПРИМЕНЕНИЯ — к тому, что сказал сосед, и
// что способно измениться от внешнего события. Отказ разбора говорит о самой
// строке: её тело не станет разбираемым ни от какого события, поэтому повтор —
// вечная работа без единого шанса на успех.
//
// Следствие надо назвать вслух, а не проглотить: очередь с политикой повтора
// всё ещё МОЖЕТ отравить строку — по разбору. «Травиться нечему» достигается не
// терпимостью к битой строке, а тем, что такую строку НЕЛЬЗЯ ЗАПИСАТЬ: каждое
// условие отказа разбора обязано быть закрыто ограничением схемы у владельца
// очереди. Для очередей kaname это сделано миграциями 0079/0080 и 0097.
func DecideOutcome(decodeErr, applyErr error, policy PermanentPolicy) Disposition {
	if decodeErr != nil {
		return DispositionPoison
	}
	return Decide(Classify(applyErr), policy)
}
