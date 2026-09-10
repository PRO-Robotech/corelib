// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
)

// This file is the live half of the package, and it answers a different question
// from the measured runs in measure.go.
//
// A measured run replays the chain into an empty container, so its numbers are facts
// about what OUR migrations seed. A tenant database also holds what tenants wrote,
// and no container can know that — doc.go says so, and vpc's own manifest says it
// again in as many words about a table whose live contents are unbounded.
//
// So the live question is not "does this match the replayed number". It is narrower
// and harder to argue with: WILL THIS DROP DESTROY ROWS, on the database in front of
// us, right now. Any row destroyed on a tenant database is a decision somebody has
// to make, whatever a manifest measured somewhere else.

// Counter counts the rows of one table, or explains why it could not.
//
// It is a function rather than a handle so that the decision logic below can be
// exercised without a database while the only counter that reaches production stays
// [Observe] — see [Counting]. The two error values are part of the contract: a
// caller must be able to tell "there is nothing there" from "I could not look".
type Counter func(ctx context.Context, table string) (int64, error)

// Counting is the production Counter: the same [Observe] with the same refusal to
// return a number it did not read.
func Counting(db Querier) Counter {
	return func(ctx context.Context, table string) (int64, error) { return Observe(ctx, db, table) }
}

// AppliedSet reports whether a migration version has already run on this database.
//
// It returns an error rather than a bare bool because "I could not tell" must not
// collapse into either answer. Guessing "applied" would skip every check — the
// safe-LOOKING result produced by knowing nothing.
type AppliedSet func(version int64) (bool, error)

// GooseApplied reads the applied set from goose's own bookkeeping table.
//
// A version counts as applied when its most recent row says so: goose appends a row
// per transition, so a rolled-back migration has a later row with is_applied false,
// and taking any row rather than the last would report a drop as done when it has
// been undone.
//
// A database with no bookkeeping table at all has applied nothing. That is a
// LEGITIMATE state with two ordinary causes, and neither is a fault:
//
//   - a fresh install, where the chain has not run yet;
//   - a database that PREDATES this chain — created outside it, which is exactly the
//     situation the "absent"-kind declarations in this tree are addressed to.
//
// It errs in the safe direction: every drop becomes pending, so every table gets
// counted. On a fresh install they are all absent and nothing is refused; on a
// pre-chain database whatever is actually there gets counted, which is the point.
//
// THIS IS NOT THE SAME AS FAILING TO REACH THE DATABASE, and the two must never be
// folded together — they look alike (no answer about any version) and mean opposite
// things. The difference is structural rather than a judgement call: a pre-chain
// database ANSWERS the catalogue query with NULL, while an unreachable one makes
// that query fail, and a failed query is returned as an error, never as "nothing
// applied". The same distinction [Observe] draws between an absent table and an
// unreachable server. It is asserted as a pair, not assumed — see the integration
// proof that puts both to the same function and requires different outcomes.
func GooseApplied(ctx context.Context, db Querier) AppliedSet {
	// Resolved once: the answer cannot change under us mid-run, and a per-version
	// catalogue lookup would triple the round trips for nothing.
	var (
		resolved bool
		present  bool
	)
	return func(version int64) (bool, error) {
		if !resolved {
			// Unqualified on purpose: goose puts its table in whatever schema the
			// DSN's search_path selects (kacho_vpc, kaname, …), and to_regclass
			// resolves through search_path exactly as the migrations themselves do.
			if err := db.QueryRowContext(ctx,
				`SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&present); err != nil {
				return false, fmt.Errorf("reading goose bookkeeping: %w", err)
			}
			resolved = true
		}
		if !present {
			return false, nil
		}
		var applied bool
		if err := db.QueryRowContext(ctx,
			`SELECT COALESCE((SELECT is_applied FROM goose_db_version
			                   WHERE version_id = $1 ORDER BY id DESC LIMIT 1), false)`,
			version).Scan(&applied); err != nil {
			return false, fmt.Errorf("reading applied state of version %d: %w", version, err)
		}
		return applied, nil
	}
}

// ApprovalEnv is where an operator writes the drops they have decided to let
// through. It is a constant so that the reader and the refusal message that tells an
// operator what to set cannot drift into naming two different variables.
const ApprovalEnv = "KACHO_MIGRATOR_DROP_APPROVED"

// Approval is one drop an operator has decided to let through even though the table
// is not empty.
//
// It names a version AND a table, and nothing wider exists: there is deliberately no
// "skip the guard" switch. A blanket override would be worth exactly as much as the
// prose paragraphs this package replaced, and would be reached for under precisely
// the same pressure.
type Approval struct {
	Version int64
	Table   string
}

func (a Approval) key() string { return declKey(a.Version, a.Table) }

// String renders an approval the way it is written into the environment, so a
// refusal can quote back the exact text that would release it.
func (a Approval) String() string { return fmt.Sprintf("%d/%s", a.Version, a.Table) }

// ParseApprovals reads the operator's list: entries "<version>/<table>", separated
// by commas or whitespace.
//
// A malformed entry is an error and not a skipped one. Reading a typo as "approve
// nothing" would turn a mistake into a refusal to deploy whose stated reason is
// somebody else's table — and the operator would go looking at the table.
func ParseApprovals(s string) ([]Approval, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]Approval, 0, len(fields))
	for _, f := range fields {
		parts := strings.Split(f, "/")
		if len(parts) != 2 {
			return nil, fmt.Errorf("dropguard: approval %q is not <version>/<table>", f)
		}
		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("dropguard: approval %q: %q is not a migration version", f, parts[0])
		}
		table := normaliseTable(parts[1])
		if table == "" {
			return nil, fmt.Errorf("dropguard: approval %q names no table", f)
		}
		out = append(out, Approval{Version: version, Table: table})
	}
	return out, nil
}

// Target says how far the run that is about to happen will apply the chain.
//
// A drop in a migration this run will not reach cannot destroy anything in this
// run, so counting it can only produce a refusal for somebody else's drop. The
// operator then clears it the one way there is — by naming that drop — and pays a
// step for a table this deploy never touches.
//
// THE ZERO VALUE COUNTS EVERYTHING, and that is the whole design of this type.
// A caller who forgets to say how far it goes gets the widest check, never the
// narrowest: the mistake this type can still cause is an extra refusal, which an
// approval clears, and never a silent pass, which nothing clears. There is
// deliberately no "count nothing" — the same reason [Approval] names a version and
// a table and no blanket override exists.
//
// It is not a way around the count either, and the reason is structural rather
// than a promise: the target is the SAME number handed to goose. Narrowing it to
// duck a refusal narrows what gets applied by exactly as much, so the drop that
// was refused does not run.
type Target struct {
	upTo    int64
	limited bool
}

// WholeChain is every pending drop: the run stops at the head.
//
// It is spelled out at call sites rather than left implicit so that "this run has
// no target" is a statement somebody made, not a field nobody filled in.
func WholeChain() Target { return Target{} }

// UpTo stops at version, inclusive — the same boundary goose.UpTo applies.
func UpTo(version int64) Target { return Target{upTo: version, limited: true} }

// Reaches reports whether a run with this target will apply version.
func (t Target) Reaches(version int64) bool { return !t.limited || version <= t.upTo }

// String names the target for the census, so a reader can tell WHICH question the
// numbers below it answered.
func (t Target) String() string {
	if !t.limited {
		return "whole chain"
	}
	return fmt.Sprintf("up to %04d", t.upTo)
}

// PreflightReport is what a live check has to say for itself.
//
// Counted sits next to Pending for the same reason Measured sits next to
// DropsInChain in [Report]: "no refusals" from a run that asked nothing is the
// failure this package exists to prevent, and it must not read as success.
type PreflightReport struct {
	Service string
	// Target is how far this run applies. It is on the report because the numbers
	// below are answers to a question it asked, and a census that hid which
	// question it asked would be the same shape of silence this type prevents.
	Target Target
	// DropsInChain is every Up-section drop the migrations contain.
	DropsInChain int
	// Pending is how many of them have not run on THIS database yet AND lie within
	// reach of this run — the only ones that can still destroy anything here.
	Pending int
	// Deferred lists drops the target puts OUT OF REACH of this run.
	//
	// Whether they have already run is deliberately NOT asked — see the loop — so
	// this is not a list of pending drops and must not be read as one. It is on
	// the record all the same: silence would make "the target narrowed the check"
	// indistinguishable from "there was nothing else to check", and the run that
	// widens the target is the one that answers for them.
	Deferred []string
	// Counted is how many pending drops were actually put to the database. An
	// observed absence counts: it is an answer, not a failure to get one.
	Counted int
	// Rows is the live row count per pending drop, keyed "NNNN/table".
	Rows map[string]int64
	// AbsentAt lists pending drops whose table is not on this database at all.
	AbsentAt []string
	// Approved lists non-empty drops an operator released by name.
	Approved []string
	// StaleApprovals lists approvals that matched no pending drop. They release
	// nothing, so they are reported rather than obeyed.
	StaleApprovals []string
	// DynamicDrops is how many DROP TABLE statements the inventory could not read
	// because their subject is computed at run time. They are not Pending and not
	// Deferred — this run cannot tell whether they destroy anything, or what — and
	// the census prints the number so that inability is visible rather than absent.
	// See [DynamicDrop].
	DynamicDrops []string
	Violations   []Violation
}

// OK reports that every pending drop was counted and none was refused.
func (r PreflightReport) OK() bool { return r.Counted == r.Pending && len(r.Violations) == 0 }

// Summary is a one-line census: how much was looked at, not only what was found.
func (r PreflightReport) Summary() string {
	verdict := "OK"
	switch {
	case len(r.Violations) > 0:
		verdict = "REFUSED"
	case r.Pending > 0 && r.Counted == 0:
		verdict = "NOT VERIFIED"
	case r.Counted < r.Pending:
		verdict = "PARTIAL"
	case r.Pending == 0:
		verdict = "NOTHING PENDING"
	}
	return fmt.Sprintf(
		"drop-preflight %s: %s — %d drop(s) in chain, %s, %d pending, %d deferred beyond the target, counted %d of %d, %d observed absent, %d approved by operator, %d refusal(s), %d drop(s) not readable (subject computed at run time)",
		r.Service, verdict, r.DropsInChain, r.Target, r.Pending, len(r.Deferred), r.Counted, r.Pending,
		len(r.AbsentAt), len(r.Approved), len(r.Violations), len(r.DynamicDrops))
}

// WriteCensus prints what was read and what was decided, unconditionally.
func (r PreflightReport) WriteCensus(w io.Writer) {
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
	for _, k := range r.Deferred {
		_, _ = fmt.Fprintf(w, "  not reached by this run %s (%s) — it is not executed here, so it destroys nothing here\n", k, r.Target)
	}
	for _, k := range r.Approved {
		_, _ = fmt.Fprintf(w, "  APPROVED BY OPERATOR %s — rows will be destroyed\n", k)
	}
	for _, k := range r.DynamicDrops {
		_, _ = fmt.Fprintf(w, "  NOT READABLE %s — the table name is built at run time; this gate cannot say what it destroys\n", k)
	}
	for _, k := range r.StaleApprovals {
		_, _ = fmt.Fprintf(w, "  approval %s matches no pending drop and released nothing; remove it\n", k)
	}
	for _, v := range r.Violations {
		_, _ = fmt.Fprintf(w, "  %s\n", v.Error())
	}
}

// Preflight counts, on the database in front of it, every table that a migration
// which has NOT YET RUN — and which target puts within reach of this run — is going
// to drop.
//
// The order is the whole point: this happens before the chain is applied, while the
// rows still exist and while refusing still costs nothing but a deploy.
//
// What each outcome means, and why none of them is folded into another:
//
//	rows > 0        the drop destroys tenant data. REFUSED unless an operator named
//	                this exact drop in advance.
//	rows == 0       the table is there and empty. Nothing to lose; proceed.
//	table absent    nothing there to destroy. That is an ANSWER read from the
//	                database, not a failure to get one, and it is the ordinary case
//	                on a fresh install where the chain has not built the table yet.
//	                (The replayed guard treats absence as unverified, because there
//	                the table SHOULD exist at version-1. Here it need not.)
//	no bookkeeping  a database with no goose table has applied nothing. LEGITIMATE,
//	                not a fault: it is a fresh install, or a database that PREDATES
//	                this chain — the state the "absent"-kind declarations are aimed
//	                at. Every drop is therefore pending and every table gets counted,
//	                which errs toward more checking, not less. Do not read this as a
//	                failure and do not relax it; see [GooseApplied] for why it cannot
//	                be confused with an unreachable database.
//	cannot count    NOT VERIFIED. An unreachable database is not an empty one, and a
//	                guard that answers "zero" when it never looked reports the safe
//	                answer under exactly the conditions in which it knows nothing.
//	cannot tell     NOT VERIFIED. If the applied set is unreadable we do not know
//	                which drops are still ahead, and assuming "already applied"
//	                would skip every check.
//	beyond target   DEFERRED, and reported as such. A run that stops at 0007 does
//	                not execute the drop in 0011, so counting that table can only
//	                refuse this deploy over rows this deploy leaves alone. Whether
//	                it has already run is not asked and the census does not claim
//	                either way; the run that widens the target answers for it. See
//	                [Target] for why the zero value counts everything and why
//	                narrowing cannot be used as a bypass.
//
// The window it does not close, stated rather than implied: rows can arrive between
// this count and the drop, because the old pods are still serving while the migrator
// runs. This makes the answer seconds old instead of never taken; it does not make
// it atomic, and nothing available here would.
func Preflight(ctx context.Context, count Counter, inv Inv, applied AppliedSet, approvals []Approval, target Target) PreflightReport {
	rep := PreflightReport{Service: inv.Service, Target: target, DropsInChain: len(inv.Drops), DynamicDrops: inv.UnreadableDrops(), Rows: map[string]int64{}}

	byKey := map[string]Approval{}
	for _, a := range approvals {
		byKey[a.key()] = a
	}
	used := map[string]bool{}

	for _, drop := range inv.Drops {
		key := fmt.Sprintf("%04d/%s", drop.Version, drop.Table)

		// The target is asked FIRST, before anything is asked of the database.
		//
		// It is our own parameter — the same number this run hands to goose — so
		// the answer needs no lookup, and a drop out of reach must not be able to
		// produce a violation of ANY kind, including "could not tell whether it
		// already ran". Refusing a deploy because the bookkeeping was unreadable
		// for a migration this run will not execute is the same defect in a
		// different coat.
		//
		// The cost of that order, stated rather than hidden: a deferred drop may
		// well have run already, and nothing here knows which. That is why the
		// census says only that this run does not reach it — see [PreflightReport]
		// Deferred — and why the report never calls these pending.
		if !target.Reaches(drop.Version) {
			rep.Deferred = append(rep.Deferred, key)
			continue
		}

		done, err := applied(drop.Version)
		if err != nil {
			rep.Violations = append(rep.Violations, Violation{
				Kind: ViolationUnverified, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("could not tell whether migration %04d has already run, so it is unknown whether this drop is still ahead: %v. Not knowing is not the same as nothing to do",
					drop.Version, err),
			})
			continue
		}
		if done {
			// The drop already happened. Whatever is in that name now is not what
			// the drop would have destroyed, and counting it would measure the
			// wrong thing rather than nothing.
			continue
		}
		rep.Pending++

		rows, obsErr := count(ctx, drop.Table)
		switch {
		case errors.Is(obsErr, ErrTableAbsent):
			rep.Counted++
			rep.AbsentAt = append(rep.AbsentAt, key)
		case obsErr != nil:
			rep.Violations = append(rep.Violations, Violation{
				Kind: ViolationUnverified, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("nothing was counted before %s:%d destroys this table: %v. An unreachable database is not an empty one, and the drop stays refused until the question is answered",
					drop.File, drop.Line, obsErr),
			})
		default:
			rep.Counted++
			rep.Rows[key] = rows
			if rows == 0 {
				continue
			}
			appr, ok := byKey[declKey(drop.Version, drop.Table)]
			if ok {
				used[appr.key()] = true
				rep.Approved = append(rep.Approved, fmt.Sprintf("%s (%d row(s))", key, rows))
				continue
			}
			rep.Violations = append(rep.Violations, Violation{
				Kind: ViolationRowCount, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				// SAVING IS NAMED BEFORE DESTROYING, and both are named as commands.
				// The message used to offer one executable step — approve the drop —
				// and leave the other as "establish where they come from", which is
				// research, not a step. An operator reading a stopped deploy takes the
				// step that can be pasted, so the only pasteable step must not be the
				// one that loses the rows.
				Detail: fmt.Sprintf("holds %d row(s) on this database, and %s:%d destroys the table. Those rows are gone the moment the migration runs, and the down migration brings back the shape, not the data. Save them first, with DSN set to this database's connection string: %s — then establish where they come from; to destroy them deliberately, name this drop: %s=%s",
					rows, drop.File, drop.Line, PreserveCommand(drop.Table), ApprovalEnv, Approval{Version: drop.Version, Table: drop.Table}),
			})
		}
	}

	for _, a := range approvals {
		if !used[a.key()] {
			rep.StaleApprovals = append(rep.StaleApprovals, a.String())
		}
	}
	sort.Strings(rep.StaleApprovals)
	return rep
}

// Gate is the whole live check as one call, for a migration runner to make before
// it applies anything.
//
// It is deliberately the only shape a runner needs, and it takes no options that
// could turn it off. Seven binaries wire this line; a check each of them assembled
// for itself would drift, and the one that drifted would be the one nobody looked
// at.
//
// target is how far the caller is about to apply, and it is REQUIRED rather than
// optional for that reason: a runner that stops at a version has to say so in the
// same call, next to the goose call it mirrors, where the two can be read
// together. It is not an off switch — [Target] says why the zero value counts
// everything and why narrowing it narrows what runs by exactly as much.
//
// The census goes to out on every run, refused or not — what was read is as much of
// the result as what was found. The returned error is what stops the deploy.
func Gate(ctx context.Context, db Querier, service string, fsys fs.FS, out io.Writer, target Target) error {
	inv, err := Inventory(service, fsys)
	if err != nil {
		// Unreadable migrations mean the set of pending drops is unknown. That is
		// not "no drops"; the chain is about to be applied from these same files.
		return fmt.Errorf("drop-preflight %s: could not read the migrations, so it is unknown what they drop: %w", service, err)
	}

	approvals, err := ParseApprovals(os.Getenv(ApprovalEnv))
	if err != nil {
		return fmt.Errorf("drop-preflight %s: %s is set but unreadable: %w", service, ApprovalEnv, err)
	}

	rep := Preflight(ctx, Counting(db), inv, GooseApplied(ctx, db), approvals, target)
	rep.WriteCensus(out)

	if !rep.OK() {
		return fmt.Errorf("drop-preflight %s: refusing to migrate — %d pending drop(s) counted of %d, %d refusal(s); see the census above",
			service, rep.Counted, rep.Pending, len(rep.Violations))
	}
	return nil
}
