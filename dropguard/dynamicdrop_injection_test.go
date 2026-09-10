// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/PRO-Robotech/corelib/dropguard"
)

// oneMigration wraps a single Up-section statement in the smallest migration goose
// would run, so each case differs from its twin in ONE fact: the statement.
func oneMigration(stmt string) fstest.MapFS {
	return fstest.MapFS{"0001_case.sql": &fstest.MapFile{
		Data: []byte("-- +goose Up\nCREATE TABLE kaname.limits (id TEXT PRIMARY KEY);\n" + stmt + "\n"),
	}}
}

// TestEveryNamedDropFormIsClassifiedAsDocumented injects one drop per form the
// package doc names and asserts the classification the doc claims for it.
//
// The doc is a promise about what this reader sees. A promise nobody injects
// against is the same note-instead-of-a-property the package was written to
// refuse — so every line of that list appears here as a case, in BOTH directions:
// the forms said to be judged must yield a named table, and the forms said to be
// unreadable must yield no table and a count.
func TestEveryNamedDropFormIsClassifiedAsDocumented(t *testing.T) {
	cases := []struct {
		name string
		stmt string
		// wantTable is the table the reader must name, or "" when the form is one
		// the doc says it cannot read.
		wantTable string
		// wantUnreadable is how many drops must be counted as not readable.
		wantUnreadable int
	}{
		// --- judged: the subject is written down ---
		{"plain", `DROP TABLE kaname.limits;`, "kaname.limits", 0},
		{"if exists", `DROP TABLE IF EXISTS kaname.limits;`, "kaname.limits", 0},
		{
			// The legal twin of the format cases: EXECUTE does not hide a drop.
			// Were this counted as unreadable, the count would be inflated by
			// drops the gate can perfectly well judge, and a real one could hide
			// among them.
			"execute a literal", `EXECUTE 'DROP TABLE kaname.limits';`, "kaname.limits", 0,
		},
		{"execute a literal with if exists", `EXECUTE 'DROP TABLE IF EXISTS kaname.limits';`, "kaname.limits", 0},
		{
			// Concatenation AFTER a written name is still a written name. This is
			// the twin that keeps the `||` rule from swallowing judgeable drops.
			"concatenated suffix, name still written", `EXECUTE 'DROP TABLE kaname.limits' || ' CASCADE';`, "kaname.limits", 0,
		},

		// --- seen, not judged, counted ---
		{"format placeholder", `EXECUTE format('DROP TABLE %I', t);`, "", 1},
		{"format with written schema, computed leaf", `EXECUTE format('DROP TABLE kaname.%I', t);`, "", 1},
		{"concatenated name", `EXECUTE 'DROP TABLE ' || quote_ident(t);`, "", 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inv, err := dropguard.Inventory("demo", oneMigration(c.stmt))
			if err != nil {
				t.Fatalf("inventory: %v", err)
			}

			var got []string
			for _, d := range inv.Drops {
				got = append(got, d.Table)
			}
			switch {
			case c.wantTable == "" && len(got) != 0:
				t.Errorf("form %q: read table(s) %v, but its name is computed at run time and is not in the file — naming one asserts something unread", c.stmt, got)
			case c.wantTable != "" && (len(got) != 1 || got[0] != c.wantTable):
				t.Errorf("form %q: read %v, want exactly [%s] — the doc lists this form as judged, so a miss here means the doc promises a reading that does not happen", c.stmt, got, c.wantTable)
			}

			if n := len(inv.DynamicDrops); n != c.wantUnreadable {
				t.Errorf("form %q: counted %d unreadable, want %d", c.stmt, n, c.wantUnreadable)
			}
			for _, u := range inv.UnreadableDrops() {
				if !strings.Contains(u, "0001_case.sql") {
					t.Errorf("unreadable drop %q does not name its file; a census that cannot be followed to a coordinate is an assertion, not evidence", u)
				}
			}
		})
	}
}

// TestUnreadableDropIsNotAPhantomTable pins the one case that is worse than not
// seeing a drop: seeing a truncation and believing it is a name.
//
// `format('DROP TABLE kaname.%I', t)` leaves `kaname.` where the table would be.
// Before this was suppressed the inventory took that fragment for a table, and
// everything downstream followed: a declaration would have been demanded for it,
// and the measurement sent to count rows in a relation that does not exist.
func TestUnreadableDropIsNotAPhantomTable(t *testing.T) {
	inv, err := dropguard.Inventory("demo", oneMigration(`EXECUTE format('DROP TABLE kaname.%I', t);`))
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	for _, d := range inv.Drops {
		if strings.HasSuffix(d.Table, ".") || d.Table == "" {
			t.Fatalf("inventory produced %q as a table name — that is the text before a placeholder, not a relation", d.Table)
		}
	}
	if len(inv.Drops) != 0 {
		t.Fatalf("drops: got %+v, want none — the subject of this statement is not in the file", inv.Drops)
	}
	if len(inv.DynamicDrops) != 1 {
		t.Fatalf("unreadable: got %d, want 1 — suppressing the phantom must not also suppress the count, or the drop leaves no trace at all", len(inv.DynamicDrops))
	}
}

// TestUnreadableCountIsNotSilentlyZero is the census control: a chain with no
// dynamic drops must report zero, and a chain with them must report the number.
// Without the first half a broken counter that always returns zero would look
// exactly like a clean tree — which is the state the whole package refuses.
func TestUnreadableCountIsNotSilentlyZero(t *testing.T) {
	clean, err := dropguard.Inventory("demo", oneMigration(`DROP TABLE kaname.limits;`))
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if n := len(clean.UnreadableDrops()); n != 0 {
		t.Errorf("a chain whose drops are all written down reports %d unreadable, want 0", n)
	}

	dirty, err := dropguard.Inventory("demo", fstest.MapFS{"0001_case.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE kaname.limits (id TEXT PRIMARY KEY);
EXECUTE format('DROP TABLE %I', a);
EXECUTE format('DROP TABLE %I', b);
`)}})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if n := len(dirty.UnreadableDrops()); n != 2 {
		t.Fatalf("two computed drops reported as %d — the count must move with the tree, not sit at a constant", n)
	}
}

// TestProseAboutADynamicDropIsNotADrop is the legal twin for the whole family: a
// migration that TALKS about the dynamic form, as this repo's own comments do, must
// count nothing. A recogniser that reads text rather than code would fire on the
// paragraph explaining itself.
func TestProseAboutADynamicDropIsNotADrop(t *testing.T) {
	inv, err := dropguard.Inventory("demo", fstest.MapFS{"0001_case.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
-- One day this may run EXECUTE format('DROP TABLE %I', t), but it does not today.
/* Nor does this block, which also writes EXECUTE 'DROP TABLE ' || quote_ident(t). */
CREATE TABLE kaname.limits (id TEXT PRIMARY KEY);
`)}})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if n := len(inv.DynamicDrops); n != 0 {
		t.Fatalf("counted %d unreadable drops in a migration that only mentions them: %v", n, inv.UnreadableDrops())
	}
	if len(inv.Drops) != 0 {
		t.Fatalf("read %+v as drops from prose", inv.Drops)
	}
}
