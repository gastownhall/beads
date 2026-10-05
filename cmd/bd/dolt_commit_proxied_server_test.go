package main

import "testing"

// TestRenderDoltCommitAuthor pins the '--author' argument the proxied flush
// passes to DOLT_COMMIT (GH#4995). Dolt parses the git "Name <email>" grammar
// and rejects anything else, and the name half is an actor string the caller
// controls, so a stray bracket or newline must be stripped rather than turned
// into a failed flush — the flush is the only commit batch mode keeps.
func TestRenderDoltCommitAuthor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		actor string
		email string
		want  string
	}{
		{name: "actor and email", actor: "agent-7", email: "agent7@example.com", want: "agent-7 <agent7@example.com>"},
		{name: "trims surrounding space", actor: "  agent-7 ", email: " agent7@example.com\n", want: "agent-7 <agent7@example.com>"},
		{name: "strips brackets the grammar owns", actor: "agent <7>", email: "a@b", want: "agent 7 <a@b>"},
		{name: "strips embedded newline", actor: "agent\n7", email: "a@b", want: "agent 7 <a@b>"},
		{name: "defaults an empty identity", actor: "", email: "", want: "beads <beads@localhost>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderDoltCommitAuthor(tc.actor, tc.email); got != tc.want {
				t.Fatalf("renderDoltCommitAuthor(%q, %q) = %q, want %q", tc.actor, tc.email, got, tc.want)
			}
		})
	}
}
