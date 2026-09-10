// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// hide_existence_test.go — hiding a resource must be indistinguishable from the
// resource not being there.
//
// When the check answers "this object exists, but not for you", the interceptor
// blocks the handler and answers NOT_FOUND on purpose: a caller must not learn
// that someone else's resource exists. That only holds if the answer looks
// EXACTLY like a genuine miss. The service's own miss carries the contract tone
// "<Resource> <id> not found"; a bare "not found" is a different string, and a
// different string is an oracle — the caller separates "exists, not mine" from
// "does not exist" by reading the message.
//
// So the two answers are compared byte for byte, in the same test, through the
// same interceptor.

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
)

const (
	hideMethod    = "/kacho.cloud.vpc.v1.SubnetService/Get"
	hideSubnetID  = "sub00000000000000abc"
	hideBackendNF = "Subnet " + hideSubnetID + " not found"
)

// hideMap — one object-scoped read RPC on a resource whose owner answers with the
// contract tone.
func hideMap() authz.RPCMap {
	return authz.RPCMap{
		hideMethod: {
			Relation: "v_get",
			Extract: authz.StaticExtractor("vpc_subnet", func(req any) (string, error) {
				return req.(*fakeReq).id, nil
			}),
		},
	}
}

// answer runs one call through the interceptor and returns what the caller sees.
// The handler stands in for the owning service: it reports its genuine miss.
func answer(t *testing.T, check authz.CheckClientFunc, id string) (codes.Code, string) {
	t.Helper()
	intr := authz.NewInterceptor(authz.InterceptorOptions{Cache: authz.NewCache(0), Map: hideMap(), Client: check})
	_, err := intr.Unary()(ctxWithPrincipal(t, "usr_bob", "user"), &fakeReq{id: id},
		&grpc.UnaryServerInfo{FullMethod: hideMethod},
		func(context.Context, any) (any, error) {
			return nil, status.Error(codes.NotFound, hideBackendNF)
		})
	st := status.Convert(err)
	return st.Code(), st.Message()
}

// TestHideExistence_IsIndistinguishableFromAGenuineMiss — the oracle probe.
func TestHideExistence_IsIndistinguishableFromAGenuineMiss(t *testing.T) {
	// (a) The object exists and the caller may not see it: the check reports
	// hide-existence and the handler is never reached.
	hiddenCode, hiddenMsg := answer(t, func(context.Context, string, string, string) (bool, error) {
		return false, authz.ErrHideExistence
	}, hideSubnetID)

	// (b) The object is genuinely absent: the check finds no path, the handler
	// runs and reports the owner's miss.
	missCode, missMsg := answer(t, func(context.Context, string, string, string) (bool, error) {
		return false, authz.ErrNoPath
	}, hideSubnetID)

	if hiddenCode != missCode || hiddenMsg != missMsg {
		t.Fatalf("existence oracle: hidden answers %s %q, genuinely absent answers %s %q — the caller reads existence off the difference",
			hiddenCode, hiddenMsg, missCode, missMsg)
	}
	if missMsg != hideBackendNF {
		t.Fatalf("the shared answer must be the owner's own miss text %q, got %q", hideBackendNF, missMsg)
	}
}

// TestHideExistence_NeverEchoesTheInternalType — the message must not carry the
// authorization object type. It appears nowhere on the public surface (the
// resource is `Subnet`, the path `/subnets`, the field `subnetId`), so emitting
// it both leaks the internal dictionary and is itself the distinguishing mark.
func TestHideExistence_NeverEchoesTheInternalType(t *testing.T) {
	_, msg := answer(t, func(context.Context, string, string, string) (bool, error) {
		return false, authz.ErrHideExistence
	}, hideSubnetID)

	if got := "vpc_subnet"; contains(msg, got) {
		t.Fatalf("the hide-existence message must not contain the authorization object type %q: %q", got, msg)
	}
}

// TestHideExistence_UnmappedType_StaysLeastInformative — for an object type with
// no owning-service text to match, byte-identity is not achievable; the fallback
// must then be the least informative answer, never the internal type.
func TestHideExistence_UnmappedType_StaysLeastInformative(t *testing.T) {
	m := authz.RPCMap{
		hideMethod: {
			Relation: "v_get",
			Extract: authz.StaticExtractor("some_unmapped_type", func(req any) (string, error) {
				return req.(*fakeReq).id, nil
			}),
		},
	}
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Cache: authz.NewCache(0),
		Map:   m,
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return false, authz.ErrHideExistence
		}),
	})
	_, err := intr.Unary()(ctxWithPrincipal(t, "usr_bob", "user"), &fakeReq{id: hideSubnetID},
		&grpc.UnaryServerInfo{FullMethod: hideMethod},
		func(context.Context, any) (any, error) { return nil, nil })

	st := status.Convert(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("hide-existence must answer NotFound, got %v", st.Code())
	}
	if st.Message() != "not found" {
		t.Fatalf("unmapped type must fall back to the neutral %q, got %q", "not found", st.Message())
	}
	if contains(st.Message(), "some_unmapped_type") {
		t.Fatalf("the fallback must not echo the authorization object type: %q", st.Message())
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}
