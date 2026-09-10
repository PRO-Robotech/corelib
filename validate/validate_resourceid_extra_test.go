// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package validate

import "testing"

// parseResourceIDPrefixes normalises a comma-separated env value into the set of
// meaningful (exactly-3-char) id prefixes; malformed tokens are dropped.
func TestParseResourceIDPrefixes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"single", "xyz", []string{"xyz"}},
		{"multi with spaces", " xyz , abc ", []string{"xyz", "abc"}},
		{"drops non-3-char", "xyz,ab,abcd,", []string{"xyz"}},
		{"lowercases", "XYZ", []string{"xyz"}},
		{"dedup ignored (set semantics upstream)", "xyz,xyz", []string{"xyz", "xyz"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseResourceIDPrefixes(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("parseResourceIDPrefixes(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("parseResourceIDPrefixes(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}

// buildResourceIDPrefixes must include the base platform prefixes AND any extra
// prefixes supplied via config — so a new domain can be routed at the authz edge
// without a corelib release (only an env/config change on the gateway).
func TestBuildResourceIDPrefixes_MergesExtras(t *testing.T) {
	m := buildResourceIDPrefixes("xyz,qqq")

	// Base prefix still present.
	if _, ok := m["net"]; !ok {
		t.Errorf("base prefix 'net' missing from built set")
	}
	// Extras merged.
	for _, p := range []string{"xyz", "qqq"} {
		if _, ok := m[p]; !ok {
			t.Errorf("extra prefix %q not merged into built set", p)
		}
	}
}

// A resource id whose prefix was supplied only via the extra-config path must be
// accepted by the family-agnostic check that ResourceID relies on.
func TestBuildResourceIDPrefixes_ExtraPrefixAccepted(t *testing.T) {
	m := buildResourceIDPrefixes("zzz")
	id := "zzz_deadbeef"
	if _, ok := m[id[:3]]; !ok {
		t.Errorf("id %q with config-supplied prefix must be recognised", id)
	}
}

// Empty config leaves exactly the base set (no accidental widening).
func TestBuildResourceIDPrefixes_EmptyConfigIsBaseOnly(t *testing.T) {
	base := buildResourceIDPrefixes("")
	if _, ok := base["zzz"]; ok {
		t.Errorf("unexpected prefix 'zzz' present with empty config")
	}
	if _, ok := base["net"]; !ok {
		t.Errorf("base prefix 'net' missing with empty config")
	}
}

// parseHyphenPrefixes normalises a comma-separated env value into the set of
// hyphen-form id prefixes. Unlike the legacy 3-char parser it does NOT enforce
// a fixed length (`ns`/`mt`/`vt` are 2 chars) but DOES drop tokens that already
// contain a hyphen (the hyphen is the id separator, never part of the prefix).
func TestParseHyphenPrefixes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"single 3-char", "ins", []string{"ins"}},
		{"two-char allowed", "ns", []string{"ns"}},
		{"multi with spaces", " ins , ns ", []string{"ins", "ns"}},
		{"lowercases", "INS", []string{"ins"}},
		{"drops token containing hyphen", "ins,vt-,ns", []string{"ins", "ns"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseHyphenPrefixes(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("parseHyphenPrefixes(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("parseHyphenPrefixes(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}

// buildHyphenPrefixes must include the canonical going-forward hyphen prefixes
// AND any extra prefixes supplied via config — so a new domain's hyphen-form id
// can be routed at the authz edge without a corelib release (only an env change
// on the gateway), mirroring the legacy 3-char extra path.
func TestBuildHyphenPrefixes_MergesExtras(t *testing.T) {
	m := buildHyphenPrefixes("foo,bar")

	// Canonical base prefix still present.
	if _, ok := m["ins"]; !ok {
		t.Errorf("canonical hyphen prefix 'ins' missing from built set")
	}
	if _, ok := m["ns"]; !ok {
		t.Errorf("canonical hyphen prefix 'ns' missing from built set")
	}
	// Extras merged.
	for _, p := range []string{"foo", "bar"} {
		if _, ok := m[p]; !ok {
			t.Errorf("extra hyphen prefix %q not merged into built set", p)
		}
	}
}

// Empty config leaves exactly the canonical hyphen set (no accidental widening).
func TestBuildHyphenPrefixes_EmptyConfigIsBaseOnly(t *testing.T) {
	base := buildHyphenPrefixes("")
	if _, ok := base["foo"]; ok {
		t.Errorf("unexpected hyphen prefix 'foo' present with empty config")
	}
	if _, ok := base["ins"]; !ok {
		t.Errorf("canonical hyphen prefix 'ins' missing with empty config")
	}
}
