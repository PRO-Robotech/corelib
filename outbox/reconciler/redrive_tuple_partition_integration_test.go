// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package reconciler_test

// The poison backstop must work on the outbox that carries the platform's
// grants and revocations — kaname.fga_outbox — and that queue is NOT
// partitioned on resource_id.
//
// Its ordering key is the full tuple identity (user, relation, object),
// materialised into a `tuple_key` column by iam migration 0067, because the state
// this queue feeds is a SET OF TUPLES: a WRITE and a DELETE conflict only when
// they name the same triple. Two rows that merely share an `object` touch
// different entries and commute.
//
// That set used to live in an external relation engine; since stage S6 it is
// iam's own `kaname.relation_fact`, folded out of this very journal by a
// trigger. The queue, its ordering key and this backstop outlived the consumer
// precisely because the property is about the SHAPE of the state, not about who
// holds it.
//
// Until this file existed the backstop was unreachable for that queue: the revival
// hard-coded `resource_id`, a column the tuple outbox does not have and never
// will. A row poisoned there stayed poisoned for good — and poisoning is only a
// bounded pause in a service that re-drives (see drainer/classify.go). The
// observable that follows is not academic: a refused GRANT is access the owner
// never gets, and a refused REVOKE is access that outlives its withdrawal.
//
// The four cases below are the whole contract of a partitioned revival:
//
//	revive        — a poisoned WRITE with nothing delivered past it reaches the target
//	revive        — a poisoned DELETE likewise (the quiet direction: "still granted"
//	                and "working" look identical from outside)
//	do not revive — a WRITE whose own tuple's DELETE already landed (over-grant)
//	do revive     — a WRITE whose tuple merely SHARES AN OBJECT with a delivered
//	                DELETE (the key is the tuple, not the object — without this the
//	                first "do not revive" would also pass on a key that blocks
//	                everything)
//
// Run: go test ./pkg/outbox/reconciler/... -race -p 1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox/drainer"
	"github.com/PRO-Robotech/corelib/outbox/reconciler"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// tupleOutboxSchema mirrors kaname.fga_outbox as migrations 0001 + 0063 + 0067
// leave it: the tuple identity in its own column, filled by a BEFORE INSERT
// trigger, plus the two partial indexes the partition-head claim reads. The
// trigger is copied rather than paraphrased — a fixture that derives the key
// differently from production would prove a property production does not have.
const tupleOutboxSchema = `
CREATE SCHEMA IF NOT EXISTS kacho_tuple;

CREATE TABLE kacho_tuple.fga_outbox (
    id            bigserial   PRIMARY KEY,
    event_type    text        NOT NULL,
    payload       jsonb       NOT NULL,
    tuple_key     text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    sent_at       timestamptz,
    last_error    text,
    attempt_count integer     NOT NULL DEFAULT 0,
    CONSTRAINT fga_outbox_event_type_check
        CHECK (event_type IN ('fga.tuple.write', 'fga.tuple.delete'))
);

CREATE OR REPLACE FUNCTION kacho_tuple.fga_outbox_tuple_key() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
    NEW.tuple_key := (NEW.payload->>'user') || ' ' || (NEW.payload->>'object');
    RETURN NEW;
END;
$fn$;

CREATE TRIGGER fga_outbox_tuple_key_trigger
    BEFORE INSERT ON kacho_tuple.fga_outbox
    FOR EACH ROW EXECUTE FUNCTION kacho_tuple.fga_outbox_tuple_key();

CREATE INDEX fga_outbox_tuple_head_idx
    ON kacho_tuple.fga_outbox (tuple_key, id) WHERE sent_at IS NULL;
CREATE INDEX fga_outbox_claim_order_idx
    ON kacho_tuple.fga_outbox (attempt_count, id) WHERE sent_at IS NULL;

CREATE OR REPLACE FUNCTION kacho_tuple.fga_outbox_notify() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
    PERFORM pg_notify('kacho_tuple_fga_outbox', NEW.id::text);
    RETURN NEW;
END;
$fn$;

CREATE TRIGGER fga_outbox_notify_trg
    AFTER INSERT ON kacho_tuple.fga_outbox
    FOR EACH ROW EXECUTE FUNCTION kacho_tuple.fga_outbox_notify();
`

const (
	tupleTable     = "kacho_tuple.fga_outbox"
	tupleChannel   = "kacho_tuple_fga_outbox"
	tupleKeyColumn = "tuple_key"
	tupleMaxAtt    = 10
)

// tuple is one (user, relation, object) triple — the unit the target keys its
// state on, and therefore the unit this queue must order by.
type tuple struct{ user, relation, object string }

// key is the GRANT key production partitions on since iam migration 0098: one row there
// carries a subject's whole relation set on one object, so the partition covers the set.
// Copied from the trigger rather than paraphrased — a fixture that derives the key
// differently from production would prove a property production does not have.
func (tp tuple) key() string { return tp.user + " " + tp.object }

// tupleStore models the target closely enough for what is asserted: a SET of
// tuples, where a write inserts and a delete removes, and a delete of an absent
// tuple is not an error (the applier maps that to already-applied).
type tupleStore struct {
	mu      sync.Mutex
	present map[string]struct{}
	applied []string
}

func newTupleStore() *tupleStore {
	return &tupleStore{present: map[string]struct{}{}}
}

func (s *tupleStore) apply(_ context.Context, eventType string, raw json.RawMessage) error {
	var p struct {
		User     string `json:"user"`
		Relation string `json:"relation"`
		Object   string `json:"object"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	k := tuple{p.User, p.Relation, p.Object}.key()

	s.mu.Lock()
	defer s.mu.Unlock()
	switch eventType {
	case "fga.tuple.write":
		s.present[k] = struct{}{}
	case "fga.tuple.delete":
		delete(s.present, k)
	}
	s.applied = append(s.applied, eventType+":"+k)
	return nil
}

func (s *tupleStore) has(tp tuple) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.present[tp.key()]
	return ok
}

func (s *tupleStore) set(tp tuple) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.present[tp.key()] = struct{}{}
}

func (s *tupleStore) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.applied...)
}

func setupTupleOutboxPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgtest.NewDB(t))
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool)
	require.NoError(t, pool.Ping(ctx))
	return pool
}

// seedTupleIntent inserts one intent in a chosen delivery state. tuple_key is left
// to the trigger — exactly as every production emit site does.
func seedTupleIntent(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	eventType string, tp tuple, attempt int, delivered bool,
) int64 {
	t.Helper()
	sentAt := "NULL"
	if delivered {
		sentAt = "now()"
	}
	var id int64
	err := pool.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO kacho_tuple.fga_outbox (event_type, payload, attempt_count, sent_at)
		VALUES ($1, jsonb_build_object('user',$2::text,'relation',$3::text,'object',$4::text), $5, %s)
		RETURNING id`, sentAt),
		eventType, tp.user, tp.relation, tp.object, attempt).Scan(&id)
	require.NoError(t, err)
	return id
}

func tupleAttemptCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, rowID int64) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT attempt_count FROM kacho_tuple.fga_outbox WHERE id=$1`, rowID).Scan(&n))
	return n
}

func newTupleRedriver(t *testing.T, pool *pgxpool.Pool) *reconciler.Reconciler {
	t.Helper()
	r, err := reconciler.NewRedriveOnly(pool, reconciler.Config{
		Table:           tupleTable,
		Channel:         tupleChannel,
		MaxAttempts:     tupleMaxAtt,
		PartitionColumn: tupleKeyColumn,
		// The same coverage rule iam wires (cmd/kaname/fga_outbox_redrive_backstop.go):
		// a delivered successor voids a poisoned row only if it re-determined everything
		// that row named. Without it, a partition key that covers a SET reads "somebody
		// restated something nearby" as "somebody restated this".
		SupersededCoverageSQL: `coalesce(s.payload->'relations', jsonb_build_array(s.payload->>'relation'))
		                        @> coalesce(t.payload->'relations', jsonb_build_array(t.payload->>'relation'))`,
	}, nil)
	require.NoError(t, err)
	return r
}

// drainTupleOutbox runs a real drainer over the tuple outbox with the production
// wiring of kaname (PartitionColumn = tuple_key) and stops once nothing is
// deliverable. The revival is only worth anything if the drainer then DELIVERS,
// so the assertions below read the target, not the row.
func drainTupleOutbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool, s *tupleStore) {
	t.Helper()
	d, err := drainer.New[json.RawMessage](pool, drainer.Config{
		Table:           tupleTable,
		Channel:         tupleChannel,
		BatchSize:       16,
		PollFallback:    200 * time.Millisecond,
		MaxAttempts:     tupleMaxAtt,
		BackoffMin:      10 * time.Millisecond,
		BackoffMax:      50 * time.Millisecond,
		ApplyTimeout:    5 * time.Second,
		PartitionColumn: tupleKeyColumn,
	}, func(payload []byte) (json.RawMessage, error) { return json.RawMessage(payload), nil },
		s.apply, nil)
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(runCtx) }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var pending int
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT count(*) FROM kacho_tuple.fga_outbox
			 WHERE sent_at IS NULL AND attempt_count < $1`, tupleMaxAtt).Scan(&pending))
		if pending == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
}

// Test_Redrive_TupleKeyedOutbox_RevivesBothDirections — the predicate of the
// defect: a row poisoned by a cause that has since been removed comes back into
// the queue and is delivered, WITHOUT anyone editing the database by hand.
//
// Both directions are asserted because they fail differently and only one of them
// is loud. An undelivered GRANT is visible — the owner reports a 403 on their own
// resource. An undelivered REVOKE shows nothing at all: "the tuple is still there"
// and "everything works" are the same observation from outside.
func Test_Redrive_TupleKeyedOutbox_RevivesBothDirections(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool := setupTupleOutboxPG(t)

	grant := tuple{"user:alice", "v_get", "vpc_network:net-1"}
	revoke := tuple{"user:bob", "v_update", "vpc_network:net-2"}

	// A grant that was refused until it poisoned — #431's shape exactly: the model
	// did not yet carry the relation when the drainer first tried.
	grantID := seedTupleIntent(t, ctx, pool, "fga.tuple.write", grant, tupleMaxAtt, false)
	// A revocation that poisoned the same way. Its tuple is present in the target:
	// the grant it withdraws was delivered long ago.
	revokeID := seedTupleIntent(t, ctx, pool, "fga.tuple.delete", revoke, tupleMaxAtt, false)

	s := newTupleStore()
	s.set(revoke)

	n, err := newTupleRedriver(t, pool).RedrivePoisoned(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n, "nothing has overtaken either intent: both are revivable")

	for _, id := range []int64{grantID, revokeID} {
		require.Lessf(t, tupleAttemptCount(t, ctx, pool, id), tupleMaxAtt,
			"row %d must be claimable again after revival", id)
	}

	drainTupleOutbox(t, ctx, pool, s)

	require.Truef(t, s.has(grant),
		"a revived grant must reach the target — otherwise a permanent-looking refusal "+
			"costs the owner access to their own resource for good: applied=%v", s.log())
	require.Falsef(t, s.has(revoke),
		"a revived revocation must reach the target — a grant that outlives its "+
			"withdrawal is over-grant, and it is the SILENT half: applied=%v", s.log())
}

// Test_Redrive_TupleKeyedOutbox_RespectsTupleOrder — the guard, on this key.
//
// One tuple, two intents: the WRITE poisoned, the DELETE delivered. Reviving the
// WRITE replays a grant past its own delivered revocation, and a set-shaped
// target has no tombstone to compare against — the tuple simply comes back. That is the
// over-grant the partition ordering exists to prevent, re-opened through the
// backstop that repairs the queue.
//
// The row seeded alongside is the control that keeps this from passing on a key
// that blocks EVERYTHING: it shares the OBJECT with the delivered delete but is a
// different tuple, and migration 0067 exists precisely because those two commute.
func Test_Redrive_TupleKeyedOutbox_RespectsTupleOrder(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool := setupTupleOutboxPG(t)

	withdrawn := tuple{"user:carol", "v_delete", "vpc_network:net-9"}
	// Same object, different subject — a DIFFERENT entry in the target's set.
	sameObject := tuple{"user:dave", "v_get", "vpc_network:net-9"}

	supersededID := seedTupleIntent(t, ctx, pool, "fga.tuple.write", withdrawn, tupleMaxAtt, false)
	seedTupleIntent(t, ctx, pool, "fga.tuple.delete", withdrawn, 0, true) // already delivered
	neighbourID := seedTupleIntent(t, ctx, pool, "fga.tuple.write", sameObject, tupleMaxAtt, false)

	s := newTupleStore()
	// The target reflects the delivered revocation: the withdrawn tuple is gone.
	require.False(t, s.has(withdrawn), "precondition: the revoked tuple is absent")

	n, err := newTupleRedriver(t, pool).RedrivePoisoned(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n,
		"exactly the neighbour is revived: the superseded write is not, the neighbour is")

	require.GreaterOrEqualf(t, tupleAttemptCount(t, ctx, pool, supersededID), tupleMaxAtt,
		"a write whose OWN tuple was already revoked must stay parked")
	require.Lessf(t, tupleAttemptCount(t, ctx, pool, neighbourID), tupleMaxAtt,
		"a write that merely shares an OBJECT with a delivered delete is a different "+
			"tuple and must still be revived — otherwise the key is wider than the "+
			"conflict and the backstop parks healthy rows")

	drainTupleOutbox(t, ctx, pool, s)

	require.Falsef(t, s.has(withdrawn),
		"revoked access came back through the backstop: applied=%v", s.log())
	require.Truef(t, s.has(sameObject),
		"the neighbouring tuple must have been delivered: applied=%v", s.log())
}

// seedGrantSetIntent seeds a SET-shaped row — one subject's several relations on one
// object, the shape iam emits since the grant became the row's unit.
func seedGrantSetIntent(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	eventType, user, object string, relations []string, attempt int, delivered bool,
) int64 {
	t.Helper()
	sentAt := "NULL"
	if delivered {
		sentAt = "now()"
	}
	var id int64
	err := pool.QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO kacho_tuple.fga_outbox (event_type, payload, attempt_count, sent_at)
		VALUES ($1,
		        jsonb_build_object('user',$2::text,'object',$3::text,
		                           'relations', to_jsonb($4::text[])),
		        $5, %s)
		RETURNING id`, sentAt),
		eventType, user, object, relations, attempt).Scan(&id)
	require.NoError(t, err)
	return id
}

// Test_Redrive_GrantSet_SupersededOnlyByACoveringSuccessor — the rule that a partition
// covering a SET needs, and the direction where getting it wrong is silent.
//
// A poisoned row is void only when a later delivered row RE-DETERMINED everything it
// named. Once a row carries a set, "a later delivered row of the same partition" no
// longer implies that: the successor may have restated part of it.
//
// The pair below is the whole rule, and each half is required. Without the covering
// case the check would pass on a rule that never voids anything (the backstop would
// replay outdated intent past a delivered successor — the over-grant the ordering
// exists to prevent). Without the partial case it would pass on the old direction-blind
// rule, whose failure is a revoke retired while most of its set is still live — and
// "revoked" and "still granted" look identical from outside.
func Test_Redrive_GrantSet_SupersededOnlyByACoveringSuccessor(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool := setupTupleOutboxPG(t)

	const (
		subject     = "user:erin"
		objPartial  = "vpc_network:net-partial"
		objCovering = "vpc_network:net-covering"
	)
	full := []string{"v_get", "v_list", "v_update", "v_delete"}

	// (a) poisoned REVOKE of the whole set; the delivered successor re-grants only ONE
	//     relation of it. The other three were never restated by anyone.
	partialID := seedGrantSetIntent(t, ctx, pool, "fga.tuple.delete", subject, objPartial, full, tupleMaxAtt, false)
	seedGrantSetIntent(t, ctx, pool, "fga.tuple.write", subject, objPartial, []string{"v_get"}, 0, true)

	// (b) poisoned REVOKE of the whole set; the delivered successor re-grants ALL of it.
	//     Nothing this row named is left unstated, so replaying it would strip access the
	//     successor deliberately granted.
	coveringID := seedGrantSetIntent(t, ctx, pool, "fga.tuple.delete", subject, objCovering, full, tupleMaxAtt, false)
	seedGrantSetIntent(t, ctx, pool, "fga.tuple.write", subject, objCovering, full, 0, true)

	n, err := newTupleRedriver(t, pool).RedrivePoisoned(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n, "exactly the partially-restated revoke is revived")

	require.Lessf(t, tupleAttemptCount(t, ctx, pool, partialID), tupleMaxAtt,
		"a revoke whose set was only PARTLY restated must be revived: the relations nobody "+
			"restated would otherwise survive their own removal, with the queue reporting the work done")
	require.GreaterOrEqualf(t, tupleAttemptCount(t, ctx, pool, coveringID), tupleMaxAtt,
		"a revoke whose set was FULLY restated by a later delivered row must stay parked: "+
			"replaying it would strip what the successor granted")
}
