// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// gate_integration_test.go — the live half against a real database and real goose
// bookkeeping.
//
// The unit probes next door decide everything with a fake counter, which is right for
// the decision logic and says nothing about the two pieces that only exist against
// Postgres: that Observe counts what is actually there, and that GooseApplied reads
// goose's own table the way goose writes it. Those are asserted here, on a chain this
// file brings with it — vpc's real history would tie the proof to one service's past.
package dropguard_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"testing/fstest"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/PRO-Robotech/corelib/dropguard"
	"github.com/PRO-Robotech/corelib/pgtest"
)

var liveChain = fstest.MapFS{
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
}

// TestIntegration_GateCountsTheLiveDatabase walks one database through the states an
// operator's database is actually in, and asserts the gate at each.
//
// It is one test rather than several because the states are sequential: "already
// applied" only exists after "pending", and rebuilding the sequence per case would
// prove less at three times the cost.
func TestIntegration_GateCountsTheLiveDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("live drop preflight needs a database; -short leaves it unmeasured, which is not the same as clean")
	}
	ctx := context.Background()

	db, err := sql.Open("pgx", pgtest.NewEmptyDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(liveChain)
	if serr := goose.SetDialect("postgres"); serr != nil {
		t.Fatalf("goose dialect: %v", serr)
	}
	goose.SetLogger(goose.NopLogger())

	gate := func(t *testing.T) (string, error) {
		t.Helper()
		var sb strings.Builder
		err := dropguard.Gate(ctx, db, "demo", liveChain, &sb, dropguard.WholeChain())
		t.Logf("%s", sb.String())
		return sb.String(), err
	}

	t.Run("nothing built yet: both tables absent, nothing to destroy", func(t *testing.T) {
		census, err := gate(t)
		if err != nil {
			t.Fatalf("a database the chain has not built holds nothing: %v", err)
		}
		if !strings.Contains(census, "observed absent") {
			t.Errorf("absence must be on the record, census was:\n%s", census)
		}
	})

	if uerr := goose.UpTo(db, ".", 1); uerr != nil {
		t.Fatalf("up to 0001: %v", uerr)
	}

	t.Run("tables exist and are empty: the drops are free", func(t *testing.T) {
		census, err := gate(t)
		if err != nil {
			t.Fatalf("empty tables must pass: %v", err)
		}
		if !strings.Contains(census, "counted 2 of 2") {
			t.Errorf("census must say how many of how many were counted:\n%s", census)
		}
	})

	if _, ierr := db.ExecContext(ctx, `INSERT INTO widgets (id) VALUES ('kept-by-a-tenant')`); ierr != nil {
		t.Fatalf("seed: %v", ierr)
	}

	t.Run("a row nobody accounted for stops the migration", func(t *testing.T) {
		census, err := gate(t)
		if err == nil {
			t.Fatal("a pending drop of a table holding a row must refuse")
		}
		for _, want := range []string{"widgets", "1 row"} {
			if !strings.Contains(census, want) {
				t.Errorf("census does not name %q:\n%s", want, census)
			}
		}
		if !strings.Contains(census, dropguard.ApprovalEnv) {
			t.Errorf("the refusal must tell the operator what to set:\n%s", census)
		}
	})

	t.Run("the operator names that drop and it proceeds", func(t *testing.T) {
		t.Setenv(dropguard.ApprovalEnv, "3/widgets")
		census, err := gate(t)
		if err != nil {
			t.Fatalf("an approved drop must proceed: %v", err)
		}
		if !strings.Contains(census, "APPROVED BY OPERATOR") {
			t.Errorf("an approval must stay on the record:\n%s", census)
		}
	})

	t.Run("approving one drop does not release another", func(t *testing.T) {
		if _, ierr := db.ExecContext(ctx, `INSERT INTO gadgets (id) VALUES ('also-kept')`); ierr != nil {
			t.Fatalf("seed: %v", ierr)
		}
		t.Setenv(dropguard.ApprovalEnv, "3/widgets")
		census, err := gate(t)
		if err == nil {
			t.Fatal("the unnamed drop must still refuse")
		}
		if !strings.Contains(census, "gadgets") {
			t.Errorf("census does not name the refused table:\n%s", census)
		}
		if _, derr := db.ExecContext(ctx, `DELETE FROM gadgets`); derr != nil {
			t.Fatalf("cleanup: %v", derr)
		}
	})

	// Now apply the whole chain. Both drops become history, and the gate must read
	// that from goose's own table rather than from anything this test told it.
	t.Setenv(dropguard.ApprovalEnv, "3/widgets")
	if uerr := goose.Up(db, "."); uerr != nil {
		t.Fatalf("up to head: %v", uerr)
	}

	t.Run("applied drops are not pending, and the count says so", func(t *testing.T) {
		t.Setenv(dropguard.ApprovalEnv, "")
		census, err := gate(t)
		if err != nil {
			t.Fatalf("no drop is pending, so nothing can be destroyed: %v", err)
		}
		if !strings.Contains(census, "0 pending") {
			t.Errorf("the applied set was not read from goose's own table:\n%s", census)
		}
		// The chain still contains both drops — losing that number would mean the
		// gate had stopped reading the migrations, not that the work was done.
		if !strings.Contains(census, "2 drop(s) in chain") {
			t.Errorf("chain census lost:\n%s", census)
		}
	})
}

// TestIntegration_DatabaseThatPredatesTheChain — a database our migrations did not
// build.
//
// It has the table and no goose bookkeeping at all, which is precisely what the
// "absent"-kind declarations in this tree are aimed at: statements addressed to
// databases where something was created outside the chain. On such a database
// nothing is applied, so every drop is pending, and rows written before the chain
// existed are still rows.
//
// It is separate from the walk above because its whole point is the ABSENCE of the
// bookkeeping table — a state the walk destroys the moment it runs goose once.
func TestIntegration_DatabaseThatPredatesTheChain(t *testing.T) {
	if testing.Short() {
		t.Skip("live drop preflight needs a database; -short leaves it unmeasured, which is not the same as clean")
	}
	ctx := context.Background()

	db, err := sql.Open("pgx", pgtest.NewEmptyDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// No goose anywhere: somebody else made this table and put a row in it.
	for _, q := range []string{
		`CREATE TABLE widgets (id TEXT PRIMARY KEY)`,
		`INSERT INTO widgets (id) VALUES ('written-before-the-chain')`,
	} {
		if _, e := db.ExecContext(ctx, q); e != nil {
			t.Fatalf("%s: %v", q, e)
		}
	}

	var sb strings.Builder
	gerr := dropguard.Gate(ctx, db, "demo", liveChain, &sb, dropguard.WholeChain())
	t.Logf("%s", sb.String())

	if gerr == nil {
		t.Fatal("rows written before the chain existed are still rows; the drop must refuse")
	}
	if !strings.Contains(sb.String(), "widgets") {
		t.Errorf("census does not name the refused table:\n%s", sb.String())
	}
	// The mirror inside the same run: the other drop's table was never created here,
	// so it is reported absent rather than swept into the refusal.
	if !strings.Contains(sb.String(), "observed absent") {
		t.Errorf("a table that is not there destroys nothing, and that must be on the record:\n%s", sb.String())
	}
}

// TestIntegration_NoBookkeepingIsNotUnreachable — the pair that must never collapse.
//
// Both states answer nothing about any migration version, and they look alike from a
// distance. They mean opposite things:
//
//	no bookkeeping table   the database is real and reachable and has applied
//	                       nothing — a fresh install, or one that predates this
//	                       chain. Legitimate. Every drop is pending, so MORE gets
//	                       counted, not less.
//	unreachable            we know nothing at all, and "applied nothing" would be a
//	                       guess that happens to look safe.
//
// Asserting them separately would leave the interesting question — whether the code
// can tell them apart — untouched, so they are put to the SAME function in the same
// test and required to come back different.
func TestIntegration_NoBookkeepingIsNotUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("live drop preflight needs a database; -short leaves it unmeasured, which is not the same as clean")
	}
	ctx := context.Background()

	live, err := sql.Open("pgx", pgtest.NewEmptyDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	t.Run("no bookkeeping: answered, and the answer is 'nothing applied'", func(t *testing.T) {
		applied, aerr := dropguard.GooseApplied(ctx, live)(3)
		if aerr != nil {
			t.Fatalf("a reachable database without goose bookkeeping has applied nothing; "+
				"treating that as an error would refuse every fresh install: %v", aerr)
		}
		if applied {
			t.Fatal("nothing has been applied here")
		}
	})

	t.Run("unreachable: NOT answered, and it must say so", func(t *testing.T) {
		dead, oerr := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
		if oerr != nil {
			t.Fatalf("open: %v", oerr)
		}
		if cerr := dead.Close(); cerr != nil {
			t.Fatalf("close: %v", cerr)
		}
		applied, aerr := dropguard.GooseApplied(ctx, dead)(3)
		if aerr == nil {
			t.Fatalf("an unreachable database returned (%v, nil) — a guess that looks safe "+
				"is the whole failure mode this package exists to prevent", applied)
		}
	})
}

// TestIntegration_TargetedRunCountsOnlyWhatItWillApply — the live half of #1487.
//
// The decision logic is settled next door with a fake counter. What only a database
// can show is that the narrowing rides on the SAME goose bookkeeping and the same
// live counts as the unnarrowed run — that it is a question about this database,
// not a branch that stopped asking.
//
// The pairing is the point, and both halves run against ONE database in ONE state:
// the rows, the applied set and the migrations are identical across the subtests,
// and the target is the only thing that differs.
func TestIntegration_TargetedRunCountsOnlyWhatItWillApply(t *testing.T) {
	if testing.Short() {
		t.Skip("live drop preflight needs a database; -short leaves it unmeasured, which is not the same as clean")
	}
	ctx := context.Background()

	db, err := sql.Open("pgx", pgtest.NewEmptyDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(liveChain)
	if serr := goose.SetDialect("postgres"); serr != nil {
		t.Fatalf("goose dialect: %v", serr)
	}
	goose.SetLogger(goose.NopLogger())
	if uerr := goose.UpTo(db, ".", 1); uerr != nil {
		t.Fatalf("up to 0001: %v", uerr)
	}

	gate := func(t *testing.T, target dropguard.Target) (string, error) {
		t.Helper()
		var sb strings.Builder
		gerr := dropguard.Gate(ctx, db, "demo", liveChain, &sb, target)
		t.Logf("%s", sb.String())
		return sb.String(), gerr
	}

	// A tenant row in gadgets, which 0009 drops. A run that stops at 0003 never
	// executes that migration.
	if _, ierr := db.ExecContext(ctx, `INSERT INTO gadgets (id) VALUES ('beyond-the-target')`); ierr != nil {
		t.Fatalf("seed: %v", ierr)
	}

	t.Run("beyond the target: a run stopping at 0003 proceeds", func(t *testing.T) {
		census, gerr := gate(t, dropguard.UpTo(3))
		if gerr != nil {
			t.Fatalf("this run never executes 0009, so it cannot destroy those rows: %v", gerr)
		}
		if !strings.Contains(census, "not reached by this run") || !strings.Contains(census, "gadgets") {
			t.Errorf("a deferred drop must stay on the record, census was:\n%s", census)
		}
	})

	t.Run("no target on the SAME database: refused", func(t *testing.T) {
		census, gerr := gate(t, dropguard.WholeChain())
		if gerr == nil {
			t.Fatal("without a target the run reaches 0009; narrowing must not be a way past the count")
		}
		if !strings.Contains(census, "gadgets") {
			t.Errorf("census does not name the refused table:\n%s", census)
		}
	})

	t.Run("the ZERO VALUE behaves as the whole chain", func(t *testing.T) {
		var forgotten dropguard.Target
		if _, gerr := gate(t, forgotten); gerr == nil {
			t.Fatal("a caller that omits the target must get the widest check, never the narrowest")
		}
	})

	// The paired positive: without it, "the narrowed run proceeded" would also be
	// true of a guard that stopped counting anything at all.
	t.Run("within the target: still refused", func(t *testing.T) {
		if _, ierr := db.ExecContext(ctx, `INSERT INTO widgets (id) VALUES ('inside-the-target')`); ierr != nil {
			t.Fatalf("seed: %v", ierr)
		}
		census, gerr := gate(t, dropguard.UpTo(3))
		if gerr == nil {
			t.Fatal("0003 is inside a run that stops at 0003; its rows go the moment it runs")
		}
		if !strings.Contains(census, "widgets") {
			t.Errorf("census does not name the refused table:\n%s", census)
		}
	})
}
