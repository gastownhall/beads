//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedUpdateClaimExplicitStatusJournalsOnce: GH#6146 — a claim
// riding an explicit --status journals the explicit transition plus the
// claim's assignee move: one status row (open→blocked), one assignee row
// (empty→alice), and no intermediate in_progress row or duplicates.
func TestEmbeddedUpdateClaimExplicitStatusJournalsOnce(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt update tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cs")
	claimed := bdCreate(t, bd, dir, "Claim with explicit status")

	claimAuditRun(t, bd, dir, "update", claimed.ID, "--claim", "--status", "blocked")

	assertClaimAuditFieldChanges(t, beadsDir, claimed.ID, []claimAuditFieldChange{
		{Field: "status", OldValue: "open", NewValue: "blocked"},
		{Field: "assignee", OldValue: "", NewValue: claimAuditActor},
	})
}
