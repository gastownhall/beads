package main

import (
	"fmt"
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

// TestDetectTestPollution_SubstantiveWorkNotFlagged pins AC-3: a prefixed open
// task with real content — a trimmed description of >= 20 bytes, or nonblank
// design, acceptance criteria, or notes — is never reported, however testy
// its title looks.
func TestDetectTestPollution_SubstantiveWorkNotFlagged(t *testing.T) {
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
		fixture("bd-desc20", func(i *types.Issue) { i.Description = strings.Repeat("Fix the parser bug", 2) }),
		fixture("bd-descpad", func(i *types.Issue) { i.Description = "   " + strings.Repeat("Investigate flaky gate", 2) + "  " }),
		fixture("bd-design", func(i *types.Issue) { i.Design = "Step 1: reproduce; step 2: fix the harness" }),
		fixture("bd-accept", func(i *types.Issue) { i.AcceptanceCriteria = "bd doctor --check=pollution stays green" }),
		fixture("bd-notes", func(i *types.Issue) { i.Notes = "Follow-up tracked in the ledger" }),
		fixture("bd-desc19", func(i *types.Issue) { i.Description = "parser bug in gate" }),
		fixture("bd-blank", func(i *types.Issue) { i.Design = " \n\t"; i.Notes = "   " }),
	}

	reported := map[string]bool{}
	for _, p := range detectTestPollution(issues) {
		reported[p.issue.ID] = true
	}
	for _, id := range []string{"bd-desc20", "bd-descpad", "bd-design", "bd-accept", "bd-notes"} {
		if reported[id] {
			t.Errorf("issue %s with substantive content flagged as pollution", id)
		}
	}
	for _, id := range []string{"bd-desc19", "bd-blank"} {
		if !reported[id] {
			t.Errorf("control: bare fixture issue %s was not reported", id)
		}
	}
}

// TestDetectTestPollution_IDAndBulkNotEvidence pins AC-4: counter-mode IDs
// (bd-123) and same-minute bulk creation are not evidence of test pollution —
// a batch of empty-description counter-ID tasks created together stays
// unreported, and no reported reason mentions the ID or the timestamp. Only
// the fixture-shaped leftover among them is reported.
func TestDetectTestPollution_IDAndBulkNotEvidence(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	issues := []*types.Issue{}
	for i := 1; i <= 12; i++ {
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bd-%d", i),
			Title:     "Imported work item",
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
			CreatedAt: created,
		})
	}
	issues = append(issues, &types.Issue{
		ID:        "bd-fixture",
		Title:     "tmp-fixture leftover",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: created,
	})

	reported := map[string]bool{}
	for _, p := range detectTestPollution(issues) {
		reported[p.issue.ID] = true
		for _, reason := range p.reasons {
			if strings.Contains(reason, "Sequential ID") || strings.Contains(reason, "same minute") {
				t.Errorf("reported issue %s carries ID/timing reason %q", p.issue.ID, reason)
			}
		}
	}
	for i := 1; i <= 12; i++ {
		if id := fmt.Sprintf("bd-%d", i); reported[id] {
			t.Errorf("counter-ID issue %s reported on ID/bulk evidence alone", id)
		}
	}
	if !reported["bd-fixture"] {
		t.Error("control: the prefixed empty-description leftover was not reported")
	}
}

// TestDetectTestPollution_RequiresCorroboration pins AC-5's exact weights: a
// prefixed title alone is only medium confidence ("Debug sync hangs" scores
// 0.8), a prefixed generic title with no description is high ("Test issue"
// scores 1.4), and a prefixed title with a short description just clears the
// reporting bar (0.7). No reason cites IDs or timestamps.
func TestDetectTestPollution_RequiresCorroboration(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	issues := []*types.Issue{
		{ID: "bd-debug", Title: "Debug sync hangs", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: created},
		{ID: "bd-testissue", Title: "Test issue", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: created},
		{ID: "bd-short", Title: "tmp leftover", Description: "short", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: created},
	}

	hits := map[string]pollutionResult{}
	for _, p := range detectTestPollution(issues) {
		hits[p.issue.ID] = p
		for _, reason := range p.reasons {
			if strings.Contains(reason, "Sequential ID") || strings.Contains(reason, "same minute") {
				t.Errorf("reported issue %s carries ID/timing reason %q", p.issue.ID, reason)
			}
		}
	}
	for id, wantScore := range map[string]float64{"bd-debug": 0.8, "bd-testissue": 1.4, "bd-short": 0.7} {
		p, ok := hits[id]
		if !ok {
			t.Fatalf("issue %s not reported", id)
		}
		if p.score != wantScore {
			t.Errorf("issue %s scored %.2f, want %.2f (reasons %v)", id, p.score, wantScore, p.reasons)
		}
	}
	if hits["bd-debug"].score >= hits["bd-testissue"].score {
		t.Errorf("generic-titled fixture must outscore the plain prefixed one")
	}
}
