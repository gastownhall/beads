//go:build cgo

package main

import (
	"strings"
	"testing"
)

// TestProxiedServerClaimPathsJournalSidecar: GH#6146 — the proxied-server
// routes of `bd update` (plain, --claim, and claim plus an explicit status)
// and of `bd ready --claim` / `bd close --claim-next` append the same
// sidecar records the direct routes do, in the client workspace, under
// canonical issue ids.
func TestProxiedServerClaimPathsJournalSidecar(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	run := func(t *testing.T, p proxiedProject, args ...string) string {
		t.Helper()
		stdout, stderr, err := bdProxiedRunEnv(t, bd, p.dir,
			[]string{"BD_AUDIT_ENABLED=1", "BD_ACTOR=" + claimAuditActor}, args...)
		if err != nil {
			t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
		return stdout
	}
	sidecar := func(p proxiedProject) string { return p.beadsDir }

	t.Run("update_claim_journals_field_changes", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pu")
		issue := bdProxiedCreate(t, bd, p.dir, "Claim target")

		run(t, p, "update", issue.ID, "--claim")

		assertClaimAuditFieldChanges(t, sidecar(p), issue.ID, []claimAuditFieldChange{
			{Field: "status", OldValue: "open", NewValue: "in_progress"},
			{Field: "assignee", OldValue: "", NewValue: claimAuditActor},
		})
	})

	t.Run("update_claim_explicit_status_journals_once", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pt")
		issue := bdProxiedCreate(t, bd, p.dir, "Claim with explicit status")

		run(t, p, "update", issue.ID, "--claim", "--status", "blocked")

		assertClaimAuditFieldChanges(t, sidecar(p), issue.ID, []claimAuditFieldChange{
			{Field: "status", OldValue: "open", NewValue: "blocked"},
			{Field: "assignee", OldValue: "", NewValue: claimAuditActor},
		})
	})

	t.Run("explicit_status_priority_update_journals", func(t *testing.T) {
		t.Parallel()
		// Ordinary explicit-field updates gained sidecar parity on this
		// route with the same change: before, the proxied update journaling
		// nothing at all.
		p := newSharedProxiedProject(t, bd, "px")
		issue := bdProxiedCreate(t, bd, p.dir, "Explicit fields")

		run(t, p, "update", issue.ID, "--status", "in_progress", "--priority", "1")

		assertClaimAuditFieldChanges(t, sidecar(p), issue.ID, []claimAuditFieldChange{
			{Field: "status", OldValue: "open", NewValue: "in_progress"},
			{Field: "priority", OldValue: "2", NewValue: "1"},
		})
	})

	t.Run("ready_claim_journals_claimed_record", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pr")
		issue := bdProxiedCreate(t, bd, p.dir, "Ready to claim")

		run(t, p, "ready", "--claim")

		assertClaimAuditClaimed(t, sidecar(p), issue.ID, "in_progress")
		if rows := claimAuditFieldChanges(t, claimAuditEntries(t, sidecar(p)), issue.ID); len(rows) != 0 {
			t.Errorf("bd ready --claim wrote field_change rows %+v for %s, want none", rows, issue.ID)
		}
	})

	t.Run("close_claim_next_journals_claimed_record", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pc")
		done := bdProxiedCreate(t, bd, p.dir, "Issue to close")
		next := bdProxiedCreate(t, bd, p.dir, "Ready successor")

		run(t, p, "close", done.ID, "--claim-next")

		entries := claimAuditEntries(t, sidecar(p))
		assertClaimAuditFieldChanges(t, sidecar(p), done.ID, []claimAuditFieldChange{
			{Field: "status", OldValue: "open", NewValue: "closed"},
		})
		assertClaimAuditClaimed(t, sidecar(p), next.ID, "in_progress")
		closeIdx, claimedIdx := -1, -1
		for i, e := range entries {
			if e.Kind == "field_change" && e.IssueID == done.ID {
				closeIdx = i
			}
			if e.Kind == "claimed" && e.IssueID == next.ID {
				claimedIdx = i
			}
		}
		if closeIdx == -1 || claimedIdx == -1 || closeIdx > claimedIdx {
			t.Errorf("close record for %s at %d, claimed record for %s at %d, want close first", done.ID, closeIdx, next.ID, claimedIdx)
		}
	})

	t.Run("idempotent_reclaim_appends_nothing", func(t *testing.T) {
		t.Parallel()
		// A reclaim by the actor who already holds the issue is a no-op
		// (Changed=false), so it must not stack duplicate rows on top of the
		// first claim's.
		p := newSharedProxiedProject(t, bd, "pi")
		issue := bdProxiedCreate(t, bd, p.dir, "Reclaim target")

		run(t, p, "update", issue.ID, "--claim")
		run(t, p, "update", issue.ID, "--claim")

		assertClaimAuditFieldChanges(t, sidecar(p), issue.ID, []claimAuditFieldChange{
			{Field: "status", OldValue: "open", NewValue: "in_progress"},
			{Field: "assignee", OldValue: "", NewValue: claimAuditActor},
		})
	})
}
