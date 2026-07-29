// Pure-Go tests for memory command helpers. No cgo / Dolt dependency, so this
// file carries no build tag and compiles under CGO_ENABLED=0 with gms_pure_go.

package main

import "testing"

// TestMatchesKnownCommand guards the `bd remember <subcommand>` misfire fix
// (GH#4401): a bare word that names a real top-level command must be flagged so
// it is not silently stored as memory content, while genuine multi-word
// insights (and arbitrary single words) must pass through untouched.
func TestMatchesKnownCommand(t *testing.T) {
	cases := []struct {
		name     string
		insight  string
		wantName string
		wantHit  bool
	}{
		{"sibling command recall", "recall", "recall", true},
		{"sibling command forget", "forget", "forget", true},
		{"sibling command memories", "memories", "memories", true},
		{"remember itself", "remember", "remember", true},
		{"case insensitive", "ReCall", "recall", true},
		{"multi-word insight", "always run tests with the -race flag", "", false},
		{"leading command word in a phrase", "recall the auth design", "", false},
		{"non-command single word", "zorblax", "", false},
		{"empty", "", "", false},
		{"whitespace only", "   ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotHit := matchesKnownCommand(rememberCmd, tc.insight)
			if gotHit != tc.wantHit {
				t.Fatalf("matchesKnownCommand(%q) hit = %v, want %v", tc.insight, gotHit, tc.wantHit)
			}
			if gotHit && gotName != tc.wantName {
				t.Errorf("matchesKnownCommand(%q) name = %q, want %q", tc.insight, gotName, tc.wantName)
			}
		})
	}
}
