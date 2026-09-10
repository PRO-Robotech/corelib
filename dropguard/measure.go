// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard

import (
	"context"
	"fmt"
	"io"
	"sort"
)

// StepTo advances a database to exactly the given migration version. Supplied by
// the caller because this package does not choose anyone's migration runner.
type StepTo func(version int64) error

// Report is what a measured run has to say for itself: what it found, and how much
// it looked at. The second half is not decoration — "no violations" from a run that
// measured nothing is the failure this package exists to prevent, so the census is
// returned alongside the verdict and callers are expected to assert on it.
type Report struct {
	Service string
	// FilesScanned is how many migration files the inventory read to produce this
	// report. It is carried so the census can tell "the chain holds no drops" from
	// "nothing was read": those are different states, and a verdict that spells
	// them the same way turns a legitimate one into an alarm. A consolidated chain
	// — one squashed into a single primary migration — legitimately holds zero
	// drops while having been read in full.
	FilesScanned int
	// DropsInChain is how many Up-section drops the migrations contain.
	DropsInChain int
	// Measured is how many of them this run actually put a question to the
	// database about. Anything less than DropsInChain means the run is partial.
	Measured int
	// Rows is the observed count per drop, keyed "NNNN/table". Absent entries were
	// observed absent, which for an idempotency preamble is the expected answer.
	Rows map[string]int64
	// AbsentAt lists drops whose table was not there to be counted.
	AbsentAt []string
	// Unmeasured lists drops this run never put to a database at all. It exists so
	// that "nothing was found" can be told apart from "nothing was read": a run
	// that skipped the measurement must say which drops it left unanswered.
	Unmeasured []string
	// DynamicDrops is how many DROP TABLE statements the inventory could not read
	// because their subject is computed at run time. They are NOT part of
	// DropsInChain — nothing here knows what they destroy — so a run can be OK with
	// this number non-zero, and the census says the number out loud rather than
	// letting it be absent. See [DynamicDrop].
	DynamicDrops []string
	Violations   []Violation
}

// OK reports that every drop was measured and none was refused.
func (r Report) OK() bool { return r.Measured == r.DropsInChain && len(r.Violations) == 0 }

// Summary is a one-line census: the number of things looked at, not just the number
// of things found.
func (r Report) Summary() string {
	verdict := "OK"
	switch {
	case r.FilesScanned == 0:
		// Nothing was read. This is the state the census exists to make loud, and
		// it is refused upstream by Inventory — reaching it here means the report
		// was built from something other than a scan.
		verdict = "NOTHING READ"
	case r.DropsInChain == 0:
		// Read in full, and the chain drops nothing. Legitimate — a consolidated
		// chain is one state, not a history — and deliberately spelled apart from
		// NOTHING READ: the two used to share a word, so a chain that had been
		// read completely was announced as a scan of nothing.
		verdict = "NO DROPS"
	case r.Measured == 0:
		verdict = "NOT VERIFIED"
	case r.Measured < r.DropsInChain:
		verdict = "PARTIAL"
	case len(r.Violations) > 0:
		verdict = "REFUSED"
	}
	return fmt.Sprintf("drop-guard %s: %s — read %d migration file(s), measured %d of %d drop(s), %d observed absent, %d violation(s), %d drop(s) not readable (subject computed at run time)",
		r.Service, verdict, r.FilesScanned, r.Measured, r.DropsInChain, len(r.AbsentAt), len(r.Violations), len(r.DynamicDrops))
}

// WriteCensus prints what the run read and what it decided, unconditionally.
//
// It is written whether or not anything was found, and whether or not the run was
// verbose, because the number that matters most is how many drops were actually put
// to the database: a report of no violations from a run that measured nothing is
// the failure this package exists to prevent, and it must not look like success.
func (r Report) WriteCensus(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%s\n", r.Summary())

	keys := make([]string, 0, len(r.Rows))
	for k := range r.Rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "  counted %s -> %d row(s)\n", k, r.Rows[k])
	}
	for _, k := range r.AbsentAt {
		_, _ = fmt.Fprintf(w, "  observed absent %s (nothing there to destroy)\n", k)
	}
	if len(r.Unmeasured) > 0 {
		_, _ = fmt.Fprintf(w, "  NOT VERIFIED — %d drop(s) were never put to a database; this is not a clean result:\n", len(r.Unmeasured))
		for _, k := range r.Unmeasured {
			_, _ = fmt.Fprintf(w, "    %s\n", k)
		}
	}
	for _, k := range r.DynamicDrops {
		_, _ = fmt.Fprintf(w, "  NOT READABLE %s — the table name is built at run time; this gate cannot say what it destroys\n", k)
	}
	for _, v := range r.Violations {
		_, _ = fmt.Fprintf(w, "  %s\n", v.Error())
	}
}

// NothingMeasured builds the report of a run that never reached a database: every
// drop in the chain listed as unanswered.
//
// It exists so that an environment without a database produces a LOUD nothing
// rather than a quiet something. A guard whose only two outcomes are "green" and
// "green because it did not run" has one outcome.
func NothingMeasured(inv Inv) Report {
	rep := Report{Service: inv.Service, FilesScanned: inv.FilesScanned, DropsInChain: len(inv.Drops), DynamicDrops: inv.UnreadableDrops(), Rows: map[string]int64{}}
	for _, d := range inv.Drops {
		rep.Unmeasured = append(rep.Unmeasured, fmt.Sprintf("%04d/%s", d.Version, d.Table))
	}
	return rep
}

// MeasureChain replays the migrations one drop-version at a time and counts each
// table at the version immediately before it is destroyed.
//
// The ordering is the point: a drop's contents can only be counted while they still
// exist, so the chain is walked forward and paused at each version-1. A run that
// cannot reach some version stops there and says so, rather than reporting the
// drops it never got to as clean.
func MeasureChain(ctx context.Context, db Querier, inv Inv, m Manifest, step StepTo) (Report, error) {
	rep := Report{Service: inv.Service, FilesScanned: inv.FilesScanned, DropsInChain: len(inv.Drops), DynamicDrops: inv.UnreadableDrops(), Rows: map[string]int64{}}

	decls := map[string]Declaration{}
	for _, d := range m.Drops {
		decls[declKey(d.Version, d.Table)] = d
	}

	byVersion := map[int64][]Drop{}
	for _, d := range inv.Drops {
		byVersion[d.Version] = append(byVersion[d.Version], d)
	}

	for _, version := range inv.DropVersions() {
		if err := step(version - 1); err != nil {
			return rep, fmt.Errorf("dropguard: %s: could not bring the database to version %04d (the state in which %04d's drops are still countable): %w",
				inv.Service, version-1, version, err)
		}
		drops := byVersion[version]
		sort.Slice(drops, func(a, b int) bool { return drops[a].Table < drops[b].Table })
		for _, drop := range drops {
			key := fmt.Sprintf("%04d/%s", drop.Version, drop.Table)
			decl, declared := decls[declKey(drop.Version, drop.Table)]
			if !declared {
				// Reconcile reports this as its own violation; measuring against a
				// declaration that does not exist would compare a number to nothing.
				// It still goes on the record as unmeasured — an undeclared drop is
				// one nobody counted, which is the thing being counted here.
				rep.Unmeasured = append(rep.Unmeasured, key)
				continue
			}
			rows, obsErr := Observe(ctx, db, drop.Table)
			rep.Measured++
			if obsErr == nil {
				rep.Rows[key] = rows
			} else {
				rep.AbsentAt = append(rep.AbsentAt, key)
			}
			rep.Violations = append(rep.Violations, Judge(drop, decl, rows, obsErr)...)
		}
	}
	return rep, nil
}
