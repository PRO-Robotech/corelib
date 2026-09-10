// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package dropguard

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Drop is one table-destroying statement found in the Up section of a migration.
type Drop struct {
	Service string
	Version int64
	// Table is the name as written, normalised: lower-cased, unquoted, and
	// schema-qualified only when the migration qualified it.
	Table string
	File  string
	Line  int
	// RecreatedHere reports that the same Up section CREATEs the table again. Such
	// a drop is an idempotency preamble — on a chain that has never run there is
	// nothing there to destroy — and it is read from the migration, never claimed.
	RecreatedHere bool
}

// DynamicDrop is a DROP TABLE whose subject is ASSEMBLED AT RUN TIME: a format
// placeholder or a concatenation stands where the identifier would be, so the name
// exists only once PL/pgSQL builds the string, and no reading of the file can say
// which table goes.
//
// It is recorded rather than dropped on the floor because the census is the whole
// point of this package: a drop nobody counted is the outcome it refuses, and one
// that cannot be counted has to say so out loud rather than be absent from a number.
type DynamicDrop struct {
	Service string
	Version int64
	File    string
	Line    int
	// Text is the fragment as written, so the census shows WHY it was not judged
	// instead of asserting that it could not be.
	Text string
}

// Inv is the result of reading a service's migration directory, together with the
// census that says how much was read. "No drops found" and "no files read" are
// different answers, and a gate that cannot tell them apart asserts nothing.
type Inv struct {
	Service      string
	FilesScanned int
	Drops        []Drop

	// DynamicDrops are the drops this inventory COULD NOT READ: their table name is
	// computed at run time. They are not Drops — nothing here knows what they
	// destroy — and they are not silence either. See [DynamicDrop] and the package
	// doc's "What the reader cannot see".
	DynamicDrops []DynamicDrop

	// seeds records, per table, the versions whose Up section INSERTs into it.
	// It is what grounds a declaration that expects rows: a table no migration
	// ever writes to cannot honestly be declared non-empty.
	seeds map[string][]int64
	// creates records, per table, the versions whose Up section CREATEs it. It is
	// what distinguishes a drop with a subject from a drop aimed at databases this
	// chain never built.
	creates map[string][]int64
}

// CreatesTable reports whether any migration in the chain CREATEs table. A drop of
// a table nothing here creates destroys nothing on any database these migrations
// produced.
func (i Inv) CreatesTable(table string) bool { return len(i.creates[strings.ToLower(table)]) > 0 }

// CreateVersions lists the migrations that CREATE table, so a message can name its
// evidence rather than allude to it.
func (i Inv) CreateVersions(table string) []int64 { return i.creates[strings.ToLower(table)] }

// SeedsTable reports whether any migration strictly BEFORE version writes rows into
// table. Strictly before, because that is the state the drop destroys.
func (i Inv) SeedsTable(table string, version int64) bool {
	for _, v := range i.seeds[strings.ToLower(table)] {
		if v < version {
			return true
		}
	}
	return false
}

// SeedVersions lists the migrations that INSERT into table before version, for a
// message that names its evidence instead of alluding to it.
func (i Inv) SeedVersions(table string, version int64) []int64 {
	var out []int64
	for _, v := range i.seeds[strings.ToLower(table)] {
		if v < version {
			out = append(out, v)
		}
	}
	return out
}

// UnreadableDrops renders each drop whose subject is computed as "NNNN file:line —
// text", so a census can name its evidence instead of alluding to a count.
func (i Inv) UnreadableDrops() []string {
	out := make([]string, 0, len(i.DynamicDrops))
	for _, d := range i.DynamicDrops {
		out = append(out, fmt.Sprintf("%04d %s:%d — %s", d.Version, d.File, d.Line, d.Text))
	}
	return out
}

// DropVersions returns every version that drops something, ascending and deduped —
// the order in which a measured run must step through the chain.
func (i Inv) DropVersions() []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, d := range i.Drops {
		if !seen[d.Version] {
			seen[d.Version] = true
			out = append(out, d.Version)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

var (
	versionRe = regexp.MustCompile(`^(\d+)_`)
	gooseUpRe = regexp.MustCompile(`(?im)^\s*--\s*\+goose\s+Up\s*$`)
	gooseDnRe = regexp.MustCompile(`(?im)^\s*--\s*\+goose\s+Down\s*$`)

	// dropTableRe captures the WHOLE table list, not the first name in it.
	//
	// `DROP TABLE a, b;` is ordinary SQL, and it is what one reaches for when
	// foreign keys make the order of separate statements awkward — a migration in
	// this tree says exactly that in a comment. Capturing one identifier meant the
	// second table and everything after it existed for no part of this gate: no
	// declaration was demanded of it, no measurement stepped over it, no violation
	// named it. It would be destroyed in silence on a green run.
	//
	// CREATE TABLE and INSERT INTO take a single table by grammar, so they stay
	// single-capture; the difference is SQL's, not a choice made here.
	dropTableRe = regexp.MustCompile(`(?is)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([A-Za-z0-9_."]+(?:\s*,\s*[A-Za-z0-9_."]+)*)`)
	// dynamicDropRe matches a DROP TABLE whose SUBJECT IS COMPUTED: a format
	// placeholder (%I, %s) or a concatenation stands where the identifier would be.
	//
	// Such a statement carries no table name to read. The name exists only once
	// PL/pgSQL assembles the string at run time, so no pattern here can say which
	// table it destroys — and that is a boundary of this reader, not a gap in it.
	// It is matched anyway, and counted, because the alternative is SILENCE:
	// dropTableRe simply would not fire, and a drop nobody saw is precisely the
	// outcome the rest of this package exists to refuse.
	//
	// It also prevents AN ANSWER WORSE THAN SILENCE. On `format('DROP TABLE
	// sch.%I', t)` dropTableRe does fire, and captures `sch.` — a truncation that
	// names nothing. Unsuppressed it would enter the inventory as a table, demand a
	// declaration no one can write, and send the measurement at a table that does
	// not exist. A match here suppresses that phantom.
	//
	// The identifier before the placeholder must end in `.` (a schema qualifier),
	// so `EXECUTE 'DROP TABLE a' || ' CASCADE'` — whose table IS written down —
	// stays an ordinary drop and keeps being judged.
	dynamicDropRe = regexp.MustCompile(`(?is)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:[A-Za-z0-9_."]*\.)?(?:%[A-Za-z]|'\s*\|\||\|\|)`)
	createTableRe = regexp.MustCompile(`(?is)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z0-9_."]+)`)
	insertIntoRe  = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+([A-Za-z0-9_."]+)`)
)

// Inventory reads every *.sql in fsys as a goose migration and returns the drops in
// their Up sections. Down sections are excluded on purpose: a Down runs only on a
// rollback, where the table it drops is one the matching Up created.
func Inventory(service string, fsys fs.FS) (Inv, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return Inv{}, fmt.Errorf("dropguard: read migration dir: %w", err)
	}
	inv := Inv{Service: service, seeds: map[string][]int64{}, creates: map[string][]int64{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, rerr := fs.ReadFile(fsys, e.Name())
		if rerr != nil {
			return Inv{}, fmt.Errorf("dropguard: read %s: %w", e.Name(), rerr)
		}
		if perr := inv.addFile(service, e.Name(), string(raw)); perr != nil {
			return Inv{}, perr
		}
	}
	if inv.FilesScanned == 0 {
		return Inv{}, fmt.Errorf("dropguard: %s: no migration files were read — a scan of nothing is not a clean scan", service)
	}
	sort.Slice(inv.Drops, func(a, b int) bool {
		if inv.Drops[a].Version != inv.Drops[b].Version {
			return inv.Drops[a].Version < inv.Drops[b].Version
		}
		return inv.Drops[a].Table < inv.Drops[b].Table
	})
	return inv, nil
}

func (i *Inv) addFile(service, name, body string) error {
	m := versionRe.FindStringSubmatch(path.Base(name))
	if m == nil {
		return fmt.Errorf("dropguard: %s: filename does not start with a goose version", name)
	}
	version, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return fmt.Errorf("dropguard: %s: version %q: %w", name, m[1], err)
	}

	upStart := gooseUpRe.FindStringIndex(body)
	if upStart == nil {
		return fmt.Errorf("dropguard: %s: no `-- +goose Up` marker — goose would not run this file, and neither can this gate read it", name)
	}
	up := body[upStart[1]:]
	if dn := gooseDnRe.FindStringIndex(up); dn != nil {
		up = up[:dn[0]]
	}
	lineOffset := strings.Count(body[:upStart[1]], "\n")

	// Comments are stripped BEFORE any statement is matched: a paragraph that
	// explains a drop is not a drop, and a gate that reads text rather than code
	// stays green while the statement it was written to catch sits three lines away
	// behind a `/*`.
	code := stripSQLComments(up)

	i.FilesScanned++

	created := map[string]bool{}
	for _, c := range createTableRe.FindAllStringSubmatch(code, -1) {
		tbl := normaliseTable(c[1])
		created[tbl] = true
		i.creates[tbl] = append(i.creates[tbl], version)
		if bare := bareName(tbl); bare != tbl {
			i.creates[bare] = append(i.creates[bare], version)
		}
	}
	for _, ins := range insertIntoRe.FindAllStringSubmatch(code, -1) {
		tbl := normaliseTable(ins[1])
		i.seeds[tbl] = append(i.seeds[tbl], version)
		if bare := bareName(tbl); bare != tbl {
			i.seeds[bare] = append(i.seeds[bare], version)
		}
	}
	// Drops whose subject is computed are found FIRST, so the loop below can tell a
	// truncation from a name. Their offsets are keyed by the start of the statement,
	// which both patterns anchor at the same `DROP`.
	dynamicAt := map[int]bool{}
	for _, loc := range dynamicDropRe.FindAllStringIndex(code, -1) {
		dynamicAt[loc[0]] = true
		i.DynamicDrops = append(i.DynamicDrops, DynamicDrop{
			Service: service,
			Version: version,
			File:    name,
			Line:    lineOffset + strings.Count(code[:loc[0]], "\n") + 1,
			Text:    strings.Join(strings.Fields(code[loc[0]:loc[1]]), " "),
		})
	}

	for _, loc := range dropTableRe.FindAllStringSubmatchIndex(code, -1) {
		if dynamicAt[loc[0]] {
			// The capture here is the fragment before a placeholder, not a table.
			// Recording it would invent a drop of something that has no name.
			continue
		}
		// One Drop per table in the list, each with ITS OWN line: a list may span
		// several lines, and a coordinate pointing at the statement's first line
		// would send the reader to somewhere the table is not written.
		for _, item := range splitTableList(code[loc[2]:loc[3]]) {
			tbl := normaliseTable(item.name)
			if tbl == "" {
				continue
			}
			at := loc[2] + item.offset
			i.Drops = append(i.Drops, Drop{
				Service:       service,
				Version:       version,
				Table:         tbl,
				File:          name,
				Line:          lineOffset + strings.Count(code[:at], "\n") + 1,
				RecreatedHere: created[tbl],
			})
		}
	}
	return nil
}

// listItem is one identifier of a comma-separated table list, with its byte offset
// inside the list so the line it sits on can be reported.
type listItem struct {
	name   string
	offset int
}

// splitTableList splits `a, b.c, "d"` into its items, keeping each one's offset.
//
// Only the list CAPTURED after DROP TABLE is split, never the whole statement: a
// column list or a VALUES tuple elsewhere in the migration is also full of commas,
// and reading those as tables would invent drops that never happen.
func splitTableList(list string) []listItem {
	var out []listItem
	start := 0
	for i := 0; i <= len(list); i++ {
		if i < len(list) && list[i] != ',' {
			continue
		}
		part := list[start:i]
		lead := len(part) - len(strings.TrimLeft(part, " \t\r\n"))
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, listItem{name: trimmed, offset: start + lead})
		}
		start = i + 1
	}
	return out
}

// normaliseTable lower-cases and unquotes an identifier, keeping any schema
// qualification the migration wrote.
func normaliseTable(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), `"`, ``))
}

// bareName strips a schema qualifier: `kaname.roles` → `roles`.
func bareName(s string) string {
	if idx := strings.LastIndex(s, "."); idx >= 0 {
		return s[idx+1:]
	}
	return s
}

// stripSQLComments blanks out `--` line comments and `/* */` block comments while
// preserving every byte offset and newline, so a match still reports the line it was
// found on. Single-quoted string literals are stepped over: a `--` inside a seeded
// description is data, not a comment.
func stripSQLComments(s string) string {
	out := []byte(s)
	blank := func(from, to int) {
		for k := from; k < to && k < len(out); k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	for i := 0; i < len(s); {
		switch {
		case s[i] == '\'':
			i++
			for i < len(s) {
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' { // '' escape
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case strings.HasPrefix(s[i:], "--"):
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				blank(i, len(s))
				return string(out)
			}
			blank(i, i+end)
			i += end
		case strings.HasPrefix(s[i:], "/*"):
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				blank(i, len(s))
				return string(out)
			}
			blank(i, i+2+end+2)
			i += 2 + end + 2
		default:
			i++
		}
	}
	return string(out)
}
