package main

import "testing"

// TestClassifiedReadOpen pins the four-way permission decision for the root
// store's ClassifiedRead (bd-6zf0q5, AC-4): the permission exists so
// bd ready/bd list --ready may run the lazy defer-wake sweep (GH#804,
// be-vbhpf), and that sweep is a write, so each input forbids it on its own.
// Only a plain classified read — no strict --readonly, no preview, no
// migration freeze — may carry the permission; the other fifteen
// combinations must refuse, so an always-false or always-true helper and a
// dropped guard each fail a row.
func TestClassifiedReadOpen(t *testing.T) {
	cases := []struct {
		name           string
		policyReadOnly bool
		readonlyMode   bool
		previewMode    bool
		frozen         bool
		want           bool
	}{
		{"plain classified read", true, false, false, false, true},
		{"strict readonly", true, true, false, false, false},
		{"preview", true, false, true, false, false},
		{"frozen", true, false, false, true, false},
		{"strict readonly and preview", true, true, true, false, false},
		{"strict readonly and frozen", true, true, false, true, false},
		{"preview and frozen", true, false, true, true, false},
		{"strict readonly, preview and frozen", true, true, true, true, false},
		{"not a classified-read policy", false, false, false, false, false},
		{"not classified, strict readonly", false, true, false, false, false},
		{"not classified, preview", false, false, true, false, false},
		{"not classified, frozen", false, false, false, true, false},
		{"not classified, strict readonly and preview", false, true, true, false, false},
		{"not classified, strict readonly and frozen", false, true, false, true, false},
		{"not classified, preview and frozen", false, false, true, true, false},
		{"not classified, strict readonly, preview and frozen", false, true, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifiedReadOpen(tc.policyReadOnly, tc.readonlyMode, tc.previewMode, tc.frozen); got != tc.want {
				t.Errorf("classifiedReadOpen(policyReadOnly=%t, readonlyMode=%t, previewMode=%t, frozen=%t) = %t, want %t",
					tc.policyReadOnly, tc.readonlyMode, tc.previewMode, tc.frozen, got, tc.want)
			}
		})
	}
}
