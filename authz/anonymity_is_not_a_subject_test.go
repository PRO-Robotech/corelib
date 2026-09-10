// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// Anonymity must never become a subject of the permission model.
//
// The platform deliberately publishes a global catalogue (regions, zones, disk
// types, machine types) to EVERY AUTHENTICATED tenant, and it does so with a
// wildcard grant on the cluster singleton. A wildcard is satisfied by any subject
// string of the right shape — so the moment the reserved "nobody" marker is
// allowed to become one, the grant meant for "everyone who authenticated" also
// answers "yes" to a caller who never did.
//
// These tests therefore assert the OBSERVABLE: a request carrying the marker is
// refused, and the model is not consulted at all. Asserting "the extractor
// returned false" would not catch a later refactor that reintroduces the subject
// on another path.

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/operations"
)

// catalogueMap models the global-catalogue lane: a read RPC gated by `viewer` on
// the cluster singleton, which is exactly where the wildcard grant lives.
func catalogueMap() authz.RPCMap {
	return authz.RPCMap{
		"/kacho.cloud.geo.v1.ZoneService/List": {
			Relation: "viewer",
			Extract: authz.StaticExtractor("cluster", func(any) (string, error) {
				return "kacho_root", nil
			}),
		},
	}
}

// wildcardCheck models `cluster:<root>#viewer@user:*` — it says yes to ANY
// subject on that relation/object. Everything it is asked is recorded so a test
// can assert the model was never consulted.
type wildcardCheck struct{ asked []string }

func (w *wildcardCheck) client() authz.CheckClient {
	return authz.CheckClientFunc(func(_ context.Context, subject, relation, object string) (bool, error) {
		w.asked = append(w.asked, subject+" "+relation+" "+object)
		return true, nil // the wildcard is satisfied by any subject
	})
}

// TestInterceptor_AnonymousMarkerNeverReachesTheWildcard — the reserved marker in
// any shape a caller or the edge can produce must be refused BEFORE the model is
// asked, even though the model would say yes.
func TestInterceptor_AnonymousMarkerNeverReachesTheWildcard(t *testing.T) {
	cases := []struct {
		name      string
		principal operations.Principal
	}{
		// What the edge injects for a request with no credential.
		{"edge marker", operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID}},
		// The same word arriving on the principal headers with another declared
		// type — the type is chosen by whoever sent the headers, so it cannot be
		// what distinguishes a real identity from the marker.
		{"marker declared as user", operations.Principal{Type: "user", ID: operations.AnonymousPrincipalID}},
		{"marker declared as service account", operations.Principal{Type: "service_account", ID: operations.AnonymousPrincipalID}},
		// Half-empty pairs: a type with no id, an id with no type.
		{"type without id", operations.Principal{Type: "user", ID: ""}},
		{"id without type", operations.Principal{Type: "", ID: "usr_alice"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &wildcardCheck{}
			intr := authz.NewInterceptor(authz.InterceptorOptions{
				Cache:       authz.NewCache(0),
				ServiceName: "kacho-geo-test",
				Map:         catalogueMap(),
				Client:      w.client(),
			})
			ctx := operations.WithPrincipal(context.Background(), tc.principal)

			_, err := runUnary(intr, ctx, "/kacho.cloud.geo.v1.ZoneService/List", &fakeReq{})

			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("anonymous %+v must be refused; got err=%v (code %v)",
					tc.principal, err, status.Code(err))
			}
			if len(w.asked) != 0 {
				t.Fatalf("the permission model must not be asked about anonymity; it was asked: %v", w.asked)
			}
		})
	}
}

// TestInterceptor_AuthenticatedSubjectStillReadsTheCatalogue — the other
// direction of the same gate. Narrowing anonymity must not narrow the catalogue
// for the authenticated tenants it exists for: a real principal still reaches the
// model and is allowed by the wildcard.
func TestInterceptor_AuthenticatedSubjectStillReadsTheCatalogue(t *testing.T) {
	for _, p := range []operations.Principal{
		{Type: "user", ID: "usr_alice", DisplayName: "alice"},
		{Type: "service_account", ID: "sva_reader"},
		// A real id that merely CONTAINS the reserved word is a real identity.
		{Type: "user", ID: "anonymous_ish"},
	} {
		w := &wildcardCheck{}
		intr := authz.NewInterceptor(authz.InterceptorOptions{
			Cache:       authz.NewCache(0),
			ServiceName: "kacho-geo-test",
			Map:         catalogueMap(),
			Client:      w.client(),
		})
		ctx := operations.WithPrincipal(context.Background(), p)

		resp, err := runUnary(intr, ctx, "/kacho.cloud.geo.v1.ZoneService/List", &fakeReq{})
		if err != nil {
			t.Fatalf("authenticated %+v must read the catalogue; got %v", p, err)
		}
		if resp != "handled" {
			t.Fatalf("authenticated %+v: handler was not called", p)
		}
		if len(w.asked) != 1 {
			t.Fatalf("authenticated %+v: expected exactly one model question, got %v", p, w.asked)
		}
	}
}

// TestInterceptor_AnonymousMarkerDeniedUnderAllowSystemPrincipal — the marker is
// not the bootstrap identity. AllowSystemPrincipal opens a blanket allow for
// {system, bootstrap}; the marker shares that declared type and must not inherit
// the allowance.
func TestInterceptor_AnonymousMarkerDeniedUnderAllowSystemPrincipal(t *testing.T) {
	w := &wildcardCheck{}
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache:                authz.NewCache(0),
		Map:                  catalogueMap(),
		Client:               w.client(),
		AllowSystemPrincipal: true,
	})
	ctx := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID})

	_, err := runUnary(intr, ctx, "/kacho.cloud.geo.v1.ZoneService/List", &fakeReq{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("the marker must be refused under AllowSystemPrincipal; got %v", err)
	}
	if len(w.asked) != 0 {
		t.Fatalf("the model must not be asked about anonymity; it was asked: %v", w.asked)
	}
}

// TestInterceptorStream_AnonymousMarkerDenied — the stream lane runs the same
// authorize(), and a gate that only covers unary is half a gate.
func TestInterceptorStream_AnonymousMarkerDenied(t *testing.T) {
	w := &wildcardCheck{}
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache:  authz.NewCache(0),
		Map:    catalogueMap(),
		Client: w.client(),
	})
	ctx := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID})

	called, err := runStream(intr, ctx, "/kacho.cloud.geo.v1.ZoneService/List")
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("stream: the marker must be refused; got %v", err)
	}
	if called {
		t.Fatalf("stream: handler must not run for anonymity")
	}
	if len(w.asked) != 0 {
		t.Fatalf("stream: the model must not be asked about anonymity; it was asked: %v", w.asked)
	}
}
