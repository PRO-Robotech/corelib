// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package validate

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestResourceID_InteractiveClient — the router must classify the interactive
// client's going-forward hyphen id (IAM-INT-1 scenarios 01 and 05).
//
// The two halves are a PAIR on purpose: the negative alone ("malformed is
// rejected") stays green when the prefix is unregistered — because then
// EVERYTHING is rejected — and would attest to nothing.
func TestResourceID_InteractiveClient(t *testing.T) {
	const wellFormed = "ic-0123456789abcdefg"

	// Positive: a well-formed id of the new resource passes the format gate and
	// is left to repo.Get to answer (NOT_FOUND lane), per by-lane code-split.
	if err := ResourceID("interactive client", "ic", wellFormed); err != nil {
		t.Errorf("ResourceID(%q) = %v, want nil — the well-formed hyphen id of the "+
			"interactive client must pass the format gate", wellFormed, err)
	}

	// Negative: a string that cannot be an id of any declared family is rejected
	// synchronously, with the contract tone of api-conventions.md.
	const malformed = "не-идентификатор"
	err := ResourceID("interactive client", "ic", malformed)
	if err == nil {
		t.Fatalf("ResourceID(%q) = nil, want InvalidArgument", malformed)
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("ResourceID(%q): code = %s, want InvalidArgument", malformed, st.Code())
	}
	if want := "invalid interactive client id '" + malformed + "'"; st.Message() != want {
		t.Errorf("ResourceID(%q): message = %q, want %q", malformed, st.Message(), want)
	}

	// Control on the gate's own premise: an unregistered prefix of the SAME
	// hyphen SHAPE is still rejected. Without this, a canon that accepted every
	// hyphen string would pass the positive above and the gate would measure
	// nothing.
	const foreignShape = "zz-0123456789abcdefg"
	if err := ResourceID("interactive client", "ic", foreignShape); err == nil {
		t.Errorf("ResourceID(%q) = nil — the canon accepts an UNREGISTERED hyphen "+
			"prefix, so the positive case above proves nothing about %q", foreignShape, "ic")
	} else if st, _ := status.FromError(err); !strings.Contains(st.Message(), foreignShape) {
		t.Errorf("ResourceID(%q): message %q does not name the offending id", foreignShape, st.Message())
	}
}
