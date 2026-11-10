package main

import (
	"strings"
	"testing"
)

// TestAuditSidecarHelpDocumentsKinds: GH#6146 — `bd audit`'s help is the
// shipped description of the optional interactions sidecar, so it must name
// both automatic entry kinds and what each carries, say the sidecar is
// best-effort and that create/comment write nothing, and point replay
// consumers at the events journal while keeping the per-issue history
// pointer.
func TestAuditSidecarHelpDocumentsKinds(t *testing.T) {
	long := auditCmd.Long
	for _, want := range []string{
		"field_change",
		"claimed",
		"best-effort",
		"bd create",
		"bd comment",
		"bd events tail",
		"bd history <id> --events",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("bd audit help is missing %q:\n%s", want, long)
		}
	}
	// The two kinds must be described by what they carry, so a consumer can
	// tell a transition record from a post-state-only one.
	for _, phrase := range []string{"old_value", "post-state"} {
		if !strings.Contains(long, phrase) {
			t.Errorf("bd audit help does not describe the kinds' payloads (missing %q):\n%s", phrase, long)
		}
	}
}
