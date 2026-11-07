//go:build cgo

package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoctorReportsBlockedStatusWithoutFailing runs bd doctor on a server-mode
// workspace before and after an issue is set to status 'blocked' with nothing
// blocking it (bd-e5d8fd): Blocked Status reports it with its ID and the
// manual guidance, the issue keeps its status, and the warning does not change
// OverallOK, which every other check decides as before.
func TestDoctorReportsBlockedStatusWithoutFailing(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	store := newTestStoreIsolatedDB(t, filepath.Join(beadsDir, "dolt"), "dbs")
	t.Setenv("BEADS_DIR", beadsDir)
	ctx := context.Background()

	blockedStatus := func(result doctorResult) doctorCheck {
		t.Helper()
		i := slices.IndexFunc(result.Checks, func(c doctorCheck) bool { return c.Name == doctor.BlockedStatusCheckName })
		if i < 0 {
			t.Fatalf("bd doctor ran no %s check", doctor.BlockedStatusCheckName)
		}
		return result.Checks[i]
	}

	before := runDiagnostics(repo)
	if c := blockedStatus(before); c.Status != statusOK {
		t.Fatalf("%s on a database with no blocked issue = %s %q (%s), want ok", c.Name, c.Status, c.Message, c.Detail)
	}

	held := &types.Issue{Title: "held by hand", Status: types.StatusBlocked, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, held, "test"); err != nil {
		t.Fatal(err)
	}

	after := runDiagnostics(repo)
	c := blockedStatus(after)
	if c.Status != statusWarning || !strings.HasPrefix(c.Message, "1 issue(s) remain in status 'blocked'") ||
		!strings.HasPrefix(c.Detail, held.ID+". ") || !strings.Contains(c.Fix, "--status open") {
		t.Errorf("%s = %s %q (%s; fix %q), want a warning naming %s with the manual release", c.Name, c.Status, c.Message, c.Detail, c.Fix, held.ID)
	}
	if c.Category != doctor.CategoryData {
		t.Errorf("%s category = %q, want %q", c.Name, c.Category, doctor.CategoryData)
	}
	if after.OverallOK != before.OverallOK {
		t.Errorf("OverallOK went from %v to %v: the Blocked Status advisory must not decide it", before.OverallOK, after.OverallOK)
	}
	got, err := store.GetIssue(ctx, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.StatusBlocked {
		t.Errorf("bd doctor changed %s's status to %q", held.ID, got.Status)
	}
}
