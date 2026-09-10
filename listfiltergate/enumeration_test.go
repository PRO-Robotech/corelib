// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listfiltergate

// enumeration_test.go — the derived ban, asserted in BOTH directions on fixtures
// small enough that each case isolates one property.
//
// The property under test is the discrimination itself: a call that PRODUCES a set
// of identifiers is an enumeration, a call that judges identifiers it was handed is
// not. A test that only ever saw the first kind would prove the gate fires, and
// nothing about what it lets through — and "lets through" is the whole risk here,
// because the previous ban let a whole form through for months (#651).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree materialises files under a throwaway root and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return root
}

// TestDeriveEnumerations_TellsAnAnswerFromAVerdict — one source, every result shape
// the tree actually uses, and the expectation stated per method.
func TestDeriveEnumerations_TellsAnAnswerFromAVerdict(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/clients/port.go": `package clients

type Relations interface {
	// PRODUCES ids → enumeration.
	ListObjects(ctx context.Context, subject, relation, objectType string) ([]string, error)
	// PRODUCES ids, with a continuation alongside → still an enumeration: the token
	// rides with the answer, it is not the answer.
	ListSubjects(ctx context.Context, objectType, id, relation string) ([]string, string, error)
	// JUDGES ids the caller holds → not an enumeration.
	Check(ctx context.Context, subject, relation, object string) (bool, error)
	BatchCheck(ctx context.Context, subject, relation string, objects []string) ([]bool, error)
	VisibleSet(ctx context.Context, subject string, objects []string) (map[string]bool, error)
	// Neither: a tree, and a count.
	Expand(ctx context.Context, object, relation string) (*ExpandTree, error)
	CountVisible(ctx context.Context, subject string) (int, error)
	// No result at all.
	Warm(ctx context.Context)
}
`})

	got := deriveEnumerations(root, []EnumerationSource{{Dir: "internal/clients", Type: "Relations"}})

	if len(got.Findings) != 0 {
		t.Fatalf("derivation reported findings on a healthy source: %v", got.Findings)
	}
	want := []string{"ListObjects", "ListSubjects"}
	if strings.Join(got.Names, ",") != strings.Join(want, ",") {
		t.Fatalf("derived %v; want %v — a verdict must never be read as an enumeration, "+
			"and an enumeration must never be missed", got.Names, want)
	}
	if got.Origin["ListObjects"] != "internal/clients.Relations" {
		t.Fatalf("origin of a derived name must NAME its source; got %q", got.Origin["ListObjects"])
	}
	if len(got.Sources) != 1 || !strings.Contains(got.Sources[0], "8 method(s), 2 enumerating") {
		t.Fatalf("the census must state what was read; got %v", got.Sources)
	}
}

// TestDeriveEnumerations_ReadsMethodsDeclaredOnAConcreteType — the second form the
// tree uses. iam asks the store through an interface and its own database through a
// concrete type; a derivation that read only interfaces would have derived half the
// ban and said nothing about the other half — which is exactly the half #651 is about.
func TestDeriveEnumerations_ReadsMethodsDeclaredOnAConcreteType(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/repo/verdict/asker.go": `package verdict

type Asker struct{ pool *pgxpool.Pool }

func (a *Asker) Allowed(ctx context.Context, subject, object string) (bool, error) { return false, nil }

func (a *Asker) Objects(ctx context.Context, subject, objectType string, relations []string, limit int) ([]string, bool, error) {
	return nil, false, nil
}

// unexported: not a call another package can make.
func (a *Asker) objects(ctx context.Context) ([]string, error) { return nil, nil }

// a different receiver: not this source.
func (o *Other) Objects(ctx context.Context) ([]string, error) { return nil, nil }
`})

	got := deriveEnumerations(root, []EnumerationSource{{Dir: "internal/repo/verdict", Type: "Asker"}})

	if len(got.Findings) != 0 {
		t.Fatalf("unexpected findings: %v", got.Findings)
	}
	if strings.Join(got.Names, ",") != "Objects" {
		t.Fatalf("derived %v; want [Objects] — the receiver TYPE decides membership, not the name", got.Names)
	}
}

// TestDeriveEnumerations_SourceWithNothingLeftToDescribeIsAFinding — the self-expiry.
// A source that stopped resolving takes its whole derived ban with it; silence there
// would leave every listing judged against a narrower ban than the profile declares,
// and the run would still print OK.
func TestDeriveEnumerations_SourceWithNothingLeftToDescribeIsAFinding(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/clients/port.go": `package clients

type Renamed interface {
	ListObjects(ctx context.Context, subject string) ([]string, error)
}
`})

	got := deriveEnumerations(root, []EnumerationSource{
		{Dir: "internal/clients", Type: "Relations"},
		{Dir: "internal/nowhere", Type: "Relations"},
	})

	if len(got.Findings) != 2 {
		t.Fatalf("want a finding for the missing TYPE and one for the missing DIRECTORY; got %v", got.Findings)
	}
	if !strings.Contains(got.Findings[0], "no such type is declared") {
		t.Fatalf("the finding must say the entry has nothing left to describe; got %q", got.Findings[0])
	}
	if !strings.Contains(got.Findings[1], "could not be read") {
		t.Fatalf("an unreadable source must not read as an empty one; got %q", got.Findings[1])
	}
	if len(got.Names) != 0 {
		t.Fatalf("a source that did not resolve must derive nothing; got %v", got.Names)
	}
}

// TestDeriveEnumerations_SourceThatDerivesNothingIsAFinding — the premise check.
// The source was named BECAUSE it answers "which objects". A method set holding no
// such shape means the rule stopped matching the tree, and an EMPTY ban derived in
// silence is worse than a wrong one: nothing says it happened.
func TestDeriveEnumerations_SourceThatDerivesNothingIsAFinding(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/clients/port.go": `package clients

type Relations interface {
	Check(ctx context.Context, subject, relation, object string) (bool, error)
	// The answer changed form — a declared id type instead of a bare string. The
	// rule no longer matches, and the gate has to SAY so rather than derive nothing.
	ListObjects(ctx context.Context, subject string) ([]ObjectID, error)
}
`})

	got := deriveEnumerations(root, []EnumerationSource{{Dir: "internal/clients", Type: "Relations"}})

	if len(got.Findings) != 1 || !strings.Contains(got.Findings[0], "no enumerating shape at all") {
		t.Fatalf("want the premise check to fire; got %v", got.Findings)
	}
}

// TestAudit_DerivedNameIsBannedInsideANarrowingListing — end to end through Audit,
// in both directions: the same call, banned under a narrowing shape and legitimate
// under StoreQuery, where the enumeration is what the caller asked for.
func TestAudit_DerivedNameIsBannedInsideANarrowingListing(t *testing.T) {
	source := `package clients

type Relations interface {
	Objects(ctx context.Context, subject, objectType string) ([]string, error)
	Check(ctx context.Context, subject, relation, object string) (bool, error)
}
`
	listing := `package user

type Handler struct{ uc *UseCase }

func (h *Handler) List(ctx context.Context) error { return h.uc.Execute(ctx) }

func (u *UseCase) Execute(ctx context.Context) error {
	ids, _ := u.relations.Objects(ctx, "user:u", "iam_user")
	_ = ids
	return nil
}
`
	p := Profile{
		Service:        "fixture-derived",
		AnchorRoot:     "internal/api",
		PerPackage:     true,
		ReceiverSuffix: "Handler",
		Filters:        []string{"VisibleSet"},
		EnumerationSources: []EnumerationSource{
			{Dir: "internal/clients", Type: "Relations"},
		},
	}
	files := map[string]string{
		"internal/clients/port.go":  source,
		"internal/api/user/list.go": listing,
	}

	// Narrowing shape → the call is refused, and the message names the source that
	// made it a ban rather than leaving the reader to guess where it came from.
	out, err := run(t, p, files, map[string]Listing{"user.List": {Shape: RowFilter}})
	if err == nil {
		t.Fatalf("a listing that takes its page from an enumeration must be refused; output:\n%s", out)
	}
	if !strings.Contains(out, "reaches Objects") || !strings.Contains(out, "internal/clients.Relations") {
		t.Fatalf("the finding must name the call AND its source; got:\n%s", out)
	}
	if !strings.Contains(out, "list.go") {
		t.Fatalf("the finding must carry the coordinate of the declaration; got:\n%s", out)
	}

	// The LEGAL twin of the same outward form: the enumeration IS the response. Same
	// call, same tree, different shape — and the gate must be silent. Without this
	// the case above would only prove the gate fires on the token.
	out, err = run(t, p, files, map[string]Listing{
		"user.List": {Shape: StoreQuery, Gate: "authorizeCaller"},
	})
	if err == nil {
		t.Fatalf("StoreQuery still owes ParentGate's evidence; expected a finding about the gate, got:\n%s", out)
	}
	if strings.Contains(out, "reaches Objects") {
		t.Fatalf("the enumeration ban must NOT apply where the enumeration is the response; got:\n%s", out)
	}
}

// TestAudit_CensusStatesHowTheBanWasObtained — a run that derived nothing must not
// read like a run that derived and found nothing to ban. The ban is the part of this
// gate that comes from the tree rather than from a list, so it is printed on every
// path, including the passing one.
func TestAudit_CensusStatesHowTheBanWasObtained(t *testing.T) {
	files := map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}
`}
	out, err := run(t, flatProfile, files)
	if err != nil {
		t.Fatalf("compliant fixture must pass; got %v\n%s", err, out)
	}
	if !strings.Contains(out, "enumerate-then-narrow ban — 2 call(s)") {
		t.Fatalf("the effective ban must be stated on the passing path; got:\n%s", out)
	}
	if !strings.Contains(out, "no enumeration source declared") {
		t.Fatalf("a profile deriving nothing must SAY so — otherwise it is indistinguishable "+
			"from one whose derivation works; got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// #684 — the two premises, the shared source, and the declared inapplicability
// ---------------------------------------------------------------------------

// verdictSurface is a source of the second kind: it answers ABOUT ids the caller
// holds and enumerates nothing. Every consumer service's authorization surface has
// this shape, which is exactly why the ban there has to arrive before the first
// enumerating method rather than after it.
const verdictSurface = `package check

type IAMCheckClient struct{ cli Stub }

func (c *IAMCheckClient) Check(ctx context.Context, subject, relation, object string) (bool, error) {
	return false, nil
}
`

// TestDeriveEnumerations_AVerdictSurfaceWithNothingToBanIsNotAFinding — the premise
// that consumer services stand on.
//
// Under the single premise this rule shipped with, naming such a surface was a
// FINDING ("no enumerating shape at all"), so the only way to keep a consumer's gate
// green was to name nothing — which is the state #684 exists to end. The census must
// still SAY what it read: "watched, and there is nothing to ban" has to stay
// distinguishable from "not watched", and those two produce the same empty ban.
func TestDeriveEnumerations_AVerdictSurfaceWithNothingToBanIsNotAFinding(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/check/check_client.go": verdictSurface})

	got := deriveEnumerations(root, []EnumerationSource{
		{Dir: "internal/check", Type: "IAMCheckClient", Role: AsksVerdicts},
	})

	if len(got.Findings) != 0 {
		t.Fatalf("a surface declared as answering verdicts must not be a finding for holding "+
			"no enumeration — that is the state it is named FOR; got %v", got.Findings)
	}
	if len(got.Names) != 0 {
		t.Fatalf("nothing here enumerates, so nothing may be derived; got %v", got.Names)
	}
	if len(got.Sources) != 1 || !strings.Contains(got.Sources[0], "verdicts only, as declared") {
		t.Fatalf("the census must state that the surface was read and held nothing — otherwise "+
			"an empty ban from a WATCHED source is indistinguishable from an unwatched one; got %v",
			got.Sources)
	}

	// The paired opposite, on the SAME tree: declared as the service's enumeration
	// surface, the very same method set is a finding. Without this half the case
	// above would only prove that AsksVerdicts silences the check, not that the
	// check still exists for the sources that stand on the other premise.
	strict := deriveEnumerations(root, []EnumerationSource{
		{Dir: "internal/check", Type: "IAMCheckClient"},
	})
	if len(strict.Findings) != 1 || !strings.Contains(strict.Findings[0], "no enumerating shape at all") {
		t.Fatalf("the strict premise must still fire on the same tree; got %v", strict.Findings)
	}
}

// TestDeriveEnumerations_AVerdictSurfaceThatStartsEnumeratingExtendsTheBan — the
// property the declaration is made for, and the whole of its value: the ban arrives
// with the method, not with the incident.
//
// It is deliberately NOT a finding. The protection is the finding, at the call site,
// with a coordinate; making the mere existence of the method red as well would fire
// on correct code — an enumerating method nothing lists from is not a defect — and a
// gate that fires on correct code is a gate somebody switches off. What the census
// owes instead is to say it out loud, because a ban that grew silently is a ban
// nobody re-read.
func TestDeriveEnumerations_AVerdictSurfaceThatStartsEnumeratingExtendsTheBan(t *testing.T) {
	root := writeTree(t, map[string]string{"internal/check/check_client.go": strings.Replace(
		verdictSurface,
		"func (c *IAMCheckClient) Check(",
		"func (c *IAMCheckClient) AllowedIDs(ctx context.Context, subject string) ([]string, error) {\n"+
			"\treturn nil, nil\n}\n\nfunc (c *IAMCheckClient) Check(", 1)})

	got := deriveEnumerations(root, []EnumerationSource{
		{Dir: "internal/check", Type: "IAMCheckClient", Role: AsksVerdicts},
	})

	if len(got.Findings) != 0 {
		t.Fatalf("a method nothing lists from is not itself a defect; got %v", got.Findings)
	}
	if strings.Join(got.Names, ",") != "AllowedIDs" {
		t.Fatalf("the ban must extend by itself, with no list edited; derived %v", got.Names)
	}
	if got.Origin["AllowedIDs"] != "internal/check.IAMCheckClient" {
		t.Fatalf("a derived name must carry the declaration it came from; got %q", got.Origin["AllowedIDs"])
	}
	if len(got.Sources) != 1 || !strings.Contains(got.Sources[0], "NOW ENUMERATES") {
		t.Fatalf("a declaration whose premise changed must be said out loud, not carried "+
			"silently; got %v", got.Sources)
	}
}

// ourModuleDecl is the `module` line of a fixture go.mod that the walk-up must
// recognise as OURS.
//
// It is derived from this package's own import path rather than written out,
// for the same reason moduleRootOf no longer names a module: `pkg/listfiltergate`
// leaves for the foundation module with `pkg/`, and a fixture naming the platform
// would then declare a module that is not ours — every Shared-source test would
// start failing on a tree that is perfectly well formed.
//
// Any module CONTAINING this package is ours by moduleDeclIsOurs, and the package
// path contains itself; so the package path is the one value that is correct in
// both layouts without naming either.
func ourModuleDecl() string {
	return "module " + ownModulePath() + "\n\ngo 1.24\n"
}

// moduleTree materialises the real layout — go.mod on top, services/<svc> beneath
// it, pkg/ beside it — and returns the SERVICE root inside it.
func moduleTree(t *testing.T, files map[string]string, withGoMod bool) string {
	t.Helper()
	root := t.TempDir()
	if withGoMod {
		files["go.mod"] = ourModuleDecl()
	}
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return filepath.Join(root, "services", "fixture")
}

// sharedPort is the shared foundation's narrow port to the authorization service —
// the surface every consumer asks through, and the one that fronts the RPC that
// enumerates.
const sharedPort = `package listnarrow

type AuthorizeClient interface {
	BatchCheck(ctx context.Context, in *Req, opts ...grpc.CallOption) (*Resp, error)
}
`

// TestDeriveEnumerations_SharedSourceIsResolvedFromTheModuleRoot — a source that is
// NOT service code.
//
// Every consumer service reaches kaname through one shared port, so a derivation
// that could only read the service's own tree would leave the shortest path from
// "narrow this page" to "enumerate the universe" watched by nobody: each profile
// would name its own client and none would name the port they all share.
func TestDeriveEnumerations_SharedSourceIsResolvedFromTheModuleRoot(t *testing.T) {
	svcRoot := moduleTree(t, map[string]string{
		"pkg/listnarrow/narrower.go": strings.Replace(sharedPort,
			"type AuthorizeClient interface {",
			"type AuthorizeClient interface {\n\tVisibleObjectIDs(ctx context.Context, subject string) ([]string, error)", 1),
		"services/fixture/internal/check/check_client.go": verdictSurface,
	}, true)

	got := deriveEnumerations(svcRoot, []EnumerationSource{
		{Dir: "internal/check", Type: "IAMCheckClient", Role: AsksVerdicts},
		{Dir: "pkg/listnarrow", Type: "AuthorizeClient", Role: AsksVerdicts, Shared: true},
	})

	if len(got.Findings) != 0 {
		t.Fatalf("both sources resolve here; got %v", got.Findings)
	}
	if strings.Join(got.Names, ",") != "VisibleObjectIDs" {
		t.Fatalf("the shared port's method set must be read; derived %v", got.Names)
	}
	if got.Origin["VisibleObjectIDs"] != "pkg/listnarrow.AuthorizeClient" {
		t.Fatalf("the derived name must name the SHARED declaration; got %q", got.Origin["VisibleObjectIDs"])
	}

	// The mirror: the same declaration, resolved from the SERVICE root instead of
	// the module root, finds nothing — which is what would happen if Shared were
	// ignored, and is why it cannot be ignored silently.
	notShared := deriveEnumerations(svcRoot, []EnumerationSource{
		{Dir: "pkg/listnarrow", Type: "AuthorizeClient", Role: AsksVerdicts},
	})
	if len(notShared.Findings) != 1 || !strings.Contains(notShared.Findings[0], "could not be read") {
		t.Fatalf("a shared path read from the service root must fail loudly, not derive nothing; got %v",
			notShared.Findings)
	}
}

// TestDeriveEnumerations_SharedSourceWithNoModuleRootIsAFinding — "zero derived"
// must be unreachable from "zero read", one level further out than before: here it
// is not the source directory that is missing but the root the source is resolved
// FROM. A gate that answered "nothing to ban" there would report OK while the ban it
// declares had quietly ceased to exist.
func TestDeriveEnumerations_SharedSourceWithNoModuleRootIsAFinding(t *testing.T) {
	svcRoot := moduleTree(t, map[string]string{
		"pkg/listnarrow/narrower.go": sharedPort,
	}, false)

	got := deriveEnumerations(svcRoot, []EnumerationSource{
		{Dir: "pkg/listnarrow", Type: "AuthorizeClient", Role: AsksVerdicts, Shared: true},
	})

	if len(got.Findings) != 1 || !strings.Contains(got.Findings[0], "no go.mod declaring") {
		t.Fatalf("a shared source whose module root cannot be found must be a finding; got %v", got.Findings)
	}
	if len(got.Sources) != 0 {
		t.Fatalf("a source that was never read must not appear in the census as read; got %v", got.Sources)
	}
}

// TestDeriveEnumerations_SharedSourceIsResolvedFromTheOuterModuleNotANestedOne —
// PRO-Robotech/kacho#2211 (census row 2, moduleRootOf). The walk-up stops at the
// FIRST go.mod it meets on the way up and never asks which module it declares.
// services/iam sits inside its OWN go.mod today (module kaname, not kacho) — it
// just declares no Shared source yet, so the gap has no live consequence. This
// fixture makes it live: the service root itself carries a go.mod, and a decoy
// exists at the SAME relative path a Shared source resolves to under it. Left
// unchecked, moduleRootOf would stop at the nested go.mod immediately (zero
// climbs) and the decoy would be read as if it were the shared foundation port —
// not a loud "could not be read" finding (the sibling test above locks that one),
// but a SILENT wrong derivation from someone else's file.
func TestDeriveEnumerations_SharedSourceIsResolvedFromTheOuterModuleNotANestedOne(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	write("go.mod", ourModuleDecl())
	write("pkg/listnarrow/narrower.go", strings.Replace(sharedPort,
		"type AuthorizeClient interface {",
		"type AuthorizeClient interface {\n\tVisibleObjectIDs(ctx context.Context, subject string) ([]string, error)", 1))

	svcRoot := filepath.Join(root, "services", "fixture")
	write("services/fixture/go.mod", "module github.com/PRO-Robotech/kaname\n\ngo 1.24\n")
	write("services/fixture/pkg/listnarrow/narrower.go", `package listnarrow

type AuthorizeClient interface {
	DecoyEnumerate(ctx context.Context) ([]string, error)
}
`)

	got := deriveEnumerations(svcRoot, []EnumerationSource{
		{Dir: "pkg/listnarrow", Type: "AuthorizeClient", Role: AsksVerdicts, Shared: true},
	})

	for _, n := range got.Names {
		if n == "DecoyEnumerate" {
			t.Fatalf("resolved the SERVICE's own nested go.mod instead of the outer shared "+
				"foundation module — derived %v from the decoy under services/fixture/pkg/listnarrow, "+
				"not the real port at the tree root (sources: %v)", got.Names, got.Sources)
		}
	}
	if strings.Join(got.Names, ",") != "VisibleObjectIDs" {
		t.Fatalf("the OUTER shared port's method must be derived; got %v (sources: %v)", got.Names, got.Sources)
	}
}

// catalogListing is a listing answered from a global catalog: no page here is
// narrowed, so the enumerate-then-narrow ban has nothing to apply to.
const catalogListing = `package handler

type RegionHandler struct{}

func (h *RegionHandler) List(ctx context.Context) error {
	rows, _ := h.repo.List(ctx)
	_ = rows
	return nil
}
`

// TestAudit_DeclaredInapplicabilityIsProvedByWhatTheGateJudged — the third answer a
// profile may give, and the only one that is provable rather than asserted.
//
// A service whose every listing is answered without narrowing has no page that could
// be taken from an enumeration. Naming a source there — copying a neighbour's answer
// — would be a declaration with nothing behind it. So the profile states the reason
// and the GATE checks the premise, on every run, against the listings it actually
// judged.
func TestAudit_DeclaredInapplicabilityIsProvedByWhatTheGateJudged(t *testing.T) {
	p := flatProfile
	p.Service = "fixture-catalog"
	p.EnumerationInapplicable = "the placement catalog is read whole by every authenticated caller"
	files := map[string]string{"internal/handler/region_handler.go": catalogListing}

	// Premise holds: nothing narrows, so the declaration stands — and the census
	// says so in words that cannot be confused with "nobody looked".
	out, err := run(t, p, files, map[string]Listing{"region.List": {Shape: ClusterScoped, Reason: "catalog"}})
	if err != nil {
		t.Fatalf("a profile with nothing to narrow must pass; got %v\n%s", err, out)
	}
	if !strings.Contains(out, "no enumeration source, and none can apply: 0 of 1") {
		t.Fatalf("the census must state the count that PROVES the premise; got:\n%s", out)
	}
	if strings.Contains(out, "no enumeration source declared") {
		t.Fatalf("a declared absence must not print as an undeclared one — that is the whole "+
			"difference #684 is about; got:\n%s", out)
	}

	// The mirror, same tree: one listing narrows and the exemption is gone. Without
	// this half, EnumerationInapplicable would be a way for any service to silence
	// the line by writing a sentence.
	out, err = run(t, p, files, map[string]Listing{"region.List": {Shape: RowFilter}})
	if err == nil {
		t.Fatalf("a narrowing listing must take the exemption away; got:\n%s", out)
	}
	if !strings.Contains(out, "The premise is gone") || !strings.Contains(out, "region.List") {
		t.Fatalf("the finding must say the premise is gone AND name the listing that took it; got:\n%s", out)
	}
}

// TestAudit_InapplicabilityAndDeclaredSourcesCannotBothStand — two statements about
// one thing, of which one is wrong. Either the ban has nothing to apply to here, or
// these sources derive it; a profile asserting both leaves the reader to pick.
func TestAudit_InapplicabilityAndDeclaredSourcesCannotBothStand(t *testing.T) {
	p := flatProfile
	p.Service = "fixture-both"
	p.EnumerationInapplicable = "nothing narrows here"
	p.EnumerationSources = []EnumerationSource{{Dir: "internal/check", Type: "IAMCheckClient", Role: AsksVerdicts}}
	files := map[string]string{
		"internal/handler/region_handler.go": catalogListing,
		"internal/check/check_client.go":     verdictSurface,
	}

	out, err := run(t, p, files, map[string]Listing{"region.List": {Shape: ClusterScoped, Reason: "catalog"}})
	if err == nil {
		t.Fatalf("declaring both must be a finding; got:\n%s", out)
	}
	if !strings.Contains(out, "two "+"statements about one thing") {
		t.Fatalf("the finding must say why both cannot stand; got:\n%s", out)
	}
}
