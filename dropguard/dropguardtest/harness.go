// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package dropguardtest runs a service's drop guard against a real database.
//
// It lives apart from dropguard so that the library which decides things stays free
// of a dependency on the test runner. What a service needs to adopt the guard is one
// call: the harness replays that service's own migrations into a throwaway Postgres,
// pauses immediately before every drop, counts, and refuses on any disagreement with
// the service's dropguard.json.
package dropguardtest

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver for the harness
	"github.com/pressly/goose/v3"

	"github.com/PRO-Robotech/corelib/dropguard"
	"github.com/PRO-Robotech/corelib/pgtest"
)

// Options configures one measured run.
type Options struct {
	// Service is the service name, matched against the manifest's own field.
	Service string
	// FS is the service's embedded migration directory.
	FS fs.FS
	// ManifestPath is the path to dropguard.json, usually "dropguard.json"
	// relative to the migrations package under test.
	//
	// A chain that drops nothing owes no declarations, so an absent manifest is
	// accepted for such a chain and only for it — the same rule the repo-wide
	// static gate already applies to a service that has never dropped a table.
	ManifestPath string
	// DropsExpected is how many Up-section DROP TABLE statements the CALLER says
	// its chain holds. It is declared here rather than inferred, and that is the
	// whole of its value: it cannot move on its own, so a drop that appeared
	// without it moving is a drop nobody looked at.
	//
	// ZERO IS A LEGITIMATE VALUE, and saying so is the point. This used to be an
	// unstated assumption inside the harness — a chain with no drops was refused
	// outright, on the reasoning that the run would then assert nothing. That
	// reasoning was true of the population the harness was written for, where
	// every caller had drops, and it stopped being true when a service squashed
	// its chain into one primary migration: a consolidated chain is one STATE, and
	// a state has no history of drops to count. The refusal then fired on the
	// correct answer.
	//
	// What the run still asserts at zero is not nothing: the chain replays to head
	// against a real database, the manifest is reconciled against the migrations
	// (so a declaration that outlived its drop is still refused), and this number
	// still ratchets — add a drop and the count moves off zero and goes red.
	//
	// The undeclared zero value is safe in the only direction that matters: a
	// caller who forgets the field declares zero, and a chain that actually drops
	// something goes red rather than quiet.
	DropsExpected int
	// Seed, when set, runs each time the chain reaches a version at which
	// something is about to be counted, and is given the version actually
	// reached. It exists so the guard can be proved to fail on a table that is
	// not empty: the proof injects a row here, and the run must go red naming
	// that table. Nil in the gate itself.
	Seed func(ctx context.Context, db *sql.DB, reachedVersion int64) error
}

// Run replays the chain, measures every drop, and fails t on any refusal.
//
// It also fails when the run measured fewer drops than the chain contains. A guard
// that reports no violations because it never got as far as asking is the exact
// shape of the problem this package was written for, so "how many did you look at"
// is asserted next to "what did you find".
//
// Returns the report so a caller can assert something more specific.
func Run(t *testing.T, opts Options) dropguard.Report {
	t.Helper()
	rep := Measure(t, opts)
	for _, v := range rep.Violations {
		t.Errorf("%s", v.Error())
	}
	if rep.Measured != rep.DropsInChain {
		t.Errorf("%s: measured %d of %d drop(s); the ones not reached are unverified, not clean",
			opts.Service, rep.Measured, rep.DropsInChain)
	}
	return rep
}

// Measure does the work and REPORTS, without turning a refusal into a test failure.
//
// It is separate from Run so that the guard can be proved to refuse: a proof injects
// a row, calls Measure, and asserts on the violations it gets back. Were the only
// entry point one that fails the test, the only way to check that the guard fails
// would be to watch it fail — which is not a check.
//
// It still fails t for anything that means no measurement happened at all: a chain
// that will not replay, an unreadable manifest, a container that will not start.
// Those are not clean runs either.
func Measure(t *testing.T, opts Options) dropguard.Report {
	t.Helper()
	ctx := context.Background()

	// The static half never degrades: it needs no database, so it runs in every
	// environment and fails hard. Reading the migrations and reading the manifest
	// are the two things that are always possible.
	inv, err := dropguard.Inventory(opts.Service, opts.FS)
	if err != nil {
		// Inventory itself refuses a scan that read no files, so "nothing was
		// read" is already fatal and does not need repeating here. What is left
		// below is a claim about DROPS, which is a different thing entirely.
		t.Fatalf("inventory: %v", err)
	}
	// The judgement lives in the library, not here, so that it can be handed
	// numbers directly by an injection: a comparison exercisable only by breaking a
	// real service's chain is a comparison nobody exercises.
	if finding := dropguard.AdjudicateDeclaredDropCount(
		opts.Service, len(inv.Drops), inv.FilesScanned, opts.DropsExpected); finding != "" {
		t.Error(finding)
	}

	man, err := dropguard.LoadManifest(opts.ManifestPath)
	if err != nil {
		// A chain that drops nothing has nothing to declare, so its manifest may
		// be absent — and only then. Any other unreadable manifest is fatal: a
		// guard that cannot read what it is checking against has not checked.
		if !dropguard.ManifestAbsenceIsLegitimate(len(inv.Drops), err) {
			t.Fatalf("manifest: %v", err)
		}
		man = dropguard.Manifest{Service: opts.Service}
	}
	staticViolations := dropguard.Reconcile(inv, man)

	// The measured half needs a database, and that is the one dimension that can
	// be unavailable. When it is, the run does not quietly pass: every drop is
	// printed as unanswered, counted in the census, and the test is marked skipped
	// — so "the count did not run" can never be read as "the count came back
	// clean". Same discipline as the tracker dimension in the storage audits.
	if testing.Short() {
		rep := dropguard.NothingMeasured(inv)
		rep.Violations = staticViolations
		rep.WriteCensus(os.Stdout)
		for _, v := range staticViolations {
			t.Errorf("%s", v.Error())
		}
		if rep.DropsInChain == 0 {
			t.Skipf("%s: -short reaches no database; the chain holds no drops, so nothing was owed a count, but neither was the chain replayed to head",
				opts.Service)
		}
		t.Skipf("%s: -short leaves %d drop(s) uncounted; the declarations were checked, the rows were not",
			opts.Service, rep.DropsInChain)
		return rep
	}

	db := openEmptyDatabase(t)

	goose.SetBaseFS(opts.FS)
	if serr := goose.SetDialect("postgres"); serr != nil {
		t.Fatalf("goose dialect: %v", serr)
	}
	goose.SetLogger(goose.NopLogger())

	step := func(version int64) error {
		if version > 0 {
			if uerr := goose.UpTo(db, ".", version); uerr != nil {
				return uerr
			}
		}
		if opts.Seed != nil {
			return opts.Seed(ctx, db, version)
		}
		return nil
	}

	rep, err := dropguard.MeasureChain(ctx, db, inv, man, step)
	rep.Violations = append(staticViolations, rep.Violations...)
	// The census is written before anything else can end the run, and written
	// whether or not the run was verbose: what was read is as much of the result
	// as what was found.
	rep.WriteCensus(os.Stdout)
	if err != nil {
		t.Fatalf("%s: the chain could not be replayed, so %d drop(s) were never counted: %v",
			opts.Service, rep.DropsInChain-rep.Measured, err)
	}

	// Leave the database at head so the run also proves the chain completes.
	if uerr := goose.Up(db, "."); uerr != nil {
		t.Fatalf("%s: chain does not reach head: %v", opts.Service, uerr)
	}
	return rep
}

// openEmptyDatabase takes this run's database from the one Postgres the test binary
// owns, instead of starting a container per call.
//
// EMPTY is not incidental, it is the precondition: the harness replays the service's
// chain itself and pauses before every drop to count, so it must start from before the
// first migration. A consumer therefore wires pgtest with NO Migrate — a pre-migrated
// template would leave nothing to walk and the census would count nothing.
//
// The posture on "no database" is unchanged and deliberate: pgtest.NewEmptyDB fails t
// rather than skipping it, because a run that could not count must never be readable as
// a run that counted and found nothing. That is the same reason -short above prints
// every drop as unanswered instead of passing quietly.
func openEmptyDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", pgtest.NewEmptyDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Registered after pgtest's own drop, so it runs before it: LIFO cleanup closes the
	// connection first, and the database goes with (FORCE) regardless.
	t.Cleanup(func() { _ = db.Close() })
	return db
}
