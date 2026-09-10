// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard

import (
	"fmt"
	"strings"
)

// PreserveCommand is the one place that says HOW a table's rows are saved before a
// drop destroys them.
//
// # Why a command and not a sentence
//
// The refusal that carries it is read at a bad minute: the deploy has stopped, the
// operator did not expect it, and the only other next step the message offers is
// destruction. A sentence ("take a backup first") leaves them to compose a query
// right then, so the executable option and the safe option are not the same option —
// and the executable one wins. This returns something that can be pasted.
//
// # Why psql and not a verb of our own
//
// The rows belong to whoever runs the database, and they must be readable when the
// service is NOT running — the refusal happens before the chain is applied and long
// before any process starts. A verb of ours would have to be shipped, versioned and
// reachable at exactly the moment the installation is half-upgraded; psql is already
// how a database is administered, and it needs nothing from us.
//
// # Why a bare DSN slot and not the runner's environment variable
//
// The runner takes its address from three sources — a flag, an environment
// variable, and the service configuration — and in a chart deployment it is the
// third that holds it. Naming the environment variable would produce a command that
// is EMPTY for most operators while looking configured, which is the same failure
// this package exists to prevent, moved into the fix. The slot is deliberately
// nameless and the message says what to put in it.
//
// # Why the table name is passed through untouched
//
// It is the SAME string the guard counted — the one [Observe] resolved through the
// catalogue before it reported a number. So the operator exports exactly the object
// that was refused, qualified exactly as the migration wrote it; a name re-derived
// here could resolve to a different object under a different search_path and export
// the wrong rows while looking right.
//
// The file lands in the working directory under a name derived from the table, so
// two tables saved in one sitting do not overwrite each other.
func PreserveCommand(table string) string {
	return fmt.Sprintf(
		`psql "$DSN" --no-psqlrc -v ON_ERROR_STOP=1 -c "\copy (%s) TO '%s' WITH (FORMAT csv, HEADER)"`,
		PreserveSelect(table), preserveFile(table))
}

// preserveFile turns a table name into a file name that is safe to type and unique
// per table: qualification survives, separators do not.
func preserveFile(table string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, table)
	return strings.Trim(name, "-") + ".csv"
}

// PreserveSelect is the query inside [PreserveCommand] — the half that a database
// can execute, without psql's meta-command around it.
//
// It exists so that a probe can put the DOCUMENTED command to a real schema instead
// of putting a copy of it there. A probe that retyped the query would agree with
// itself about a table shape the command never names.
func PreserveSelect(table string) string {
	return fmt.Sprintf("SELECT * FROM %s", table)
}
