// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package drainer_test

// Integration tests for Config.PartitionColumn — order-preserving concurrent
// drain that closes the cross-batch reorder LEAK, at the NARROWEST key that
// actually needs ordering.
//
// Background / root cause (CRIT-1)
//
//	iam's fga_outbox carries BOTH tuple WRITES (grant / label-register) AND
//	DELETES (revoke / delete-stale) of the SAME (user,relation,object). These are
//	NOT commutative. ApplyConcurrency>1 does not preserve apply order — and the
//	claim `ORDER BY (attempt_count,id)` splits a bumped WRITE (attempt≥1 after a
//	transient failure) and a fresh DELETE (attempt=0) into DIFFERENT claim batches.
//	The delete is then claimed and applied BEFORE its predecessor write →
//	delete-before-write → the tuple survives the revoke → authz OVER-GRANT leak.
//
//	Fix (Config.PartitionColumn): the claim never takes a row if a DELIVERABLE
//	(sent_at IS NULL AND attempt_count < MaxAttempts) same-partition predecessor
//	with a smaller id exists. A successor is therefore never claimed ahead of an
//	unsent predecessor — per-partition FIFO holds cross-batch (and cross-replica),
//	so a write always applies before a later delete of the same key.
//
// # Why the key is the TUPLE, not the object
//
//	The partition must be the narrowest key over which events fail to commute.
//	For an FGA tuple stream that key is the full (user, relation, object) triple:
//	the target's state is a SET OF TUPLES, so a write of (u1,v_get,O) and a delete
//	of (u2,v_get,O) touch different entries and commute freely. Partitioning by
//	`object` lumps every subject of one object into ONE ordering chain, so a revoke
//	waits behind every unrelated grant queued earlier on the same object — pure
//	over-serialisation, no correctness gained. Measured on the live stand during a
//	packed e2e run: 8 643 pending rows spread over 1 439 objects but 8 641 distinct
//	tuples, and a single revoke sat behind up to 632 same-object predecessors.
//	kaname therefore partitions on `tuple_key` (migration 0067).
//
// Run: go test ./pkg/outbox/drainer/... -race -p 1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/outbox/drainer"

	"github.com/PRO-Robotech/corelib/pgtest"
)

// iamPartitionKey mirrors the partition key kaname ships for
// kaname.fga_outbox (drainer.Config.PartitionColumn in
// services/iam/cmd/kaname/serve.go): the FULL tuple identity
// (user, relation, object), materialised by migration 0067 into the `tuple_key`
// column and indexed by fga_outbox_tuple_head_idx. Every test below runs the
// SHIPPED key, so a widening of it (back to `payload->>'object'`, or to any other
// coarser handle) fails the concurrency guard in
// Test_1_4_46_PartitionKey_SameObject_DistinctSubjects_StayConcurrent, and a
// narrowing/removal fails the ordering guards.
const iamPartitionKey = "tuple_key"

// tupleState is an order-sensitive fake target: it models PRESENCE of an
// individual tuple — keyed by the FULL (user, relation, object) triple, exactly
// as the target keys its tuple set — honouring event_type (write → present,
// delete → absent) in the order the drainer invokes Apply. A delete of an absent
// tuple is an idempotent no-op; a write is idempotent-add. The final
// presence of a tuple thus reflects the LAST apply OF THAT TUPLE — so a reordered
// delete-then-write leaves it PRESENT (the leak), while write-then-delete leaves
// it ABSENT (correct).
//
// Keying on the whole triple (not on `object` alone) is what makes this model
// faithful: two tuples that merely share an object are INDEPENDENT entries in the
// target's state, so their applies commute. Only same-triple applies are
// order-sensitive — the exact boundary Config.PartitionColumn must draw.
// Concurrency-safe (ApplyConcurrency>1 fans applies out).
type tupleState struct {
	mu      sync.Mutex
	present map[string]bool
	log     []string // ordered "write:<key>" / "delete:<key>"
	delay   time.Duration
}

func newTupleState() *tupleState { return &tupleState{present: map[string]bool{}} }

// tupleKeyOf renders the (user, relation, object) triple that identifies one
// tuple — the model's state key and, byte-for-byte, the value kaname's
// fga_outbox trigger stores in its `tuple_key` column (migration 0067). The
// separator is a space: no component of a tuple key may contain whitespace,
// and the rendering matches the canonical `user relation object` notation, so
// the per-partition wedge WARN stays readable. Even if a component ever did carry
// a space, two triples could only COLLIDE into one partition — over-ordering, the
// safe direction — never split one triple across partitions.
func tupleKeyOf(user, relation, object string) string {
	return user + " " + relation + " " + object
}

// apply is a drainer.Applier[rawPayload] over the modelled tuple set.
func (s *tupleState) apply(ctx context.Context, eventType string, payload rawPayload) error {
	var e struct {
		User     string `json:"user"`
		Relation string `json:"relation"`
		Object   string `json:"object"`
	}
	if err := json.Unmarshal(payload, &e); err != nil {
		return errors.Join(drainer.ErrPermanent, err)
	}
	key := tupleKeyOf(e.User, e.Relation, e.Object)
	// Delay OUTSIDE the lock so applies of distinct tuples genuinely overlap
	// under ApplyConcurrency>1 (models a slow peer).
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch eventType {
	case "fga.tuple.write":
		s.present[key] = true
		s.log = append(s.log, "write:"+key)
	case "fga.tuple.delete":
		delete(s.present, key)
		s.log = append(s.log, "delete:"+key)
	default:
		return errors.Join(drainer.ErrPermanent, fmt.Errorf("unknown event_type %q", eventType))
	}
	return nil
}

func (s *tupleState) isPresent(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.present[key]
}

// opsFor returns the ordered apply log restricted to one tuple key.
func (s *tupleState) opsFor(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.log {
		if e == "write:"+key || e == "delete:"+key {
			out = append(out, e)
		}
	}
	return out
}

func (s *tupleState) snapshotLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

// insertOutboxRowAttempt inserts one fga_outbox row with an explicit
// attempt_count (models a row already bumped by prior transient claims).
func insertOutboxRowAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType, payload string, attempt int) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx,
		`INSERT INTO kaname.fga_outbox (event_type, payload, attempt_count)
		 VALUES ($1, $2::jsonb, $3) RETURNING id`,
		eventType, payload, attempt,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

// insertOutboxRowAged inserts a row with an explicit attempt_count AND a
// backdated created_at (now()-age) — models a partition head that has been unsent
// for a while (wedge attribution tests).
func insertOutboxRowAged(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType, payload string, attempt int, age time.Duration) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx,
		`INSERT INTO kaname.fga_outbox (event_type, payload, attempt_count, created_at)
		 VALUES ($1, $2::jsonb, $3, now() - make_interval(secs => $4)) RETURNING id`,
		eventType, payload, attempt, age.Seconds(),
	).Scan(&id)
	require.NoError(t, err)
	return id
}

// startDrainerApplier launches one drainer over a caller-supplied
// drainer.Applier[rawPayload] (the order-sensitive tupleState.apply), rather than
// the shared record-only fakeApplier.
func startDrainerApplier(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	cfg drainer.Config,
	applier drainer.Applier[rawPayload],
	opts ...drainer.Option[rawPayload],
) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	d, err := drainer.New[rawPayload](pool, cfg, rawDecoder, applier, testLogger(), opts...)
	require.NoError(t, err)
	dCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.Run(dCtx)
	}()
	return cancel, done
}

// Test_1_4_40_PartitionOrder_CrossBatch_NoDeleteBeforeWrite — the CRIT-1 RED→GREEN
// lock. A bumped WRITE (attempt_count=5) and a fresh DELETE (attempt_count=0) of
// the SAME object, plus fresh rows of other objects that fill the small claim
// batches so the write and delete land in different batches under
// ORDER BY (attempt_count,id). Without partition-head-only claim the delete is
// applied first and the write re-materialises the tuple → PRESENT (leak). With
// the fix the write applies before the delete → ABSENT.
func Test_1_4_40_PartitionOrder_CrossBatch_NoDeleteBeforeWrite(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	st := newTupleState()

	const obj = "vpc_network:leak0000000000000001"
	key := tupleKeyOf("user:u", "v_get", obj)

	// W: write(tuple), already bumped to attempt_count=5 (prior transient history).
	// Inserted FIRST → smallest id (grant precedes revoke).
	wID := insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.write",
		fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":%q}`, obj), 5)
	// D: delete(tuple), fresh attempt_count=0. Inserted SECOND → id > wID (revoke).
	dID := insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.delete",
		fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":%q}`, obj), 0)
	// ≥2 fresh rows of OTHER tuples (attempt_count=0) → fill claim batches so the
	// attempt=5 write and the attempt=0 delete never share one 2-row batch.
	fresh := make([]int64, 0, 4)
	for i := 0; i < 4; i++ {
		fresh = append(fresh, insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":"other:obj%02d"}`, i), 0))
	}

	cfg := testCfg()
	cfg.ApplyConcurrency = 2
	cfg.PartitionColumn = iamPartitionKey // fix under test
	cfg.MaxAttempts = 10
	cfg.PollFallback = 300 * time.Millisecond

	dCancel, done := startDrainerApplier(t, ctx, pool, cfg, st.apply)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	// All rows must reach sent_at (the drain completes for both paths).
	all := append([]int64{wID, dID}, fresh...)
	for _, id := range all {
		waitForRowSent(t, ctx, pool, id, 30*time.Second)
	}

	// The delete is the LAST op on the tuple (by id order) → final state = ABSENT.
	require.Falsef(t, st.isPresent(key),
		"cross-batch reorder leak: tuple %q PRESENT after revoke — apply log=%v",
		key, st.snapshotLog())
	// And the two ops must have applied in id order (write then delete).
	assert.Equalf(t, []string{"write:" + key, "delete:" + key}, st.opsFor(key),
		"same-tuple ops must apply in id order (write→delete); log=%v", st.snapshotLog())
}

// Test_1_4_41_PartitionOrder_MultiOp_StrictFIFO — a 6-op interleaved
// write/delete sequence on ONE object, with scattered attempt_counts (forcing
// cross-batch splits under ORDER BY (attempt_count,id)) and filler rows, must
// apply in strict id order under ApplyConcurrency>1. The final state equals the
// LAST op (delete → absent); the ops of the object appear in exact id order.
func Test_1_4_41_PartitionOrder_MultiOp_StrictFIFO(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	st := newTupleState()
	st.delay = 10 * time.Millisecond // overlap distinct-tuple applies

	const obj = "vpc_subnet:fifo0000000000000001"
	key := tupleKeyOf("user:u", "v_get", obj)

	// Alternating write/delete on obj, scattered attempt_counts. Insert order =
	// id order = the intended apply order (grant,revoke,grant,revoke,grant,revoke).
	type op struct {
		et      string
		attempt int
	}
	seq := []op{
		{"fga.tuple.write", 4},
		{"fga.tuple.delete", 0},
		{"fga.tuple.write", 7},
		{"fga.tuple.delete", 1},
		{"fga.tuple.write", 2},
		{"fga.tuple.delete", 0},
	}
	ids := make([]int64, 0, len(seq))
	for _, o := range seq {
		ids = append(ids, insertOutboxRowAttempt(t, ctx, pool, o.et,
			fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":%q}`, obj), o.attempt))
	}
	// Filler rows of other tuples so the key's ops span several claim batches.
	filler := make([]int64, 0, 8)
	for i := 0; i < 8; i++ {
		filler = append(filler, insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":"noise:obj%02d"}`, i), 0))
	}

	cfg := testCfg()
	cfg.ApplyConcurrency = 4
	cfg.PartitionColumn = iamPartitionKey
	cfg.MaxAttempts = 10
	cfg.PollFallback = 300 * time.Millisecond

	dCancel, done := startDrainerApplier(t, ctx, pool, cfg, st.apply)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	for _, id := range append(append([]int64{}, ids...), filler...) {
		waitForRowSent(t, ctx, pool, id, 30*time.Second)
	}

	want := []string{
		"write:" + key, "delete:" + key,
		"write:" + key, "delete:" + key,
		"write:" + key, "delete:" + key,
	}
	assert.Equalf(t, want, st.opsFor(key),
		"strict per-partition FIFO under ApplyConcurrency; full log=%v", st.snapshotLog())
	assert.Falsef(t, st.isPresent(key),
		"final state = last op (delete) → absent; log=%v", st.snapshotLog())
}

// Test_1_4_42_PartitionColumn_DistinctTuples_StayConcurrent — the partition
// predicate must NOT serialise UNRELATED partitions. K rows of K DISTINCT tuples
// (each its own partition, each a partition head) with a fixed apply latency L
// must drain concurrently (~ceil(K/N)*L), not serially (~K*L). Guards against the
// NOT EXISTS accidentally throttling cross-partition throughput (the whole point
// of raising ApplyConcurrency for iam).
func Test_1_4_42_PartitionColumn_DistinctTuples_StayConcurrent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	const (
		k = 24
		l = 200 * time.Millisecond
		n = 8
	)
	fa.setDelay("fga.tuple.write", l)

	cfg := testCfg()
	cfg.ApplyConcurrency = n
	cfg.PartitionColumn = iamPartitionKey
	cfg.ApplyTimeout = 5 * time.Second
	cfg.PollFallback = 500 * time.Millisecond

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	ids := make([]int64, k)
	for i := 0; i < k; i++ {
		// DISTINCT tuple per row → each is its own partition head.
		ids[i] = insertOutboxRow(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":"obj:%03d"}`, i))
	}

	// ceil(24/8)*200ms = 600ms concurrent vs 4.8s sequential. 3s sits between.
	const deadline = 3 * time.Second
	require.Truef(t, fa.waitForCalls(k, deadline),
		"distinct-partition rows must drain concurrently within %s (sequential ~%s); got %d in flight",
		deadline, time.Duration(k)*l, fa.countCalls())

	for _, id := range ids {
		waitForRowSent(t, ctx, pool, id, 3*time.Second)
	}
}

// Test_1_4_43_PoisonedPredecessor_DoesNotWedgeSuccessor — a POISONED predecessor
// (attempt_count = MaxAttempts, never claimable) of a partition must NOT block a
// deliverable successor of the same partition forever. The blocking predicate
// excludes poisoned rows (`p.attempt_count < MaxAttempts`) precisely so a
// permanently-dead head does not wedge its partition. (Without that exclusion the
// successor would never be claimed → sent_at stays NULL → this times out.)
func Test_1_4_43_PoisonedPredecessor_DoesNotWedgeSuccessor(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier() // default success; poisoned row is never claimed/applied

	const obj = "vpc_network:wedge000000000000001"
	const maxAttempts = 10

	// P: poisoned predecessor — attempt_count already at MaxAttempts, so the
	// claim's poison-gate never selects it. Smaller id than the successor.
	_ = insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.write",
		fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":%q}`, obj), maxAttempts)
	// S: deliverable successor of the SAME TUPLE (same partition — identical
	// user/relation/object, so the narrowed key still puts it behind P), fresh.
	// id > P.
	sID := insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.delete",
		fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":%q}`, obj), 0)

	cfg := testCfg()
	cfg.ApplyConcurrency = 4
	cfg.PartitionColumn = iamPartitionKey
	cfg.MaxAttempts = maxAttempts
	cfg.PollFallback = 300 * time.Millisecond

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	// S must be delivered despite the poisoned predecessor sharing its partition.
	row := waitForRowSent(t, ctx, pool, sID, 10*time.Second)
	assert.NotNil(t, row.sentAt,
		"deliverable successor must not be wedged by a poisoned same-partition predecessor")
}

// Test_1_4_44_WedgeObserver_ReportsStuckPartition — the opt-in wedge observer
// reports a partition whose head has been unsent longer than WedgeWarnAfter (a
// stuck head that blocks its successors), and does NOT report a fresh partition.
func Test_1_4_44_WedgeObserver_ReportsStuckPartition(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier() // fresh rows apply+clear; poisoned head is never claimed

	const stuckObj = "vpc_network:wedgeobs0000000001"
	const freshObj = "vpc_network:wedgeobsfresh000001"
	const maxAttempts = 10
	// The observer reports the PARTITION key, which is now the tuple triple.
	stuck := tupleKeyOf("user:u", "v_get", stuckObj)
	fresh := tupleKeyOf("user:u", "v_get", freshObj)

	// A poisoned (never-claimed) head, backdated 8s → an old unsent partition head.
	insertOutboxRowAged(t, ctx, pool, "fga.tuple.write",
		fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":%q}`, stuckObj), maxAttempts, 8*time.Second)
	// A fresh deliverable row of another tuple — applied quickly, never wedged.
	insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		fmt.Sprintf(`{"user":"user:u","relation":"v_get","object":%q}`, freshObj))

	var mu sync.Mutex
	seen := map[string]time.Duration{}
	obs := func(part string, age time.Duration) {
		mu.Lock()
		seen[part] = age
		mu.Unlock()
	}

	cfg := testCfg()
	cfg.PartitionColumn = iamPartitionKey
	cfg.WedgeWarnAfter = 200 * time.Millisecond
	cfg.MaxAttempts = maxAttempts
	cfg.PollFallback = 100 * time.Millisecond

	dCancel, done := startDrainerApplier(t, ctx, pool, cfg, applierFromFake(fa),
		drainer.WithWedgeObserver[rawPayload](obs))
	defer func() { dCancel(); <-done }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		age, ok := seen[stuck]
		_, reportedFresh := seen[fresh]
		mu.Unlock()
		if ok {
			assert.GreaterOrEqualf(t, age, cfg.WedgeWarnAfter,
				"reported wedge age must exceed WedgeWarnAfter (head backdated 8s)")
			assert.Falsef(t, reportedFresh,
				"fresh (non-wedged) partition %q must NOT be reported as wedged", fresh)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("wedge observer never reported stuck partition %q; seen=%v", stuck, seen)
}

// Test_1_4_46_PartitionKey_SameObject_DistinctSubjects_StayConcurrent — the
// partition key must be the NARROWEST key over which events fail to commute.
//
// K rows on ONE object, each for a DISTINCT subject (distinct user), each a
// separate FGA tuple. They commute: the target keys its state by the whole
// (user, relation, object) triple, so no ordering constraint exists between them.
// With the shipped `tuple_key` partition each row is its own head and they drain
// concurrently (~ceil(K/N)*L). With a partition of `payload->>'object'` they land
// in ONE chain and drain strictly one-at-a-time (~K*L) — that is exactly the
// production defect: a revoke queued behind every unrelated grant on the same
// object. On the live stand a single revoke sat behind up to 632 same-object
// predecessors while its own tuple had at most 3.
//
// RED with PartitionColumn = "payload->>'object'" (24 x 200ms serial ≈ 4.8s > the
// 3s deadline); GREEN with "tuple_key" (ceil(24/8) x 200ms ≈ 0.6s).
func Test_1_4_46_PartitionKey_SameObject_DistinctSubjects_StayConcurrent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	const (
		k   = 24
		l   = 200 * time.Millisecond
		n   = 8
		obj = "vpc_network:hotobject00000001"
	)
	fa.setDelay("fga.tuple.write", l)

	cfg := testCfg()
	cfg.ApplyConcurrency = n
	cfg.PartitionColumn = iamPartitionKey
	cfg.ApplyTimeout = 5 * time.Second
	cfg.PollFallback = 500 * time.Millisecond

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	ids := make([]int64, k)
	for i := 0; i < k; i++ {
		// SAME object, DISTINCT subject → K independent tuples, K partitions.
		ids[i] = insertOutboxRow(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"user":"user:usr%03d","relation":"v_get","object":%q}`, i, obj))
	}

	// ceil(24/8)*200ms = 600ms concurrent vs 4.8s serialised. 3s sits between.
	const deadline = 3 * time.Second
	require.Truef(t, fa.waitForCalls(k, deadline),
		"rows that share an OBJECT but not a TUPLE must drain concurrently within %s "+
			"(over-serialised ~%s): the partition key is wider than the non-commutative "+
			"key, so every revoke queues behind unrelated grants on the same object; "+
			"got %d of %d applied",
		deadline, time.Duration(k)*l, fa.countCalls(), k)

	for _, id := range ids {
		waitForRowSent(t, ctx, pool, id, 5*time.Second)
	}
}

// Test_1_4_47_NarrowKey_PreservesOrder_UnderSameObjectNoise — narrowing the
// partition from the object to the tuple must NOT open an ordering window.
//
// One tuple K carries the non-commutative pair WRITE(attempt=5) → DELETE(attempt=0)
// with the delete at the LARGER id; six further rows share K's OBJECT but belong to
// OTHER subjects (other tuples). Under `ORDER BY (attempt_count, id)` the bumped
// write sorts AFTER every fresh row, so the noise is claimed first and the write
// and the delete of K land in DIFFERENT batches — the cross-batch reorder that
// leaks a surviving tuple past a revoke.
//
// Asserted together:
//   - ORDER HELD: K's ops apply write→delete and K ends ABSENT, even though they
//     were split across batches (the narrowed partition still blocks a successor
//     behind an unsent same-TUPLE predecessor);
//   - NOT OVER-SERIALISED: at least one same-object OTHER-tuple row applied BEFORE
//     K's write, i.e. the narrowed key really did release the rows that do commute.
//     Under a `payload->>'object'` partition K's write is the object's head, so no
//     same-object row could precede it — this half is RED there.
func Test_1_4_47_NarrowKey_PreservesOrder_UnderSameObjectNoise(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	st := newTupleState()

	const obj = "vpc_network:mixed000000000001"
	key := tupleKeyOf("user:target", "v_get", obj)

	// W then D of the SAME tuple; W bumped (prior transient), D fresh → the claim's
	// ORDER BY (attempt_count,id) would place D in an EARLIER batch than W.
	wID := insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.write",
		fmt.Sprintf(`{"user":"user:target","relation":"v_get","object":%q}`, obj), 5)
	dID := insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.delete",
		fmt.Sprintf(`{"user":"user:target","relation":"v_get","object":%q}`, obj), 0)

	// Same OBJECT, other SUBJECTS — commuting rows that must not be held behind W.
	noise := make([]int64, 0, 6)
	noiseKeys := make(map[string]bool, 6)
	for i := 0; i < 6; i++ {
		user := fmt.Sprintf("user:other%02d", i)
		noise = append(noise, insertOutboxRowAttempt(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"user":%q,"relation":"v_get","object":%q}`, user, obj), 0))
		noiseKeys["write:"+tupleKeyOf(user, "v_get", obj)] = true
	}

	cfg := testCfg()
	cfg.ApplyConcurrency = 4
	cfg.PartitionColumn = iamPartitionKey
	cfg.MaxAttempts = 10
	cfg.PollFallback = 300 * time.Millisecond

	dCancel, done := startDrainerApplier(t, ctx, pool, cfg, st.apply)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, cfg.Channel)

	for _, id := range append([]int64{wID, dID}, noise...) {
		waitForRowSent(t, ctx, pool, id, 30*time.Second)
	}

	log := st.snapshotLog()

	// (1) Order across batches held for the non-commutative pair.
	assert.Equalf(t, []string{"write:" + key, "delete:" + key}, st.opsFor(key),
		"narrowing the partition must not reorder the same TUPLE: expected write→delete; log=%v", log)
	require.Falsef(t, st.isPresent(key),
		"cross-batch reorder leak: tuple %q PRESENT after its revoke; log=%v", key, log)

	// (2) …while the rows that DO commute were not held behind it.
	precededW := 0
	for _, e := range log {
		if e == "write:"+key {
			break
		}
		if noiseKeys[e] {
			precededW++
		}
	}
	assert.Positivef(t, precededW,
		"no same-object OTHER-tuple row applied before the bumped write of %q: the partition "+
			"is still serialising rows that commute (partition key wider than the tuple); log=%v",
		key, log)
}

// Test_1_4_48_NarrowKey_OrderHolds_AcrossReplicas — per-partition FIFO must hold
// CROSS-REPLICA, not merely cross-batch, at the narrowed key.
//
// Two drainer instances race on one database (the HA shape of
// TestW1_1_10). The ordering guarantee comes from the claim predicate reading the
// peer's uncommitted claim as still `sent_at IS NULL` in its own snapshot: while
// replica A holds an unsent predecessor of tuple K, replica B cannot claim K's
// successor, so no replica can apply a DELETE ahead of its WRITE. Narrowing the
// partition to the tuple does not weaken this — the predicate is unchanged, only
// its equality key is.
func Test_1_4_48_NarrowKey_OrderHolds_AcrossReplicas(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool1, dsn := setupDrainerPG(t)
	pool2, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	pgtest.ClosePoolAtEnd(t, pool2)

	// ONE shared order-sensitive model — both replicas apply into it.
	st := newTupleState()
	st.delay = 15 * time.Millisecond // widen the window a reorder would exploit

	const obj = "vpc_network:harepl00000000001"
	key := tupleKeyOf("user:target", "v_get", obj)

	wID := insertOutboxRowAttempt(t, ctx, pool1, "fga.tuple.write",
		fmt.Sprintf(`{"user":"user:target","relation":"v_get","object":%q}`, obj), 5)
	dID := insertOutboxRowAttempt(t, ctx, pool1, "fga.tuple.delete",
		fmt.Sprintf(`{"user":"user:target","relation":"v_get","object":%q}`, obj), 0)
	filler := make([]int64, 0, 12)
	for i := 0; i < 12; i++ {
		filler = append(filler, insertOutboxRowAttempt(t, ctx, pool1, "fga.tuple.write",
			fmt.Sprintf(`{"user":"user:other%02d","relation":"v_get","object":%q}`, i, obj), 0))
	}

	cfg := testCfg()
	cfg.ApplyConcurrency = 4
	cfg.PartitionColumn = iamPartitionKey
	cfg.MaxAttempts = 10
	cfg.PollFallback = 300 * time.Millisecond

	d1Cancel, done1 := startDrainerApplier(t, ctx, pool1, cfg, st.apply)
	defer func() { d1Cancel(); <-done1 }()
	d2Cancel, done2 := startDrainerApplier(t, ctx, pool2, cfg, st.apply)
	defer func() { d2Cancel(); <-done2 }()

	waitForListenerReady(t, ctx, pool1, cfg.Channel)

	for _, id := range append([]int64{wID, dID}, filler...) {
		waitForRowSent(t, ctx, pool1, id, 45*time.Second)
	}

	assert.Equalf(t, []string{"write:" + key, "delete:" + key}, st.opsFor(key),
		"two replicas must not reorder one tuple's write/delete; log=%v", st.snapshotLog())
	require.Falsef(t, st.isPresent(key),
		"cross-REPLICA reorder leak: tuple %q PRESENT after its revoke; log=%v",
		key, st.snapshotLog())
}
