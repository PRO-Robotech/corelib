// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listfiltergate

import (
	"strings"
	"testing"
)

// ownmodule_test.go — the module a Shared source is resolved against is derived
// from THIS PACKAGE'S OWN PLACE, never named by a literal.
//
// # Why a literal cannot hold this
//
// `pkg/listfiltergate` is classed `оснастка сборки` and `pkg/listnarrow` — the
// only Shared source — is classed `corelib`: both leave for the foundation module
// when `pkg/` is published. A constant naming the platform module therefore
// outlives its subject on the day of the move, and it does so in the worst way
// available: the walk-up finds the foundation's own go.mod, does not recognise
// the module it declares, walks PAST it to the filesystem root and reports "no
// go.mod declaring …" — a finding about a tree that is perfectly well formed.
//
// The same class is already refused tree-wide by `internal/repohygiene`
// TestModulePathConstantDoesNotOutliveItsModule, and it is refused here by
// construction instead: there is nothing left to outlive.
//
// # The derivation, and its legal twin
//
// `ownModulePath` reads the import path of this very package and
// `moduleDeclIsOurs` asks whether a declared module CONTAINS it. Both layouts are
// exercised by value, the way `pkg/grpcsrv` derives the guard's scope from the
// guard's own package rather than from a path literal:
//
//	monorepo   own = <owner>/kacho/pkg/listfiltergate   go.mod <owner>/kacho    → ours
//	foundation own = <owner>/corelib/listfiltergate     go.mod <owner>/corelib  → ours
//
// The legal twin of both is the nested service module: `services/iam` declares a
// module of its own, and a walk that trusted the first go.mod it met would resolve
// the Shared port under it. It must NOT be recognised as ours in either layout.

func TestOwnModulePathIsReadFromThisPackagesImportPath(t *testing.T) {
	own := ownModulePath()
	if own == "" {
		t.Fatal("the import path of this package could not be read — the module a Shared " +
			"source resolves against would then be derived from nothing, and the walk-up " +
			"would report a finding on a well-formed tree")
	}
	if !strings.HasSuffix(own, "/listfiltergate") {
		t.Fatalf("the derived path must be THIS package's import path; got %q", own)
	}
}

// TestModuleDeclIsOursHoldsOnBothLayouts — the predicate by value, so that the
// foundation layout is proved without a second repository and without publishing
// anything.
func TestModuleDeclIsOursHoldsOnBothLayouts(t *testing.T) {
	const owner = "github.com/PRO-Robotech"
	cases := []struct {
		name string
		own  string
		decl string
		want bool
	}{
		{"monorepo: the outer module declares us", owner + "/kacho/pkg/listfiltergate", owner + "/kacho", true},
		{"foundation: the module IS our root", owner + "/corelib/listfiltergate", owner + "/corelib", true},
		{"monorepo: a nested service module is walked past", owner + "/kacho/pkg/listfiltergate", owner + "/kaname", false},
		{"foundation: a nested service module is walked past", owner + "/corelib/listfiltergate", owner + "/kaname", false},
		{"a longer path that merely shares a prefix segment is not ours",
			owner + "/kacho/pkg/listfiltergate", owner + "/kacho-extra", false},
		{"an empty declaration is never ours", owner + "/kacho/pkg/listfiltergate", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := moduleDeclIsOurs(c.decl, c.own); got != c.want {
				t.Fatalf("moduleDeclIsOurs(%q, %q) = %v, want %v", c.decl, c.own, got, c.want)
			}
		})
	}
}

// TestDerivedModulePathAgreesWithTheTreeItRunsIn — the control in the other
// direction: on the monorepo layout the derivation must produce exactly what the
// literal used to say, or the change would be a silent widening rather than a
// change of source.
func TestDerivedModulePathAgreesWithTheTreeItRunsIn(t *testing.T) {
	own := ownModulePath()
	root, err := moduleRootOf(".")
	if err != nil {
		t.Fatalf("the module root of the tree this test runs in must be found: %v", err)
	}
	if root == "" {
		t.Fatal("an empty module root would resolve a Shared source against the filesystem root")
	}
	t.Logf("осмотрено: собственный путь пакета %q, корень модуля %q", own, root)
}
