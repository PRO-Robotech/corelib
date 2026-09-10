// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/ids"
)

// allIDPrefixes enumerates EVERY canonical 3-char resource/operation prefix
// live in the platform, so the guard test below can assert each is a member of
// `resourceIDPrefixes`. The family-agnostic `ResourceID` validator returns
// InvalidArgument for any unknown 3-char prefix; a prefix that is live in some
// service but missing from `resourceIDPrefixes` would make every well-formed id
// of that family 400 at the api-gateway authz edge.
//
//   - ids.Prefix* — enumerated from pkg/ids (compiler-checked: a new
//     prefix constant added there but not referenced here is invisible, but a
//     constant renamed/removed breaks this test, prompting a re-audit).
//   - IAM domain prefixes — mirrored as string literals from the IAM domain
//     constants (corelib is UPSTREAM of kaname in the build graph and
//     `internal/` is not importable, so the IAM prefixes cannot be imported;
//     they are duplicated here as the single source-of-truth pointer).
//     cag/cond/soc/evt are deliberately excluded: they use the
//     `<prefix>_<17-char>` underscore format (not the 3-char ids.NewID shape)
//     and are not exposed on the public REST resource surface gated by authz.
var allIDPrefixes = func() map[string]string {
	m := map[string]string{
		// pkg/ids — resource prefixes
		"PrefixCloud":            ids.PrefixCloud,
		"PrefixFolder":           ids.PrefixFolder,
		"PrefixOrganization":     ids.PrefixOrganization,
		"PrefixNetwork":          ids.PrefixNetwork,
		"PrefixSubnet":           ids.PrefixSubnet,
		"PrefixAddress":          ids.PrefixAddress,
		"PrefixRouteTable":       ids.PrefixRouteTable,
		"PrefixSecurityGroup":    ids.PrefixSecurityGroup,
		"PrefixGateway":          ids.PrefixGateway,
		"PrefixNetworkInterface": ids.PrefixNetworkInterface,
		"PrefixAddressPool":      ids.PrefixAddressPool,
		"PrefixAnycastPool":      ids.PrefixAnycastPool,
		"PrefixInstance":         ids.PrefixInstance,
		"PrefixDisk":             ids.PrefixDisk,
		"PrefixImage":            ids.PrefixImage,
		"PrefixSnapshot":         ids.PrefixSnapshot,
		"PrefixStorageImage":     ids.PrefixStorageImage,
		"PrefixLoadBalancer":     ids.PrefixLoadBalancer,
		"PrefixListener":         ids.PrefixListener,
		"PrefixTargetGroup":      ids.PrefixTargetGroup,
		// pkg/ids — per-domain operation prefixes
		"PrefixOperationRM":      ids.PrefixOperationRM,
		"PrefixOperationVPC":     ids.PrefixOperationVPC,
		"PrefixOperationCompute": ids.PrefixOperationCompute,
		"PrefixOperationNLB":     ids.PrefixOperationNLB,
		// IAM domain constants (mirrored literals)
		"iam.PrefixAccount":         "acc",
		"iam.PrefixProject":         "prj",
		"iam.PrefixUser":            "usr",
		"iam.PrefixServiceAccount":  "sva",
		"iam.PrefixGroup":           "grp",
		"iam.PrefixRole":            "rol",
		"iam.PrefixAccessBinding":   "acb",
		"iam.PrefixOperationIAM":    "iop",
		"iam.PrefixUserOAuthClient": "uoc",
	}
	return m
}()

// TestResourceID_GuardEveryLivePrefixIsKnown is the regression guard ensuring a
// future new resource/operation prefix that lands in pkg/ids (or a new
// IAM resource) is also registered in `resourceIDPrefixes`, otherwise every
// well-formed id of that family would be rejected with InvalidArgument (400) at
// the gateway authz edge instead of reaching the owner service.
func TestResourceID_GuardEveryLivePrefixIsKnown(t *testing.T) {
	for name, prefix := range allIDPrefixes {
		if len(prefix) != 3 {
			t.Fatalf("%s = %q: canonical resource prefix must be exactly 3 chars", name, prefix)
		}
		if _, ok := resourceIDPrefixes[prefix]; !ok {
			t.Errorf("%s = %q is a live platform prefix but missing from resourceIDPrefixes — "+
				"well-formed %q ids would 400 at the authz edge instead of routing to their owner service",
				name, prefix, prefix)
		}
	}
}

// TestResourceID_KnownPrefixesAcceptValid asserts a well-formed id with each
// newly added (and previously known) prefix passes ResourceID (returns nil) —
// family-agnostic acceptance.
func TestResourceID_KnownPrefixesAcceptValid(t *testing.T) {
	body := strings.Repeat("0", 17)
	cases := []struct {
		name   string
		prefix string
	}{
		// IAM
		{"account", "acc"},
		{"project", "prj"},
		{"user", "usr"},
		{"service account", "sva"},
		{"group", "grp"},
		{"role", "rol"},
		{"access binding", "acb"},
		{"iam operation", "iop"},
		{"user oauth client", "uoc"},
		// nlb
		{"load balancer", "nlb"},
		{"listener", "lst"},
		{"target group", "tgr"},
		// storage — Volume/Snapshot/Image. `img` is storage's boot-image prefix
		// (ids.PrefixStorageImage), distinct from compute's `fd8` (ids.PrefixImage).
		// Regression #59: storage Image.Create emits `img<17>` ids but `img` was not
		// registered in the central known-prefix set → the gateway authz edge 400'd
		// every storage image Get/Update/Delete ("invalid resource id 'img…'").
		{"storage volume", "vol"},
		{"storage snapshot", "snp"},
		{"storage image", "img"},
		// previously known (regression)
		{"network", "net"},
		{"subnet", "sub"},
		{"vpc operation", "enp"},
		{"instance", "epd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.prefix + body
			if err := ResourceID(tc.name, tc.prefix, id); err != nil {
				t.Errorf("ResourceID(%q, _, %q) = %v, want nil (well-formed known-prefix id)", tc.name, id, err)
			}
		})
	}
}

// TestResourceID_MalformedRejected asserts an unknown 3-char prefix and a too
// short id both produce InvalidArgument with the flat-message contract.
func TestResourceID_MalformedRejected(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"unknown prefix", "zzz00000000000000000"},
		{"wrong-prefix word", "not-a-valid-acb-id-at-all-verylongstring"},
		{"too short", "ab"},
		{"two chars", "ac"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ResourceID("access binding", "acb", tc.id)
			if err == nil {
				t.Fatalf("ResourceID(_, _, %q) = nil, want InvalidArgument", tc.id)
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Fatalf("ResourceID(_, _, %q) code = %v, want InvalidArgument", tc.id, got)
			}
			if msg := status.Convert(err).Message(); !strings.Contains(msg, "invalid access binding id") {
				t.Errorf("ResourceID(_, _, %q) message = %q, want contains %q", tc.id, msg, "invalid access binding id")
			}
		})
	}
}

// TestResourceID_EmptyPasses — empty id is intentionally not rejected here
// (required-check / transcoding routing handled separately).
func TestResourceID_EmptyPasses(t *testing.T) {
	if err := ResourceID("access binding", "acb", ""); err != nil {
		t.Errorf("ResourceID(_, _, \"\") = %v, want nil", err)
	}
}

// TestResourceID_HyphenFormClassified — B3 (redesign-2026): the router must
// classify the going-forward hyphen form "<prefix>-<crockford-base32>"
// (e.g. "ins-…", "ns-…") ALONGSIDE the legacy 3-char concatenated form
// ("net…"), because services migrate their id prefix one at a time in their own
// redesign. The crockford body never contains '-', so a hyphen is an
// unambiguous signal of the new form; the prefix is the segment before the
// first hyphen and MAY be 2 chars (`ns`/`mt`/`vt`) — not the fixed-3 legacy
// shape. Acceptance is family-agnostic (prefix known ⇒ ok, body not validated
// here).
func TestResourceID_HyphenFormClassified(t *testing.T) {
	body := strings.Repeat("0", 17)
	accepted := []struct {
		name   string
		prefix string
	}{
		// compute going-forward canon
		{"instance", "ins"},
		{"machine type", "mt"},
		{"placement group", "plg"},
		{"volume type", "vt"},
		// storage
		{"image", "img"},
		// registry
		{"namespace", "ns"},
		// iam (invitation is new; acc/prj also valid in hyphen form)
		{"invitation", "inv"},
		{"account", "acc"},
	}
	for _, tc := range accepted {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			id := tc.prefix + "-" + body
			if err := ResourceID(tc.name, tc.prefix, id); err != nil {
				t.Errorf("ResourceID(%q, _, %q) = %v, want nil (well-formed hyphen-prefix id)", tc.name, id, err)
			}
		})
	}

	// Hyphen form with an UNKNOWN prefix segment is rejected with the flat
	// contract message — a hyphen alone does not launder an unknown family.
	rejected := []struct {
		name string
		id   string
	}{
		{"unknown hyphen prefix", "zzz-" + body},
		{"leading hyphen (empty prefix)", "-" + body},
		{"two-char unknown", "qq-" + body},
	}
	for _, tc := range rejected {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			err := ResourceID("resource", "ins", tc.id)
			if err == nil {
				t.Fatalf("ResourceID(_, _, %q) = nil, want InvalidArgument", tc.id)
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Fatalf("ResourceID(_, _, %q) code = %v, want InvalidArgument", tc.id, got)
			}
			if msg := status.Convert(err).Message(); !strings.Contains(msg, "invalid resource id") {
				t.Errorf("ResourceID(_, _, %q) message = %q, want contains %q", tc.id, msg, "invalid resource id")
			}
		})
	}
}

// TestResourceID_LegacyFormUnaffectedByHyphenSupport — regression guard: adding
// hyphen-form acceptance must be strictly ADDITIVE. Legacy concatenated ids
// (no hyphen) keep classifying by their first 3 chars, and a legacy-prefixed
// string that happens to carry a hyphen still resolves via the legacy 3-char
// fallback (no real id ever carries a hyphen, so this only affects malformed
// probes — none regress to a false reject).
func TestResourceID_LegacyFormUnaffectedByHyphenSupport(t *testing.T) {
	body := strings.Repeat("0", 17)
	for _, p := range []string{"net", "sub", "epd", "nlb", "acb"} {
		id := p + body
		if err := ResourceID("legacy", p, id); err != nil {
			t.Errorf("legacy ResourceID(_, _, %q) = %v, want nil", id, err)
		}
	}
	// Legacy 3-char prefix followed by a hyphen still accepted via the legacy
	// fallback (family-agnostic, body not validated) — no regression.
	if err := ResourceID("legacy-hyphen", "net", "net-"+body); err != nil {
		t.Errorf("ResourceID(_, _, %q) = %v, want nil (legacy 3-char fallback)", "net-"+body, err)
	}
}
