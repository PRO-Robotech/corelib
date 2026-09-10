// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard_test

import (
	"testing"
	"testing/fstest"

	"github.com/PRO-Robotech/corelib/dropguard"
)

// chain is a small migration set with one of each shape the reconciler has to tell
// apart: a table that is seeded then retired, one retired empty, one dropped as an
// idempotency preamble, and one dropped though nothing here ever created it.
func chain(t *testing.T) dropguard.Inv {
	t.Helper()
	inv, err := dropguard.Inventory("demo", fstest.MapFS{
		"0001_initial.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE catalogue (id TEXT PRIMARY KEY);
CREATE TABLE ledger (id TEXT PRIMARY KEY);
INSERT INTO catalogue (id) VALUES ('a'), ('b');
-- +goose Down
DROP TABLE ledger;
DROP TABLE catalogue;
`)},
		"0002_preamble.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
DROP TABLE IF EXISTS scratch;
CREATE TABLE scratch (id TEXT PRIMARY KEY);
-- +goose Down
DROP TABLE scratch;
`)},
		"0003_retire.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
DROP TABLE catalogue;
DROP TABLE ledger;
DROP TABLE IF EXISTS never_here;
-- +goose Down
CREATE TABLE catalogue (id TEXT PRIMARY KEY);
CREATE TABLE ledger (id TEXT PRIMARY KEY);
`)},
	})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	return inv
}

func decl(v int64, table string, k dropguard.Kind, rows int64, note string) dropguard.Declaration {
	return dropguard.Declaration{Version: v, Table: table, Kind: k, ExpectRows: rows, Note: note}
}

func kinds(vs []dropguard.Violation) map[dropguard.ViolationKind]int {
	out := map[dropguard.ViolationKind]int{}
	for _, v := range vs {
		out[v.Kind]++
	}
	return out
}

// TestReconcileAcceptsTheHonestManifest — the silent direction. A gate proved only
// by making it fail catches shape, not substance, and the first false alarm gets it
// switched off.
func TestReconcileAcceptsTheHonestManifest(t *testing.T) {
	inv := chain(t)
	m := dropguard.Manifest{Service: "demo", Drops: []dropguard.Declaration{
		decl(2, "scratch", dropguard.KindRecreate, 0, ""),
		decl(3, "catalogue", dropguard.KindRetire, 2, "catalogue seeded by 0001; the owner re-seeds it"),
		decl(3, "ledger", dropguard.KindRetire, 0, ""),
		decl(3, "never_here", dropguard.KindAbsent, 0, "predates the chain; nothing here creates it"),
	}}
	if vs := dropguard.Reconcile(inv, m); len(vs) != 0 {
		t.Fatalf("an accurate manifest was refused: %+v", vs)
	}
}

// TestReconcileRefusesEachWayOfNotSayingTheNumber — one case per way the old habit
// comes back: not stating it, stating it for a drop that no longer exists, stating
// it twice, mislabelling what the migration does, and expecting rows in a table
// nothing ever writes to.
func TestReconcileRefusesEachWayOfNotSayingTheNumber(t *testing.T) {
	inv := chain(t)
	full := []dropguard.Declaration{
		decl(2, "scratch", dropguard.KindRecreate, 0, ""),
		decl(3, "catalogue", dropguard.KindRetire, 2, "seeded by 0001"),
		decl(3, "ledger", dropguard.KindRetire, 0, ""),
		decl(3, "never_here", dropguard.KindAbsent, 0, "predates the chain"),
	}
	without := func(table string) []dropguard.Declaration {
		var out []dropguard.Declaration
		for _, d := range full {
			if d.Table != table {
				out = append(out, d)
			}
		}
		return out
	}
	with := func(extra ...dropguard.Declaration) []dropguard.Declaration {
		return append(append([]dropguard.Declaration{}, full...), extra...)
	}
	replace := func(table string, d dropguard.Declaration) []dropguard.Declaration {
		out := without(table)
		return append(out, d)
	}

	for name, tc := range map[string]struct {
		drops []dropguard.Declaration
		want  dropguard.ViolationKind
	}{
		"drop with no entry": {
			drops: without("ledger"), want: dropguard.ViolationUndeclared,
		},
		"entry with no drop": {
			drops: with(decl(3, "long_gone", dropguard.KindRetire, 0, "")), want: dropguard.ViolationExpired,
		},
		"two entries for one drop": {
			drops: with(decl(3, "ledger", dropguard.KindRetire, 0, "")), want: dropguard.ViolationDuplicate,
		},
		"retire labelled as preamble": {
			drops: replace("ledger", decl(3, "ledger", dropguard.KindRecreate, 0, "")), want: dropguard.ViolationKindMismatch,
		},
		"preamble labelled as retire": {
			drops: replace("scratch", decl(2, "scratch", dropguard.KindRetire, 0, "")), want: dropguard.ViolationKindMismatch,
		},
		"retire labelled as absent though the chain creates it": {
			drops: replace("ledger", decl(3, "ledger", dropguard.KindAbsent, 0, "predates the chain")), want: dropguard.ViolationKindMismatch,
		},
		"rows expected in a table nothing seeds": {
			drops: replace("ledger", decl(3, "ledger", dropguard.KindRetire, 7, "because I said so")), want: dropguard.ViolationUngrounded,
		},
		"rows destroyed with no reason given": {
			drops: replace("catalogue", decl(3, "catalogue", dropguard.KindRetire, 2, "")), want: dropguard.ViolationUnjustified,
		},
		"absent claimed with no reason given": {
			drops: replace("never_here", decl(3, "never_here", dropguard.KindAbsent, 0, "")), want: dropguard.ViolationUnjustified,
		},
		"unknown kind": {
			drops: replace("ledger", decl(3, "ledger", "probably-fine", 0, "")), want: dropguard.ViolationKindMismatch,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := kinds(dropguard.Reconcile(inv, dropguard.Manifest{Service: "demo", Drops: tc.drops}))
			if got[tc.want] == 0 {
				t.Fatalf("want a %q violation, got %v", tc.want, got)
			}
		})
	}
}

// TestAbsentIsRefusedTheMomentTheTableTurnsUp — the kind that would otherwise be a
// licence to skip counting. "Nothing here creates it" is checked against the parsed
// chain, and separately against the database: a table that IS there at the version
// before the drop makes the claim false, and the drop owes a count again.
func TestAbsentIsRefusedTheMomentTheTableTurnsUp(t *testing.T) {
	drop := dropguard.Drop{Service: "demo", Version: 3, Table: "never_here", File: "0003_retire.sql"}
	d := decl(3, "never_here", dropguard.KindAbsent, 0, "predates the chain")

	if vs := dropguard.Judge(drop, d, 0, dropguard.ErrTableAbsent); len(vs) != 0 {
		t.Fatalf("an absent table is the answer this kind claims; got %+v", vs)
	}
	vs := dropguard.Judge(drop, d, 5, nil)
	if len(vs) != 1 || vs[0].Kind != dropguard.ViolationKindMismatch {
		t.Fatalf("a table holding 5 rows was accepted as absent: %+v", vs)
	}
}
