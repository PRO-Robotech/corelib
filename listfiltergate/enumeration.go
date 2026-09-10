// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listfiltergate

// enumeration.go — where the enumerate-then-narrow ban comes FROM.
//
// The ban used to be a list of two call names. `security.md` refuses the shape by
// its substance — an enumeration has a ceiling and no continuation, so a tenant's
// own rows fall past the ceiling and go invisible at live rights — and a list of
// names refuses only the instances someone already met. A third form existed in
// iam's own database the whole time and was invisible to the gate for exactly that
// reason (#651).
//
// So the names are DERIVED from a property of the declarations:
//
//	a verdict is asked ABOUT identifiers the caller holds     → bool, []bool, map[string]bool
//	an enumeration PRODUCES the identifiers                   → []string
//
// The profile names the TYPES that answer authorization questions; this file reads
// their method sets and returns the enumerating ones. Adding a method to such a type
// extends the ban without anyone editing a list.
//
// # What this deliberately does NOT do
//
// It does not decide whether a call is "about authorization" by its name, its
// parameters, or the words in its doc comment. All three are naming habits, and the
// gate's own doctrine is that a habit changed by a refactor must not change what the
// gate sees. The authorization surfaces are named ONCE, as declared types, and
// everything else is read off the declaration.
//
// The consequence is stated rather than hidden: matching at the call site is by the
// method NAME, exactly as Profile.Banned already matches, because this analyser
// resolves calls syntactically and does not leave the package it judges. A listing
// that reaches an unrelated method of the same name is therefore a finding too. That
// is the safe direction — it over-reports and never under-reports — but it is a real
// cost, and a service that hits it should say so in its profile rather than widen
// the shape rule until the gate stops meaning anything.
//
// # Two premises, because there are two kinds of source (#684)
//
// iam's sources enumerate TODAY, so a method set that holds no enumerating shape
// means the shape rule stopped matching the tree — a silently empty ban. Every
// consumer service is the other way round: the surface through which it asks the
// authorization question answers verdicts and enumerates NOTHING, and that is the
// entire point of naming it. The ban has to arrive BEFORE the first enumerating
// method, not after the incident that revealed one.
//
// A source therefore declares which of the two it is (SourceRole), and the premise
// that can rot in silence is the one that is checked: a source declared as the
// service's enumeration surface must still hold one. The other direction — a
// verdict surface that starts enumerating — needs no finding, because the ban
// extends by itself and the census says so out loud; making it red as well would
// fire on correct code, and a gate that fires on correct code is a gate somebody
// switches off.
//
// # Where a source is resolved from
//
// A service's own declarations are resolved from Options.Root. The port through
// which consumer services ask kaname is NOT service code — it is shared
// foundation (pkg/…), and it is the shortest path from "narrow this page" to
// "enumerate the universe", because the RPC it fronts is the one that enumerates.
// Such a source declares Shared, and is resolved from the MODULE root, which is
// found by walking up from Options.Root to the directory holding go.mod. A module
// root that cannot be found is a FINDING, never a skipped source.

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// enumerationCensus is what one derivation found, kept so a passing run can state
// how the ban was obtained instead of leaving it unsaid.
type enumerationCensus struct {
	// Sources is one line per declared source: "<dir>.<Type>: n method(s), k enumerating".
	Sources []string
	// Names are the derived call names, sorted and de-duplicated.
	Names []string
	// Origin maps a derived name to the source it came from, so a finding can say
	// WHICH declaration made the call a ban rather than leaving the reader to guess
	// whether it was hand-written or read off the tree.
	Origin map[string]string
	// Findings are the derivation's own failures: a source that resolves to nothing,
	// or one whose method set no longer holds a single enumerating shape.
	Findings []string
}

// deriveEnumerations reads every declared source under root and returns the call
// names whose answer is a set of identifiers.
//
// A source that cannot be resolved is a FINDING, never an empty result. The whole
// point of this field is to produce a ban; a source silently resolving to nothing
// would remove that ban while the gate went on printing OK — the exact shape the
// gate exists to refuse.
func deriveEnumerations(root string, sources []EnumerationSource) enumerationCensus {
	out := enumerationCensus{Origin: map[string]string{}}
	seen := map[string]bool{}

	// The module root is resolved ONCE, and only when something needs it: a service
	// whose sources are all its own must not fail because the copy it is pointed at
	// happens to carry no go.mod.
	moduleRoot := ""
	if anyShared(sources) {
		mr, err := moduleRootOf(root)
		if err != nil {
			out.Findings = append(out.Findings, err.Error())
		}
		moduleRoot = mr
	}

	for _, src := range sources {
		label := src.Dir + "." + src.Type
		base := root
		if src.Shared {
			if moduleRoot == "" {
				// Already reported above; deriving from the service root instead
				// would silently read the wrong tree — or nothing — and call it a ban.
				continue
			}
			base = moduleRoot
		}
		dir := filepath.Join(base, src.Dir)
		u, err := loadUnit(dir, "")
		if err != nil {
			out.Findings = append(out.Findings, fmt.Sprintf(
				"Profile.EnumerationSources names %q, but %s could not be read (%v) — the ban this "+
					"source derives would silently disappear while the gate reported OK", label, dir, err))
			continue
		}
		methods, found := u.methodSetOf(src.Type)
		if !found {
			out.Findings = append(out.Findings, fmt.Sprintf(
				"Profile.EnumerationSources names type %q, but no such type is declared in %s — the "+
					"entry has nothing left to describe, and the calls it used to ban are now allowed "+
					"everywhere; move it with its subject or remove it", src.Type, src.Dir))
			continue
		}
		var derived []string
		for _, m := range methods {
			if answersWithIdentifierSet(m.results) {
				derived = append(derived, m.name)
			}
		}
		// The premise, checked rather than assumed — and which premise it is comes
		// from the declaration, because the two kinds of source rot in opposite
		// directions (see the package comment).
		if len(derived) == 0 && src.Role == Enumerates {
			out.Findings = append(out.Findings, fmt.Sprintf(
				"Profile.EnumerationSources names %q as this service's ENUMERATION surface, but its "+
					"%d method(s) hold no enumerating shape at all: nothing there answers with a set of "+
					"identifiers ([]string). Either the type is no longer that surface — drop the entry, "+
					"or declare it AsksVerdicts — or the answer changed form and this rule now derives "+
					"an EMPTY ban from it",
				label, len(methods)))
		}
		sort.Strings(derived)
		line := fmt.Sprintf("%s: %d method(s), %d enumerating", label, len(methods), len(derived))
		switch {
		case src.Role == AsksVerdicts && len(derived) == 0:
			// Said out loud, because this is the state the entry exists FOR: the
			// surface answers verdicts today, and the ban it will produce is the one
			// nobody has written yet. A census that printed nothing here would make
			// "watched, and there is nothing to ban" look like "not watched".
			line += " (verdicts only, as declared — the ban arrives with the first method that enumerates)"
		case src.Role == AsksVerdicts:
			// The safe direction: the ban extended by itself. Not a finding — the
			// protection is the finding, at the call site, with a coordinate — but it
			// must not pass unread either.
			line += fmt.Sprintf(" (declared verdicts-only, and it NOW ENUMERATES: %s — the ban extended "+
				"by itself; check that each is legitimate and correct the declaration)",
				strings.Join(derived, ", "))
		}
		out.Sources = append(out.Sources, line)
		for _, n := range derived {
			if !seen[n] {
				seen[n] = true
				out.Names = append(out.Names, n)
				out.Origin[n] = label
			}
		}
	}
	sort.Strings(out.Names)
	return out
}

// declaredMethod is one method of a declared source: its name and its results.
type declaredMethod struct {
	name    string
	results []ast.Expr
}

// methodSetOf returns the methods of the named type — the interface's own method
// list, or the methods declared on it as a receiver.
//
// Both forms are read because both occur: iam asks the store through an interface
// (clients.RelationQueries) and its own database through a concrete type
// (relverdict.Asker). Accepting only one of them would have derived half the ban
// and said nothing about the other half.
func (u *unit) methodSetOf(typeName string) ([]declaredMethod, bool) {
	var out []declaredMethod
	found := false

	for _, f := range u.files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != typeName {
					continue
				}
				found = true
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok || it.Methods == nil {
					continue
				}
				for _, m := range it.Methods.List {
					ft, ok := m.Type.(*ast.FuncType)
					if !ok || len(m.Names) == 0 {
						// An embedded interface: its methods are declared elsewhere and
						// are not read here. Named in the census by absence — the count
						// this source reports is the count of what was actually read.
						continue
					}
					out = append(out, declaredMethod{name: m.Names[0].Name, results: resultTypes(ft)})
				}
			}
		}
	}

	// Methods on the type as a receiver. `u.methods` is keyed "Type.Method", so the
	// receiver's TYPE decides membership — the receiver variable's name is never
	// consulted, exactly as when anchors are attributed.
	for key, fn := range u.methods {
		recvType, name, ok := splitMethodKey(key)
		if !ok || recvType != typeName {
			continue
		}
		found = true
		if !ast.IsExported(name) {
			// An unexported helper is not a call any other package can make, so it
			// cannot be the form a listing reaches.
			continue
		}
		out = append(out, declaredMethod{name: name, results: resultTypes(fn.Type)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, found
}

// splitMethodKey splits "Type.Method" as u.methods keys it.
func splitMethodKey(key string) (typeName, method string, ok bool) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '.' {
			return key[:i], key[i+1:], i > 0 && i+1 < len(key)
		}
	}
	return "", "", false
}

// resultTypes returns the declared result types of a function type, in order.
func resultTypes(ft *ast.FuncType) []ast.Expr {
	if ft == nil || ft.Results == nil {
		return nil
	}
	var out []ast.Expr
	for _, fld := range ft.Results.List {
		n := len(fld.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, fld.Type)
		}
	}
	return out
}

// answersWithIdentifierSet reports whether the FIRST non-error result is a set of
// identifiers — that is, whether the callee PRODUCES the ids rather than judging ids
// it was handed.
//
// `error` is skipped because it is carried by every result list here and says
// nothing about the answer. Everything after the first real result is skipped too:
// a continuation token or a completeness flag rides alongside the answer, it is not
// the answer.
//
// `[]string` and nothing else. `[]bool` is one verdict per supplied object;
// `map[string]bool` is the same keyed by the caller's own ids; `bool` is one
// verdict. Widening this to "any slice" would swallow those and, worse, would flag
// every page read in the tree, since a page is a slice too — a gate that fires on
// correct code is a gate somebody switches off.
func answersWithIdentifierSet(results []ast.Expr) bool {
	for _, r := range results {
		if isErrorType(r) {
			continue
		}
		at, ok := r.(*ast.ArrayType)
		if !ok || at.Len != nil {
			return false // the first real answer is not a slice: a verdict, a tree, a page count
		}
		id, ok := at.Elt.(*ast.Ident)
		return ok && id.Name == "string"
	}
	return false
}

// mergeSorted returns the union of two name lists, de-duplicated and sorted.
func mergeSorted(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, n := range list {
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// isErrorType reports whether the expression is the predeclared `error`.
func isErrorType(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "error"
}

// anyShared reports whether any source is resolved from the module root.
func anyShared(sources []EnumerationSource) bool {
	for _, s := range sources {
		if s.Shared {
			return true
		}
	}
	return false
}

// sharedFoundationModule is the module a Shared source resolves against. Today's
// only Shared source (pkg/listnarrow) lives here, at the outer kacho module —
// never inside a service's own nested one (services/iam declares module kaname).
const sharedFoundationModule = "github.com/PRO-Robotech/kacho"

// moduleRootOf walks up from root until it finds the directory whose go.mod
// declares sharedFoundationModule.
//
// The module root is a FACT about the tree — a file that is there or is not —
// rather than a count of path segments above the service. "services/<x> is two
// levels below the root" is a layout habit, and a habit changed by a move must not
// change what the gate reads; a missing go.mod, by contrast, says plainly that the
// tree it was pointed at is not a module.
//
// A go.mod that exists but declares a DIFFERENT module is not a skip either — it
// is walked PAST, not trusted (PRO-Robotech/kacho#2211, census row 2). A service
// can sit inside its own nested module (services/iam does, module kaname) while
// declaring no Shared source today; the day one is added, stopping at the first
// go.mod met would resolve pkg/listnarrow under the WRONG root. Anything that
// happens to exist at the same relative path there is then read as if it were the
// shared foundation port — not a loud "could not be read" finding, a silent wrong
// derivation from a file nobody meant to name.
//
// Not finding the declaring go.mod at all is a FINDING, never an empty answer: a
// shared source that silently resolved to nothing would take its whole derived ban
// with it while the run went on printing OK — the exact shape this gate exists to
// refuse.
func moduleRootOf(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf(
			"Profile.EnumerationSources declares a Shared source, but %s has no absolute path (%v) — "+
				"the module root it is resolved from cannot be found, and a source that resolves to "+
				"nothing removes the ban it derives while the run reports OK", root, err)
	}
	for dir := abs; ; {
		if raw, rerr := os.ReadFile(filepath.Join(dir, "go.mod")); rerr == nil { // #nosec G304 -- walk-up path under the tree the caller named
			switch decl := moduleDeclOf(raw); decl {
			case "":
				return "", fmt.Errorf(
					"Profile.EnumerationSources declares a Shared source, and %s declares a go.mod, "+
						"but it names no `module` line — the module root it is resolved from cannot "+
						"be confirmed, so a source that resolves to nothing removes the ban it derives "+
						"while the run reports OK", filepath.Join(dir, "go.mod"))
			case sharedFoundationModule:
				return dir, nil
			default:
				// A REAL module, just not this one — a service nested in its own
				// module (services/iam) sits inside a go.mod exactly like this.
				// Walked past, not trusted: the shared foundation is further up.
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf(
				"Profile.EnumerationSources declares a Shared source, but no go.mod declaring %s was "+
					"found walking up from %s — the module root it is resolved from does not exist "+
					"here, so the ban it derives would silently disappear while the gate reported OK",
				sharedFoundationModule, abs)
		}
		dir = parent
	}
}

// moduleDeclRe matches the `module` directive's argument, the same form
// tools/unreadfieldaudit/protofieldreaders reads its own go.mod files with.
var moduleDeclRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

// moduleDeclOf returns the module path raw declares, or "" if there is none.
func moduleDeclOf(raw []byte) string {
	m := moduleDeclRe.FindSubmatch(raw)
	if m == nil {
		return ""
	}
	return string(m[1])
}
