//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedReadyClaimJournalsClaimedRecord: GH#6146 — `bd ready --claim`
// leaves one post-state-only claimed record for the row it returned, in the
// built-in open status and from a configured custom active status alike, and
// writes no field_change rows for the claim.
func TestEmbeddedReadyClaimJournalsClaimedRecord(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt ready tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("built_in_open", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ro")
		issue := bdCreate(t, bd, dir, "Ready to claim")

		claimAuditRun(t, bd, dir, "ready", "--claim")

		assertClaimAuditClaimed(t, beadsDir, issue.ID, "in_progress")
		if rows := claimAuditFieldChanges(t, claimAuditEntries(t, beadsDir), issue.ID); len(rows) != 0 {
			t.Errorf("bd ready --claim wrote field_change rows %+v for %s, want none (the claimed record carries the move)", rows, issue.ID)
		}
	})

	t.Run("custom_active_status", func(t *testing.T) {
		t.Parallel()
		// A claimable row can sit in a configured custom active status, not
		// only open, so the record must carry the returned post-state rather
		// than an assumed prior.
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ra")
		cfg := exec.Command(bd, "config", "set", "status.custom", "in_review:active")
		cfg.Dir = dir
		cfg.Env = bdEnv(dir)
		if out, err := cfg.CombinedOutput(); err != nil {
			t.Fatalf("bd config set status.custom failed: %v\n%s", err, out)
		} else if strings.Contains(string(out), "not a recognized config key") {
			t.Fatalf("bd config set status.custom warned about an unrecognized key:\n%s", out)
		}
		issue := bdCreate(t, bd, dir, "Custom active status")
		claimAuditRun(t, bd, dir, "update", issue.ID, "--status", "in_review")

		claimAuditRun(t, bd, dir, "ready", "--claim")

		assertClaimAuditClaimed(t, beadsDir, issue.ID, "in_progress")
	})

	t.Run("empty_ready_front_appends_nothing", func(t *testing.T) {
		t.Parallel()
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "re")
		issue := bdCreate(t, bd, dir, "Held by another actor")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "bob", "--status", "in_progress")

		claimAuditRun(t, bd, dir, "ready", "--claim")

		if entries := claimAuditEntries(t, beadsDir); len(entries) != 0 {
			t.Errorf("bd ready --claim with an empty front appended %+v, want nothing", entries)
		}
	})
}
