// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package ids

import "testing"

// TestInteractiveClientPrefix_InCanon — the interactive-login client resource
// (IAM-INT-1, scenario 01) is addressed as "ic-<17 crockford>". The hyphen form
// is classified by the router against KnownHyphenPrefixes(); a prefix absent
// from that canon never reaches validate.ResourceID's accept branch, so every
// well-formed id of the new resource would be rejected as malformed.
//
// This asserts the CANON by literal, not via the exported constant, so it fails
// as an assertion rather than as a compile error while the prefix is still
// unregistered — a compile failure would not distinguish "prefix unregistered"
// from "file does not build".
func TestInteractiveClientPrefix_InCanon(t *testing.T) {
	canon := KnownHyphenPrefixes()
	if _, ok := canon["ic"]; !ok {
		t.Errorf("hyphen prefix %q missing from KnownHyphenPrefixes — "+
			"validate.ResourceID would reject every well-formed %q- id "+
			"(IAM-INT-1 scenarios 01/05)", "ic", "ic")
	}
}

// TestNewHyphenID_InteractiveClient_Shape — CONTROL, not the subject. It records
// that the generator already accepts the two-character prefix and already emits
// the shape scenario 01 asserts ("ic-" + 17 body = 20 chars). It is green before
// the canon entry exists and stays green after, which is precisely what makes it
// useful: it localises the gap to the canon and rules out the generator.
func TestNewHyphenID_InteractiveClient_Shape(t *testing.T) {
	id := NewHyphenID("ic")
	if len(id) != 20 {
		t.Errorf("NewHyphenID(%q) = %q: length %d, want 20 (prefix + '-' + 17 body)", "ic", id, len(id))
	}
	if id[:3] != "ic-" {
		t.Errorf("NewHyphenID(%q) = %q: want %q prefix segment", "ic", id, "ic-")
	}
	for i := 3; i < len(id); i++ {
		if !isCrockfordChar(id[i]) {
			t.Errorf("NewHyphenID(%q) = %q: body char %q at %d is not crockford-base32", "ic", id, id[i], i)
		}
	}
}
