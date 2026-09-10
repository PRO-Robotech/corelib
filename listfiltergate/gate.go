// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package listfiltergate is the analyser behind `make -C services/<svc>
// audit-list-filter` for the services whose listing surface is a set of pages handed
// to a caller: kacho-compute, kacho-nlb, kacho-vpc and kacho-geo.
//
// # What it asserts
//
// Every LISTING method must hand back only the rows its caller may see, and must say
// HOW that is achieved. "Listing method" means every method whose name begins with
// List — not only the collection RPC called exactly `List`.
//
// Each one carries a declared Shape (Profile.Listings), and the gate verifies the
// evidence that shape implies: a per-object filter reached from the call graph
// (RowFilter), a containing object read and acted on before the page (ParentGate), a
// per-RPC authorization with a non-wildcard scope extractor in the proto (EdgeGate),
// a narrowing by the authenticated caller (SubjectScoped), or a stated reason why
// none applies (ClusterScoped). A listing method with no declaration is a finding;
// a declaration with no method is a finding.
//
// The inverse shape — "enumerate every id the subject may see, then narrow the SQL
// to that set" — is refused explicitly. That enumeration is capped server-side with
// no continuation token, so a tenant's own row silently falls outside the prefix
// and becomes invisible for good. The shape is gone from all three services; the
// gate names it so it cannot come back "as an equivalent".
//
// # Why this parses instead of grepping
//
// The gates this replaced recognised a resource by a MUTABLE LABEL: compute by the
// receiver VARIABLE's name (`func (h *…Handler) List(`), nlb by a file being called
// list.go, vpc by a file being called handler.go. Every one of those is changed by
// a refactor no reviewer would stop — rename the receiver, split a package, rename
// a file — and the resource then left the gate's view entirely: whatever its List
// did afterwards went unjudged while the gate printed OK.
//
// Identification here is by what the declaration IS: a method whose name begins with
// `List`, on the transport TYPE (Profile.ReceiverSuffix).
//
// The PREFIX matters as much as the type. This package matched the name `List`
// exactly until 2026-08, so it saw the collection RPC of each resource and nothing
// else: twenty further listing methods across compute, nlb, vpc and storage — child
// collections and operation histories, every one of them a page handed to a caller —
// were outside its view while it printed OK. The census could not reveal the gap
// either, because it counted RESOURCES: "8 resources" read the same whether those 8
// resources held 8 listing methods or 21. Worse, this package's own suite contained
// a case named "not a resource: ListOperations is a different method", asserting
// that an entirely unnarrowed ListOperations must leave the gate SILENT — the blind
// spot was pinned as correct behaviour, which is why it survived review. The correct
// form was already in the tree, in services/registry/tools/auditlistfilter, which
// has matched by prefix from the start.
//
// Two further properties follow from parsing rather than searching text, and both
// were live defects:
//
//   - comments are not code. A comment naming the filter cannot satisfy the check,
//     and a comment explaining why enumeration is banned cannot trip it. Deleting a
//     call and leaving the sentence that described it is the ordinary shape of a
//     deletion, and text search cannot tell the two apart;
//   - the judgement is scoped to what List actually CALLS, not to the file it sits
//     in. A filter-typed struct field, or a filtered sibling method, must not vouch
//     for a List that dropped its own. This is not hypothetical: compute's gate
//     reported OK for a List with its filter removed, because the token it searched
//     for survived in the handler field's type.
//
// # Census
//
// The gate reports what it examined — files parsed, packages, resources, and the
// LISTING METHODS it judged by name — on every path, and treats "nothing examined"
// as a finding. The method count is there because the resource count was not enough
// to notice the gap above.
//
// # Exclusions
//
// A cluster catalog or an admin-only internal surface — per-object grants
// inapplicable by design, as with compute's machine_type and vpc's addresspool — is
// declared ClusterScoped with a Reason.
//
// This replaced --allow=<resource>, which excluded a whole RESOURCE. That is a wider
// exclusion than anyone writing it intends: excluding addresspool as a cluster
// catalog also excluded its ListAddresses, a method the exclusion was never written
// about. Declarations are per method, so an exclusion can no longer take its
// neighbours with it, and it is a finding once its method is gone.
//
// # Relationship to the other gates of this class
//
// kacho-storage, kacho-registry and kaname own bespoke analysers of the same class
// (services/{storage,registry,iam}/tools/auditlistfilter) and are NOT driven from here.
// Storage asserts more than this package does — its repo adapter must narrow by
// project_id and its use-case must reject an empty projectId — because its layout
// puts both within reach of one walk. Registry's List surface is not project-scoped
// at all (grants live on the registry and the repository; tags are not grantable
// objects), so it declares a per-RPC enforcement shape instead. kaname has the
// widest listing surface in the repository (30 methods) and, until 2026-08, no
// analyser of this class at all.
//
// That every service has one is not left to memory: pkg/listfiltergate/
// coverage_test.go derives the service list from the committed tree and reports a
// service nobody analyses as a FINDING. The set used to be written by hand, in a CI
// loop and in whoever remembered to create a directory, and iam and geo were in
// neither list. What is shared across all of them is the discipline, not the code.
package listfiltergate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// Profile describes one service's layout: where the public List declarations live,
// how one resource is told apart from its neighbours, and which call proves that
// the page was narrowed per object.
//
// Every field below names a directory or a DECLARED type/function — never a naming
// habit — so renaming a variable or a file cannot change what the gate sees.
type Profile struct {
	// Service is the service directory name, used in messages.
	Service string

	// AnchorRoot holds the public List declarations, relative to the service root.
	AnchorRoot string

	// PerPackage selects how one resource is delimited:
	//
	//   false — AnchorRoot is ONE package and resources are told apart by the
	//           receiver TYPE (compute: InstanceHandler and MachineTypeHandler in
	//           internal/handler). Resource name = snake_case of the type's stem.
	//   true  — each immediate subdirectory of AnchorRoot is one resource package
	//           whose transport type carries the same name everywhere (vpc and nlb
	//           both call it `Handler`). Resource name = the directory.
	PerPackage bool

	// ReceiverSuffix is the transport type's exact name (when PerPackage) or its
	// suffix (otherwise). It is a declared TYPE: `func (h *X) List` and
	// `func (handler *X) List` are the same declaration to this gate.
	ReceiverSuffix string

	// ExtraReceivers names ADDITIONAL transport types that serve the same resource
	// as ReceiverSuffix, under PerPackage. Exact type names, not suffixes.
	//
	// The premise of PerPackage — "one transport type per package" — stops holding
	// the moment a resource is served on two listeners at once: an administrative
	// surface being published keeps its internal transport while the public one is
	// introduced beside it, and for the length of that window the package declares
	// two. Both hand a page to a caller, so both must be judged; a type this
	// profile does not recognise falls out of the resource set entirely and goes
	// unjudged while the gate prints OK.
	//
	// The entry EXPIRES BY ITSELF: a name here that no List declaration in the tree
	// carries is a finding, so the window cannot outlive its subject and quietly
	// keep a second surface excused. Without that, retiring the internal transport
	// would leave a permanent hole shaped exactly like the one this gate exists to
	// close.
	ExtraReceivers []string

	// Filters are the call names that prove the page was narrowed per object.
	Filters []string

	// Banned are the call names that ask the authorization store to enumerate
	// everything a subject may see.
	//
	// This is a hand-written FLOOR, kept for forms whose declaration is not in the
	// service's own tree (`ListAllowedIDs` is another service's vocabulary and has no
	// declaration in iam at all). It is not the whole ban: a list of names grows one
	// incident at a time, and the incident that grows it is the one that already
	// happened. EnumerationSources is where the ban comes FROM.
	Banned []string

	// EnumerationSources name the DECLARED TYPES through which this service asks the
	// authorization question. The gate reads their method sets and DERIVES which
	// methods enumerate; those names are banned inside every narrowing listing
	// exactly as Banned's are.
	//
	// # Why derived rather than listed
	//
	// `security.md` refuses "enumerate the universe → filter" by its SUBSTANCE, not
	// by the name of the call that does it: the enumeration has a ceiling and no
	// continuation, so rows past the ceiling become invisible to their own owner at
	// live rights. Two names were banned here — the store's `ListObjects` and another
	// service's `ListAllowedIDs` — and a THIRD form existed the whole time, in iam's
	// own database: a paged verdict resolution over the service's own tables. The
	// gate did not see it, because a name it was never told about is a name it cannot
	// refuse. A fourth would have passed the same way (#651).
	//
	// # The property
	//
	// A verdict is asked ABOUT identifiers the caller already holds: it answers
	// `bool`, `[]bool`, `map[string]bool`. An enumeration PRODUCES the identifiers:
	// its first non-error result is a set of them, `[]string`. That difference is the
	// whole of the security rule — "page → check the page" versus "which objects →
	// intersect" — and it is a fact about the DECLARATION, so the gate reads it
	// instead of being told.
	//
	// Adding a method to a named source therefore extends the ban by itself. That is
	// the point: the next form does not wait for an incident to be noticed.
	EnumerationSources []EnumerationSource

	// EnumerationInapplicable states why the enumerate-then-narrow ban has nothing
	// to apply to in this service — for the one case where that is PROVABLE rather
	// than asserted: no listing this profile declares narrows at all.
	//
	// The ban is about how a page is narrowed. A service whose every listing is
	// answered without narrowing (a global catalog, a store query whose enumeration
	// IS the response, a surface that never serves) has no page that could be taken
	// from an enumeration, and naming an enumeration source there would be a
	// declaration with nothing behind it — the copied-from-a-neighbour answer this
	// gate exists to refuse.
	//
	// The gate PROVES the premise: if any listing it judges carries a narrowing
	// shape, this entry is a FINDING. So the exemption expires with its subject —
	// the first listing that narrows takes it away — and it cannot be used to
	// silence a service that does narrow.
	//
	// What it does NOT claim, said so nobody reads more into it: not that the
	// service has no authorization surface at all, only that no page here is
	// narrowed. Declare it together with EnumerationSources and that is a finding
	// too: two declarations of one thing, of which one is wrong.
	EnumerationInapplicable string

	// SubjectScopers are the call names that narrow a page by the AUTHENTICATED
	// caller taken from the context — not by an id the request supplied. The
	// canonical one is operations.ListForCaller, which every service's
	// ListOperations reaches.
	SubjectScopers []string

	// Listings declares, per listing method, how its visibility is decided. The key
	// is "<resource>.<Method>" — the same resource name the analyser derives, and
	// the method exactly as declared (List, ListOperations, ListBySubnet…).
	//
	// It is deliberately explicit and total: a listing method with no entry is a
	// finding, so a new List RPC cannot ship unjudged, and an entry with no method
	// is a finding, so a declaration expires the moment its subject is gone.
	Listings map[string]Listing

	// ProtoFiles are the .proto files carrying this service's RPC options, relative
	// to Options.ProtoRoot. Read only to verify EdgeGate declarations; a profile
	// with no EdgeGate listing does not need them.
	ProtoFiles []string

	// FGAModel is the authorization model, relative to Options.ProtoRoot. Read only
	// to verify EdgeGate declarations that resolve their scope from "*": whether
	// such a scope narrows anything depends entirely on whether a wildcard tuple
	// satisfies the relation, and that is a fact about the model, not a guess.
	FGAModel string
}

// EnumerationSource is one declared type whose method set the gate reads to derive
// the enumerating call names (see Profile.EnumerationSources).
//
// Both fields name something DECLARED — a directory in the tree and a type in it —
// never a naming habit, so the derivation cannot be changed by renaming a variable
// or a file. An entry that resolves to nothing is a FINDING: a source that stopped
// existing takes its whole derived ban with it, silently, and the ban is the thing
// this field exists to produce.
type EnumerationSource struct {
	// Dir holds the declaration, relative to Options.Root — or, when Shared is set,
	// relative to the MODULE root.
	Dir string
	// Type is the declared interface or receiver type name.
	Type string

	// Role says what this source's method set holds TODAY, so that the premise of
	// the derivation is checked instead of assumed. The zero value is the strict
	// one: an author who forgets the field gets the premise that fails closed.
	Role SourceRole

	// Shared marks a declaration that is NOT service code — the shared foundation
	// under pkg/, resolved from the module root instead of Options.Root.
	//
	// Every consumer service asks kaname through the same narrow port, and that
	// port fronts the RPC that enumerates. Leaving it out would mean each consumer
	// watched only the client it declares itself, while the shortest path from
	// "narrow this page" to "enumerate the universe" ran through a package no
	// profile named.
	//
	// What a shared source must NOT be, recorded because it was considered and
	// rejected rather than overlooked: the NARROWER itself. Its narrowing entry
	// point takes the page's identifiers and answers with the visible subset, and
	// its page-predicate lookup answers with the relations of a type — both
	// []string, both correct, neither an enumeration. Naming it would derive a ban on
	// the very call that does the narrowing, and a gate that fires on correct code is
	// a gate somebody switches off. The shape rule reads RESULTS only, so it cannot
	// tell those apart by itself; what keeps it honest is naming the port through
	// which the question is ASKED, not the machinery that asks it.
	Shared bool
}

// SourceRole is which of the two premises a declared source stands on.
//
// The distinction is not decoration: it decides what an EMPTY derivation means, and
// the two kinds mean opposite things by it (see the package comment of
// enumeration.go).
type SourceRole int

const (
	// Enumerates — the source answers "which objects may this subject act on"
	// TODAY. A method set that holds no such shape means the shape rule stopped
	// matching the tree: the ban this source produces went empty in silence, which
	// is worse than a wrong ban because nothing says it happened. That is a finding.
	//
	// This is the zero value deliberately. A forgotten field must land on the
	// premise that can fail, not on the one that cannot.
	Enumerates SourceRole = iota

	// AsksVerdicts — the source answers ABOUT identifiers the caller already holds
	// and enumerates NOTHING today. It is named so that the FIRST method added to it
	// that answers with a set of identifiers is banned inside every narrowing
	// listing the day it is written — the ban arriving before the first caller
	// rather than after the incident that revealed one (#684).
	//
	// An empty derivation here is the declared state, not a finding; the census says
	// so on every run, so "watched, nothing to ban" stays distinguishable from
	// "not watched".
	AsksVerdicts
)

// Shape is how one listing method's visibility is decided.
//
// There are five because there are five genuinely different answers to "what stops
// this caller seeing rows that are not theirs", each with its own evidence. The
// previous edition knew one shape, asserted it on the methods named exactly `List`,
// and was silent about every other listing method in the tree — 20 of them across
// four services. Naming the shapes is what makes the silence impossible: every
// listing method now has to say which one it is.
type Shape int

const (
	// RowFilter — the page is read first, then narrowed per object: the call graph
	// must reach one of Profile.Filters.
	RowFilter Shape = iota

	// ParentGate — the page belongs to ONE object named in the request, and the
	// service reads that object first and acts on the verdict. Evidence: the call
	// graph reaches Listing.Gate, and the gate's result is tested in a branch that
	// leaves the method.
	ParentGate

	// EdgeGate — same containment as ParentGate, but the check is the per-RPC
	// authorization at the edge rather than a read in the service. Evidence: the
	// RPC's proto options carry a required_relation AND a scope_extractor whose
	// from_request_field is Listing.ParentField.
	//
	// A scope resolved from "*" — the cluster singleton — is allowed ONLY when the
	// relation is one no wildcard tuple satisfies. Whether it does is a fact about
	// the authorization model, never a guess: `cluster#viewer` is deliberately
	// declared `[user, user:*, service_account]` so that every authenticated tenant
	// can read the global catalog, and a listing gated on it means "authenticated",
	// not "authorized". `cluster#system_admin` is `[user, service_account]` and
	// narrows properly. The gate reads the model and tells the two apart; a blunt
	// "the field must not be *" rule would have failed ListAdmins, which is correct
	// code, and taught the next reader to suppress the check.
	//
	// Accepting a bare declaration without reading proto and model would make this
	// shape a rubber stamp — the form of a check with no substance, which is the
	// class the whole gate exists to catch.
	EdgeGate

	// SubjectScoped — the query is narrowed by the authenticated caller taken from
	// the context. Evidence: the call graph reaches one of Profile.SubjectScopers.
	SubjectScoped

	// StoreQuery — the response IS the authorization store's answer, not a page
	// narrowed by it. iam's AuthorizeService.ListObjects and .ListSubjects are the
	// RPCs of this shape: asking the store to enumerate is what the caller came for,
	// so the enumeration ban is inapplicable by construction rather than waived. The
	// caller is instead gated on the subject or resource they named, so the evidence
	// is ParentGate's: reach Listing.Gate and act on its verdict.
	//
	// "ListObjects is the only such RPC" stood here until #651. ListSubjects was
	// declared ParentGate and nothing could tell, because the ban held two names and
	// `ListSubjects` was not one of them — a wrong shape costs nothing while the ban
	// it exempts you from does not apply anyway. Deriving the ban from the store's
	// declared method set surfaced it on the first run.
	StoreQuery

	// ClusterScoped — a cluster-wide catalog or an admin-only internal surface with
	// no per-object grants to narrow to. The only shape with no code evidence, so it
	// carries its justification inline (Listing.Reason) and expires with its method.
	//
	// It replaces the --allow=<resource> flag, which excluded a whole RESOURCE —
	// including listing methods nobody had considered, such as addresspool's
	// ListAddresses — and recorded its reason nowhere the gate could read.
	ClusterScoped

	// NeverServes — the method returns no page at all: every path out of it is a
	// refusal. Nothing is narrowed because nothing is served, so demanding a
	// narrowing shape of it would be demanding evidence for an event that cannot
	// happen.
	//
	// Evidence is in the code and is checked, not taken on the word of the
	// declaration: EVERY return statement of the method must hand back a nil
	// response together with a non-nil error. One path that builds a response is
	// enough to make the declaration false, and the gate says so.
	//
	// This shape exists because a declared-but-unimplemented RPC is now written by
	// hand rather than inherited from the generated stub — the refusal has to name
	// its reason and the owner of the capability, and that turns it into a method
	// this gate can see. Before that it was invisible here: an inherited stub
	// declares no receiver, so the analyser had nothing to judge and stayed silent
	// on an RPC the contract advertised.
	//
	// It expires with its method twice over: implement the listing and the shape
	// becomes false (a response is built), retire the RPC and the entry has no
	// subject.
	NeverServes
)

// String names a shape for messages.
func (s Shape) String() string {
	switch s {
	case RowFilter:
		return "RowFilter"
	case ParentGate:
		return "ParentGate"
	case EdgeGate:
		return "EdgeGate"
	case SubjectScoped:
		return "SubjectScoped"
	case StoreQuery:
		return "StoreQuery"
	case ClusterScoped:
		return "ClusterScoped"
	case NeverServes:
		return "NeverServes"
	}
	return "unknown"
}

// Listing is the enforcement declared for one listing method.
type Listing struct {
	Shape Shape

	// Gate is the call that must precede the read, for ParentGate. Written as
	// "<field>.<Method>" (h.get.Execute → "get.Execute") so the handler's parent
	// read is told apart from its page read — both are called Execute, and matching
	// on the bare method name would let the page read vouch for its own gate.
	Gate string

	// ProtoFile names which of Profile.ProtoFiles declares this RPC, for EdgeGate.
	//
	// Required, and the index is keyed per file, because an RPC NAME is not unique
	// across a service's protos: iam declares `rpc List` in nine different service
	// files. A name-keyed index would silently answer about whichever file was read
	// last, and the gate would then be verifying a declaration against another
	// resource's options — a check that runs, passes, and means nothing.
	ProtoFile string

	// ParentField is the request field naming the containing object, for EdgeGate.
	// Must match the proto scope_extractor's from_request_field. The literal "*"
	// declares the cluster singleton, which is only accepted when the RPC's relation
	// is not satisfiable by a wildcard tuple (see EdgeGate).
	ParentField string

	// Reason justifies a ClusterScoped listing. Required for that shape: an
	// exclusion whose reason lives only in a reviewer's memory is indistinguishable
	// from one nobody thought about.
	Reason string
}

// Options configures one audit run.
type Options struct {
	// Root is the service root (the directory holding internal/…).
	Root string
	// ProtoRoot is where Profile.ProtoFiles are resolved from. Needed only when the
	// profile declares an EdgeGate listing; when it does and this is unset or
	// unreadable, that is a finding, not a skip — a gate that cannot reach the
	// evidence has not verified the declaration.
	ProtoRoot string
}

// Report is the census plus the findings of one run.
type Report struct {
	Files     int      // non-test .go files parsed under AnchorRoot
	Packages  int      // packages parsed (1 when Profile.PerPackage is false)
	Resources []string // resources discovered, sorted
	Checked   []string // resources actually judged
	// Listings are the listing methods discovered, "<resource>.<Method>", sorted.
	// This is the number the previous edition could not report: it saw only the
	// methods named exactly `List`, so its census of 8 resources stood for 21
	// listing methods and said nothing about the other 13.
	Listings []string
	// ClusterScoped are the listing methods declared as needing no narrowing.
	ClusterScoped []string
	// Narrowing are the listing methods the enumerate-then-narrow ban applies to.
	// Kept in the census because it is the premise of EnumerationInapplicable: a
	// service claiming the ban cannot apply must be able to show the number is zero,
	// and a reader must be able to see that it was counted rather than assumed.
	Narrowing []string
	// Undeclared are listing methods with no Profile entry — each is also a finding.
	Undeclared []string
	// Unattributed are public List declarations that resolved to no resource —
	// each one is also a finding. Kept in the census so a passing run can state
	// that the number is zero, rather than leaving it unsaid.
	Unattributed []string

	// BannedCalls is the EFFECTIVE ban applied to every narrowing listing: the
	// profile's hand-written floor plus the names derived from its declared
	// enumeration sources, de-duplicated and sorted.
	BannedCalls []string
	// DerivedEnumerations are the names that came from the sources, and
	// EnumerationSources is one census line per source. Both are printed on every
	// path: a run that derived nothing must not read like a run that derived and
	// found nothing to ban, and a profile that declares no source at all must say
	// so out loud rather than look identical to one whose derivation is working.
	DerivedEnumerations []string
	EnumerationSources  []string

	Findings []string // one line per finding; non-empty ⇒ the gate fails
}

// Audit runs the gate against o.Root, writes its census and findings to out, and
// returns an error when the tree does not satisfy the contract. "Nothing examined"
// is an error too — see the package comment.
func Audit(p Profile, o Options, out io.Writer) (Report, error) {
	rep := Report{}
	root := o.Root
	if root == "" {
		root = "."
	}
	anchor := filepath.Join(root, p.AnchorRoot)

	// The ban is assembled BEFORE anything is judged, and its own failures are
	// findings in their own right: a derivation that quietly produced nothing would
	// leave every listing judged against a narrower ban than the profile declares,
	// and the run would still say OK.
	enum := deriveEnumerations(root, p.EnumerationSources)
	if p.EnumerationInapplicable != "" && len(p.EnumerationSources) > 0 {
		rep.Findings = append(rep.Findings, fmt.Sprintf(
			"Profile declares BOTH EnumerationInapplicable and %d EnumerationSource(s) — two "+
				"statements about one thing, of which one is wrong: either the ban has nothing to "+
				"apply to here, or these sources derive it. Keep the one that is true",
			len(p.EnumerationSources)))
	}
	rep.EnumerationSources = enum.Sources
	rep.DerivedEnumerations = enum.Names
	rep.Findings = append(rep.Findings, enum.Findings...)
	banned := mergeSorted(p.Banned, enum.Names)
	rep.BannedCalls = banned

	units, err := loadUnits(p, anchor)
	for _, u := range units {
		rep.Files += len(u.files)
	}
	rep.Packages = len(units)
	if err != nil {
		rep.Findings = append(rep.Findings, err.Error())
		return finish(p, rep, out)
	}

	anchors := map[string][]anchorDecl{}
	seenReceivers := map[string]struct{}{}
	for _, u := range units {
		found, orphans, seen := u.anchors(p)
		for res, decls := range found {
			anchors[res] = append(anchors[res], decls...)
		}
		for typ := range seen {
			seenReceivers[typ] = struct{}{}
		}
		rep.Unattributed = append(rep.Unattributed, orphans...)
	}

	// Самоистечение ExtraReceivers: запись, которой больше нечего называть, — это
	// послабление, пережившее свой предмет. Оно не безобидно: следующий тип с этим
	// именем получит освобождение, которого никто не выдавал. Проверяется здесь, а
	// не при разборе профиля, потому что предикат — факт о ДЕРЕВЕ, а не о профиле.
	for _, extra := range p.ExtraReceivers {
		if _, ok := seenReceivers[extra]; !ok {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"Profile.ExtraReceivers names %q, but no List declaration in %s carries that receiver "+
					"type — the entry has nothing left to admit and must be removed with its subject",
				extra, p.AnchorRoot))
		}
	}
	for res := range anchors {
		rep.Resources = append(rep.Resources, res)
	}
	sort.Strings(rep.Resources)
	sort.Strings(rep.Unattributed)

	// A public List the gate could not attribute is a finding, not a census gap.
	// Reported before the "nothing examined" branch below on purpose: a tree whose
	// EVERY List is unattributed reads as "no resources found", and "the gate does
	// not recognise this layout" is a far more actionable answer than "the anchor
	// root holds no List".
	for _, orphan := range rep.Unattributed {
		rep.Findings = append(rep.Findings, fmt.Sprintf(
			"public List declaration attributed to no resource — its List goes unjudged while the "+
				"gate reports OK: %s\n  identification is by the declared transport type (%s); either "+
				"name the type so it is recognised, or state in the service's Profile that this is the "+
				"transport surface",
			orphan, p.ReceiverSuffix))
	}

	if len(rep.Resources) == 0 {
		rep.Findings = append(rep.Findings, fmt.Sprintf(
			"no public List declaration found under %s (parsed %d file(s) in %d package(s)) — the gate "+
				"examined nothing, so it proved nothing; zero findings must not be reachable from zero reads",
			anchor, rep.Files, rep.Packages))
		return finish(p, rep, out)
	}

	// Every discovered listing method is named "<resource>.<Method>". This — not the
	// resource — is the unit the gate judges and reports.
	for _, res := range rep.Resources {
		for _, a := range anchors[res] {
			rep.Listings = append(rep.Listings, res+"."+a.method)
		}
	}
	sort.Strings(rep.Listings)

	// A declaration lives only while it has a subject. Checked before anything is
	// judged so that a profile pointed at a tree whose methods moved says exactly
	// that, instead of reporting a page of "undeclared" findings.
	declaredKeys := make([]string, 0, len(p.Listings))
	for k := range p.Listings {
		declaredKeys = append(declaredKeys, k)
	}
	sort.Strings(declaredKeys)
	for _, k := range declaredKeys {
		if !contains(rep.Listings, k) {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"declared listing %q matches no method under %s — the declaration has nothing left to "+
					"describe; drop it, or the next method of that name inherits an enforcement claim "+
					"nobody checked", k, anchor))
		}
	}

	var protos *protoOptions
	if profileNeedsProto(p) {
		var perr error
		protos, perr = loadProtoOptions(o.ProtoRoot, p.ProtoFiles)
		if perr != nil {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"this profile declares EdgeGate listings, whose evidence lives in the proto options, "+
					"and those could not be read: %v\n  an EdgeGate declaration the gate cannot verify is "+
					"a rubber stamp — it has the form of a check and no substance", perr))
		} else if wc, werr := loadWildcardRelations(o.ProtoRoot, p.FGAModel); werr != nil {
			rep.Findings = append(rep.Findings, fmt.Sprintf(
				"this profile declares EdgeGate listings and the authorization model could not be "+
					"read: %v\n  whether a scope resolved from \"*\" narrows anything is a fact about "+
					"the model; without it the gate would be guessing", werr))
			protos = nil
		} else {
			protos.wildcard = wc
		}
	}

	for _, res := range rep.Resources {
		rep.Checked = append(rep.Checked, res)
		for _, a := range anchors[res] {
			key := res + "." + a.method
			l, ok := p.Listings[key]
			if !ok {
				// `List` alone has a shape fixed by convention: it is the resource's
				// project-scoped collection RPC in every service (api-conventions.md,
				// "Get/List sync"), so its default is the STRICTEST shape. A default
				// that only ever demands more can add findings, never hide them — and
				// a List that is genuinely a cluster catalog says so explicitly, as
				// machine_type and addresspool do.
				//
				// Every OTHER listing method is a child or derived listing whose shape
				// really does vary — parent-gated, edge-gated, subject-scoped — so
				// there is no defensible default and the gate fails closed until the
				// service states one. This is what stops the next ListSomething from
				// shipping unjudged, which is precisely how the previous twenty got in.
				if a.method != "List" {
					rep.Undeclared = append(rep.Undeclared, key)
					rep.Findings = append(rep.Findings, fmt.Sprintf(
						"%s — a listing method with no declared enforcement: nothing states how this page "+
							"is narrowed, so nothing checks that it is\n  declared: %s\n  add an entry to "+
							"the service's Profile.Listings naming the shape (RowFilter / ParentGate / "+
							"EdgeGate / SubjectScoped / ClusterScoped) — until it is stated, the gate "+
							"fails closed", key, a.pos))
					continue
				}
				l = Listing{Shape: RowFilter}
			}
			if l.Shape == ClusterScoped {
				rep.ClusterScoped = append(rep.ClusterScoped, key)
			}
			// The same predicate checkListing applies the ban under, counted here so
			// EnumerationInapplicable is judged against what was actually seen rather
			// than against what the profile says about itself.
			if banApplies(l.Shape) {
				rep.Narrowing = append(rep.Narrowing, key)
			}
			rep.Findings = append(rep.Findings, checkListing(p, banned, enum.Origin, key, l, a, protos)...)
		}
	}

	sort.Strings(rep.Narrowing)
	// The premise of EnumerationInapplicable, proved rather than taken: the ban is
	// inapplicable only while nothing here narrows. The first listing that does takes
	// the exemption away, so it cannot outlive its subject — and it cannot be reached
	// for by a service that simply has not declared a source.
	if p.EnumerationInapplicable != "" && len(rep.Narrowing) > 0 {
		rep.Findings = append(rep.Findings, fmt.Sprintf(
			"Profile.EnumerationInapplicable says the enumerate-then-narrow ban has nothing to "+
				"apply to here, but %d listing method(s) narrow: %s. The premise is gone — either "+
				"declare the authorization surfaces the ban is derived from (EnumerationSources), "+
				"or state why these pages are not narrowed after all",
			len(rep.Narrowing), strings.Join(rep.Narrowing, ", ")))
	}

	return finish(p, rep, out)
}

// profileNeedsProto reports whether any declaration in p needs the proto options.
func profileNeedsProto(p Profile) bool {
	for _, l := range p.Listings {
		if l.Shape == EdgeGate {
			return true
		}
	}
	return false
}

// anchorDecl is one public listing declaration together with the package it can reach.
type anchorDecl struct {
	pos    string // "<file>:<line>:<col>" of the declaration
	method string // the method name exactly as declared (List, ListOperations, …)
	fn     *ast.FuncDecl
	unit   *unit
}

// banApplies reports whether the enumerate-then-narrow ban has anything to say
// about a listing of this shape.
//
// It is one function rather than the same condition written twice because the
// census of narrowing listings is the PREMISE of Profile.EnumerationInapplicable:
// were the two to drift, a service could be excused by a count taken under one rule
// while its listings were judged under another.
func banApplies(sh Shape) bool {
	return sh != ClusterScoped && sh != StoreQuery && sh != NeverServes
}

// checkListing judges one listing declaration against the shape it declares.
//
// banned is the EFFECTIVE ban — the profile's floor plus what was derived from its
// declared enumeration sources — and origin says which source produced a derived
// name, so a finding can point at the declaration that made the call a ban.
func checkListing(
	p Profile, banned []string, origin map[string]string,
	key string, l Listing, a anchorDecl, protos *protoOptions,
) []string {
	called := a.unit.reachableCalls(a.fn)

	var findings []string

	// The enumerate-then-narrow ban applies to every shape that narrows at all: it
	// is about HOW a page is narrowed, not about which shape does it.
	if banApplies(l.Shape) {
		for _, b := range banned {
			if !called[b] {
				continue
			}
			// The ceiling is written in a different place for each form, and naming
			// the right one matters: the store caps its own answer, iam's own tables
			// cap theirs by page size. Both lose the same thing — the rows past the
			// ceiling, for their own owner, at live rights.
			where := "the authorization store caps this answer server-side with no continuation token"
			if src, ok := origin[b]; ok {
				where = fmt.Sprintf(
					"derived from %s: its answer is a SET OF IDENTIFIERS, so the page is being taken "+
						"from the enumeration rather than judged after it is read", src)
			}
			findings = append(findings, fmt.Sprintf(
				"%s — reaches %s: %s, so the caller's own rows fall outside the answer and disappear "+
					"while its rights are live; read the page from this service's own tables and put "+
					"THAT page to the model, one batched question per page\n  declared: %s",
				key, b, where, a.pos))
		}
	}

	switch l.Shape {
	case RowFilter:
		if !anyOf(called, p.Filters) {
			findings = append(findings, fmt.Sprintf(
				"%s — declared RowFilter, but hands back its page without a per-object visibility "+
					"filter: nothing along the calls it makes reaches %s (kaname BatchCheck; предикат "+
					"страницы объявляет сам сервис — PageRelations)\n  declared: %s",
				key, orList(p.Filters), a.pos))
		}

	case ParentGate, StoreQuery:
		if l.Gate == "" {
			findings = append(findings, fmt.Sprintf(
				"%s — declared ParentGate with no Gate call named, so the declaration asserts nothing"+
					"\n  declared: %s", key, a.pos))
			break
		}
		if !called[l.Gate] {
			findings = append(findings, fmt.Sprintf(
				"%s — declared ParentGate on %s, but nothing along the calls it makes reaches it: the "+
					"page of a containing object is returned without reading that object first"+
					"\n  declared: %s", key, l.Gate, a.pos))
			break
		}
		if !a.unit.gateActedOn(a.fn, l.Gate) {
			findings = append(findings, fmt.Sprintf(
				"%s — calls %s but never acts on its verdict: a denial or a miss on the containing "+
					"object does not stop the page being returned\n  declared: %s",
				key, l.Gate, a.pos))
		}

	case EdgeGate:
		findings = append(findings, checkEdgeGate(key, l, a, protos)...)

	case SubjectScoped:
		if !anyOf(called, p.SubjectScopers) {
			findings = append(findings, fmt.Sprintf(
				"%s — declared SubjectScoped, but nothing along the calls it makes reaches %s: the "+
					"query is not narrowed by the authenticated caller\n  declared: %s",
				key, orList(p.SubjectScopers), a.pos))
		}

	case NeverServes:
		if served, where := a.unit.returnsAResponse(a.fn); served {
			findings = append(findings, fmt.Sprintf(
				"%s — declared NeverServes, but a path out of it builds a response (%s): the "+
					"declaration says no page is ever served, so that page is served without any "+
					"narrowing at all\n  declared: %s", key, where, a.pos))
		}

	case ClusterScoped:
		if strings.TrimSpace(l.Reason) == "" {
			findings = append(findings, fmt.Sprintf(
				"%s — declared ClusterScoped with no Reason: this is the one shape with no code "+
					"evidence, so an unstated reason makes it indistinguishable from a listing nobody "+
					"thought about\n  declared: %s", key, a.pos))
		}
	}
	return findings
}

// checkEdgeGate verifies that the per-RPC authorization the declaration points at
// actually exists in the proto and actually narrows.
func checkEdgeGate(key string, l Listing, a anchorDecl, protos *protoOptions) []string {
	if l.ParentField == "" {
		return []string{fmt.Sprintf(
			"%s — declared EdgeGate with no ParentField named, so there is nothing to verify against "+
				"the proto\n  declared: %s", key, a.pos)}
	}
	if protos == nil {
		return []string{fmt.Sprintf(
			"%s — declared EdgeGate, but the proto options were not read, so the declaration was "+
				"taken on trust\n  declared: %s", key, a.pos)}
	}
	if l.ProtoFile == "" {
		return []string{fmt.Sprintf(
			"%s — declared EdgeGate with no ProtoFile named: an rpc name is not unique across a "+
				"service's protos, so there is no way to know which declaration to verify against"+
				"\n  declared: %s", key, a.pos)}
	}
	opt, ok := protos.byMethod[l.ProtoFile+"#"+a.method]
	if !ok {
		return []string{fmt.Sprintf(
			"%s — declared EdgeGate, but %s declares no rpc %s carrying authorization options: the "+
				"check it delegates to does not exist\n  declared: %s",
			key, l.ProtoFile, a.method, a.pos)}
	}
	var out []string
	if opt.requiredRelation == "" {
		out = append(out, fmt.Sprintf(
			"%s — declared EdgeGate, but rpc %s carries no required_relation: there is no per-RPC "+
				"authorization for the page to be delegated to\n  declared: %s", key, a.method, a.pos))
	}
	switch {
	case opt.fromRequestField == "":
		out = append(out, fmt.Sprintf(
			"%s — declared EdgeGate on field %q, but rpc %s carries no scope_extractor: nothing "+
				"resolves the containing object, so the relation is checked against nothing"+
				"\n  declared: %s", key, l.ParentField, a.method, a.pos))
	case opt.fromRequestField == "*":
		// A scope on the singleton narrows exactly as much as its relation does.
		if protos.wildcard[opt.objectType+"#"+opt.requiredRelation] {
			out = append(out, fmt.Sprintf(
				"%s — declared EdgeGate, but rpc %s resolves its scope from \"*\" and is gated on "+
					"%s#%s, which the model opens to a wildcard subject: the relation is satisfied by "+
					"every authenticated caller, so this check means \"authenticated\", not "+
					"\"authorized\", and narrows NOTHING\n  declared: %s",
				key, a.method, opt.objectType, opt.requiredRelation, a.pos))
		} else if l.ParentField != "*" {
			out = append(out, fmt.Sprintf(
				"%s — declared EdgeGate on field %q, but rpc %s resolves its scope from \"*\" (the "+
					"%s singleton): the declaration describes a check that is not the one being made"+
					"\n  declared: %s", key, l.ParentField, a.method, opt.objectType, a.pos))
		}
	case opt.fromRequestField != l.ParentField:
		out = append(out, fmt.Sprintf(
			"%s — declared EdgeGate on field %q, but rpc %s resolves its scope from %q: the "+
				"declaration describes a check that is not the one being made\n  declared: %s",
			key, l.ParentField, a.method, opt.fromRequestField, a.pos))
	}
	return out
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// unit is one parsed package, indexed for call resolution.
type unit struct {
	dir   string
	name  string // resource name when Profile.PerPackage (the directory), else ""
	fset  *token.FileSet
	files []*ast.File

	funcs   map[string]*ast.FuncDecl     // package-level funcs by name
	methods map[string]*ast.FuncDecl     // "Type.Method"
	fields  map[string]map[string]string // struct Type → field → package-local type
	recv    map[*ast.FuncDecl][2]string  // fn → {receiver var, receiver type}
}

// loadUnits parses the anchor tree. An absent or unreadable root is an error, never
// an empty result: a gate that could not open the tree has proven nothing.
func loadUnits(p Profile, anchor string) ([]*unit, error) {
	info, err := os.Stat(anchor)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf(
			"anchor root %s is absent — the gate examined nothing, so it proved nothing "+
				"(a gate pointed at the wrong tree must not be indistinguishable from a clean one)", anchor)
	}
	if !p.PerPackage {
		u, uerr := loadUnit(anchor, "")
		if uerr != nil {
			return nil, uerr
		}
		return []*unit{u}, nil
	}
	entries, rerr := os.ReadDir(anchor)
	if rerr != nil {
		return nil, fmt.Errorf("read %s: %w", anchor, rerr)
	}
	var out []*unit
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		u, uerr := loadUnit(filepath.Join(anchor, e.Name()), e.Name())
		if uerr != nil {
			return out, uerr
		}
		out = append(out, u)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf(
			"anchor root %s holds no package directory — the gate examined nothing, so it proved nothing", anchor)
	}
	return out, nil
}

// loadUnit parses every non-test .go file directly inside dir and indexes it.
//
// Comments are NOT retained (parser mode 0): the gate must judge code, never prose
// about code.
func loadUnit(dir, name string) (*unit, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	u := &unit{
		dir:     dir,
		name:    name,
		fset:    token.NewFileSet(),
		funcs:   map[string]*ast.FuncDecl{},
		methods: map[string]*ast.FuncDecl{},
		fields:  map[string]map[string]string{},
		recv:    map[*ast.FuncDecl][2]string{},
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, perr := parser.ParseFile(u.fset, path, nil, 0)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, perr)
		}
		u.files = append(u.files, f)
	}
	u.index()
	return u, nil
}

// index builds the tables call resolution needs: package funcs, methods keyed by
// "Type.Method", and struct field → package-local type.
func (u *unit) index() {
	for _, f := range u.files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil {
					continue
				}
				if d.Recv == nil {
					u.funcs[d.Name.Name] = d
					continue
				}
				v, typ := receiverOf(d)
				if typ == "" {
					continue
				}
				u.recv[d] = [2]string{v, typ}
				u.methods[typ+"."+d.Name.Name] = d
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						continue
					}
					m := map[string]string{}
					for _, fld := range st.Fields.List {
						t := bareTypeName(fld.Type)
						if t == "" {
							continue
						}
						for _, n := range fld.Names {
							m[n.Name] = t
						}
					}
					u.fields[ts.Name.Name] = m
				}
			}
		}
	}
}

// anchors returns this unit's public List declarations, keyed by resource, plus
// the ones it could NOT attribute to any resource.
//
// The second return value is not diagnostics — it is a finding. Attribution is by
// the declared transport type, so a `List` on a type this profile does not
// recognise falls out of the resource set entirely and goes unjudged. That is the
// same hole the predecessors had (they keyed on a mutable label and lost a resource
// to a rename), only one level down: here the census showed it — "more packages
// than resources" — and nothing acted on the gap.
func (u *unit) anchors(p Profile) (map[string][]anchorDecl, []string, map[string]struct{}) {
	out := map[string][]anchorDecl{}
	var unattributed []string
	// Типы получателей, фактически встреченные у объявлений `List*`. Нужны для
	// самоистечения ExtraReceivers: запись, которой больше нечего называть, —
	// находка, а не безобидный остаток.
	seen := map[string]struct{}{}
	for _, f := range u.files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			// Identification is by PREFIX, matching the form already proven in
			// services/registry/tools/auditlistfilter.
			//
			// The exact-name predicate that stood here was the gate's own blind spot,
			// and it was a large one: `List` is only the collection RPC. Everything
			// that lists a CHILD collection — ListOperations, ListSubnets,
			// ListSecurityGroups, ListRouteTables, ListUsedAddresses, ListBySubnet,
			// ListAddresses, ListAttachments — is equally a page handed to a caller,
			// and every one of them was outside the gate's view while it printed OK.
			// Twenty such methods across four services; the census said "8 resources"
			// and nothing said that those 8 resources held 21 listing methods.
			if !ok || !strings.HasPrefix(fn.Name.Name, "List") || fn.Body == nil || fn.Recv == nil {
				continue
			}
			_, typ := receiverOf(fn)
			seen[typ] = struct{}{}
			res, ok := p.resourceOf(u, typ)
			if !ok {
				unattributed = append(unattributed, fmt.Sprintf("%s (receiver type %s)",
					u.fset.Position(fn.Pos()).String(), quoteType(typ)))
				continue
			}
			out[res] = append(out[res], anchorDecl{
				pos:    u.fset.Position(fn.Pos()).String(),
				method: fn.Name.Name,
				fn:     fn,
				unit:   u,
			})
		}
	}
	return out, unattributed, seen
}

// quoteType names a receiver type for a message, including the case where the
// receiver is not a plain package-local named type at all.
func quoteType(typ string) string {
	if typ == "" {
		return "<not a package-local named type>"
	}
	return typ
}

// resourceOf maps a receiver TYPE to the resource it serves, or reports that the
// declaration is not a public List transport at all.
func (p Profile) resourceOf(u *unit, typ string) (string, bool) {
	if typ == "" {
		return "", false
	}
	if p.PerPackage {
		// One transport type per package — plus any named in ExtraReceivers, which
		// serve the SAME resource on another listener. Either way the package IS the
		// resource.
		if typ != p.ReceiverSuffix && !slices.Contains(p.ExtraReceivers, typ) {
			return "", false
		}
		return u.name, true
	}
	if typ == p.ReceiverSuffix || !strings.HasSuffix(typ, p.ReceiverSuffix) {
		return "", false
	}
	return toSnake(strings.TrimSuffix(typ, p.ReceiverSuffix)), true
}

// ---------------------------------------------------------------------------
// Call-graph reachability
// ---------------------------------------------------------------------------

// reachableCalls returns every call NAME reachable from fn without leaving fn's
// package. Resolution is syntactic and deliberately narrow:
//
//   - `f(…)`                → the package-level func f, if there is one;
//   - `<recv>.M(…)`         → method M on the receiver's own type;
//   - `<recv>.<field>.M(…)` → method M on the package-local type of that field
//     (the handler → use-case hop of vpc and nlb);
//   - anything else         → the NAME is recorded but not followed.
//
// Recording the name of an unfollowed call is what makes `authzfilter.
// FilterVisiblePage(…)` count: it leads into another package, so there is nothing
// local to walk into, but that List reaches it is exactly the point.
//
// The walk is scoped to what List CALLS rather than to the file it sits in, so a
// filtered sibling method — or a filter-typed struct field — cannot vouch for a
// List that dropped its own filter.
func (u *unit) reachableCalls(fn *ast.FuncDecl) map[string]bool {
	names := map[string]bool{}
	seen := map[*ast.FuncDecl]bool{}
	u.walk(fn, names, seen)
	return names
}

func (u *unit) walk(fn *ast.FuncDecl, names map[string]bool, seen map[*ast.FuncDecl]bool) {
	if fn == nil || fn.Body == nil || seen[fn] {
		return
	}
	seen[fn] = true
	recvVar, recvType := u.recv[fn][0], u.recv[fn][1]

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := unwrapIndex(call.Fun).(type) {
		case *ast.Ident:
			names[f.Name] = true
			u.walk(u.funcs[f.Name], names, seen)
		case *ast.SelectorExpr:
			names[f.Sel.Name] = true
			switch x := f.X.(type) {
			case *ast.Ident:
				if recvVar != "" && x.Name == recvVar {
					u.walk(u.methods[recvType+"."+f.Sel.Name], names, seen)
					break
				}
				// Не приёмник — значит квалификатор пакета либо локальная
				// переменная. Записываем и КВАЛИФИЦИРОВАННОЕ имя
				// («listnarrow.Page»), потому что голый хвост селектора слишком
				// широк: профиль, назвавший сужателем `Page`, был бы удовлетворён
				// любым чужим методом с этим именем — то есть проверял бы
				// написание, а не предмет.
				names[x.Name+"."+f.Sel.Name] = true
			case *ast.SelectorExpr:
				inner, ok := x.X.(*ast.Ident)
				if !ok || recvVar == "" || inner.Name != recvVar {
					return true
				}
				// Also record the call QUALIFIED by the field it went through
				// ("get.Execute", "svc.Get"). Without this, a handler's parent read
				// and its page read are the same token — both are `Execute` — and a
				// ParentGate declaration could be satisfied by the very call it is
				// supposed to precede.
				names[x.Sel.Name+"."+f.Sel.Name] = true
				if ft := u.fields[recvType][x.Sel.Name]; ft != "" {
					u.walk(u.methods[ft+"."+f.Sel.Name], names, seen)
				}
			}
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// AST helpers
// ---------------------------------------------------------------------------

// receiverOf returns the receiver's variable name and bare type name. The variable
// name is returned only so `<recv>.field.M(…)` can be resolved; it is NEVER part of
// how a declaration is identified.
func receiverOf(fn *ast.FuncDecl) (varName, typeName string) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", ""
	}
	fld := fn.Recv.List[0]
	if len(fld.Names) == 1 {
		varName = fld.Names[0].Name
	}
	return varName, bareTypeName(fld.Type)
}

// bareTypeName strips pointers, generic instantiation and parentheses, returning ""
// for anything that is not a plain package-local named type (qualified types, maps,
// funcs, inline interfaces).
func bareTypeName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.SelectorExpr:
			return "" // a type from another package: not resolvable here
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// ---------------------------------------------------------------------------
// ParentGate: the verdict has to stop the response
// ---------------------------------------------------------------------------

// gateActedOn reports whether the gate's verdict is acted on in the declaring
// method OR in any package-local function it reaches.
//
// The hop matters: vpc and compute call the gate straight from the handler, but iam
// puts it in the use-case the handler delegates to. Judging only the declaring body
// would have made every iam ParentGate a false finding, and the obvious repair — drop
// the verdict check — would have thrown away the half of the shape that has teeth.
func (u *unit) gateActedOn(fn *ast.FuncDecl, gate string) bool {
	seen := map[*ast.FuncDecl]bool{}
	var walk func(*ast.FuncDecl) bool
	walk = func(f *ast.FuncDecl) bool {
		if f == nil || f.Body == nil || seen[f] {
			return false
		}
		seen[f] = true
		if gateVerdictActedOn(f, gate) {
			return true
		}
		recvVar, recvType := u.recv[f][0], u.recv[f][1]
		found := false
		ast.Inspect(f.Body, func(n ast.Node) bool {
			if found {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch e := unwrapIndex(call.Fun).(type) {
			case *ast.Ident:
				if walk(u.funcs[e.Name]) {
					found = true
				}
			case *ast.SelectorExpr:
				switch x := e.X.(type) {
				case *ast.Ident:
					if recvVar != "" && x.Name == recvVar && walk(u.methods[recvType+"."+e.Sel.Name]) {
						found = true
					}
				case *ast.SelectorExpr:
					inner, ok := x.X.(*ast.Ident)
					if !ok || recvVar == "" || inner.Name != recvVar {
						return true
					}
					if ft := u.fields[recvType][x.Sel.Name]; ft != "" &&
						walk(u.methods[ft+"."+e.Sel.Name]) {
						found = true
					}
				}
			}
			return true
		})
		return found
	}
	return walk(fn)
}

// gateVerdictActedOn reports whether the gate call's result is tested in a branch
// that leaves the method.
//
// Reaching the gate is not enough on its own: calling it and dropping the error
// leaves the page exactly as unprotected as never calling it, and reads, in a diff,
// like a check. The predicate is deliberately narrow — the gate call must sit in an
// `if` that can return, or be assigned to a name that a later `if` tests and returns
// on — because a broader one ("some error is tested somewhere in the body") is
// satisfied by almost every Go method ever written, `err` being what it is. That
// broader form was a real defect in the sibling analyser, fixed there for the same
// reason.
func gateVerdictActedOn(fn *ast.FuncDecl, gate string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		// Pattern 1: `if _, err := <gate>(…); err != nil { … return … }`.
		if ifs, ok := n.(*ast.IfStmt); ok && ifs.Init != nil {
			if as, ok := ifs.Init.(*ast.AssignStmt); ok && assignCallsGate(as, gate) && blockReturns(ifs.Body) {
				found = true
				return false
			}
		}
		// Pattern 2: `x, err := <gate>(…)` followed by `if err != nil { return … }`.
		if blk, ok := n.(*ast.BlockStmt); ok && blockGatesThenReturns(blk, gate) {
			found = true
			return false
		}
		// Pattern 3: `if !<gate>(…) { … return … }` — a predicate gate whose answer
		// is the condition itself. iam writes its self-check this way.
		if ifs, ok := n.(*ast.IfStmt); ok && blockReturns(ifs.Body) && condCallsGate(ifs.Cond, gate) {
			found = true
			return false
		}
		return true
	})
	return found
}

// condCallsGate reports whether the gate is called anywhere in an if-condition.
func condCallsGate(cond ast.Expr, gate string) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && qualifiedCallName(call) == gate {
			found = true
		}
		return !found
	})
	return found
}

// assignCallsGate reports whether the assignment's right-hand side is the gate call.
func assignCallsGate(as *ast.AssignStmt, gate string) bool {
	if len(as.Rhs) != 1 {
		return false
	}
	call, ok := unwrapIndex(as.Rhs[0]).(*ast.CallExpr)
	if !ok {
		return false
	}
	return qualifiedCallName(call) == gate
}

// blockGatesThenReturns handles the two-statement form: assign the gate's result to
// a name, then test that name in a following `if` that returns.
func blockGatesThenReturns(blk *ast.BlockStmt, gate string) bool {
	for i, st := range blk.List {
		as, ok := st.(*ast.AssignStmt)
		if !ok || !assignCallsGate(as, gate) {
			continue
		}
		names := map[string]bool{}
		for _, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
				names[id.Name] = true
			}
		}
		for _, later := range blk.List[i+1:] {
			ifs, ok := later.(*ast.IfStmt)
			if !ok || !blockReturns(ifs.Body) {
				continue
			}
			referenced := false
			ast.Inspect(ifs.Cond, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && names[id.Name] {
					referenced = true
				}
				return true
			})
			if referenced {
				return true
			}
		}
	}
	return false
}

// blockReturns reports whether the block can leave the method.
func blockReturns(blk *ast.BlockStmt) bool {
	found := false
	ast.Inspect(blk, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

// qualifiedCallName renders a call as the gate writes it: "field.Method" for
// `h.field.Method(…)`, "Method" for `h.Method(…)` and plain `Method(…)`.
func qualifiedCallName(call *ast.CallExpr) string {
	sel, ok := unwrapIndex(call.Fun).(*ast.SelectorExpr)
	if !ok {
		if id, ok := unwrapIndex(call.Fun).(*ast.Ident); ok {
			return id.Name
		}
		return ""
	}
	if inner, ok := sel.X.(*ast.SelectorExpr); ok {
		return inner.Sel.Name + "." + sel.Sel.Name
	}
	return sel.Sel.Name
}

// ---------------------------------------------------------------------------
// EdgeGate: the per-RPC authorization the declaration delegates to
// ---------------------------------------------------------------------------

// rpcOptions is the slice of an RPC's proto options this gate needs.
type rpcOptions struct {
	requiredRelation string
	objectType       string
	fromRequestField string
}

// protoOptions indexes rpcOptions by RPC name, together with the set of
// "<type>#<relation>" pairs a wildcard tuple satisfies.
type protoOptions struct {
	byMethod map[string]rpcOptions
	wildcard map[string]bool
	files    int
}

// loadWildcardRelations returns the "<type>#<relation>" pairs whose direct-assignable
// list admits a wildcard subject (`user:*`), read from the authorization model.
//
// These are the relations that answer "yes" to every authenticated subject. There are
// meant to be very few — the global reference catalog every tenant must read, and a
// public repository's read verb — and each is a deliberate decision recorded in the
// model. Deriving the set instead of hard-coding it is the point: the model is where
// the decision lives, so a relation opened to a wildcard tomorrow is caught the same
// day, and one closed again stops being flagged without anyone editing this gate.
func loadWildcardRelations(root, rel string) (map[string]bool, error) {
	if rel == "" {
		return nil, fmt.Errorf("the profile names no FGAModel")
	}
	path := filepath.Join(root, rel)
	raw, err := os.ReadFile(path) // #nosec G304 -- path is joined from --*-root and a relative name listed in the gate's own profile; this is a build-time tool, no request data reaches it
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	out := map[string]bool{}
	typ := ""
	types := 0
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // a comment mentioning user:* is not a grant of it
		}
		if m := fgaTypeRe.FindStringSubmatch(trimmed); m != nil {
			typ = m[1]
			types++
			continue
		}
		m := fgaDefineRe.FindStringSubmatch(trimmed)
		if m == nil || typ == "" {
			continue
		}
		// Only the DIRECT-assignable list — the bracketed part — can carry a
		// wildcard subject; `or other_relation` merely delegates.
		direct := m[2]
		if i := strings.Index(direct, "["); i >= 0 {
			if j := strings.Index(direct[i:], "]"); j > 0 {
				direct = direct[i : i+j]
			}
		} else {
			continue
		}
		if strings.Contains(direct, ":*") {
			out[typ+"#"+m[1]] = true
		}
	}
	if types == 0 {
		return nil, fmt.Errorf("parsed %s and found no type declaration — the model was not read, "+
			"so no EdgeGate scope could be judged against it", path)
	}
	return out, nil
}

var (
	rpcHeadRe      = regexp.MustCompile(`(?m)^\s*rpc\s+([A-Za-z0-9_]+)\s*\(`)
	relationRe     = regexp.MustCompile(`required_relation\)?\s*=\s*"([^"]*)"`)
	objectTypeRe   = regexp.MustCompile(`object_type:\s*"([^"]*)"`)
	fromReqFieldRe = regexp.MustCompile(`from_request_field:\s*"([^"]*)"`)
	fgaTypeRe      = regexp.MustCompile(`^type\s+([a-z_][a-z0-9_]*)\s*$`)
	fgaDefineRe    = regexp.MustCompile(`^define\s+([a-z_][a-z0-9_]*)\s*:\s*(.*)$`)
)

// loadProtoOptions reads the authorization options of every rpc declared in files.
//
// The proto is the authority here rather than either generated copy: the gateway
// middleware's catalog and the iam seed are both produced FROM it, and are held
// byte-identical to it by a separate drift gate. Reading a generated copy would make
// this gate agree with whichever copy it happened to read.
//
// An unreadable or empty proto set is an error, never an empty index: an EdgeGate
// declaration verified against nothing is the rubber stamp this shape exists to
// avoid.
func loadProtoOptions(root string, files []string) (*protoOptions, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("the profile names no ProtoFiles")
	}
	if root == "" {
		return nil, fmt.Errorf("no proto root was given (--proto-root)")
	}
	po := &protoOptions{byMethod: map[string]rpcOptions{}}
	for _, rel := range files {
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path) // #nosec G304 -- path is joined from --*-root and a relative name listed in the gate's own profile; this is a build-time tool, no request data reaches it
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		po.files++
		text := string(raw)
		heads := rpcHeadRe.FindAllStringSubmatchIndex(text, -1)
		for i, h := range heads {
			name := text[h[2]:h[3]]
			end := len(text)
			if i+1 < len(heads) {
				end = heads[i+1][0]
			}
			body := text[h[1]:end]
			opt := rpcOptions{}
			if m := relationRe.FindStringSubmatch(body); m != nil {
				opt.requiredRelation = m[1]
			}
			if m := objectTypeRe.FindStringSubmatch(body); m != nil {
				opt.objectType = m[1]
			}
			if m := fromReqFieldRe.FindStringSubmatch(body); m != nil {
				opt.fromRequestField = m[1]
			}
			po.byMethod[rel+"#"+name] = opt
		}
	}
	if len(po.byMethod) == 0 {
		return nil, fmt.Errorf("parsed %d proto file(s) and found no rpc declaration — the "+
			"evidence for every EdgeGate declaration was absent, so none of them was verified", po.files)
	}
	return po, nil
}

// unwrapIndex strips generic instantiation from a call's function expression, so
// `filterVisible[T](…)` and `filterVisible(…)` resolve alike.
func unwrapIndex(e ast.Expr) ast.Expr {
	for {
		switch t := e.(type) {
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		default:
			return e
		}
	}
}

func anyOf(names map[string]bool, want []string) bool {
	for _, w := range want {
		if names[w] {
			return true
		}
	}
	return false
}

func orList(xs []string) string {
	switch len(xs) {
	case 0:
		return "any per-object filter"
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}

// toSnake converts an exported Go type stem to snake_case (MachineType → machine_type).
func toSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// finish prints the census — on every path, including the ones that could not read
// anything — then the findings, and turns findings into an error.
func finish(p Profile, rep Report, out io.Writer) (Report, error) {
	_, _ = fmt.Fprintf(out,
		"audit-list-filter[%s]: examined %d file(s) in %d package(s), %d resource(s), "+
			"%d listing method(s) (%d undeclared, %d cluster-scoped, %d declaration(s) attributed "+
			"to no resource)\n",
		p.Service, rep.Files, rep.Packages, len(rep.Resources), len(rep.Listings),
		len(rep.Undeclared), len(rep.ClusterScoped), len(rep.Unattributed))
	if len(rep.Listings) > 0 {
		_, _ = fmt.Fprintf(out, "audit-list-filter[%s]: judged %s\n", p.Service, strings.Join(rep.Listings, ", "))
	}
	if len(rep.ClusterScoped) > 0 {
		_, _ = fmt.Fprintf(out, "audit-list-filter[%s]: cluster-scoped by declaration %s\n",
			p.Service, strings.Join(rep.ClusterScoped, ", "))
	}
	// The ban is printed on EVERY path, including the passing one. It is the part of
	// this gate that is derived from the tree rather than written down, so "derived
	// nothing" and "derived, and nothing matched" have to be told apart from the
	// output alone — otherwise a source that silently stopped resolving looks exactly
	// like a clean run.
	_, _ = fmt.Fprintf(out,
		"audit-list-filter[%s]: enumerate-then-narrow ban — %d call(s) [%s]; %d declared, "+
			"%d derived from %d source(s)\n",
		p.Service, len(rep.BannedCalls), strings.Join(rep.BannedCalls, ", "),
		len(p.Banned), len(rep.DerivedEnumerations), len(p.EnumerationSources))
	for _, line := range rep.EnumerationSources {
		_, _ = fmt.Fprintf(out, "audit-list-filter[%s]:   source %s\n", p.Service, line)
	}
	switch {
	case len(p.EnumerationSources) > 0:
		// already stated, one line per source, above
	case p.EnumerationInapplicable != "":
		// A declared absence is NOT the same output as an undeclared one, and the
		// difference is the whole of #684: one says "nothing to apply, and here is
		// the count that proves it", the other says "nobody looked".
		_, _ = fmt.Fprintf(out,
			"audit-list-filter[%s]:   no enumeration source, and none can apply: %d of %d "+
				"declared listing(s) narrow — %s\n",
			p.Service, len(rep.Narrowing), len(rep.Listings), p.EnumerationInapplicable)
	default:
		_, _ = fmt.Fprintf(out,
			"audit-list-filter[%s]:   no enumeration source declared — the ban above is the "+
				"hand-written list ONLY, and a form this service invents in its own tables would "+
				"pass unnoticed (see Profile.EnumerationSources)\n", p.Service)
	}
	if len(rep.Findings) == 0 {
		_, _ = fmt.Fprintf(out, "audit-list-filter[%s]: OK\n", p.Service)
		return rep, nil
	}
	for _, f := range rep.Findings {
		_, _ = fmt.Fprintf(out, "audit-list-filter[%s]: %s\n", p.Service, f)
	}
	_, _ = fmt.Fprint(out, explanation)
	return rep, fmt.Errorf("audit-list-filter[%s]: %d finding(s)", p.Service, len(rep.Findings))
}

const explanation = `
Every public List<Resource> RPC must read its page from the service's own DB and
then narrow that page to the rows the caller may actually see — a batched
per-object check against kaname (AuthorizeService.BatchCheck, on the same
relation Get enforces). Project scope alone does NOT answer "may this caller see
THESE objects": without the per-object filter every project member sees every row
of the project.

Enumerating all allowed ids (ListAllowedIDs/ListObjects) is not a substitute: that
answer is capped server-side with no continuation token, so a tenant's own rows
fall outside the cap and become invisible.

Every listing method — not only the one called List — declares how its page is
narrowed, in the service's Profile.Listings. A method with no declaration is a
finding, so a new one cannot ship unjudged; a declaration with no method is a
finding, so an exclusion cannot outlive its subject.

A cluster catalog or an admin-only internal surface is declared ClusterScoped
with a reason. That reason lives in the profile, where this gate reads it — not
in a reviewer's memory.
`

// returnsAResponse reports whether any return statement of fn hands back a
// non-nil first result — that is, whether the method can serve a page at all.
//
// This is the evidence behind Shape NeverServes. It is deliberately a property
// of the METHOD's own returns, not of its call graph: a refusal that delegates
// to a helper still has to return that helper's error, and a path that builds a
// response has to name it here.
//
// The two-result shape (response, error) is the only one gRPC unary handlers
// have, so a return with fewer results means the anchor is not a handler and the
// caller should not have declared this shape; that is reported as "serves" so the
// declaration cannot pass on a method the analyser does not understand — silence
// on an unrecognised shape is exactly how a gate stops being one.
func (u *unit) returnsAResponse(fn *ast.FuncDecl) (bool, string) {
	if fn == nil || fn.Body == nil {
		return true, "no body to inspect"
	}
	served := false
	where := ""
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if served {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if len(ret.Results) != 2 {
			served, where = true, fmt.Sprintf("return with %d result(s) at %s",
				len(ret.Results), u.fset.Position(ret.Pos()))
			return false
		}
		// nil first result = nothing served on this path.
		if id, ok := ret.Results[0].(*ast.Ident); ok && id.Name == "nil" {
			return true
		}
		served, where = true, fmt.Sprintf("non-nil response at %s", u.fset.Position(ret.Pos()))
		return false
	})
	return served, where
}
