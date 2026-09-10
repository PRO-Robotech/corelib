// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package reconciler is the backstop layer over the outbox/drainer. The drainer
// delivers intents at-least-once; the reconciler repairs the cases the drainer
// alone cannot:
//
//   - RedrivePoisoned — re-drives poisoned/exhausted rows (sent_at IS NULL AND
//     attempt_count >= MaxAttempts) back to claimable so the drainer retries
//     them (e.g. after the permanent cause was fixed, or it was misclassified).
//     Never past a delivered successor of the same resource: replaying an intent
//     over a later one that already landed undoes it, which on a register-outbox
//     resurrects access that was revoked. See the method doc.
//
// # Здесь стояли ещё два прохода — сверка с состоянием и сбор осиротевших
//
// Оба сняты (#760). Их предикаты были НЕДОСТИЖИМЫ by construction: сверка брала
// кандидатов из resource-таблиц и спрашивала о них ТУ ЖЕ очередь, а сбор —
// наоборот; при этом намерение пишется в очередь В ТОЙ ЖЕ транзакции, что и сама
// строка ресурса, а ни одна регистрируемая таблица не является дочерней в каскаде.
// Значит «ресурс есть, намерения нет» и «намерение есть, ресурса нет» не
// возникали никогда, и оба прохода были механизмом самоисправления, которого нет.
// Ни один сервис их не звал — за всё время существования.
//
// Настоящая сверка — с ВЛАДЕЛЬЦЕМ зеркала, а не со своей же очередью — потребует
// чтения у iam и нового ребра рантайма; это отдельный предмет, и здесь его нет.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/corelib/outbox"
)

// supersededSampleLimit bounds how many partition keys RedrivePoisoned names in
// its WARN. Attribution needs examples, not the whole set; an unbounded list would
// turn one stuck partition into an unreadable log line.
const supersededSampleLimit = 20

// RegisterOutboxPartition is the ordering partition key of every register-outbox
// in the platform: one row per FGA object, stamped with that object's globally
// unique id. Named once so the five services that wire this backstop cannot drift
// into five literals — and so the one queue that is NOT a register-outbox
// (iam's fga_outbox, keyed on the full tuple) has to say so out loud rather than
// inherit a default.
const RegisterOutboxPartition = "resource_id"

// Config parameterises a Reconciler.
type Config struct {
	// Table — full outbox table name (`<schema>.<table>`). Must be the SAME table
	// the drainer drains: RedrivePoisoned hands rows back to that claim path, so a
	// second table here would repair a queue nobody delivers.
	Table string
	// Channel — LISTEN/NOTIFY channel of the table (for parity / future use).
	Channel string
	// MaxAttempts — poison threshold (default 10) used by RedrivePoisoned.
	MaxAttempts int
	// PartitionColumn — the ORDERING PARTITION key of this outbox: the column over
	// which its events fail to commute. REQUIRED; there is no default and no zero
	// value, because an unset key would read as "no ordering" and silently turn the
	// revival into the over-grant it exists to prevent (RedrivePoisoned §Reviving
	// respects the order of the partition).
	//
	// It MUST be the SAME column the drainer of this table is configured with
	// (drainer.Config.PartitionColumn). The two carry the two halves of one rule —
	// the claim refuses to take a row ahead of a DELIVERABLE predecessor, the
	// revival refuses to raise a row past a DELIVERED successor — and on different
	// keys each half guards a partition the other does not.
	//
	// Two shapes exist in this platform, and they are not interchangeable:
	//
	//   - a register-outbox (`fga_register_outbox` in vpc / compute / nlb / storage /
	//     registry) carries one row per FGA object and keys on RegisterOutboxPartition
	//     ("resource_id");
	//   - iam's `fga_outbox` carries tuple WRITES and DELETES and keys on `tuple_key`,
	//     which since iam migration 0098 renders the GRANT (user, object): one row there
	//     carries a subject's whole relation SET on one object, and a partition has to
	//     cover every row it can be ordered against.
	PartitionColumn string
	// SupersededCoverageSQL — an EXTRA condition a delivered successor must satisfy
	// before a poisoned row is treated as void. Empty (the default) keeps the plain
	// rule: any later delivered row of the same partition supersedes.
	//
	// WHEN IT IS REQUIRED. The plain rule is sound only while a partition key implies
	// that two rows touch THE SAME THING. That holds when a row carries one tuple; it
	// stops holding the moment a row carries a SET, because then a successor may have
	// re-determined only part of what the poisoned row named. Voiding it there discards
	// intent nobody re-stated — and in the removal direction that is access outliving
	// its own revoke, which looks exactly like a revoke that worked.
	//
	// The fragment is SQL, evaluated with `s` bound to the delivered successor and `t`
	// to the poisoned row, e.g. "s.payload->'relations' @> t.payload->'relations'". It
	// must express COVERAGE — «the successor re-determined everything this row named» —
	// and nothing else: direction (write vs delete) is deliberately NOT part of the
	// test, because a later delivered row states the desired final state whichever way
	// it points.
	//
	// It is the caller's SQL because only the caller knows its payload shape; the
	// contract this package holds is the meaning, not the expression.
	SupersededCoverageSQL string
}

func (c Config) withDefaults() Config {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	return c
}

// Reconciler is the backstop over one outbox table. Its single pass —
// RedrivePoisoned — is shape-agnostic: it is pure SQL over the table and reads no
// domain state, so a register-outbox and a tuple-keyed outbox take the same one.
type Reconciler struct {
	pool *pgxpool.Pool
	cfg  Config
	log  *slog.Logger
}

// NewRedriveOnly constructs a Reconciler. Имя оставлено прежним намеренно: оно
// описывает, ЧТО конструируется (backstop, умеющий один проход — возврат
// отравленных), а не противопоставляет второму конструктору. Второго нет —
// прежний New требовал двух доменных адаптеров ради проходов, которые никто не
// звал, и снят вместе с ними (#760).
//
// The backstop is not optional in any service whose register-outbox can poison.
// A poisoned row is excluded from the claim query's blocking set, so its partition
// unblocks immediately — that is what stops one refused intent from silencing every
// later intent for the same resource. But the row itself then stays undelivered
// forever unless something re-drives it, and an undelivered registration means the
// resource has no mirror row in kaname, hence no owner tuple and no materialized
// verbs: invisible to authz until someone edits the database by hand. The redrive
// is what makes poisoning a bounded pause rather than a permanent loss — on a
// timer for the register-outboxes, on the authorization-model-change event for
// iam's tuple outbox, where the permanent cause is known and a blind repeat of a
// refused write cannot pass.
func NewRedriveOnly(pool *pgxpool.Pool, cfg Config, logger *slog.Logger) (*Reconciler, error) {
	if pool == nil {
		return nil, errors.New("reconciler.NewRedriveOnly: pool is nil")
	}
	if cfg.Table == "" {
		return nil, errors.New("reconciler.NewRedriveOnly: Config.Table required")
	}
	if !validIdentifier(cfg.PartitionColumn) {
		return nil, fmt.Errorf(
			"reconciler.NewRedriveOnly: Config.PartitionColumn must be a plain column "+
				"identifier and MUST match the drainer's PartitionColumn for %s "+
				"(unset would read as \"no ordering\" and revive an intent past a "+
				"delivered successor), got %q",
			cfg.Table, cfg.PartitionColumn)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		pool: pool,
		cfg:  cfg.withDefaults(),
		log:  logger.With(slog.String("component", "outbox_reconciler"), slog.String("table", cfg.Table)),
	}, nil
}

// RedrivePoisoned resets poisoned/exhausted rows (sent_at IS NULL AND
// attempt_count >= MaxAttempts) back to claimable (attempt_count = 0, last_error
// = NULL) so the drainer retries them. Returns the number re-driven.
//
// # Reviving respects the order of the partition
//
// An intent is NOT revived once a LATER intent for the SAME resource has already
// been delivered — and, where the caller sets Config.SupersededCoverageSQL, only
// when that successor RE-DETERMINED everything the poisoned row named. Without
// that qualifier the rule is sound exactly while one row means one thing: it reads
// «somebody restated this» from «somebody restated something in the same
// partition», which stops being the same statement the moment a row carries a set.
// Reviving past a delivered successor replays an outdated intent
// on top of the current state — and for a register-outbox that is precisely the
// over-grant the partition ordering exists to prevent: the target (kaname's
// resource_mirror) versions only its update branch, deregistration is a hard
// delete that keeps no tombstone, so a replayed registration finds nothing to
// compare against, takes the insert branch and resurrects the mirror row of a
// deleted resource. iam's reconciler is level-triggered off that mirror, so the
// owner tuple is then re-materialised forever — revoked access returns and stays.
//
// The drainer's claim already refuses to take a row ahead of a DELIVERABLE
// same-partition predecessor (drainer Config.PartitionColumn). That guard says
// nothing about rows already delivered, because a delivered row leaves the
// deliverable set — so the backstop has to carry the other half of the same rule
// itself, or it re-opens through repair exactly what the claim closed.
//
// The partition is Config.PartitionColumn — REQUIRED, with no default and no zero
// value, because a knob left unset here would read as "no ordering" and silently
// restore the defect. It must be the SAME column the drainer of this table is
// given: the claim's guard covers deliverable predecessors, this one covers
// delivered successors, and on two different keys each half guards a partition
// the other does not.
//
// One shape is live today. A register-outbox writes one row per object of the
// rights model and stamps RegisterOutboxPartition ("resource_id") with that
// object's globally-unique id, so "same partition" is exactly "same target
// object"; that is also the key lockResource and intendedRegistered use, which is
// why a full Reconciler (New) accepts nothing else.
//
// A second shape stood beside it and is worth keeping as the worked example of a
// NARROWER key: iam's `fga_outbox` carried tuple WRITES and DELETES and keyed on
// `tuple_key`, the full (user, relation, object) triple materialised by iam
// migration 0067, because the target's state was a set of tuples and rows that
// merely share an object commute. It took NewRedriveOnly. That journal is no
// longer drained anywhere — its rows are folded into direct facts by a trigger —
// so it neither poisons nor needs a redrive; the key-width rule it illustrates is
// unaffected.
//
// # Two honest limits of this rule
//
// A row with an EMPTY partition key names nothing, so "a later intent for the same
// partition" cannot be established for it. Such rows group together — the drainer's
// claim already groups them the same way and the two must agree — and the
// conservative reading is deliberate: reviving one risks replaying it past a
// delivered intent, which is the defect this guard exists for. The cost is real
// and is NOT merely a delay: one delivered empty-key row parks every earlier
// poisoned empty-key row permanently. No live emitter writes an empty key — on the
// register side this is reachable only for rows predating the column (vpc migration
// 0008 added it without a backfill), and on iam's side migration 0067's NOT VALID
// check makes an incomplete key fail at emit — which is why the report below names
// the rows rather than counting them.
//
// The check is evaluated against ONE statement snapshot, so it is "no delivered
// successor as of this pass", not a serialised guarantee: a successor the drainer
// commits WHILE the statement runs is invisible to it. This narrows the original
// window from "every pass, forever" to "a delivery landing inside one statement",
// and the pass is periodic (minutes) while the statement is milliseconds. Closing
// it fully would mean taking a per-row advisory lock and giving up the set-based
// pass; that trade is not obviously worth it and is written down here rather than
// papered over.
//
// A superseded row stays poisoned: its intent is void, and it keeps its original
// last_error for diagnosis. Note what that means operationally — the row remains
// in the pending set, so it keeps contributing to the table's backlog and
// oldest-pending-age gauges, which will not fall back to zero on their own. That
// is why the report below is a WARN carrying the resource ids and not a bare
// count: the condition needs a human to retire the rows, and an alarm nobody can
// clear stops being an alarm.
//
// Cost: the supersession check is a correlated anti-join over the table, which is
// unindexed for delivered rows (the partition-head index is partial on
// sent_at IS NULL). It runs only for rows in the outer set, i.e. only while
// something is poisoned — an alarm condition in itself — and not at all on a
// healthy queue. A service whose outbox both grows large and poisons routinely
// should add a plain btree on (resource_id, id) to turn it into a lookup.
func (r *Reconciler) RedrivePoisoned(ctx context.Context) (int, error) {
	table := outbox.SanitizeTable(r.cfg.Table)
	part := pgx.Identifier{r.cfg.PartitionColumn}.Sanitize()
	coverage := "TRUE"
	if r.cfg.SupersededCoverageSQL != "" {
		coverage = "(" + r.cfg.SupersededCoverageSQL + ")"
	}
	q := fmt.Sprintf(`
		WITH poisoned AS (
		    SELECT t.id, t.%[3]s AS partition_key,
		           EXISTS (
		               SELECT 1 FROM %[1]s s
		                WHERE s.%[3]s = t.%[3]s
		                  AND s.id > t.id
		                  AND s.sent_at IS NOT NULL
		                  AND %[4]s
		           ) AS superseded
		      FROM %[1]s t
		     WHERE t.sent_at IS NULL AND t.attempt_count >= $1
		),
		revived AS (
		    UPDATE %[1]s
		       SET attempt_count = 0, last_error = NULL
		     WHERE id IN (SELECT id FROM poisoned WHERE NOT superseded)
		    RETURNING 1
		)
		SELECT (SELECT count(*) FROM revived),
		       (SELECT count(*) FROM poisoned WHERE superseded),
		       (SELECT coalesce(array_agg(partition_key ORDER BY id), '{}')
		          FROM (SELECT partition_key, id FROM poisoned
		                 WHERE superseded ORDER BY id LIMIT %[2]d) sample)`,
		table, supersededSampleLimit, part, coverage)

	var revived, superseded int64
	var sample []string
	if err := r.pool.QueryRow(ctx, q, r.cfg.MaxAttempts).Scan(&revived, &superseded, &sample); err != nil {
		return 0, fmt.Errorf("reconciler.RedrivePoisoned %s: %w", r.cfg.Table, err)
	}
	if superseded > 0 {
		// Named, not counted: these rows stay in the pending set and keep the
		// table's backlog/age gauges off zero, so an operator has to retire them
		// by hand and needs to know WHICH. Resource ids are id-based handles, not
		// PII or infrastructure detail — the same call the drainer's wedge
		// reporter makes.
		r.log.Warn("poisoned intents left un-revived: a later intent for the same "+
			"partition has already been delivered, so replaying them would undo it; "+
			"they remain pending and must be retired by hand",
			slog.Int64("superseded", superseded),
			slog.Int("sampled", len(sample)),
			slog.String("partition_column", r.cfg.PartitionColumn),
			slog.String("partitions", strings.Join(sample, ",")))
	}
	return int(revived), nil
}

// validIdentifier reports whether s is a plain unquoted SQL column identifier
// ([A-Za-z_][A-Za-z0-9_]*). The name is interpolated into the redrive statement,
// so it is checked at CONSTRUCTION — a bad value must refuse to build a
// Reconciler, not surface as a syntax error on the first pass minutes later, and
// must never be able to carry SQL. pgx.Identifier.Sanitize quotes it as well; this
// is the belt to that suspenders, and it also rejects the empty string, which is
// the value that would silently mean "no ordering".
func validIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}
