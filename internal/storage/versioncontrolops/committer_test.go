package versioncontrolops

import "testing"

// TestResolveCommitter pins the per-field identity rule both store modes
// share (#7628): explicit value, else GIT_AUTHOR_*, else beads/beads@local.
func TestResolveCommitter(t *testing.T) {
	tests := []struct {
		name               string
		envName, envEmail  string
		argName, argEmail  string
		wantName, wantMail string
	}{
		{name: "defaults", wantName: "beads", wantMail: "beads@local"},
		{name: "env", envName: "Env Author", envEmail: "env@example.com", wantName: "Env Author", wantMail: "env@example.com"},
		{name: "explicit wins over env", envName: "Env Author", envEmail: "env@example.com",
			argName: "Arg Author", argEmail: "arg@example.com", wantName: "Arg Author", wantMail: "arg@example.com"},
		{name: "fields resolve independently", envEmail: "env@example.com", argName: "Arg Author",
			wantName: "Arg Author", wantMail: "env@example.com"},
		{name: "env name fills in beside an explicit email", envName: "Env Author", argEmail: "arg@example.com",
			wantName: "Env Author", wantMail: "arg@example.com"},
		// Emptiness, not blankness, falls through: a whitespace-only value is
		// a resolved value, never trimmed or skipped, matching the server rule.
		{name: "whitespace env value is kept", envName: " ", envEmail: " ", wantName: " ", wantMail: " "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GIT_AUTHOR_NAME", tc.envName)
			t.Setenv("GIT_AUTHOR_EMAIL", tc.envEmail)
			got := ResolveCommitter(tc.argName, tc.argEmail)
			if got.Name != tc.wantName || got.Email != tc.wantMail {
				t.Fatalf("ResolveCommitter(%q, %q) = %+v, want {%s %s}", tc.argName, tc.argEmail, got, tc.wantName, tc.wantMail)
			}
			if want := tc.wantName + " <" + tc.wantMail + ">"; got.Author() != want {
				t.Fatalf("Author() = %q, want %q", got.Author(), want)
			}
		})
	}
}
