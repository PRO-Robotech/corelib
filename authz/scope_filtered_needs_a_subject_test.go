// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// A scope-filtered RPC still needs a subject.
//
// `ScopeFiltered` says "do not ask one question about one object" — correct, because
// the rows such an RPC returns belong to individually-owned objects the caller never
// names, so there is no single object to ask about. It does NOT say "do not find out
// who is calling".
//
// The distinction matters because there is nothing underneath. A per-RPC Check is
// what normally refuses an unrecognised caller; a scope-filtered RPC has none by
// construction, so if the interceptor also declines to look at the subject, the only
// remaining narrowing lives in the handler — and a handler whose filter is absent,
// switched off, or degraded then answers a caller nobody identified. That is the
// project rule "an empty subject is cut UNCONDITIONALLY, not when the filter is
// wired".
//
// Asserted on the observable: the handler does not run.

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/operations"
)

func scopeFilteredMap() authz.RPCMap {
	return authz.RPCMap{
		"/kacho.cloud.compute.v1.InstanceService/List":       {ScopeFiltered: true},
		"/kacho.cloud.compute.v1.InternalWatchService/Watch": {ScopeFiltered: true},
	}
}

// refuseToBeAsked — the model must not be consulted for a scope-filtered RPC at all:
// there is no object to ask about, so any question would be about the wrong thing.
func refuseToBeAsked(t *testing.T) authz.CheckClient {
	t.Helper()
	return authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
		t.Fatalf("a scope-filtered RPC must not produce a single-object Check")
		return false, nil
	})
}

func TestInterceptor_ScopeFilteredRPCStillRequiresASubject(t *testing.T) {
	cases := []struct {
		name string
		ctx  func() context.Context
	}{
		// Nothing in the context: no principal-extract ran, or it declined to name one.
		{"context names nobody", context.Background},
		// The reserved marker the edge injects for a request with no credential —
		// in every shape whoever sent the identity headers can choose.
		{"edge anonymity marker", func() context.Context {
			return operations.WithPrincipal(context.Background(),
				operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID})
		}},
		{"marker declared as a user", func() context.Context {
			return operations.WithPrincipal(context.Background(),
				operations.Principal{Type: "user", ID: operations.AnonymousPrincipalID})
		}},
		{"type without id", func() context.Context {
			return operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: ""})
		}},
	}

	for _, method := range []string{
		"/kacho.cloud.compute.v1.InstanceService/List",
		"/kacho.cloud.compute.v1.InternalWatchService/Watch",
	} {
		for _, tc := range cases {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				intr := authz.NewInterceptor(authz.InterceptorOptions{
					Cache:       authz.NewCache(0),
					ServiceName: "kacho-compute-test",
					Map:         scopeFilteredMap(),
					Client:      refuseToBeAsked(t),
				})

				unaryRan := false
				_, uerr := intr.Unary()(tc.ctx(), struct{}{},
					&grpc.UnaryServerInfo{FullMethod: method},
					func(context.Context, any) (any, error) { unaryRan = true; return "handled", nil })

				if status.Code(uerr) != codes.PermissionDenied {
					t.Fatalf("unary: want PermissionDenied for a request that names nobody, got %v", uerr)
				}
				if unaryRan {
					t.Fatal("unary: handler ran for an unidentified caller; a scope-filtered RPC has no " +
						"per-RPC Check underneath to refuse it")
				}

				streamRan := false
				serr := intr.Stream()(nil, &ctxOnlyStream{ctx: tc.ctx()},
					&grpc.StreamServerInfo{FullMethod: method},
					func(any, grpc.ServerStream) error { streamRan = true; return nil })

				if status.Code(serr) != codes.PermissionDenied {
					t.Fatalf("stream: want PermissionDenied for a request that names nobody, got %v", serr)
				}
				if streamRan {
					t.Fatal("stream: handler ran for an unidentified caller")
				}
			})
		}
	}
}

// TestInterceptor_ScopeFilteredRPCPassesAnIdentifiedCaller — the counter-case, and it
// is what keeps the rule above from being a blanket refusal: an identified caller must
// still reach the handler WITHOUT a single-object Check, because that is the whole
// point of scope filtering. Without this half, tightening the subject rule could be
// "satisfied" by refusing everyone.
func TestInterceptor_ScopeFilteredRPCPassesAnIdentifiedCaller(t *testing.T) {
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache:       authz.NewCache(0),
		ServiceName: "kacho-compute-test",
		Map:         scopeFilteredMap(),
		Client:      refuseToBeAsked(t),
	})
	// A subject with no grants at all: the handler (not the interceptor) narrows the
	// result to empty. The interceptor must let it through.
	ctx := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: "usr_nonmember"})

	ran := false
	resp, err := intr.Unary()(ctx, struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.compute.v1.InstanceService/List"},
		func(context.Context, any) (any, error) { ran = true; return "handled", nil })

	if err != nil {
		t.Fatalf("an identified caller must pass a scope-filtered RPC, got %v", err)
	}
	if !ran || resp != "handled" {
		t.Fatal("handler must be invoked for an identified caller")
	}
}

// ctxOnlyStream — a grpc.ServerStream that carries only a Context; Stream() reads
// nothing else before delegating.
type ctxOnlyStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *ctxOnlyStream) Context() context.Context { return s.ctx }
