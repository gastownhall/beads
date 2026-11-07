//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDoctorReportsBlockedStatusWithoutFailing pins the full-doctor wiring of
// the Blocked Status check (GH#6565): a status='blocked' row whose stored
// dependency-blocked flag is clear makes bd doctor warn with the count and
// the id for review, while the warning alone leaves OverallOK and the row's
// status untouched. Baseline and drift runs share one workspace, so
// unrelated warnings cannot mask the OverallOK claim.
func TestDoctorReportsBlockedStatusWithoutFailing(t *testing.T) {
	repo := t.TempDir()
	store := newTestStoreIsolatedDB(t, filepath.Join(repo, ".beads", "dolt"), "dbsw")
	t.Setenv("BEADS_DIR", filepath.Join(repo, ".beads"))
	ctx := context.Background()
	held := &types.Issue{ID: "dbsw-held", Title: "held past its blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, held, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	blockedStatusCheck := func(result doctorResult) doctorCheck {
		i := slices.IndexFunc(result.Checks, func(c doctorCheck) bool { return c.Name == "Blocked Status" })
		if i < 0 {
			names := make([]string, len(result.Checks))
			for j, c := range result.Checks {
				names[j] = c.Name
			}
			t.Fatalf("bd doctor ran no Blocked Status check; checks were %v", names)
		}
		return result.Checks[i]
	}

	// Warm-up, then baseline: the same doctor run sequence twice, so writes
	// only the first run makes (version tracking, migrations) cannot read as
	// a difference the check caused.
	_ = runDiagnostics(repo)
	baseline := runDiagnostics(repo)
	if c := blockedStatusCheck(baseline); c.Status != statusOK {
		t.Fatalf("clean database: Blocked Status = %q (%s / %s), want %q", c.Status, c.Message, c.Detail, statusOK)
	}

	// The drift row GH#6565 names: a manual hold whose stored blocked flag
	// the dependency graph no longer sets.
	if err := store.UpdateIssue(ctx, held.ID, map[string]interface{}{"status": string(types.StatusBlocked)}, "test"); err != nil {
		t.Fatalf("hold the status: %v", err)
	}

	after := runDiagnostics(repo)
	c := blockedStatusCheck(after)
	if c.Status != statusWarning {
		t.Fatalf("drift row: Blocked Status = %q (%s / %s), want %q", c.Status, c.Message, c.Detail, statusWarning)
	}
	if !strings.Contains(c.Message, "1 issue(s) remain in status 'blocked'") || !strings.Contains(c.Detail, held.ID) {
		t.Errorf("warning = %q (%s), want the count and the held id %s", c.Message, c.Detail, held.ID)
	}
	if c.Fix == "" || !strings.Contains(c.Fix, "bd update <id> --status open") {
		t.Errorf("Fix = %q, want the manual-release guidance", c.Fix)
	}
	if after.OverallOK != baseline.OverallOK {
		t.Errorf("OverallOK flipped from %v to %v on a warn-only check", baseline.OverallOK, after.OverallOK)
	}
	issue, err := store.GetIssue(ctx, held.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Status != types.StatusBlocked {
		t.Errorf("status = %q after bd doctor, want 'blocked' untouched", issue.Status)
	}
}
