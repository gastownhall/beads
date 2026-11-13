package main

import (
	"fmt"
	"slices"
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
		// From PR #5137: the same fixture closed, as an epic, and open.
		{ID: "bd-1", Title: "test issue fixture", Status: types.StatusClosed, IssueType: types.TypeTask},
		{ID: "bd-2", Title: "test issue fixture", Status: types.StatusOpen, IssueType: types.TypeEpic},
		{ID: "bd-3", Title: "test issue fixture", Status: types.StatusOpen, IssueType: types.TypeTask},
		// From PR #5137: the GH#5025 epic, open with a real description.
		{ID: "dcr-9f3", Title: "Test-quality architecture", Description: "Epic-scoped plan for improving component test suite quality across the monorepo.", Status: types.StatusOpen, IssueType: types.TypeEpic},
	}

	got := pollutedIDs(issues)
	for _, id := range []string{"bd-c1", "bd-c2", "bd-e1", "bd-e2", "bd-1", "bd-2", "dcr-9f3"} {
		if p, ok := got[id]; ok {
			t.Errorf("%s (%q) reported as pollution (score %.2f, reasons %v); closed issues and epics are never candidates", id, p.issue.Title, p.score, p.reasons)
		}
	}
	for _, id := range []string{"bd-o1", "bd-3"} {
		if _, ok := got[id]; !ok {
			t.Errorf("open task %s not reported; eligibility must not hide open non-epic fixtures", id)
		}
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
		// From PR #5137: a real infra bug with a test-prefixed title.
		{ID: "dcr-lzse", Title: "test-driver.sh isolation broken — constants.sh clobbers env", Description: "Long description of a real test-harness bug with repro and expected behavior.", Status: types.StatusOpen, IssueType: types.TypeBug},
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

// TestDetectTestPollution_IDAndCreationTimeAreNotEvidence pins GH#5025:
// counter-mode IDs (bd-1, bd-2, ...) and same-minute batch creation are how
// ordinary work and imports look, so neither is pollution evidence. Ten
// ordinary-title counter-ID tasks with empty descriptions created together
// are not reported, alone or as a batch, and no reported hit cites an ID or
// creation-time reason.
func TestDetectTestPollution_IDAndCreationTimeAreNotEvidence(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var batch []*types.Issue
	for i := 1; i <= 10; i++ {
		batch = append(batch, &types.Issue{
			ID:        fmt.Sprintf("bd-%d", i),
			Title:     fmt.Sprintf("Import billing ledger %d", i),
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: created,
		})
	}

	for _, issue := range batch {
		if p, ok := pollutedIDs([]*types.Issue{issue})[issue.ID]; ok {
			t.Errorf("alone: %s reported (score %.2f, reasons %v); counter IDs are not evidence", issue.ID, p.score, p.reasons)
		}
	}
	for id, p := range pollutedIDs(batch) {
		t.Errorf("batch: %s reported (score %.2f, reasons %v); same-minute creation is not evidence", id, p.score, p.reasons)
	}

	// From PR #5137: a same-minute import of test-prefixed real work.
	var imported []*types.Issue
	for i := 0; i < 12; i++ {
		imported = append(imported, &types.Issue{
			ID:          fmt.Sprintf("imp-%d", i),
			Title:       "test-quality report item",
			Description: "Imported engineering task from report batch with full body text here.",
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			CreatedAt:   created,
		})
	}
	for id, p := range pollutedIDs(imported) {
		t.Errorf("import: %s reported (score %.2f, reasons %v); bulk import of real work is not evidence", id, p.score, p.reasons)
	}

	fixture := &types.Issue{ID: "bd-11", Title: "Test issue", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: created}
	p, ok := pollutedIDs(append(batch, fixture))[fixture.ID]
	if !ok {
		t.Fatalf("%s (%q) not reported", fixture.ID, fixture.Title)
	}
	for _, reason := range p.reasons {
		if strings.Contains(reason, "Sequential") || strings.Contains(reason, "same minute") {
			t.Errorf("%s reasons %v cite ID or creation time", fixture.ID, p.reasons)
			break
		}
	}
}

// TestDetectTestPollution_ConfidenceBands pins GH#5025's weights, in tenths:
// a test title prefix +6, a generic test phrase +6 once, an empty trimmed
// description +2 and a short one +1; 0.7 reports, 0.9 is high confidence.
// One title signal is therefore review-only (medium) and only both title
// signals reach high confidence, whatever the ID or creation time.
func TestDetectTestPollution_ConfidenceBands(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		title, desc string
		score       float64
		high        bool
	}{
		{"Debug sync hangs", "", 0.8, false},
		{"Test issue", "", 1.4, true},
		{"Debug sync hangs", " wip ", 0.7, false},
		{"Test issue", "wip", 1.3, true},
		{"Write a sample issue template", "  ", 0.8, false},
		{"Sample issue for testing", "", 1.4, true},
		{"test-foo bare fixture", "", 0.8, false}, // from PR #5137: prefix plus empty description
	}
	for _, ids := range [][]string{{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5", "bd-6", "tmp-1"}, {"bd-q9x", "bd-r2k", "bd-z7m", "bd-a0p", "bd-w3c", "bd-k8d", "bd-t4f"}} {
		var issues []*types.Issue
		for i, tt := range tests {
			issues = append(issues, &types.Issue{ID: ids[i], Title: tt.title, Description: tt.desc, Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: created})
		}
		got := pollutedIDs(issues)
		for i, tt := range tests {
			p, ok := got[ids[i]]
			if !ok {
				t.Errorf("%s %q desc %q not reported, want score %.1f", ids[i], tt.title, tt.desc, tt.score)
				continue
			}
			if p.score != tt.score || p.highConfidence() != tt.high {
				t.Errorf("%s %q desc %q: score %v high %v (reasons %v), want score %v high %v", ids[i], tt.title, tt.desc, p.score, p.highConfidence(), p.reasons, tt.score, tt.high)
			}
		}
	}
}

// TestDetectTestPollution_ReasonOrder pins the order reasons are reported in
// (prefix, description, generic title), so JSON consumers see them in the
// same relative order as before GH#5025's reweighting.
func TestDetectTestPollution_ReasonOrder(t *testing.T) {
	issue := &types.Issue{ID: "bd-r1", Title: "Test issue", Status: types.StatusOpen, IssueType: types.TypeTask}
	p, ok := pollutedIDs([]*types.Issue{issue})[issue.ID]
	if !ok {
		t.Fatalf("%q not reported", issue.Title)
	}
	want := []string{"Title starts with test prefix", "No description", "Generic test title"}
	if !slices.Equal(p.reasons, want) {
		t.Errorf("reasons = %v, want %v", p.reasons, want)
	}
}

// TestDetectTestPollution_PR5137Boundaries keeps PR #5137's boundary
// regressions at GH#5025's approved weights (title signal 0.6, empty
// description 0.2, short description 0.1). The PR pinned a test-prefixed
// sequential ID with a thin description at >= 0.9; IDs are no longer
// evidence, so that fixture is now exactly 0.7 (review-only).
func TestDetectTestPollution_PR5137Boundaries(t *testing.T) {
	tests := []struct {
		name, id, title, desc string
		score                 float64 // 0: not reported
		high                  bool
	}{
		// Prefix 0.6 + short description 0.1 clears the 0.7 reporting bar.
		{"short description dead zone closed", "bd-1hao", "test-foo", "x", 0.7, false},
		// A normal-length description is authored content: never reported.
		{"bare prefix, normal description", "bd-9f3", "test-migration cleanup", "Removes the legacy migration path once the new one is verified stable.", 0, false},
		// No title signal: a counter ID plus a short description is 0.1.
		{"sequential ID, short description, no prefix", "bd-42", "Fix login redirect bug", "See logs", 0, false},
		// Prefix 0.6 + short description 0.1; the ID adds nothing.
		{"prefix, sequential ID, short description", "test-42", "test-42 fixture", "wip", 0.7, false},
		// Prefix 0.6 + short description 0.1 on a hash ID: review, not cleanup.
		{"prefix, hash ID, thin description", "dcr-a3f9", "test-driver.sh isolation broken — constants.sh clobbers env", "Broke isolation", 0.7, false},
		// Generic phrase 0.6 + empty description 0.2 stays below the 0.9 cleanup band.
		{"generic title substring alone", "dcr-b7e1", "Fix test issue detection regression", "", 0.8, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &types.Issue{ID: tt.id, Title: tt.title, Description: tt.desc, Status: types.StatusOpen, IssueType: types.TypeBug}
			p, ok := pollutedIDs([]*types.Issue{issue})[tt.id]
			if tt.score == 0 {
				if ok {
					t.Fatalf("reported (score %.2f, reasons %v), want unreported", p.score, p.reasons)
				}
				return
			}
			if !ok {
				t.Fatalf("not reported, want score %.1f", tt.score)
			}
			if p.score != tt.score || p.highConfidence() != tt.high {
				t.Errorf("score %v high %v (reasons %v), want score %v high %v", p.score, p.highConfidence(), p.reasons, tt.score, tt.high)
			}
		})
	}
}
