// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package proxytuple

// proxy_tuple_public_read_test.go — the proxy must accept the ONE intent a module
// emits to publish a resource for anonymous read, and nothing more.
//
// kacho-registry marks a repository public by emitting, through the proxy, the
// wildcard read tuple `user:* # v_get @ registry_repository:<registry>/<repo>` —
// its existence IS the public visibility (there is no separate flag the data
// plane consults). Emitting is not applying: the receiving side decides, and a
// relation outside its accepted set is refused whole, so the intent never lands
// and the repository is never actually public.
//
// The tuple asserted here is the one the emitter really builds
// (services/registry/internal/domain/fga_intent.go: FGARepoPublicGetTuple —
// subject FGASubjectPublicWildcard, relation FGARelationVGet, object
// registry_repository:<reg>/<repo>). It is spelled out literally because a test
// that asserts "some v_get is accepted" would stay green against a policy that
// accepts a shape the emitter never sends.

import (
	"testing"
)

// TestValidateTuple_PublicReadGrant — the exact register/unregister intent
// kacho-registry emits for a public repository is accepted.
func TestValidateTuple_PublicReadGrant(t *testing.T) {
	err := ValidateTuple("registry", "user:*", "v_get", "registry_repository:reg53eeeg3578y4ah0q9/team/app")
	if err != nil {
		t.Fatalf("the public-read grant a module emits to publish its own repository must be accepted, got %v", err)
	}
}

// TestValidateTuple_PublicReadIsWildcardOnly — the read relation is opened
// for the anonymous wildcard ONLY, and only on a type whose publicness is a
// product capability. A module may still not hand a named subject read access to
// its resources (that is the AccessBinding flow's decision, made where it can be
// listed, scoped and revoked), and it may not publish an arbitrary resource of
// its own.
func TestValidateTuple_PublicReadIsWildcardOnly(t *testing.T) {
	for _, tt := range []struct {
		name     string
		domain   string
		subject  string
		relation string
		object   string
		wantOK   bool
	}{
		{"registry publishes own repository", "registry", "user:*", "v_get", "registry_repository:reg1/app", true},
		// The closed list carries the ONE type whose publicness is a product
		// capability. A module may not make any other of its resources
		// world-readable — "anyone may read my network" is not a capability.
		{"registry-level object is not publishable", "registry", "user:*", "v_get", "registry_registry:reg1", false},
		{"vpc publishes its own network", "vpc", "user:*", "v_get", "vpc_network:net1", false},
		{"named user gets read", "registry", "user:usr00000000000000a1", "v_get", "registry_repository:reg1/app", false},
		{"named service account gets read", "registry", "service_account:sva0000000000000a1", "v_get", "registry_repository:reg1/app", false},
		{"wildcard of another subject type", "registry", "service_account:*", "v_get", "registry_repository:reg1/app", false},
		{"wildcard but a write verb", "registry", "user:*", "v_update", "registry_repository:reg1/app", false},
		{"wildcard but a list verb", "registry", "user:*", "v_list", "registry_repository:reg1/app", false},
		{"wildcard but a tier relation", "registry", "user:*", "viewer", "registry_repository:reg1/app", false},
		{"wildcard read on a foreign domain", "registry", "user:*", "v_get", "vpc_network:net1", false},
		{"wildcard read on an iam object", "registry", "user:*", "v_get", "account:acc1", false},
		{"wildcard read on the cluster", "registry", "user:*", "v_get", "cluster:cluster_root", false},
		// dev-mode (caller domain unknown): the domain binding cannot apply, but
		// the relation and object-type limits still do.
		{"unknown domain, wildcard read on a publishable object", "", "user:*", "v_get", "registry_repository:reg1/app", true},
		{"unknown domain, wildcard read on a non-publishable object", "", "user:*", "v_get", "vpc_network:net1", false},
		{"unknown domain, named subject read", "", "user:usr00000000000000a1", "v_get", "registry_repository:reg1/app", false},
		{"unknown domain, wildcard read on the cluster", "", "user:*", "v_get", "cluster:cluster_root", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTuple(tt.domain, tt.subject, tt.relation, tt.object)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("ValidateTuple(%q,%q,%q,%q) = %v; want accepted",
						tt.domain, tt.subject, tt.relation, tt.object, err)
				}
				return
			}
			if code := verdictOf(err); code != refused {
				t.Fatalf("ValidateTuple(%q,%q,%q,%q) code = %v; want PermissionDenied (err=%v)",
					tt.domain, tt.subject, tt.relation, tt.object, code, err)
			}
		})
	}
}
