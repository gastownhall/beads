package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// pollutedIDs returns the IDs detectTestPollution reports for issues.
func pollutedIDs(issues []*types.Issue) map[string]pollutionResult {
	got := make(map[string]pollutionResult)
	for _, p := range detectTestPollution(issues) {
		got[p.issue.ID] = p
	}
	return got
}

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

// TestDetectTestPollution_SkipsClosedAndEpics pins GH#5025: closed issues and
// epics are never pollution candidates, however fixture-like they look,
// while the same fixture as an open task is still reported.
func TestDetectTestPollution_SkipsClosedAndEpics(t *testing.T) {
	closedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	issues := []*types.Issue{
		{ID: "bd-c1", Title: "Test issue", Status: types.StatusClosed, ClosedAt: &closedAt, IssueType: types.TypeTask},
		{ID: "bd-c2", Title: "test-fixture data", Status: types.StatusClosed, ClosedAt: &closedAt, IssueType: types.TypeBug},
		{ID: "bd-e1", Title: "Test issue", Status: types.StatusOpen, IssueType: types.TypeEpic},
		{ID: "bd-e2", Title: "test-fixture data", Status: types.StatusClosed, ClosedAt: &closedAt, IssueType: types.TypeEpic},
		{ID: "bd-o1", Title: "Test issue", Status: types.StatusOpen, IssueType: types.TypeTask},
	}

	got := pollutedIDs(issues)
	for _, id := range []string{"bd-c1", "bd-c2", "bd-e1", "bd-e2"} {
		if p, ok := got[id]; ok {
			t.Errorf("%s (%q) reported as pollution (score %.2f, reasons %v); closed issues and epics are never candidates", id, p.issue.Title, p.score, p.reasons)
		}
	}
	if _, ok := got["bd-o1"]; !ok {
		t.Errorf("open task %q not reported; eligibility must not hide open non-epic fixtures", "Test issue")
	}
}

// TestDetectTestPollution_OnlyOpenPersistentCandidates pins GH#5025's
// eligibility: only open, non-ephemeral issues are pollution candidates.
// Active or parked work (in progress, blocked, deferred, pinned, hooked) and
// ephemeral wisps are skipped, while the identical fixture as an open
// Ephemeral=false task, NoHistory included, is still reported.
func TestDetectTestPollution_OnlyOpenPersistentCandidates(t *testing.T) {
	fixture := func(id string, status types.Status) *types.Issue {
		return &types.Issue{ID: id, Title: "Test issue", Status: status, IssueType: types.TypeTask}
	}
	ephemeral := fixture("bd-eph", types.StatusOpen)
	ephemeral.Ephemeral = true
	noHistory := fixture("bd-nohist", types.StatusOpen)
	noHistory.NoHistory = true
	issues := []*types.Issue{
		fixture("bd-inprog", types.StatusInProgress),
		fixture("bd-blocked", types.StatusBlocked),
		fixture("bd-deferred", types.StatusDeferred),
		fixture("bd-pinned", types.StatusPinned),
		fixture("bd-hooked", types.StatusHooked),
		ephemeral,
		fixture("bd-open", types.StatusOpen),
		noHistory,
	}

	got := pollutedIDs(issues)
	for _, id := range []string{"bd-inprog", "bd-blocked", "bd-deferred", "bd-pinned", "bd-hooked", "bd-eph"} {
		if p, ok := got[id]; ok {
			t.Errorf("%s reported as pollution (score %.2f, reasons %v); only open, non-ephemeral issues are candidates", id, p.score, p.reasons)
		}
	}
	for _, id := range []string{"bd-open", "bd-nohist"} {
		if _, ok := got[id]; !ok {
			t.Errorf("%s (open, Ephemeral=false) not reported; it must stay eligible", id)
		}
	}
}

// TestDetectTestPollution_AuthoredContentVetoes pins GH#5025's protection
// for real work: a test-prefixed task is not reported once it carries a
// trimmed description of at least 20 bytes or any nonblank design,
// acceptance criteria or notes. Whitespace never counts as authored content.
func TestDetectTestPollution_AuthoredContentVetoes(t *testing.T) {
	desc20 := "Repro steps attached" // 20 bytes
	desc19 := "Repro steps attache"  // 19 bytes
	if len(desc20) != 20 || len(desc19) != 19 {
		t.Fatalf("fixture lengths = %d/%d, want 20/19", len(desc20), len(desc19))
	}
	task := func(id string, edit func(*types.Issue)) *types.Issue {
		issue := &types.Issue{ID: id, Title: "test-runner hangs on sync", Status: types.StatusOpen, IssueType: types.TypeTask}
		edit(issue)
		return issue
	}
	vetoed := []*types.Issue{
		task("bd-desc20", func(i *types.Issue) { i.Description = desc20 }),
		task("bd-desc20pad", func(i *types.Issue) { i.Description = "\n  " + desc20 + "  \n" }),
		task("bd-design", func(i *types.Issue) { i.Design = "Use a bounded retry" }),
		task("bd-accept", func(i *types.Issue) { i.AcceptanceCriteria = "Sync completes" }),
		task("bd-notes", func(i *types.Issue) { i.Notes = "seen on CI" }),
	}
	reported := []*types.Issue{
		task("bd-desc19pad", func(i *types.Issue) { i.Description = "   " + desc19 + "   " }),
		task("bd-blankprose", func(i *types.Issue) {
			i.Design, i.AcceptanceCriteria, i.Notes = " \t", "\n", "  "
		}),
	}

	got := pollutedIDs(append(append([]*types.Issue{}, vetoed...), reported...))
	for _, issue := range vetoed {
		if p, ok := got[issue.ID]; ok {
			t.Errorf("%s reported as pollution (score %.2f, reasons %v); authored content must veto it", issue.ID, p.score, p.reasons)
		}
	}
	for _, issue := range reported {
		if _, ok := got[issue.ID]; !ok {
			t.Errorf("%s not reported; a 19-byte trimmed description or blank prose is not authored content", issue.ID)
		}
	}
}
