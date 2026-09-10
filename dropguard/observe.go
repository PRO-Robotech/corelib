// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The two ways a measurement can fail to happen. They are values, not log lines,
// because a caller must be forced to handle them: the whole failure mode this
// package exists to prevent is a guard that answers "zero" when it looked at
// nothing.
var (
	// ErrNoConnection — the database could not be reached, or its catalogue could
	// not be read. Nothing was measured.
	ErrNoConnection = errors.New("drop guard reached no database")
	// ErrTableAbsent — the connection worked and the table is not there. Nothing
	// was measured either: an absent table has no row count, and reporting zero
	// would make "already gone" indistinguishable from "safe to drop".
	ErrTableAbsent = errors.New("drop guard found no such table")
)

// Querier is the read surface a measurement needs: *sql.DB and *sql.Conn satisfy
// it, whether the handle points at a test container or at a live database.
type Querier interface {
	PingContext(ctx context.Context) error
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Observe returns the number of rows in table, having first established that it was
// in a position to count them.
//
// It returns a number only when all three of these held: the handle answered a
// ping, the catalogue lists the table, and the count query succeeded. Otherwise it
// returns ErrNoConnection or ErrTableAbsent and NO number. There is deliberately no
// path through this function that yields (0, nil) without a successful COUNT.
func Observe(ctx context.Context, db Querier, table string) (int64, error) {
	if db == nil || (isNilInterface(db)) {
		return 0, fmt.Errorf("%w: no database handle for %q", ErrNoConnection, table)
	}
	if err := db.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("%w: ping before counting %q: %v", ErrNoConnection, table, err)
	}

	schema, bare := splitTable(table)
	var present bool
	// to_regclass resolves through search_path when the name is unqualified, which
	// is how the migrations themselves address these tables.
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass($1) IS NOT NULL`, qualified(schema, bare),
	).Scan(&present); err != nil {
		return 0, fmt.Errorf("%w: reading the catalogue for %q: %v", ErrNoConnection, table, err)
	}
	if !present {
		return 0, fmt.Errorf("%w: %q", ErrTableAbsent, table)
	}

	var n int64
	// A table name cannot be a bind parameter, so it is interpolated. It is not
	// caller input: it came out of a migration file in this repository, matched
	// against [A-Za-z0-9_."], and was just resolved by to_regclass above — a name
	// the catalogue did not recognise never reaches this line.
	q := `SELECT count(*) FROM ` + qualified(schema, bare)
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("%w: counting %q: %v", ErrNoConnection, table, err)
	}
	return n, nil
}

func splitTable(table string) (schema, bare string) {
	if idx := strings.LastIndex(table, "."); idx >= 0 {
		return table[:idx], table[idx+1:]
	}
	return "", table
}

func qualified(schema, bare string) string {
	if schema == "" {
		return bare
	}
	return schema + "." + bare
}

// isNilInterface catches a typed-nil handle (var db *sql.DB; Observe(ctx, db, …)),
// which would otherwise pass the `db == nil` test and panic on the first call.
func isNilInterface(db Querier) bool {
	switch v := db.(type) {
	case *sql.DB:
		return v == nil
	case *sql.Conn:
		return v == nil
	default:
		return false
	}
}
