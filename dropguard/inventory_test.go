// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard_test

import (
	"testing"
	"testing/fstest"

	"github.com/PRO-Robotech/corelib/dropguard"
)

// TestInventoryReadsTheExecutablePart — the inventory must see statements, not
// words. A migration that merely TALKS about dropping a table has dropped nothing,
// and a gate that cannot tell the difference will demand declarations for prose and
// miss the one drop hidden behind a `/* */`.
func TestInventoryReadsTheExecutablePart(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_initial.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (id TEXT PRIMARY KEY);
INSERT INTO widgets (id) VALUES ('seed-1'), ('seed-2');
-- +goose Down
DROP TABLE widgets;
`)},
		"0002_prose_only.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
-- This migration explains why we will one day DROP TABLE widgets, but does not.
/* Nor does this block comment, which also says DROP TABLE widgets. */
ALTER TABLE widgets ADD COLUMN note TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE widgets DROP COLUMN note;
`)},
		"0003_drop_widgets.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
DROP TABLE IF EXISTS widgets;
-- +goose Down
CREATE TABLE widgets (id TEXT PRIMARY KEY);
`)},
	}

	inv, err := dropguard.Inventory("demo", fsys)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if inv.FilesScanned != 3 {
		t.Fatalf("files scanned: got %d, want 3 — a census that cannot be wrong cannot be trusted", inv.FilesScanned)
	}
	if len(inv.Drops) != 1 {
		t.Fatalf("drops: got %d %+v, want exactly 1 (0003); the Down-section drop in 0001 and the two commented mentions in 0002 destroy nothing", len(inv.Drops), inv.Drops)
	}
	d := inv.Drops[0]
	if d.Table != "widgets" || d.Version != 3 {
		t.Errorf("drop: got %s at version %d, want widgets at 3", d.Table, d.Version)
	}
	if d.RecreatedHere {
		t.Error("0003 re-creates widgets only in its Down section — that is a rollback, not an idempotency preamble")
	}
	if !inv.SeedsTable("widgets", 3) {
		t.Error("0001 INSERTs into widgets before version 3 — a declaration expecting rows there is grounded")
	}
	if inv.SeedsTable("widgets", 1) {
		t.Error("nothing INSERTs into widgets before version 1")
	}
}

// TestInventorySeesTheIdempotencyPreamble — `DROP TABLE IF EXISTS x` immediately
// followed by `CREATE TABLE x` in the same Up section destroys nothing on a chain
// that has never run: there is no x to destroy. That is a different act from a
// retire, and the difference is READ from the migration rather than asserted by
// whoever writes the declaration.
func TestInventorySeesTheIdempotencyPreamble(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_backup_and_swap.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
DROP TABLE IF EXISTS backup_roles;
CREATE TABLE backup_roles (LIKE roles INCLUDING DEFAULTS);
INSERT INTO backup_roles SELECT * FROM roles;
-- +goose Down
DROP TABLE backup_roles;
`)},
	}
	inv, err := dropguard.Inventory("demo", fsys)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(inv.Drops) != 1 {
		t.Fatalf("drops: got %d, want 1", len(inv.Drops))
	}
	if !inv.Drops[0].RecreatedHere {
		t.Error("backup_roles is re-created in the same Up section — the drop is a preamble, not a retire")
	}
}

// TestInventoryRejectsAMigrationWithoutAnUpSection — a file goose would not
// recognise must not be silently counted as "scanned, nothing found". That is the
// difference between zero findings and zero reading.
func TestInventoryRejectsAMigrationWithoutAnUpSection(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_no_markers.sql": &fstest.MapFile{Data: []byte("DROP TABLE widgets;\n")},
	}
	if _, err := dropguard.Inventory("demo", fsys); err == nil {
		t.Fatal("a migration with no `+goose Up` marker must be an error, not an empty scan")
	}
}

// TestInventorySeesEveryTableInAMultiTableDrop — `DROP TABLE a, b;` destroys BOTH.
//
// The list form is ordinary SQL and it is exactly what one reaches for when foreign
// keys make the order of separate statements awkward — one of this tree's own
// migrations says so in a comment. The reader captured a single identifier after
// `DROP TABLE`, so on that form the second table and everything after it existed
// for no part of the gate: not for the declarations it demands, not for the
// measurement it runs, not for the violations it reports. The table would be
// destroyed with nothing said about it, and the run would be green — the drop guard
// itself an instance of the class it was built for.
//
// Both directions are asserted here: every name in the list is found (with its own
// line and its own idempotency verdict), and a single-table drop keeps behaving
// exactly as before.
func TestInventorySeesEveryTableInAMultiTableDrop(t *testing.T) {
	fsys := fstest.MapFS{
		"0001_initial.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE kacho_demo.parents (id TEXT PRIMARY KEY);
CREATE TABLE kacho_demo.children (id TEXT PRIMARY KEY);
CREATE TABLE kacho_demo.pivot (id TEXT PRIMARY KEY);
INSERT INTO kacho_demo.children (id) VALUES ('seed');
-- +goose Down
DROP TABLE kacho_demo.parents;
`)},
		// The list form, spread over two lines, quoted, schema-qualified, with the
		// trailing action word — every shape a migration author may legitimately use.
		"0002_drop_list.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
DROP TABLE IF EXISTS kacho_demo.pivot,
                     kacho_demo.children,
                     "kacho_demo"."parents" CASCADE;
-- +goose Down
-- irreversible
`)},
	}

	inv, err := dropguard.Inventory("demo", fsys)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}

	got := map[string]int{}
	for _, d := range inv.Drops {
		got[d.Table] = d.Line
	}
	for _, want := range []string{"kacho_demo.pivot", "kacho_demo.children", "kacho_demo.parents"} {
		if _, ok := got[want]; !ok {
			t.Errorf("table %q is dropped by 0002 but the inventory does not see it — it would be "+
				"destroyed with no declaration demanded and no measurement run; found: %+v", want, inv.Drops)
		}
	}
	if len(inv.Drops) != 3 {
		t.Fatalf("drops: got %d, want 3 — one per table in the list; %+v", len(inv.Drops), inv.Drops)
	}

	// Each name carries ITS OWN line, so a message names the coordinate a reader can
	// open. Collapsing them onto the statement's first line would make two of three
	// findings point somewhere the table is not written.
	if got["kacho_demo.pivot"] == got["kacho_demo.children"] {
		t.Errorf("names on different lines report the same line (%d) — the coordinate is then only "+
			"approximately true, and the second table is not where the message says",
			got["kacho_demo.pivot"])
	}

	// The evidence each drop rests on is per table, and it differs here: children is
	// seeded before the drop, pivot is not. A reader that fused the list would have
	// answered for the first table on behalf of all of them.
	if !inv.SeedsTable("kacho_demo.children", 2) {
		t.Error("0001 INSERTs into children before 0002 — the drop destroys rows")
	}
	if inv.SeedsTable("kacho_demo.pivot", 2) {
		t.Error("nothing INSERTs into pivot — claiming otherwise would ground a declaration on air")
	}
	for _, tbl := range []string{"kacho_demo.pivot", "kacho_demo.children", "kacho_demo.parents"} {
		if !inv.CreatesTable(tbl) {
			t.Errorf("0001 CREATEs %s — a drop of it has a subject", tbl)
		}
	}

	// MIRROR: a single-table drop is unchanged by the list support, and a comma in a
	// neighbouring statement does not turn one drop into several.
	single := fstest.MapFS{
		"0001_one.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (id TEXT PRIMARY KEY, note TEXT NOT NULL DEFAULT '');
INSERT INTO widgets (id, note) VALUES ('a', 'x'), ('b', 'y');
DROP TABLE IF EXISTS widgets;
-- +goose Down
-- irreversible
`)},
	}
	sInv, err := dropguard.Inventory("demo", single)
	if err != nil {
		t.Fatalf("inventory(single): %v", err)
	}
	if len(sInv.Drops) != 1 || sInv.Drops[0].Table != "widgets" {
		t.Fatalf("single-table drop: got %+v, want exactly one drop of widgets — the column list and "+
			"the VALUES tuples both contain commas and must not be read as more tables", sInv.Drops)
	}
	if !sInv.Drops[0].RecreatedHere {
		t.Error("the same Up section CREATEs widgets — that is an idempotency preamble")
	}
}
