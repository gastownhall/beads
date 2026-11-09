//go:build cgo

package main

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// testPollutionCheck returns the validate gate's Test Pollution check for
// the workspace at dir.
func testPollutionCheck(t *testing.T, dir string) doctorCheck {
	t.Helper()
	for _, cr := range collectValidateChecks(dir) {
		if cr.check.Name == "Test Pollution" {
			return cr.check
		}
	}
	t.Fatal("Test Pollution check not found")
	return doctorCheck{}
}

// TestValidateCheck_PollutionIgnoresClosedAndEpics pins GH#5025 on the
// validate gate: closed issues and epics with fixture titles are not counted
// as test pollution, matching the detailed pollution check.
func TestValidateCheck_PollutionIgnoresClosedAndEpics(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "pce")
	ctx := context.Background()

	closedAt := time.Now().UTC()
	for _, issue := range []*types.Issue{
		{Title: "Test issue", Status: types.StatusClosed, ClosedAt: &closedAt, Priority: 2, IssueType: types.TypeTask},
		{Title: "test-fixture data", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
	} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%q): %v", issue.Title, err)
		}
	}
	store.Close()

	if check := testPollutionCheck(t, tmpDir); check.Status != statusOK {
		t.Errorf("Test Pollution = %q (%s), want %q: closed issues and epics are not pollution", check.Status, check.Message, statusOK)
	}
}
