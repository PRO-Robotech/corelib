// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listfiltergate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The per-service tests (services/{compute,nlb,vpc}/tools/audit_list_filter_test.go)
// inject into each REAL tree and are the primary proof. What is locked here is the
// analyser's own discrimination, on fixtures small enough that each case isolates
// ONE property — and every property is asserted in BOTH directions, because a gate
// that only ever fires proves nothing about what it would let through.

// flatProfile mirrors compute: one package, resources told apart by receiver TYPE.
var flatProfile = Profile{
	Service:        "fixture-flat",
	AnchorRoot:     "internal/handler",
	PerPackage:     false,
	ReceiverSuffix: "Handler",
	Filters:        []string{"filterVisible", "FilterVisibleIDs"},
	Banned:         []string{"ListAllowedIDs", "ListObjects"},
	SubjectScopers: []string{"ListForCaller"},
}

// pkgProfile mirrors vpc/nlb: one package per resource, one transport type name.
var pkgProfile = Profile{
	Service:        "fixture-pkg",
	AnchorRoot:     "internal/apps/kaname/api",
	PerPackage:     true,
	ReceiverSuffix: "Handler",
	Filters:        []string{"FilterVisibleIDs", "FilterVisiblePage"},
	Banned:         []string{"ListAllowedIDs", "ListObjects"},
	SubjectScopers: []string{"ListForCaller"},
}

// run materialises a throwaway tree and audits it.
func run(t *testing.T, p Profile, files map[string]string, decls ...map[string]Listing) (string, error) {
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
	var buf strings.Builder
	if len(decls) == 1 {
		p.Listings = decls[0]
	}
	_, err := Audit(p, Options{Root: root}, &buf)
	return buf.String(), err
}

func TestAudit_DiscriminatesLeakFromLegitimateShape(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		files   map[string]string
		decls   map[string]Listing
		wantErr bool
	}{
		{
			name:    "compliant: List calls the filter",
			profile: flatProfile,
			files: map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{ listFilter authzfilter.Filter }

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}
`},
		},
		{
			name:    "leak: the filter's name survives only as the field's TYPE",
			profile: flatProfile,
			// This is the shape that kept compute's previous gate green: searching
			// the FILE finds `authzfilter.Filter` in the struct declaration, which
			// says nothing whatever about what List does.
			files: map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{ listFilter authzfilter.Filter }

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	_ = rows
	return nil
}
`},
			wantErr: true,
		},
		{
			name:    "leak: the filter is named only in a comment",
			profile: flatProfile,
			files: map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	// Visibility is narrowed per object via filterVisible / FilterVisibleIDs.
	rows, _ := h.svc.List(ctx)
	_ = rows
	return nil
}
`},
			wantErr: true,
		},
		{
			name:    "leak: a filtered SIBLING method must not vouch for List",
			profile: flatProfile,
			// Get filters; List does not. A file-scoped or package-scoped search sees
			// the call and passes. Only following what List itself calls catches it.
			files: map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) Get(ctx context.Context) error {
	one, _ := h.svc.Get(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, one)
	_ = visible
	return nil
}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	_ = rows
	return nil
}
`},
			wantErr: true,
		},
		{
			name:    "compliant: the receiver variable is named anything at all",
			profile: flatProfile,
			// Identification is by the receiver's TYPE. `hd` here, `h` above; same
			// declaration, same verdict.
			files: map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (hd *InstanceHandler) List(ctx context.Context) error {
	rows, _ := hd.svc.List(ctx)
	visible, _ := filterVisible(ctx, hd.listFilter, rows)
	_ = visible
	return nil
}
`},
		},
		{
			name:    "compliant: the filter sits in a package-local helper List calls",
			profile: pkgProfile,
			// vpc's real shape: the use-case calls a helper, the helper calls the
			// port. The walk has to follow both hops.
			files: map[string]string{"internal/apps/kaname/api/network/handler.go": `package network

type Handler struct{ list *ListNetworksUseCase }

func (h *Handler) List(ctx context.Context) error {
	_, _ = h.list.Execute(ctx)
	return nil
}

type ListNetworksUseCase struct{ filter ListFilter }

func (u *ListNetworksUseCase) Execute(ctx context.Context) ([]string, error) {
	rows, _ := u.repo.List(ctx)
	return filterVisibleNetworks(ctx, u.filter, rows)
}

func filterVisibleNetworks(ctx context.Context, filter ListFilter, rows []string) ([]string, error) {
	return filter.FilterVisibleIDs(ctx, rows)
}
`},
		},
		{
			name:    "leak: the helper stops filtering, two hops down",
			profile: pkgProfile,
			files: map[string]string{"internal/apps/kaname/api/network/handler.go": `package network

type Handler struct{ list *ListNetworksUseCase }

func (h *Handler) List(ctx context.Context) error {
	_, _ = h.list.Execute(ctx)
	return nil
}

type ListNetworksUseCase struct{ filter ListFilter }

func (u *ListNetworksUseCase) Execute(ctx context.Context) ([]string, error) {
	rows, _ := u.repo.List(ctx)
	return filterVisibleNetworks(ctx, u.filter, rows)
}

func filterVisibleNetworks(ctx context.Context, filter ListFilter, rows []string) ([]string, error) {
	return rows, nil
}
`},
			wantErr: true,
		},
		{
			name:    "compliant: the declaration may live in any file of the package",
			profile: pkgProfile,
			files: map[string]string{
				"internal/apps/kaname/api/network/grpc_handler.go": `package network

type Handler struct{ list *ListNetworksUseCase }

func (h *Handler) List(ctx context.Context) error {
	_, _ = h.list.Execute(ctx)
	return nil
}
`,
				"internal/apps/kaname/api/network/list_usecase.go": `package network

type ListNetworksUseCase struct{ filter ListFilter }

func (u *ListNetworksUseCase) Execute(ctx context.Context) ([]string, error) {
	rows, _ := u.repo.List(ctx)
	return u.filter.FilterVisibleIDs(ctx, rows)
}
`,
			},
		},
		{
			name:    "reject: enumerate-then-narrow is not a per-page check",
			profile: pkgProfile,
			files: map[string]string{"internal/apps/kaname/api/network/handler.go": `package network

type Handler struct{ list *ListNetworksUseCase }

func (h *Handler) List(ctx context.Context) error {
	_, _ = h.list.Execute(ctx)
	return nil
}

type ListNetworksUseCase struct{ filter ListFilter }

func (u *ListNetworksUseCase) Execute(ctx context.Context) ([]string, error) {
	allowed, _ := u.filter.ListAllowedIDs(ctx)
	rows, _ := u.repo.ListByIDs(ctx, allowed)
	return u.filter.FilterVisibleIDs(ctx, rows)
}
`},
			wantErr: true,
		},
		{
			name:    "compliant: a comment may EXPLAIN why enumeration is banned",
			profile: pkgProfile,
			// The converse of the case above: documenting the ban must not trip it.
			files: map[string]string{"internal/apps/kaname/api/network/handler.go": `package network

type Handler struct{ list *ListNetworksUseCase }

func (h *Handler) List(ctx context.Context) error {
	_, _ = h.list.Execute(ctx)
	return nil
}

type ListNetworksUseCase struct{ filter ListFilter }

func (u *ListNetworksUseCase) Execute(ctx context.Context) ([]string, error) {
	// Never ListAllowedIDs/ListObjects here: that answer is capped server-side.
	rows, _ := u.repo.List(ctx)
	return u.filter.FilterVisibleIDs(ctx, rows)
}
`},
		},
		{
			name:    "not a resource: a package with no public List is not judged",
			profile: pkgProfile,
			files: map[string]string{
				"internal/apps/kaname/api/network/handler.go": `package network

type Handler struct{ list *ListNetworksUseCase }

func (h *Handler) List(ctx context.Context) error {
	_, _ = h.list.Execute(ctx)
	return nil
}

type ListNetworksUseCase struct{ filter ListFilter }

func (u *ListNetworksUseCase) Execute(ctx context.Context) ([]string, error) {
	rows, _ := u.repo.List(ctx)
	return u.filter.FilterVisibleIDs(ctx, rows)
}
`,
				// No List at all, and no filter either: it must not be a finding.
				"internal/apps/kaname/api/operation/handler.go": `package operation

type Handler struct{}

func (h *Handler) Get(ctx context.Context) error { return nil }
`,
			},
		},
		{
			// This case previously read "not a resource: ListOperations is a different
			// method" and asserted the gate stays SILENT on the fixture below — an
			// InstanceHandler.ListOperations that hands back a page with no narrowing
			// whatsoever. It was not merely that the gate could not see such methods:
			// its own suite asserted the blindness as correct behaviour, which is why
			// the blind spot survived every review of this file.
			//
			// It now asserts the opposite. A listing method the service has not
			// declared is a finding, so the page cannot go unjudged in silence.
			name:    "undeclared: a listing method with no declared enforcement is a finding",
			profile: flatProfile,
			files: map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}

func (h *InstanceHandler) ListOperations(ctx context.Context) error {
	rows, _ := h.svc.ListOperations(ctx)
	_ = rows
	return nil
}
`},
			wantErr: true,
		},
		{
			// The paired positive for the case above: the SAME fixture, with the
			// method declared, must be silent. Without this, "undeclared is a finding"
			// would be satisfied by a gate that simply rejects every ListSomething,
			// and the declaration machinery would be doing no work.
			name:    "declared: the same method, declared SubjectScoped, is silent",
			profile: flatProfile,
			files: map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}

func (h *InstanceHandler) ListOperations(ctx context.Context) error {
	rows, _, _ := operations.ListForCaller(ctx, h.ops, filter)
	_ = rows
	return nil
}
`},
			decls: map[string]Listing{
				"instance.ListOperations": {Shape: SubjectScoped},
			},
		},
		{
			// And the mirror of THAT: declaring SubjectScoped does not make it so.
			// A declaration the code does not back must fail, or the declaration
			// table becomes a place to write whatever makes the gate quiet.
			name:    "declared SubjectScoped but nothing narrows by the caller",
			profile: flatProfile,
			files: map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}

func (h *InstanceHandler) ListOperations(ctx context.Context) error {
	rows, _ := h.svc.ListOperations(ctx)
	_ = rows
	return nil
}
`},
			decls: map[string]Listing{
				"instance.ListOperations": {Shape: SubjectScoped},
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(t, tc.profile, tc.files, tc.decls)
			if (err != nil) != tc.wantErr {
				t.Fatalf("verdict: gotErr=%v wantErr=%v\n--- output ---\n%s", err != nil, tc.wantErr, out)
			}
		})
	}
}

// TestAudit_UnattributedListIsAFinding — a public List the gate could not attribute
// to any resource must be a FINDING, not a silent omission.
//
// This gate exists because its predecessors keyed on a mutable label, so a rename
// removed a resource from their view while they kept printing OK. Identification by
// the declared transport TYPE is the fix, but it left the same hole one level down:
// a package whose transport type happens to be named something else contributes no
// resource at all, and the `List` inside it goes unjudged. The census printed the
// discrepancy — "7 packages, 3 resources" — and nothing acted on it, so the gate
// written against this class was an instance of it.
//
// The predicate is mirror-symmetric with attribution: whatever the mode, EVERY
// `List` method declaration in the parsed tree either resolves to a resource or is
// reported. Declarations that are not the transport surface at all (a differently
// named method, a type in an unparsed test file) are not `List` declarations and so
// are outside the predicate by construction, not by exception.
func TestAudit_UnattributedListIsAFinding(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		files   map[string]string
		wantErr bool
		wantIn  string
	}{
		{
			name:    "per-package: transport type named otherwise — the package is lost",
			profile: pkgProfile,
			files: map[string]string{
				// A properly named neighbour, so the run is not "examined nothing".
				"internal/apps/kaname/api/network/handler.go": `package network

type Handler struct{}

func (h *Handler) List(ctx context.Context) error {
	rows, _ := h.repo.Page(ctx)
	visible, _ := FilterVisiblePage(ctx, rows)
	_ = visible
	return nil
}
`,
				// Same shape, type named otherwise: it used to vanish from the census.
				"internal/apps/kaname/api/subnet/handler.go": `package subnet

type SubnetHandler struct{}

func (h *SubnetHandler) List(ctx context.Context) error {
	rows, _ := h.repo.Page(ctx)
	return h.reply(rows)
}
`,
			},
			wantErr: true,
			wantIn:  "SubnetHandler",
		},
		{
			name:    "flat: receiver type without the transport suffix — the resource is lost",
			profile: flatProfile,
			files: map[string]string{
				"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}
`,
				"internal/handler/volume_service.go": `package handler

type VolumeService struct{}

func (s *VolumeService) List(ctx context.Context) error {
	rows, _ := s.repo.Page(ctx)
	return s.reply(rows)
}
`,
			},
			wantErr: true,
			wantIn:  "VolumeService",
		},
		{
			name:    "MIRROR: every List attributed — silent",
			profile: pkgProfile,
			files: map[string]string{
				"internal/apps/kaname/api/network/handler.go": `package network

type Handler struct{}

func (h *Handler) List(ctx context.Context) error {
	rows, _ := h.repo.Page(ctx)
	visible, _ := FilterVisiblePage(ctx, rows)
	_ = visible
	return nil
}
`,
				// A package with NO List at all is not a resource by construction —
				// nlb's announce/operation/shared are exactly this, and must not be
				// coloured. Otherwise the plain census gap ("more packages than
				// resources") would be the predicate, and it would fire on them.
				"internal/apps/kaname/api/shared/helpers.go": `package shared

type Helper struct{}

func (h *Helper) Page(ctx context.Context) error { return nil }
`,
			},
			wantErr: false,
		},
		{
			name:    "MIRROR: List on a non-transport type is not the transport surface",
			profile: pkgProfile,
			files: map[string]string{
				"internal/apps/kaname/api/network/handler.go": `package network

type Handler struct{}

func (h *Handler) List(ctx context.Context) error {
	rows, _ := h.repo.Page(ctx)
	visible, _ := FilterVisiblePage(ctx, rows)
	_ = visible
	return nil
}
`,
				// Parsed but NOT a transport declaration: the gate only ever looked at
				// non-test files, and a repository adapter living beside the handler
				// legitimately has a List. Colouring it would make the gate unusable.
				"internal/apps/kaname/api/network/repo_test.go": `package network

type fakeRepo struct{}

func (r *fakeRepo) List(ctx context.Context) error { return nil }
`,
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(t, tc.profile, tc.files)
			if (err != nil) != tc.wantErr {
				t.Fatalf("verdict: gotErr=%v wantErr=%v\n--- output ---\n%s", err != nil, tc.wantErr, out)
			}
			if tc.wantIn != "" && !strings.Contains(out, tc.wantIn) {
				t.Errorf("the finding must name the declaration it could not attribute (%q)\n--- output ---\n%s",
					tc.wantIn, out)
			}
		})
	}
}

// TestAudit_NothingExaminedIsNotOK — "zero findings" must be unreachable from "zero
// read". Both shapes below used to print OK and exit 0, which is the worst answer a
// gate can give: indistinguishable from a clean tree, so a gate pointed at the wrong
// place certifies a service it never opened.
func TestAudit_NothingExaminedIsNotOK(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		files   map[string]string
	}{
		{
			name:    "flat: the anchor root does not exist",
			profile: flatProfile,
			files:   map[string]string{"internal/apps/kaname/api/network/handler.go": "package network\n"},
		},
		{
			name:    "flat: the anchor root holds no public List",
			profile: flatProfile,
			files:   map[string]string{"internal/handler/doc.go": "package handler\n"},
		},
		{
			name:    "per-package: the anchor root does not exist",
			profile: pkgProfile,
			files:   map[string]string{"internal/handler/instance_handler.go": "package handler\n"},
		},
		{
			name:    "per-package: the anchor root holds no package",
			profile: pkgProfile,
			files:   map[string]string{"internal/apps/kaname/api/doc.go": "package api\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(t, tc.profile, tc.files)
			if err == nil {
				t.Fatalf("the gate exited 0 having examined nothing — that is not a pass\n--- output ---\n%s", out)
			}
			if !strings.Contains(out, "examined") {
				t.Errorf("even a run that read nothing must state its census\n--- output ---\n%s", out)
			}
		})
	}
}

// TestAudit_ReportsWhatItExamined — a pass is only worth reading if it says what it
// judged, so the census is asserted rather than narrated.
func TestAudit_ReportsWhatItExamined(t *testing.T) {
	out, err := run(t, flatProfile, map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}

type MachineTypeHandler struct{}

func (h *MachineTypeHandler) List(ctx context.Context) error { return nil }
`}, map[string]Listing{
		"machine_type.List": {Shape: ClusterScoped, Reason: "cluster-wide sizing catalog"},
	})
	if err != nil {
		t.Fatalf("compliant fixture must pass: %v\n--- output ---\n%s", err, out)
	}
	// The listing-method count is asserted alongside the resource count on purpose:
	// the previous census reported resources only, so "8 resources" stood equally
	// well for 8 listing methods and for 21, and the 13 it was not looking at were
	// invisible in the very line meant to say what had been examined.
	for _, want := range []string{
		"1 file(s)", "2 resource(s)", "2 listing method(s)",
		"instance.List", "cluster-scoped by declaration machine_type.List",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the census must state what was examined (missing %q)\n--- output ---\n%s", want, out)
		}
	}
}

// TestAudit_ExpiredDeclarationIsAFinding — an exclusion must expire by itself.
// Once no such method exists the entry describes nothing, and the next method to
// inherit that name inherits an enforcement claim nobody checked.
//
// This replaces the whitelist test of the previous edition, and the subject moved
// one level down with it: --allow excluded a whole RESOURCE, so excluding
// addresspool as a cluster catalog also, silently, excluded its ListAddresses —
// a method nobody had considered when the exclusion was written. A declaration is
// per METHOD, so an exclusion can no longer take its neighbours with it.
func TestAudit_ExpiredDeclarationIsAFinding(t *testing.T) {
	files := map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}
`}

	expired := map[string]Listing{
		"disk_type.List": {Shape: ClusterScoped, Reason: "moved to kacho-storage long ago"},
	}
	out, err := run(t, flatProfile, files, expired)
	if err == nil {
		t.Fatalf("a declaration matching no method must be reported\n--- output ---\n%s", out)
	}
	if !strings.Contains(out, "disk_type.List") {
		t.Errorf("the finding must name the expired entry\n--- output ---\n%s", out)
	}

	// Converse: an entry that still has a subject must stay silent, so the rule is
	// not merely "flag every declaration".
	files["internal/handler/machine_type_handler.go"] = `package handler

type MachineTypeHandler struct{}

func (h *MachineTypeHandler) List(ctx context.Context) error { return nil }
`
	live := map[string]Listing{
		"machine_type.List": {Shape: ClusterScoped, Reason: "cluster-wide sizing catalog"},
	}
	if out, err := run(t, flatProfile, files, live); err != nil {
		t.Fatalf("a declaration that still matches a method must not be a finding: %v\n--- output ---\n%s", err, out)
	}
}

// TestNeverServes_SilentWhenEveryPathRefuses — injection, silent direction: a
// listing method whose every return hands back (nil, error) serves no page, so
// there is nothing to narrow and the declaration holds.
func TestNeverServes_SilentWhenEveryPathRefuses(t *testing.T) {
	out, err := run(t, flatProfile, map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}

func (h *InstanceHandler) ListAccessBindings(ctx context.Context, req *Req) (*Resp, error) {
	return nil, status.Error(codes.Unimplemented, "owned by iam")
}
`}, map[string]Listing{
		"instance.ListAccessBindings": {Shape: NeverServes},
	})
	if err != nil {
		t.Fatalf("метод, у которого каждый возврат — отказ, страницы не отдаёт: %v"+
			"\n--- output ---\n%s", err, out)
	}
	if !strings.Contains(out, "instance.ListAccessBindings") {
		t.Errorf("молчание обязано относиться к осмотренному: метода нет в переписи"+
			"\n--- output ---\n%s", out)
	}
}

// TestNeverServes_RedWhenAPathBuildsAResponse — injection, defect direction: one
// path that returns a response makes the declaration false, and that page is then
// served with no narrowing at all.
//
// Without this half the shape would be a rubber stamp: "declared NeverServes"
// would exempt a method from every check while it quietly handed back rows.
func TestNeverServes_RedWhenAPathBuildsAResponse(t *testing.T) {
	out, err := run(t, flatProfile, map[string]string{"internal/handler/instance_handler.go": `package handler

type InstanceHandler struct{}

func (h *InstanceHandler) List(ctx context.Context) error {
	rows, _ := h.svc.List(ctx)
	visible, _ := filterVisible(ctx, h.listFilter, rows)
	_ = visible
	return nil
}

func (h *InstanceHandler) ListAccessBindings(ctx context.Context, req *Req) (*Resp, error) {
	if req.Sneaky {
		return &Resp{}, nil
	}
	return nil, status.Error(codes.Unimplemented, "owned by iam")
}
`}, map[string]Listing{
		"instance.ListAccessBindings": {Shape: NeverServes},
	})
	if err == nil {
		t.Fatalf("объявление NeverServes на методе, строящем ответ, обязано быть находкой"+
			"\n--- output ---\n%s", out)
	}
	if !strings.Contains(out, "builds a response") {
		t.Errorf("находка обязана называть предмет (путь, строящий ответ)"+
			"\n--- output ---\n%s", out)
	}
}
