//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestBlockedStateUpdatedAtIsUTC verifies that the blocked-state recompute
// path writes updated_at in true UTC, not local wall-clock time mislabeled
// as UTC. Regression test for GH#4298.
//
// Root cause: the embedded Dolt driver calls SetQueryTime(time.Now()) using
// local time. If the UPDATE omits updated_at, ON UPDATE CURRENT_TIMESTAMP
// fires and stores local time with a Z suffix. The fix explicitly binds
// updated_at = time.Now().UTC() so the auto-fill never triggers.
//
// This test MUST run with the embedded driver (not Docker) because the bug
// is specific to in-process SetQueryTime inheriting time.Local.
func TestBlockedStateUpdatedAtIsUTC(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	// Force a non-UTC local timezone for the duration of this test.
	// On CI machines running in UTC (offset=0) the bug is invisible,
	// so we must shift time.Local to expose the mislabeling.
	origLocal := time.Local
	time.Local = time.FixedZone("TEST-PDT", -7*3600) // UTC-7
	defer func() { time.Local = origLocal }()

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "tz", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.SetConfig(ctx, "issue_prefix", "tz"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := store.Commit(ctx, "init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Create blocker and dependent issues.
	blocker := &types.Issue{ID: "tz-blocker", Title: "Blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	blocked := &types.Issue{ID: "tz-blocked", Title: "Blocked", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, blocker, "tester"); err != nil {
		t.Fatalf("CreateIssue blocker: %v", err)
	}
	if err := store.CreateIssue(ctx, blocked, "tester"); err != nil {
		t.Fatalf("CreateIssue blocked: %v", err)
	}

	// Record approximate true UTC just before triggering the recompute.
	beforeUTC := time.Now().UTC()

	// Add a blocking dependency — triggers markDirectBlockingDependencySourceInTx
	// which updates is_blocked (and now updated_at) on tz-blocked.
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     "tz-blocked",
		DependsOnID: "tz-blocker",
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	afterUTC := time.Now().UTC()

	// Read the blocked issue back and check updated_at.
	issue, err := store.GetIssue(ctx, "tz-blocked")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	// The stored updated_at must be true UTC — within a reasonable tolerance
	// of our before/after bracket. Without the fix, it would be ~7 hours
	// behind (the UTC-7 offset applied by SetQueryTime(time.Now()) with
	// time.Local = UTC-7) and fail this check.
	const tolerance = 60 * time.Second
	if issue.UpdatedAt.Before(beforeUTC.Add(-tolerance)) || issue.UpdatedAt.After(afterUTC.Add(tolerance)) {
		t.Fatalf("updated_at appears to be local time mislabeled as UTC (GH#4298):\n"+
			"  got:    %v\n"+
			"  window: %v .. %v\n"+
			"  drift:  %v",
			issue.UpdatedAt.UTC(),
			beforeUTC, afterUTC,
			issue.UpdatedAt.Sub(beforeUTC))
	}
}
