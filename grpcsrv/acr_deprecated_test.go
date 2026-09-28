// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv_test

// acr_deprecated_test.go — the pre-acrlevel address of the ACR ranking stays
// callable: grpcsrv.ACRRank and grpcsrv.ACRSatisfies keep their v1.9.0
// signatures, answer exactly what acrlevel answers, and are deprecated
// forwarders rather than a second table.
//
// The module path carries no major-version suffix, so an exported name removed
// in a v1 minor release breaks the build of every consumer that raises its pin.
// The address goes away only with a major release of the module.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PRO-Robotech/corelib/acrlevel"
	"github.com/PRO-Robotech/corelib/grpcsrv"
)

// Signatures of v1.9.0. A declaration of another type — a variable of func type
// included — is caught by TestDeprecatedACRAddressesAreForwardingFunctions; a
// different signature fails to compile here.
var (
	_ func(string) int          = grpcsrv.ACRRank
	_ func(string, string) bool = grpcsrv.ACRSatisfies
)

// acrProbeValues — every rung of the ordering and one value the table does not
// know.
var acrProbeValues = []string{"", "0", "1", "2", "3", "unknown-acr"}

// TestDeprecatedACRAddressesAgreeWithAcrlevel — value parity on every value and
// every ordered pair of values.
func TestDeprecatedACRAddressesAgreeWithAcrlevel(t *testing.T) {
	pairs := 0
	for _, v := range acrProbeValues {
		if got, want := grpcsrv.ACRRank(v), acrlevel.Rank(v); got != want {
			t.Errorf("ACRRank(%q) = %d, acrlevel.Rank = %d", v, got, want)
		}
		for _, required := range acrProbeValues {
			pairs++
			if got, want := grpcsrv.ACRSatisfies(v, required), acrlevel.Satisfies(v, required); got != want {
				t.Errorf("ACRSatisfies(%q, %q) = %v, acrlevel.Satisfies = %v", v, required, got, want)
			}
		}
	}
	t.Logf("values: %d; ordered pairs: %d", len(acrProbeValues), pairs)
}

// forwarder — what a deprecated address must be: a function whose doc carries a
// `Deprecated:` paragraph naming the new address and whose body is one return
// of the new address called with its own parameters in order.
type forwarder struct {
	name   string // e.g. ACRRank
	target string // e.g. Rank, in package acrlevel
}

var acrForwarders = []forwarder{{"ACRRank", "Rank"}, {"ACRSatisfies", "Satisfies"}}

// forwarderDefects judges the non-test Go files of dir and names every
// forwarder that is absent, is not a function, lacks the paragraph, or does
// more than forward. files is the census: zero files read is not a verdict.
func forwarderDefects(dir string, want []forwarder) (files int, defects []string, err error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return 0, nil, err
	}
	fset := token.NewFileSet()
	funcs := map[string]*ast.FuncDecl{}
	others := map[string]string{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return files, nil, err
		}
		file, err := parser.ParseFile(fset, p, src, parser.ParseComments)
		if err != nil {
			return files, nil, err
		}
		files++
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					funcs[d.Name.Name] = d
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, n := range vs.Names {
							others[n.Name] = d.Tok.String()
						}
					}
				}
			}
		}
	}
	for _, f := range want {
		fn, ok := funcs[f.name]
		if !ok {
			if tok, declared := others[f.name]; declared {
				defects = append(defects, fmt.Sprintf("%s is declared with %s, not as a function", f.name, tok))
			} else {
				defects = append(defects, f.name+" is not declared")
			}
			continue
		}
		newAddress := "acrlevel." + f.target
		deprecated := false
		for _, para := range strings.Split(fn.Doc.Text(), "\n\n") {
			if strings.HasPrefix(para, "Deprecated: ") && strings.Contains(para, newAddress) {
				deprecated = true
			}
		}
		if !deprecated {
			defects = append(defects, fmt.Sprintf("%s: no doc paragraph starting \"Deprecated: \" names %s", f.name, newAddress))
		}
		if !forwardsTo(fn, f.target) {
			defects = append(defects, fmt.Sprintf("%s: body is not one `return %s(<its parameters>)`", f.name, newAddress))
		}
	}
	return files, defects, nil
}

// forwardsTo — the body is exactly `return acrlevel.<target>(p1, …, pn)`.
func forwardsTo(fn *ast.FuncDecl, target string) bool {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != target {
		return false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "acrlevel" {
		return false
	}
	var params []string
	for _, fld := range fn.Type.Params.List {
		for _, n := range fld.Names {
			params = append(params, n.Name)
		}
	}
	if len(call.Args) != len(params) {
		return false
	}
	for i, arg := range call.Args {
		if id, ok := arg.(*ast.Ident); !ok || id.Name != params[i] {
			return false
		}
	}
	return true
}

// TestDeprecatedACRAddressesAreForwardingFunctions — the package itself.
func TestDeprecatedACRAddressesAreForwardingFunctions(t *testing.T) {
	files, defects, err := forwarderDefects(".", acrForwarders)
	if err != nil {
		t.Fatalf("NOT EXECUTED: %v", err)
	}
	if files == 0 {
		t.Fatal("NOT EXECUTED: no non-test Go file read — zero defects would mean zero read")
	}
	for _, d := range defects {
		t.Error(d)
	}
	t.Logf("files read: %d; forwarders judged: %d", files, len(acrForwarders))
}

// TestForwarderDefectIsFoundAndItsTwinIsSilent — injection both ways on a
// synthetic package; each defect differs from the twin in one fact. The
// synthetic forwarder is named LegacyRank, not ACRRank, so that no line of this
// file reads as a declaration of the real address.
func TestForwarderDefectIsFoundAndItsTwinIsSilent(t *testing.T) {
	const decl = "func LegacyRank(acr string) int { return acrlevel.Rank(acr) }"
	const twin = "package p\n\n" +
		"import \"github.com/PRO-Robotech/corelib/acrlevel\"\n\n" +
		"// LegacyRank ranks.\n" +
		"//\n" +
		"// Deprecated: use acrlevel.Rank.\n" +
		decl + "\n"
	worlds := []struct {
		name string
		src  string
		want string
	}{
		{"variable in place of a function",
			strings.Replace(twin, decl, "var LegacyRank = acrlevel.Rank", 1),
			"declared with var"},
		{"no Deprecated paragraph",
			strings.Replace(twin, "// Deprecated: use acrlevel.Rank.", "// Use acrlevel.Rank.", 1),
			"no doc paragraph"},
		{"Deprecated inside a paragraph, not starting it",
			strings.Replace(twin, "//\n// Deprecated: use acrlevel.Rank.", "// Deprecated: use acrlevel.Rank.", 1),
			"no doc paragraph"},
		{"a table of its own",
			strings.Replace(twin, "{ return acrlevel.Rank(acr) }", "{\n\tswitch acr {\n\tcase \"3\":\n\t\treturn 3\n\t}\n\treturn 0\n}", 1),
			"body is not one"},
		{"forwards to another function",
			strings.Replace(twin, "return acrlevel.Rank(acr)", "return acrlevel.Other(acr)", 1),
			"body is not one"},
		{"forwards something other than its parameter",
			strings.Replace(twin, "return acrlevel.Rank(acr)", "return acrlevel.Rank(\"3\")", 1),
			"body is not one"},
		{"absent",
			"package p\n",
			"is not declared"},
	}
	judge := func(t *testing.T, src string) []string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o600); err != nil {
			t.Fatalf("NOT EXECUTED: %v", err)
		}
		files, defects, err := forwarderDefects(dir, []forwarder{{"LegacyRank", "Rank"}})
		if err != nil || files != 1 {
			t.Fatalf("NOT EXECUTED: files %d, err %v", files, err)
		}
		return defects
	}
	if got := judge(t, twin); len(got) != 0 {
		t.Fatalf("lawful twin reported: %v", got)
	}
	for _, w := range worlds {
		t.Run(w.name, func(t *testing.T) {
			got := judge(t, w.src)
			if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), w.want) {
				t.Errorf("defects %v, want one containing %q", got, w.want)
			}
		})
	}
}
