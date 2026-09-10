// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/PRO-Robotech/corelib/dropguard"
)

// preflightChain — two retires at different versions, so "already applied" and
// "still pending" can be told apart within one run. Built from real migration text
// through the real parser: an Inv assembled by hand would let the probe agree with
// itself about a shape the parser never produces.
func preflightChain(t *testing.T) dropguard.Inv {
	t.Helper()
	inv, err := dropguard.Inventory("demo", fstest.MapFS{
		"0001_initial.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (id TEXT PRIMARY KEY);
CREATE TABLE gadgets (id TEXT PRIMARY KEY);
-- +goose Down
DROP TABLE gadgets;
DROP TABLE widgets;
`)},
		"0003_retire_widgets.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
DROP TABLE widgets;
-- +goose Down
CREATE TABLE widgets (id TEXT PRIMARY KEY);
`)},
		"0009_retire_gadgets.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
DROP TABLE gadgets;
-- +goose Down
CREATE TABLE gadgets (id TEXT PRIMARY KEY);
`)},
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	return inv
}

// allPending — nothing has been applied yet, so every drop is still ahead of us.
func allPending(int64) (bool, error) { return false, nil }

// counts serves a fixed answer per table.
func counts(m map[string]int64) dropguard.Counter {
	return func(_ context.Context, table string) (int64, error) {
		n, ok := m[table]
		if !ok {
			return 0, fmt.Errorf("%w: %q", dropguard.ErrTableAbsent, table)
		}
		return n, nil
	}
}

// TestPreflightPassesWhatItIsMeantToPass — the positive control.
//
// It is first on purpose. An assertion that a guard refuses is worth nothing beside
// a guard that refuses everything, and the two are indistinguishable without this.
func TestPreflightPassesWhatItIsMeantToPass(t *testing.T) {
	rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"widgets": 0,
		"gadgets": 0,
	}), preflightChain(t), allPending, nil, dropguard.WholeChain())

	if !rep.OK() {
		t.Fatalf("two empty tables must pass, got %+v", rep.Violations)
	}
	if rep.Counted != 2 || rep.Pending != 2 {
		t.Fatalf("counted %d of %d pending, want 2 of 2 — a pass that measured nothing is not a pass",
			rep.Counted, rep.Pending)
	}
}

// TestPreflightRefusesATableThatStillHoldsRows — the half the task exists for.
//
// The message must name the table and the number, or the operator cannot act on it.
func TestPreflightRefusesATableThatStillHoldsRows(t *testing.T) {
	rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"widgets": 17,
		"gadgets": 0,
	}), preflightChain(t), allPending, nil, dropguard.WholeChain())

	if rep.OK() {
		t.Fatal("a pending drop of a table holding 17 rows must be refused")
	}
	if len(rep.Violations) != 1 {
		t.Fatalf("want exactly one violation, got %+v", rep.Violations)
	}
	v := rep.Violations[0]
	if v.Kind != dropguard.ViolationRowCount {
		t.Errorf("violation kind %q, want %q", v.Kind, dropguard.ViolationRowCount)
	}
	msg := v.Error()
	for _, want := range []string{"widgets", "17", "0003_retire_widgets.sql"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q — an operator cannot act on it", msg, want)
		}
	}
}

// TestUnreachableIsNotEmpty — the distinction the whole guard rests on.
//
// A database that could not be asked and a table that came back empty must not reach
// the verdict as the same thing. Reporting "zero rows" for a question never asked is
// reporting the SAFE answer under exactly the conditions in which nothing is known.
func TestUnreachableIsNotEmpty(t *testing.T) {
	unreachable := func(_ context.Context, table string) (int64, error) {
		return 0, fmt.Errorf("%w: ping before counting %q", dropguard.ErrNoConnection, table)
	}
	rep := dropguard.Preflight(context.Background(), unreachable, preflightChain(t), allPending, nil, dropguard.WholeChain())

	if rep.OK() {
		t.Fatal("an unreachable database is not a clean one")
	}
	for _, v := range rep.Violations {
		if v.Kind != dropguard.ViolationUnverified {
			t.Errorf("violation kind %q, want %q", v.Kind, dropguard.ViolationUnverified)
		}
	}
	if rep.Counted != 0 {
		t.Errorf("counted %d, want 0 — nothing was measured, and the census must say so", rep.Counted)
	}
	// The mirror: the SAME shape with a real zero passes. Without this the test above
	// would also be green on a guard that refuses every outcome alike.
	if clean := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"widgets": 0, "gadgets": 0,
	}), preflightChain(t), allPending, nil, dropguard.WholeChain()); !clean.OK() {
		t.Fatalf("a measured zero must pass where an unmeasured zero refuses, got %+v", clean.Violations)
	}
}

// TestAbsentTableDestroysNothing — absent is an ANSWER, not a failure to get one.
//
// On a live database a table that is not there cannot lose a row, whatever the
// migration says about it. That differs from the replayed-chain guard, where absence
// at version-1 means the count could not be taken where it should have been.
func TestAbsentTableDestroysNothing(t *testing.T) {
	rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"gadgets": 0, // widgets missing entirely
	}), preflightChain(t), allPending, nil, dropguard.WholeChain())

	if !rep.OK() {
		t.Fatalf("an absent table holds nothing to destroy, got %+v", rep.Violations)
	}
	if len(rep.AbsentAt) != 1 || !strings.Contains(rep.AbsentAt[0], "widgets") {
		t.Fatalf("absence must be on the record, got %+v", rep.AbsentAt)
	}
	if rep.Counted != 2 {
		t.Errorf("counted %d, want 2 — an observed absence IS a measurement", rep.Counted)
	}
}

// TestAppliedVersionsAreNotPending — the drop already happened; there is nothing
// left to guard, and counting the table now would measure whatever replaced it.
func TestAppliedVersionsAreNotPending(t *testing.T) {
	appliedThrough9 := func(v int64) (bool, error) { return v <= 9, nil }

	rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"widgets": 500, "gadgets": 500,
	}), preflightChain(t), appliedThrough9, nil, dropguard.WholeChain())

	if !rep.OK() {
		t.Fatalf("no drop is pending, so nothing can be destroyed: %+v", rep.Violations)
	}
	if rep.Pending != 0 || rep.Counted != 0 {
		t.Fatalf("pending %d counted %d, want 0/0", rep.Pending, rep.Counted)
	}
	if rep.DropsInChain != 2 {
		t.Errorf("chain census lost: %d, want 2", rep.DropsInChain)
	}
}

// TestCannotTellWhetherAppliedIsRefused — the fabricated-fallback half.
//
// If the applied set cannot be read, the honest answer is "unknown". Guessing
// "applied" would skip every check, which is the safe-looking answer produced by
// knowing nothing — the exact failure mode this package was written against.
func TestCannotTellWhetherAppliedIsRefused(t *testing.T) {
	broken := func(int64) (bool, error) { return false, errors.New("permission denied for relation goose_db_version") }

	rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"widgets": 0, "gadgets": 0,
	}), preflightChain(t), broken, nil, dropguard.WholeChain())

	if rep.OK() {
		t.Fatal("an unreadable applied-set must refuse, not silently skip every drop")
	}
	if rep.Violations[0].Kind != dropguard.ViolationUnverified {
		t.Errorf("kind %q, want %q", rep.Violations[0].Kind, dropguard.ViolationUnverified)
	}
}

// TestApprovalReleasesExactlyOneDrop — an operator decision names the drop it makes.
//
// Both halves matter: the named drop goes through, and a DIFFERENT non-empty drop is
// still refused by the same run. An approval that releases more than it names is a
// global off switch wearing a specific name.
func TestApprovalReleasesExactlyOneDrop(t *testing.T) {
	approvals := []dropguard.Approval{{Version: 3, Table: "widgets"}}

	t.Run("named drop proceeds", func(t *testing.T) {
		rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
			"widgets": 17, "gadgets": 0,
		}), preflightChain(t), allPending, approvals, dropguard.WholeChain())
		if !rep.OK() {
			t.Fatalf("the approved drop must proceed, got %+v", rep.Violations)
		}
		if len(rep.Approved) != 1 || !strings.Contains(rep.Approved[0], "widgets") {
			t.Fatalf("an approval must stay on the record, got %+v", rep.Approved)
		}
	})

	t.Run("an unnamed drop is still refused", func(t *testing.T) {
		rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
			"widgets": 17, "gadgets": 4,
		}), preflightChain(t), allPending, approvals, dropguard.WholeChain())
		if rep.OK() {
			t.Fatal("approving one drop must not release another")
		}
		if len(rep.Violations) != 1 || rep.Violations[0].Table != "gadgets" {
			t.Fatalf("want the gadgets drop refused, got %+v", rep.Violations)
		}
	})

	t.Run("an approval that matches no pending drop is reported, not obeyed", func(t *testing.T) {
		stale := []dropguard.Approval{{Version: 999, Table: "nothing"}}
		rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
			"widgets": 17, "gadgets": 0,
		}), preflightChain(t), allPending, stale, dropguard.WholeChain())
		if rep.OK() {
			t.Fatal("a stale approval releases nothing")
		}
		if len(rep.StaleApprovals) != 1 {
			t.Fatalf("a stale approval must be named in the census, got %+v", rep.StaleApprovals)
		}
	})
}

// TestParseApprovalsRefusesWhatItCannotUnderstand — a mistyped approval must not
// read as "approve nothing" on a run whose whole purpose is to refuse.
func TestParseApprovalsRefusesWhatItCannotUnderstand(t *testing.T) {
	t.Run("well formed", func(t *testing.T) {
		got, err := dropguard.ParseApprovals("3/widgets, 0045/kacho_vpc.dataplane_intent")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		want := []dropguard.Approval{{Version: 3, Table: "widgets"}, {Version: 45, Table: "kacho_vpc.dataplane_intent"}}
		if len(got) != len(want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("entry %d: got %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("empty means no approvals", func(t *testing.T) {
		got, err := dropguard.ParseApprovals("   ")
		if err != nil || len(got) != 0 {
			t.Fatalf("got (%+v, %v), want (empty, nil)", got, err)
		}
	})

	for _, bad := range []string{"widgets", "3", "3/", "/widgets", "x/widgets", "3/widgets/extra"} {
		t.Run("malformed "+bad, func(t *testing.T) {
			if _, err := dropguard.ParseApprovals(bad); err == nil {
				t.Fatalf("%q parsed without error — a typo must not read as 'approve nothing'", bad)
			}
		})
	}
}

// TestCensusSaysHowMuchWasLookedAt — "no violations" from a run that measured
// nothing must not be readable as success.
func TestCensusSaysHowMuchWasLookedAt(t *testing.T) {
	rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
		"widgets": 0, "gadgets": 0,
	}), preflightChain(t), allPending, nil, dropguard.WholeChain())

	var sb strings.Builder
	rep.WriteCensus(&sb)
	out := sb.String()
	for _, want := range []string{"demo", "2"} {
		if !strings.Contains(out, want) {
			t.Errorf("census %q does not name %q", out, want)
		}
	}
	if !strings.Contains(rep.Summary(), "counted 2 of 2") {
		t.Errorf("summary %q must say how many of how many were counted", rep.Summary())
	}
}

// TestGateRefusesBeforeItEverTouchesTheDatabase — the two ways the check can be
// unable to start.
//
// Both must stop the deploy rather than wave it through. They are reachable with a
// nil handle precisely because they are decided before anything is asked of the
// database, which is also why they can be asserted without one.
func TestGateRefusesBeforeItEverTouchesTheDatabase(t *testing.T) {
	valid := fstest.MapFS{"0003_retire.sql": &fstest.MapFile{Data: []byte(
		"-- +goose Up\nDROP TABLE widgets;\n-- +goose Down\nCREATE TABLE widgets (id TEXT);\n")}}

	t.Run("migrations unreadable", func(t *testing.T) {
		err := dropguard.Gate(context.Background(), nil, "demo", fstest.MapFS{}, io.Discard, dropguard.WholeChain())
		if err == nil {
			t.Fatal("migrations that cannot be read mean the pending drops are unknown, not absent")
		}
	})

	t.Run("approval list malformed", func(t *testing.T) {
		t.Setenv(dropguard.ApprovalEnv, "not-an-approval")
		err := dropguard.Gate(context.Background(), nil, "demo", valid, io.Discard, dropguard.WholeChain())
		if err == nil {
			t.Fatal("an unreadable approval list must stop the deploy, not read as 'approve nothing'")
		}
		if !strings.Contains(err.Error(), dropguard.ApprovalEnv) {
			t.Errorf("error %q does not name the variable the operator has to fix", err)
		}
	})
}

// TestTargetCountsOnlyWhatThisRunWillApply — the decision half of #1487.
//
// A run that stops at 0003 does not execute the drop in 0009, so counting that
// table can only refuse the deploy over rows the deploy leaves alone; the operator
// then clears it by naming somebody else's drop.
//
// The cases sit in ONE test because the proof is their contrast, not any one of
// them. Cases (b) and (c) are the SAME database — identical counts, identical
// applied set — and differ in the target alone, so an implementation that stopped
// counting altogether would pass (b) and fail (c). Case (a) is the paired positive
// without which "the narrowed run proceeded" would also be true of a guard that
// refuses nothing at all.
func TestTargetCountsOnlyWhatThisRunWillApply(t *testing.T) {
	// widgets is dropped by 0003, gadgets by 0009 — see preflightChain.
	onlyWidgets := map[string]int64{"widgets": 7, "gadgets": 0}
	onlyGadgets := map[string]int64{"widgets": 0, "gadgets": 7}

	t.Run("a: a drop WITHIN the target still refuses", func(t *testing.T) {
		rep := dropguard.Preflight(context.Background(), counts(onlyWidgets),
			preflightChain(t), allPending, nil, dropguard.UpTo(3))

		if rep.OK() {
			t.Fatal("0003 is inside a run that stops at 0003; its rows are destroyed and must be refused")
		}
		if rep.Violations[0].Table != "widgets" {
			t.Errorf("refused %q, want widgets", rep.Violations[0].Table)
		}
	})

	t.Run("b: a drop BEYOND the target is deferred, not refused", func(t *testing.T) {
		rep := dropguard.Preflight(context.Background(), counts(onlyGadgets),
			preflightChain(t), allPending, nil, dropguard.UpTo(3))

		if !rep.OK() {
			t.Fatalf("a run stopping at 0003 never executes the drop in 0009: %+v", rep.Violations)
		}
		// Deferred, not forgotten: the drop is still ahead of this database, and a
		// census that dropped it would make "the target narrowed the check"
		// indistinguishable from "there was nothing else to check".
		if len(rep.Deferred) != 1 || !strings.Contains(rep.Deferred[0], "gadgets") {
			t.Errorf("deferred %v, want the 0009 drop named", rep.Deferred)
		}
		if rep.Pending != 1 {
			t.Errorf("pending %d, want 1 — only the reachable drop can destroy anything here", rep.Pending)
		}
		if rep.DropsInChain != 2 {
			t.Errorf("chain census lost: %d, want 2", rep.DropsInChain)
		}
	})

	t.Run("c: the SAME database with no target refuses — narrowing is not a bypass", func(t *testing.T) {
		rep := dropguard.Preflight(context.Background(), counts(onlyGadgets),
			preflightChain(t), allPending, nil, dropguard.WholeChain())

		if rep.OK() {
			t.Fatal("without a target the run reaches 0009, so its rows are destroyed and must be refused")
		}
		if rep.Violations[0].Table != "gadgets" {
			t.Errorf("refused %q, want gadgets", rep.Violations[0].Table)
		}
		if len(rep.Deferred) != 0 {
			t.Errorf("nothing is out of reach of a whole-chain run, deferred %v", rep.Deferred)
		}
	})

	// The zero value is the ONE thing a caller gets by forgetting, so it is asserted
	// rather than assumed: it has to be the widest check, never the narrowest. A
	// migrator wired tomorrow that omits the target must over-count, not under-count.
	t.Run("d: the ZERO VALUE counts everything, exactly as WholeChain does", func(t *testing.T) {
		var forgotten dropguard.Target

		rep := dropguard.Preflight(context.Background(), counts(onlyGadgets),
			preflightChain(t), allPending, nil, forgotten)

		if rep.OK() {
			t.Fatal("an unset target must count the whole chain; anything else is a silent bypass")
		}
		explicit := dropguard.Preflight(context.Background(), counts(onlyGadgets),
			preflightChain(t), allPending, nil, dropguard.WholeChain())
		if rep.Pending != explicit.Pending || len(rep.Violations) != len(explicit.Violations) {
			t.Errorf("zero value (pending %d, %d refusal(s)) differs from WholeChain (pending %d, %d refusal(s))",
				rep.Pending, len(rep.Violations), explicit.Pending, len(explicit.Violations))
		}
	})

	// A drop out of reach must produce NO violation of any kind — including the one
	// raised when the bookkeeping cannot be read. Refusing a deploy because we could
	// not tell whether a migration this run will not execute has already executed is
	// the same defect wearing a different coat.
	//
	// It is also why the census never calls a deferred drop "pending": the question
	// was not asked of it, and a report that answered anyway would be inventing.
	t.Run("f: an unreadable applied-set beyond the target refuses nothing", func(t *testing.T) {
		broken := func(v int64) (bool, error) {
			if v > 3 {
				return false, errors.New("permission denied for relation goose_db_version")
			}
			return false, nil
		}

		rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
			"widgets": 0, "gadgets": 7,
		}), preflightChain(t), broken, nil, dropguard.UpTo(3))

		if !rep.OK() {
			t.Fatalf("0009 is out of reach; not knowing its applied state cannot stop this run: %+v", rep.Violations)
		}
		// The mirror, so this does not pass by the guard having stopped asking: the
		// same unreadable set WITHIN reach must still refuse.
		wide := dropguard.Preflight(context.Background(), counts(map[string]int64{
			"widgets": 0, "gadgets": 7,
		}), preflightChain(t), broken, nil, dropguard.WholeChain())
		if wide.OK() {
			t.Fatal("within reach an unreadable applied-set must still refuse")
		}
	})

	// There is no "count nothing": every target counts at least what the run applies.
	t.Run("e: a target that reaches nothing applies nothing either", func(t *testing.T) {
		rep := dropguard.Preflight(context.Background(), counts(map[string]int64{
			"widgets": 7, "gadgets": 7,
		}), preflightChain(t), allPending, nil, dropguard.UpTo(1))

		if !rep.OK() {
			t.Fatalf("a run that applies no drop destroys nothing: %+v", rep.Violations)
		}
		if len(rep.Deferred) != 2 {
			t.Errorf("deferred %v, want both drops on the record", rep.Deferred)
		}
		if !strings.Contains(rep.Summary(), "up to 0001") {
			t.Errorf("the census must say WHICH question it answered: %q", rep.Summary())
		}
	})
}
