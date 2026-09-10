// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewHyphenID_Form — going-forward hyphen-form generator (B3 canon, COMP-1
// compute redesign migration point). NewHyphenID emits "<prefix>-<17 crockford>"
// (e.g. "mt-…", "ins-…") — unlike legacy NewID's concat form. Accepts the 2-char
// (`mt`) and 3-char (`ins`) hyphen-canon prefixes. Body reuses the same crockford
// generator as NewID (idBodyLen chars).
func TestNewHyphenID_Form(t *testing.T) {
	for _, p := range []string{PrefixMachineTypeHyphen, PrefixInstanceHyphen} {
		id := NewHyphenID(p)
		require.Truef(t, strings.HasPrefix(id, p+"-"), "id %q must start with %q-", id, p)
		require.Equalf(t, len(p)+1+idBodyLen, len(id), "id %q length", id)
		body := id[len(p)+1:]
		require.Lenf(t, body, idBodyLen, "body %q", body)
		for i := 0; i < len(body); i++ {
			require.Truef(t, isCrockfordChar(body[i]), "id %q body has non-crockford char %q", id, body[i])
		}
	}
}

// TestNewHyphenID_Unique — two calls yield distinct ids (crypto/rand body).
func TestNewHyphenID_Unique(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 1000; i++ {
		id := NewHyphenID(PrefixMachineTypeHyphen)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate hyphen id %q", id)
		}
		seen[id] = struct{}{}
	}
}

// TestNewHyphenID_PanicsOnBadPrefix — prefix must be 2..3 chars (programmer error
// otherwise; prefix comes from a package-level constant).
func TestNewHyphenID_PanicsOnBadPrefix(t *testing.T) {
	require.Panics(t, func() { NewHyphenID("x") })       // 1 char
	require.Panics(t, func() { NewHyphenID("toolong") }) // >3
	require.Panics(t, func() { NewHyphenID("") })
}

// TestHyphenPrefixConstants_InCanon — every exported hyphen-prefix constant must
// be part of the KnownHyphenPrefixes router set (else validate.ResourceID would
// reject the well-formed hyphen id NewHyphenID produces).
//
// The population is READ FROM THE SOURCE, not restated here. A hand-written list
// guards only the constants somebody remembered to add to it: the next
// `Prefix<X>Hyphen` would be introduced unguarded, and the gate would stay green
// while the router rejected every id of the new resource. Enumerating the
// declarations makes the guard cover the CLASS.
//
// The gate carries its own premise-check: if the census finds nothing, the
// declaration shape changed (constants renamed or moved out of ids.go) and this
// gate is inspecting an empty set — that is a finding, not a pass.
func TestHyphenPrefixConstants_InCanon(t *testing.T) {
	const src = "ids.go"

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	require.NoError(t, err, "parse %s — the gate cannot read its own population", src)

	namePat := regexp.MustCompile(`^Prefix.*Hyphen$`)
	found := map[string]string{} // const name -> literal value

	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, id := range vs.Names {
			if !namePat.MatchString(id.Name) || i >= len(vs.Values) {
				continue
			}
			lit, ok := vs.Values[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Errorf("const %s: value %s is not a quoted string", id.Name, lit.Value)
				continue
			}
			found[id.Name] = val
		}
		return true
	})

	// Premise: the census must have read something. Zero declarations means the
	// shape this gate keys on no longer exists — indistinguishable from "all
	// constants are in the canon" unless asserted separately.
	require.NotEmpty(t, found,
		"census read 0 constants matching %s from %s — the declaration shape changed "+
			"and this gate is now inspecting nothing", namePat, src)

	canon := KnownHyphenPrefixes()
	for name, p := range found {
		if _, ok := canon[p]; !ok {
			t.Errorf("hyphen prefix %q (const %s) missing from KnownHyphenPrefixes — "+
				"validate.ResourceID would reject %q- ids", p, name, p)
		}
	}

	// Objem osmotrennogo — "zero findings" must be distinguishable from
	// "zero inspected".
	t.Logf("census: %d exported hyphen-prefix constants inspected against a canon of %d entries",
		len(found), len(canon))
}
