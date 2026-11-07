//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// TestApplyFixListLeavesBlockedStatusAlone pins the applyFixList dispatch for
// the Blocked Status check (GH#6565): bd doctor --fix names the deliberate
// refusal — status left unchanged, manual review guidance from the check's
// own Fix text — instead of the default branch's "No automatic fix
// available", which reads as a gap in --fix. The case claims no repair: the
// summary counts stay at zero.
func TestApplyFixListLeavesBlockedStatusAlone(t *testing.T) {
	check := doctorCheck{
		Name:    doctor.BlockedStatusCheckName,
		Status:  statusWarning,
		Message: "1 issue(s) remain in status 'blocked' while their stored dependency-blocked flag is clear",
		Detail:  "dbsw-held",
		Fix:     "Review each row; release with: bd update <id> --status open",
	}

	out := captureStdout(t, func() error {
		applyFixList(t.TempDir(), []doctorCheck{check})
		return nil
	})

	if !strings.Contains(out, "Status left unchanged") {
		t.Errorf("applyFixList output must say the status is left unchanged:\n%s", out)
	}
	if !strings.Contains(out, "Manual fix: Review each row; release with: bd update <id> --status open") {
		t.Errorf("applyFixList output must carry the check's manual-review guidance:\n%s", out)
	}
	if strings.Contains(out, "No automatic fix available") {
		t.Errorf("Blocked Status is a deliberate refusal, not a missing fix:\n%s", out)
	}
	if !strings.Contains(out, "Fix summary: 0 fixed, 0 skipped, 0 errors") {
		t.Errorf("Blocked Status must claim no repair in the summary:\n%s", out)
	}
}

// TestDoctorFixPreservesBlockedStatus runs bd doctor --fix against a real
// database holding the GH#6565 drift row, with --yes and without it, and
// pins both legs: the held status survives, and --yes reports the
// manual-review guidance rather than a repair. Without --yes the run stops
// at the non-interactive guard before any fix list applies.
func TestDoctorFixPreservesBlockedStatus(t *testing.T) {
	repo := t.TempDir()
	store := newTestStoreIsolatedDB(t, filepath.Join(repo, ".beads", "dolt"), "dbsw")
	t.Setenv("BEADS_DIR", filepath.Join(repo, ".beads"))
	ctx := context.Background()
	held := &types.Issue{ID: "dbsw-held", Title: "held past its blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, held, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.UpdateIssue(ctx, held.ID, map[string]interface{}{"status": string(types.StatusBlocked)}, "test"); err != nil {
		t.Fatalf("hold the status: %v", err)
	}

	origYes := doctorYes
	t.Cleanup(func() { doctorYes = origYes })

	driftResult := func() doctorResult {
		shared := doctor.NewSharedStore(repo)
		defer shared.Close()
		check := convertWithCategory(doctor.CheckBlockedStatusWithStore(shared), doctor.CategoryData)
		if check.Status != statusWarning || !strings.Contains(check.Detail, held.ID) {
			t.Fatalf("fixture: Blocked Status = %q (%s / %s), want a warning naming %s", check.Status, check.Message, check.Detail, held.ID)
		}
		return doctorResult{Path: repo, Checks: []doctorCheck{check}}
	}
	statusStillHeld := func(label string) {
		issue, err := store.GetIssue(ctx, held.ID)
		if err != nil {
			t.Fatalf("GetIssue: %v", err)
		}
		if issue.Status != types.StatusBlocked {
			t.Errorf("%s: status = %q after bd doctor --fix, want 'blocked' untouched", label, issue.Status)
		}
	}

	t.Run("yes_reports_guidance_and_repairs_nothing", func(t *testing.T) {
		doctorYes = true
		out := captureStdout(t, func() error {
			applyFixes(driftResult(), doctor.AssessSchemaFixGate(repo))
			return nil
		})
		if !strings.Contains(out, "Status left unchanged") {
			t.Errorf("--fix output must say the status is left unchanged:\n%s", out)
		}
		if strings.Contains(out, "  ✓ Fixed") {
			t.Errorf("--fix claimed a repair for a check that refuses one:\n%s", out)
		}
		statusStillHeld("--yes")
	})

	t.Run("without_yes_stops_before_fixing_and_changes_nothing", func(t *testing.T) {
		doctorYes = false
		withNullStdin(t)
		// captureStdout and captureStderr both hold stdioMutex, so they
		// cannot nest; the guard's notice goes to stderr, and stdout alone
		// shows the run stopped before any fix list applied.
		out := captureStdout(t, func() error {
			applyFixes(driftResult(), doctor.AssessSchemaFixGate(repo))
			return nil
		})
		if !strings.Contains(out, "Fixable issues") {
			t.Errorf("without --yes the run must still list the fixable check, stdout:\n%s", out)
		}
		if strings.Contains(out, "Fix summary") || strings.Contains(out, "Fixing Blocked Status") {
			t.Errorf("without --yes no fix list may apply, stdout:\n%s", out)
		}
		statusStillHeld("no --yes")
	})
}
