package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Behavioral tests for the test-pollution scorer (GH#5025). They run through
// the package-main detectTestPollution seam so they also compile against the
// pre-fix scorer, which is what makes them usable as red/green evidence; the
// scorer itself lives in cmd/bd/doctor/pollution.go and is shared with the
// doctor validation gate.

// TestDetectTestPollution_RealWorkNotFlagged reproduces GH#5025: a legitimate
// open P1 epic whose title merely starts with "Test-" and that was bulk
// imported alongside other issues must not be scored as test pollution.
func TestDetectTestPollution_RealWorkNotFlagged(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	epic := &types.Issue{
		ID:          "bd-a1b2",
		Title:       "Test-quality architecture",
		Description: strings.Repeat("Rework fixtures and harness layering. ", 5),
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   created,
	}
	issues := []*types.Issue{epic}
	for i := 0; i < 10; i++ {
		issues = append(issues, &types.Issue{
			ID:          "bd-bulk" + string(rune('a'+i)),
			Title:       "Imported work item",
			Description: strings.Repeat("Real engineering work with context. ", 3),
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			CreatedAt:   created,
		})
	}

	for _, p := range detectTestPollution(issues) {
		if p.issue.ID == epic.ID {
			t.Fatalf("open P1 epic %q flagged as pollution (score %.2f, reasons %v)", epic.Title, p.score, p.reasons)
		}
	}
}

// TestDetectTestPollution_SkipsClosedAndEpics pins AC-1 at the scorer: closed
// issues and epics are never pollution candidates, however strong their title
// or description signals are.
func TestDetectTestPollution_SkipsClosedAndEpics(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	issues := []*types.Issue{
		{ID: "bd-closed1", Title: "test-closed leftover", Status: types.StatusClosed, IssueType: types.TypeTask, CreatedAt: created},
		{ID: "bd-closed2", Title: "Sample issue archive", Status: types.StatusClosed, IssueType: types.TypeTask, CreatedAt: created},
		{ID: "bd-epic1", Title: "test-quality architecture", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: created},
		{ID: "bd-epic2", Title: "Test issue rollout epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic, CreatedAt: created},
	}

	if got := detectTestPollution(issues); len(got) != 0 {
		for _, p := range got {
			t.Errorf("ineligible issue %s (%s/%s) reported as pollution (score %.2f)", p.issue.ID, p.issue.Status, p.issue.IssueType, p.score)
		}
	}
}

// TestDetectTestPollution_ExcludesNonOpenAndEphemeral pins AC-2: non-open and
// ephemeral issues are excluded, while an identical open non-epic issue with
// Ephemeral=false — including a NoHistory wisp — remains eligible.
func TestDetectTestPollution_ExcludesNonOpenAndEphemeral(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fixture := func(id string, mutate func(*types.Issue)) *types.Issue {
		issue := &types.Issue{
			ID:        id,
			Title:     "test-" + id + " leftover",
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: created,
		}
		if mutate != nil {
			mutate(issue)
		}
		return issue
	}
	issues := []*types.Issue{
		fixture("bd-inprog", func(i *types.Issue) { i.Status = types.StatusInProgress }),
		fixture("bd-blocked", func(i *types.Issue) { i.Status = types.StatusBlocked }),
		fixture("bd-defer", func(i *types.Issue) { i.Status = types.StatusDeferred }),
		fixture("bd-eph", func(i *types.Issue) { i.Ephemeral = true }),
		fixture("bd-open", nil),
		fixture("bd-nohist", func(i *types.Issue) { i.NoHistory = true }),
	}

	reported := map[string]bool{}
	for _, p := range detectTestPollution(issues) {
		reported[p.issue.ID] = true
	}
	for _, id := range []string{"bd-inprog", "bd-blocked", "bd-defer", "bd-eph"} {
		if reported[id] {
			t.Errorf("non-open or ephemeral issue %s reported as pollution", id)
		}
	}
	for _, id := range []string{"bd-open", "bd-nohist"} {
		if !reported[id] {
			t.Errorf("eligible open non-epic issue %s (Ephemeral=false) was not reported", id)
		}
	}
}
