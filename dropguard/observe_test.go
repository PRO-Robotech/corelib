// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/PRO-Robotech/corelib/dropguard"
)

// TestObserveRefusesToGuessWhenItCannotReachTheDatabase — the precondition half.
//
// A guard whose answer is "zero rows" when it never connected reports the SAFE
// answer under exactly the conditions in which it knows nothing. It must be
// impossible for a caller to receive a number the guard did not read.
func TestObserveRefusesToGuessWhenItCannotReachTheDatabase(t *testing.T) {
	t.Run("nil handle", func(t *testing.T) {
		n, err := dropguard.Observe(context.Background(), nil, "widgets")
		if !errors.Is(err, dropguard.ErrNoConnection) {
			t.Fatalf("got (%d, %v), want ErrNoConnection", n, err)
		}
	})

	t.Run("closed handle", func(t *testing.T) {
		db, err := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if cerr := db.Close(); cerr != nil {
			t.Fatalf("close: %v", cerr)
		}
		n, oerr := dropguard.Observe(context.Background(), db, "widgets")
		if !errors.Is(oerr, dropguard.ErrNoConnection) {
			t.Fatalf("got (%d, %v), want ErrNoConnection — an unreachable database is NOT an empty table", n, oerr)
		}
	})
}

// TestUnverifiedIsNeverClean — the two refusals must reach the verdict as their own
// outcome. Folding them into "no violations" is how a gate ends up green for the
// whole of its life without having measured anything.
func TestUnverifiedIsNeverClean(t *testing.T) {
	d := dropguard.Drop{Service: "demo", Version: 3, Table: "widgets", File: "0003_x.sql"}
	decl := dropguard.Declaration{Version: 3, Table: "widgets", Kind: dropguard.KindRetire}

	for _, tc := range []struct {
		name string
		err  error
	}{
		{"no connection", dropguard.ErrNoConnection},
		{"table absent", dropguard.ErrTableAbsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vs := dropguard.Judge(d, decl, 0, tc.err)
			if len(vs) == 0 {
				t.Fatalf("%v produced no violation — unverified read as clean", tc.err)
			}
			if vs[0].Kind != dropguard.ViolationUnverified {
				t.Fatalf("violation kind %q, want %q", vs[0].Kind, dropguard.ViolationUnverified)
			}
		})
	}
}

// TestJudgeRefusesANonEmptyTable — the counter half. Equality, not "at most": a
// table that grew a row nobody accounted for is the case this exists to catch, and
// the message must name the table and both numbers or the operator cannot act on it.
func TestJudgeRefusesANonEmptyTable(t *testing.T) {
	d := dropguard.Drop{Service: "demo", Version: 3, Table: "widgets", File: "0003_x.sql"}

	t.Run("expected empty, found rows", func(t *testing.T) {
		decl := dropguard.Declaration{Version: 3, Table: "widgets", Kind: dropguard.KindRetire}
		vs := dropguard.Judge(d, decl, 1, nil)
		if len(vs) != 1 || vs[0].Kind != dropguard.ViolationRowCount {
			t.Fatalf("got %+v, want one ViolationRowCount", vs)
		}
		msg := vs[0].Error()
		for _, want := range []string{"widgets", "1", "0"} {
			if !containsToken(msg, want) {
				t.Errorf("message %q does not name %q", msg, want)
			}
		}
	})

	t.Run("expected empty, found empty", func(t *testing.T) {
		decl := dropguard.Declaration{Version: 3, Table: "widgets", Kind: dropguard.KindRetire}
		if vs := dropguard.Judge(d, decl, 0, nil); len(vs) != 0 {
			t.Fatalf("an empty table declared empty must pass, got %+v", vs)
		}
	})

	t.Run("declared row count is matched exactly", func(t *testing.T) {
		decl := dropguard.Declaration{Version: 3, Table: "widgets", Kind: dropguard.KindRetire, ExpectRows: 4, Note: "catalogue seeded by 0001"}
		if vs := dropguard.Judge(d, decl, 4, nil); len(vs) != 0 {
			t.Fatalf("declared 4, measured 4 must pass, got %+v", vs)
		}
		if vs := dropguard.Judge(d, decl, 5, nil); len(vs) != 1 {
			t.Fatalf("declared 4, measured 5 must fail, got %+v", vs)
		}
		if vs := dropguard.Judge(d, decl, 3, nil); len(vs) != 1 {
			t.Fatalf("declared 4, measured 3 must fail too — a wrong number is wrong in both directions, got %+v", vs)
		}
	})
}

func containsToken(s, tok string) bool {
	for i := 0; i+len(tok) <= len(s); i++ {
		if s[i:i+len(tok)] == tok {
			return true
		}
	}
	return false
}
