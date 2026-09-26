package main

import (
	"strings"
	"testing"
)

// TestRefuseTypedDependencyTarget pins ga-1qu26u: a `type:id` target — the
// form `bd create --deps` accepts — is refused by `bd dep add` with the
// --type rewrite, instead of falling through to the cross-prefix fallback
// and being written verbatim as a foreign id.
func TestRefuseTypedDependencyTarget(t *testing.T) {
	refused := map[string]string{
		"blocks:ga-x":          "--type blocks",
		"depends-on:ga-x":      "--type blocks",
		"blocked-by:qc-abc":    "--type blocks",
		"parent-child:ga-x.1":  "--type parent-child",
		"discovered-from:bd-1": "--type discovered-from",
	}
	for target, want := range refused {
		err := refuseTypedDependencyTarget(target)
		if err == nil {
			t.Errorf("%q: accepted, want refusal", target)
			continue
		}
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "bd dep add <from>") {
			t.Errorf("%q: err = %q, want the rewrite with %s", target, err, want)
		}
	}
	for _, target := range []string{"ga-x", "qc-abc", "external:jira:ABC-1", "http://x", "nonsense:ga-x", ""} {
		if err := refuseTypedDependencyTarget(target); err != nil {
			t.Errorf("%q: refused (%v), want accepted", target, err)
		}
	}
}

// TestCrossPrefixDependencyTarget pins the tightened fallback: only an
// id-shaped target with a different prefix is taken verbatim.
func TestCrossPrefixDependencyTarget(t *testing.T) {
	cases := []struct {
		from, target string
		ok           bool
	}{
		{"ga-abc", "qc-xyz", true},
		{"ga-abc", "qc-xyz.2", true},
		{"ga-abc", "ga-xyz", false}, // same project: must resolve locally
		{"ga-abc", "blocks:ga-x", false},
		{"ga-abc", "nonsense:ga-x", false},
		{"ga-abc", "qc-", false}, // prefix with no id
		{"ga-abc", "xyz", false}, // no prefix at all
		{"abc", "qc-xyz", false}, // source has no prefix
	}
	for _, tc := range cases {
		got, ok := crossPrefixDependencyTarget(tc.from, tc.target)
		if ok != tc.ok || (ok && got != tc.target) {
			t.Errorf("(%q, %q) = (%q, %v), want ok=%v", tc.from, tc.target, got, ok, tc.ok)
		}
	}
}
