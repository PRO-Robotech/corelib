// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// ManifestName is the file each service keeps beside its migrations, declaring what
// every drop in them expects to destroy.
const ManifestName = "dropguard.json"

// Kind is what a DROP TABLE statement is doing.
type Kind string

const (
	// KindRetire — a table that exists is being taken away. Its contents are
	// destroyed, so they are counted.
	KindRetire Kind = "retire"
	// KindRecreate — `DROP TABLE IF EXISTS x` at the head of a migration that
	// immediately CREATEs x again, so a re-run lands on the same shape. On a chain
	// that has never run there is nothing there to destroy, and the gate confirms
	// that by finding the table ABSENT rather than by believing the label.
	KindRecreate Kind = "recreate"
	// KindAbsent — the drop has no subject in this chain: no migration here ever
	// creates the table, so on any database built from these migrations the
	// statement is a no-op. Such a drop is addressed at databases that predate the
	// chain, and saying so is a claim about state this repository does not
	// contain — which is why it is the one kind that always requires a note.
	//
	// Both halves are checked, not believed: the parser confirms no CREATE exists
	// anywhere in the chain, and the measurement confirms the table is absent at
	// the version before the drop. Add a CREATE for it later and the declaration
	// stops being true, so the gate turns it back into a retire that owes a count.
	KindAbsent Kind = "absent"
)

// Declaration is one entry in a service's dropguard.json.
//
// It is not a comment. Every field is checked: Kind against what the migration
// actually does, ExpectRows against the database, and Note against the requirement
// that destroying rows be a decision somebody made rather than a number that
// drifted.
type Declaration struct {
	Version int64  `json:"version"`
	Table   string `json:"table"`
	Kind    Kind   `json:"kind"`
	// ExpectRows is the exact number of rows the drop destroys when the chain is
	// replayed into an empty database. Zero unless a migration seeds the table.
	ExpectRows int64 `json:"expect_rows"`
	// Note says why destroying ExpectRows rows is acceptable. Required whenever
	// ExpectRows is not zero: a number with no reason behind it is the reasoning
	// this package replaces, only shorter.
	Note string `json:"note,omitempty"`
}

// Manifest is a service's dropguard.json.
type Manifest struct {
	Service string        `json:"service"`
	Drops   []Declaration `json:"drops"`
}

// LoadManifest reads a dropguard.json.
func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- in-repo manifest path
	if err != nil {
		return Manifest{}, fmt.Errorf("dropguard: read %s: %w", path, err)
	}
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&m); derr != nil {
		return Manifest{}, fmt.Errorf("dropguard: decode %s: %w", path, derr)
	}
	return m, nil
}

// ViolationKind names what went wrong, so a caller can tell a measurement that
// failed from a measurement that came back bad.
type ViolationKind string

const (
	// ViolationUndeclared — a drop with no entry. The number was never stated.
	ViolationUndeclared ViolationKind = "undeclared-drop"
	// ViolationExpired — an entry with no drop. It has nothing left to describe,
	// and an exemption that outlives its subject is the next reader's blind spot.
	ViolationExpired ViolationKind = "expired-declaration"
	// ViolationDuplicate — two entries for one drop.
	ViolationDuplicate ViolationKind = "duplicate-declaration"
	// ViolationKindMismatch — the entry's Kind is not what the migration does.
	ViolationKindMismatch ViolationKind = "kind-mismatch"
	// ViolationUngrounded — rows are expected in a table no migration writes to.
	ViolationUngrounded ViolationKind = "ungrounded-expectation"
	// ViolationUnjustified — rows are expected with no reason given.
	ViolationUnjustified ViolationKind = "unjustified-expectation"
	// ViolationRowCount — the table did not hold what the entry said it held.
	ViolationRowCount ViolationKind = "row-count-mismatch"
	// ViolationUnverified — nothing was measured. Never "clean".
	ViolationUnverified ViolationKind = "not-verified"
)

// Violation is one reason a drop is refused.
type Violation struct {
	Kind    ViolationKind
	Service string
	Version int64
	Table   string
	Detail  string
}

func (v Violation) Error() string {
	return fmt.Sprintf("[%s] %s migration %04d, table %q: %s", v.Kind, v.Service, v.Version, v.Table, v.Detail)
}

// Reconcile checks the manifest against the migrations without touching a database:
// that every drop is declared, that every declaration still has a drop behind it,
// and that each entry's Kind and ExpectRows are consistent with what the migrations
// do. It is the half that can run anywhere, and it is what stops a new drop from
// being merged with its number left unstated.
func Reconcile(inv Inv, m Manifest) []Violation {
	var out []Violation

	byKey := map[string][]Declaration{}
	for _, d := range m.Drops {
		byKey[declKey(d.Version, d.Table)] = append(byKey[declKey(d.Version, d.Table)], d)
	}
	seen := map[string]bool{}

	for _, drop := range inv.Drops {
		key := declKey(drop.Version, drop.Table)
		decls := byKey[key]
		seen[key] = true
		switch {
		case len(decls) == 0:
			out = append(out, Violation{
				Kind: ViolationUndeclared, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("%s:%d drops this table and %s declares nothing about it; state the exact number of rows it destroys (expect_rows) so the claim can be measured instead of argued",
					drop.File, drop.Line, ManifestName),
			})
			continue
		case len(decls) > 1:
			out = append(out, Violation{
				Kind: ViolationDuplicate, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("%d entries describe one drop; the gate cannot tell which number it is checking", len(decls)),
			})
			continue
		}
		out = append(out, checkDeclaration(inv, drop, decls[0])...)
	}

	for _, d := range m.Drops {
		if !seen[declKey(d.Version, d.Table)] {
			out = append(out, Violation{
				Kind: ViolationExpired, Service: inv.Service, Version: d.Version, Table: d.Table,
				Detail: fmt.Sprintf("no Up section of migration %04d drops this table; the entry has nothing left to exempt and would silently cover the next drop that lands on this version", d.Version),
			})
		}
	}

	sort.Slice(out, func(a, b int) bool {
		if out[a].Version != out[b].Version {
			return out[a].Version < out[b].Version
		}
		return out[a].Table < out[b].Table
	})
	return out
}

func checkDeclaration(inv Inv, drop Drop, decl Declaration) []Violation {
	var out []Violation

	switch decl.Kind {
	case KindRecreate:
		if !drop.RecreatedHere {
			out = append(out, Violation{
				Kind: ViolationKindMismatch, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("declared %q, but %s does not CREATE this table again in the same Up section — the drop destroys whatever was there",
					KindRecreate, drop.File),
			})
		}
		if decl.ExpectRows != 0 {
			out = append(out, Violation{
				Kind: ViolationUngrounded, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("declared %q with expect_rows=%d; a preamble drop runs before its own CREATE, so on a chain that has never run there is nothing there to count",
					KindRecreate, decl.ExpectRows),
			})
		}
	case KindRetire:
		if drop.RecreatedHere {
			out = append(out, Violation{
				Kind: ViolationKindMismatch, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("declared %q, but %s CREATEs this table again in the same Up section — it is an idempotency preamble, not a retire",
					KindRetire, drop.File),
			})
		}
		if decl.ExpectRows < 0 {
			out = append(out, Violation{
				Kind: ViolationUngrounded, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("expect_rows=%d is not a row count", decl.ExpectRows),
			})
		}
		if decl.ExpectRows > 0 {
			if strings.TrimSpace(decl.Note) == "" {
				out = append(out, Violation{
					Kind: ViolationUnjustified, Service: inv.Service, Version: drop.Version, Table: drop.Table,
					Detail: fmt.Sprintf("expects to destroy %d rows and gives no reason; destroying data is a decision, and a decision has an author", decl.ExpectRows),
				})
			}
			if !inv.SeedsTable(drop.Table, drop.Version) {
				out = append(out, Violation{
					Kind: ViolationUngrounded, Service: inv.Service, Version: drop.Version, Table: drop.Table,
					Detail: fmt.Sprintf("expects %d rows, but no migration before %04d INSERTs into this table; rows that our own chain never seeded are rows somebody else wrote",
						decl.ExpectRows, drop.Version),
				})
			}
		}
	case KindAbsent:
		if inv.CreatesTable(drop.Table) {
			out = append(out, Violation{
				Kind: ViolationKindMismatch, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("declared %q, but migration(s) %v in this chain CREATE this table — the drop has a subject after all, and owes a row count",
					KindAbsent, inv.CreateVersions(drop.Table)),
			})
		}
		if decl.ExpectRows != 0 {
			out = append(out, Violation{
				Kind: ViolationUngrounded, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("declared %q with expect_rows=%d; a table this chain never creates holds nothing on any database it built", KindAbsent, decl.ExpectRows),
			})
		}
		if strings.TrimSpace(decl.Note) == "" {
			out = append(out, Violation{
				Kind: ViolationUnjustified, Service: inv.Service, Version: drop.Version, Table: drop.Table,
				Detail: fmt.Sprintf("declared %q and gives no reason; a drop aimed at databases this chain did not build is a claim about state nobody here can see, and it needs an author", KindAbsent),
			})
		}
	default:
		out = append(out, Violation{
			Kind: ViolationKindMismatch, Service: inv.Service, Version: drop.Version, Table: drop.Table,
			Detail: fmt.Sprintf("kind %q is not one of %q / %q / %q", decl.Kind, KindRetire, KindRecreate, KindAbsent),
		})
	}
	return out
}

// Judge turns one measurement into a verdict.
//
// obsErr is whatever [Observe] returned. It is a parameter rather than something
// the caller handles beforehand precisely so that a failed measurement cannot be
// dropped on the floor: there is no way to call Judge without saying what happened.
func Judge(drop Drop, decl Declaration, rows int64, obsErr error) []Violation {
	switch {
	case errors.Is(obsErr, ErrTableAbsent):
		if decl.Kind == KindRecreate || decl.Kind == KindAbsent {
			// Expected, and this absence IS the measurement rather than a failure
			// to make one: a preamble drop runs before the CREATE that first makes
			// the table, and an absent-kind drop names a table this chain never
			// creates. Either way the answer to "how much does this destroy" is
			// "there is nothing here", and it was read from the database.
			return nil
		}
		return []Violation{{
			Kind: ViolationUnverified, Service: drop.Service, Version: drop.Version, Table: drop.Table,
			Detail: fmt.Sprintf("nothing was counted: the table was not there to count at version %04d. This is not an empty table — it is an unanswered question, and the drop stays refused until it is answered (%v)",
				drop.Version-1, obsErr),
		}}
	case obsErr != nil:
		return []Violation{{
			Kind: ViolationUnverified, Service: drop.Service, Version: drop.Version, Table: drop.Table,
			Detail: fmt.Sprintf("nothing was counted: %v. An unreachable database is not a clean one", obsErr),
		}}
	}

	if decl.Kind == KindRecreate || decl.Kind == KindAbsent {
		return []Violation{{
			Kind: ViolationKindMismatch, Service: drop.Service, Version: drop.Version, Table: drop.Table,
			Detail: fmt.Sprintf("declared %q, but the table is there at version %04d holding %d row(s) — the drop destroys them, so it is a retire and owes a count",
				decl.Kind, drop.Version-1, rows),
		}}
	}
	if rows != decl.ExpectRows {
		return []Violation{{
			Kind: ViolationRowCount, Service: drop.Service, Version: drop.Version, Table: drop.Table,
			Detail: fmt.Sprintf("holds %d row(s) at version %04d, declared %d. The drop is refused: %s",
				rows, drop.Version-1, decl.ExpectRows, rowCountAdvice(rows, decl.ExpectRows)),
		}}
	}
	return nil
}

func rowCountAdvice(got, want int64) string {
	if want == 0 {
		return "rows nobody accounted for are rows somebody may still need"
	}
	if got > want {
		return "the table grew past what the declaration accounted for; re-establish where the extra rows come from before destroying them"
	}
	return "the declaration accounts for rows that are no longer there; a number that no longer matches has stopped describing anything"
}

func declKey(version int64, table string) string {
	return fmt.Sprintf("%d\x00%s", version, strings.ToLower(table))
}

// AdjudicateDeclaredDropCount compares the number of drops a chain HOLDS with the
// number its caller DECLARES, and returns the finding, or "" when they agree.
//
// # Why this is a function and not four lines inside the harness
//
// While the comparison lived inside the harness it could only be exercised by
// making a real service's chain wrong, which is to say it could not be exercised at
// all: a check that cannot be made to fail on purpose is indistinguishable from one
// that cannot fail. Splitting the JUDGEMENT from the GATHERING is what lets the
// injection hand it numbers directly.
//
// # Why the caller declares a number at all
//
// Because the number cannot move on its own. Reconcile already refuses a drop whose
// row count is unstated, so a drop can never land alone — but a drop and its
// declaration land together in one commit and internally agree, and nothing outside
// the migrations directory moves. This number is the thing outside.
//
// ZERO IS A NUMBER LIKE ANY OTHER, and that is the correction this function carries.
// The harness used to refuse a chain with no drops outright, reasoning that such a
// run would assert nothing. That was true of the population it was written for —
// every caller had drops — and it stopped being true the moment a service squashed
// its chain into one primary migration: a squashed chain is a STATE, and a state has
// no history of drops. The refusal then fired on the correct answer. What remains
// asserted at zero is the ratchet itself: add a drop and the count moves off zero.
//
// filesScanned is named in the finding rather than checked here: "the chain holds no
// drops" and "nothing was read" are different states, Inventory already refuses the
// second, and a reader looking at a mismatch needs to know how much was read to act
// on it.
func AdjudicateDeclaredDropCount(service string, found, filesScanned, declared int) string {
	if found == declared {
		return ""
	}
	return fmt.Sprintf("%s: the chain holds %d Up-section DROP TABLE statement(s) across %d migration file(s), and the caller declares %d.\n"+
		"    This number cannot move by itself. A drop that appeared without it moving is a drop nobody looked at; "+
		"a drop that disappeared without it moving is a declaration about a chain that no longer exists.",
		service, found, filesScanned, declared)
}

// ManifestAbsenceIsLegitimate says whether an unreadable manifest is acceptable.
//
// It is acceptable in exactly one case: the file is not there AND the chain drops
// nothing, so there is nothing to declare. This is the same rule the repo-wide
// static gate already applies to a service that has never dropped a table, and
// stating it in one place is the point — two mechanisms disagreeing about when a
// manifest is owed would disagree silently.
//
// Every other unreadable manifest stays fatal, including an unparseable one for a
// chain with no drops: a guard that could not read what it checks against has not
// checked, and "the file is malformed" must never resolve to "nothing was owed".
func ManifestAbsenceIsLegitimate(dropsInChain int, err error) bool {
	return err != nil && dropsInChain == 0 && errors.Is(err, fs.ErrNotExist)
}
