// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestListWithOwner_GarbagePageToken_InvalidArgument locks the
// format-validate-first contract (security.md #7): a malformed opaque
// page_token must map to gRPC InvalidArgument — symmetric with validate.PageSize
// in the same builder — and NOT fall through to a service's
// unclassified→INTERNAL mapper. registry ListOperations returned 500 for a
// garbage page_token because the corelib decode error carried no recognizable
// sentinel/status (#63). The decode failure returns before any pool access, so
// a nil-pool pgRepo exercises the path deterministically without testcontainers.
func TestListWithOwner_GarbagePageToken_InvalidArgument(t *testing.T) {
	r := &pgRepo{schema: "public"}
	for _, tok := range []string{"!!!not-base64!!!", "Zm9v", "bm90LWEtY3Vyc29y"} {
		_, _, err := r.listWithOwner(context.Background(), ListFilter{
			ResourceID: "reg000000000000000",
			PageToken:  tok,
		}, nil)
		if err == nil {
			t.Fatalf("token %q: expected error for garbage page_token, got nil", tok)
		}
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Fatalf("token %q: got code %v, want InvalidArgument", tok, got)
		}
	}
}
