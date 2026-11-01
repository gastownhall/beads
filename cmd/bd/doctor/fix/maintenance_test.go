package fix

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestStaleClosedIssues_NoDatabase(t *testing.T) {
	// Create temp directory with .beads but no database
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	// Should succeed without database
	err := StaleClosedIssues(tmpDir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestStaleClosedIssues_NoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Should fail without .beads directory
	err := StaleClosedIssues(tmpDir)
	if err == nil {
		t.Error("expected error for missing .beads directory")
	}
}

// TestSelectStaleClosedHoldsBackRetainedTypes pins the stale-closed-issues
// fix's selection: pinned beads and beads of a type types.retained names are
// held back and counted. The fix deletes row by row with no tombstone, so a
// retained bead is never its to take; an unset setting holds nothing back.
func TestSelectStaleClosedHoldsBackRetainedTypes(t *testing.T) {
	op := &types.Issue{ID: "bd-op", IssueType: types.IssueType("op")}
	task := &types.Issue{ID: "bd-task", IssueType: types.TypeTask}
	pinned := &types.Issue{ID: "bd-pinned", IssueType: types.IssueType("op"), Pinned: true}
	issues := []*types.Issue{op, task, pinned}

	deletable, skippedPinned, retained := selectStaleClosed(issues, "op")
	if len(deletable) != 1 || deletable[0] != task || skippedPinned != 1 || retained != 1 {
		t.Errorf("selectStaleClosed(op retained) = %v, %d pinned, %d retained; want [bd-task], 1, 1", deletable, skippedPinned, retained)
	}
	deletable, skippedPinned, retained = selectStaleClosed(issues, "")
	if len(deletable) != 2 || skippedPinned != 1 || retained != 0 {
		t.Errorf("selectStaleClosed(unset) = %v, %d pinned, %d retained; want 2 deletable, 1, 0", deletable, skippedPinned, retained)
	}
}
