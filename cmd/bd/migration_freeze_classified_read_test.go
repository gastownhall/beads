package main

import "testing"

// TestClassifiedReadOpen pins the single decision point that decides whether
// an ordinary read command's store open may run bd's own background
// maintenance (the lazy defer-wake sweep): a read-classified command with
// neither the strict --readonly nor the --dry-run/--inspect preview refusal
// set keeps its maintenance rights — EXCEPT during a migration freeze, when
// the freeze's read-only-during-freeze contract suppresses them (#7634).
// The direct-server store consumes this through deferWakeSweepEligible and
// the embedded store through the store factory's
// SetDeferWakeSweepEnabled, both from the one commandFreeze snapshot taken
// per invocation.
func TestClassifiedReadOpen(t *testing.T) {
	cases := []struct {
		name         string
		policyRead   bool
		readonlyMode bool
		previewMode  bool
		frozen       bool
		want         bool
	}{
		{name: "classified read keeps maintenance", policyRead: true, want: true},
		{name: "strict readonly mode never maintains", policyRead: true, readonlyMode: true, want: false},
		{name: "preview mode never maintains", policyRead: true, previewMode: true, want: false},
		// The freeze is the only new input: a frozen workspace suppresses
		// the sweep even for the ordinary read that would otherwise run it.
		{name: "frozen classified read stands down", policyRead: true, frozen: true, want: false},
		// Combinations stay suppressed by any refusal: freeze does not
		// re-enable anything, and readonly/preview stay authoritative.
		{name: "frozen strict readonly stays down", policyRead: true, readonlyMode: true, frozen: true, want: false},
		{name: "frozen preview stays down", policyRead: true, previewMode: true, frozen: true, want: false},
		// A write-classified command (policyRead false) never flows through
		// this predicate's maintenance arm at all; false documents that the
		// freeze does not silently widen write behavior.
		{name: "non-read open is not a classified read", policyRead: false, want: false},
		{name: "non-read open frozen is not a classified read", policyRead: false, frozen: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifiedReadOpen(tc.policyRead, tc.readonlyMode, tc.previewMode, tc.frozen); got != tc.want {
				t.Fatalf("classifiedReadOpen(policyReadOnly=%v, readonlyMode=%v, previewMode=%v, frozen=%v) = %v, want %v",
					tc.policyRead, tc.readonlyMode, tc.previewMode, tc.frozen, got, tc.want)
			}
		})
	}
}
