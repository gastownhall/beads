package doctor

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Pure tests for the shared test-pollution scorer (GH#5025). The behavioral
// history of these contracts — including the red/green evidence — lives in
// cmd/bd/detect_pollution_test.go; these tables pin the exported API.

// pollutionFixtureIssue returns an open task whose title starts with "test-",
// the shape every false positive in GH#5025 shared.
func pollutionFixtureIssue(id string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     "test-" + id + " leftover",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
}

// TestDetectTestPollution_SkipsClosedAndEpics pins AC-1: closed issues and
// epics are never pollution candidates, however strong their fixture signals.
func TestDetectTestPollution_SkipsClosedAndEpics(t *testing.T) {
	closed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	issues := []*types.Issue{
		{ID: "bd-closed1", Title: "test-closed leftover", Status: types.StatusClosed, IssueType: types.TypeTask, CreatedAt: closed},
		{ID: "bd-closed2", Title: "Sample issue archive", Status: types.StatusClosed, IssueType: types.TypeTask, CreatedAt: closed},
		{ID: "bd-epic1", Title: "test-quality architecture", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: closed},
		{ID: "bd-epic2", Title: "Test issue rollout epic", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeEpic, CreatedAt: closed},
	}
	if got := DetectTestPollution(issues); len(got) != 0 {
		for _, p := range got {
			t.Errorf("ineligible issue %s (%s/%s) reported as pollution (score %.2f)", p.Issue.ID, p.Issue.Status, p.Issue.IssueType, p.Score)
		}
	}
}

// TestDetectTestPollution_ExcludesNonOpenAndEphemeral pins AC-2: non-open and
// ephemeral issues are excluded, while an identical open non-epic issue with
// Ephemeral=false — including a NoHistory wisp — remains eligible.
func TestDetectTestPollution_ExcludesNonOpenAndEphemeral(t *testing.T) {
	mutate := func(id string, f func(*types.Issue)) *types.Issue {
		i := pollutionFixtureIssue(id)
		f(i)
		return i
	}
	issues := []*types.Issue{
		mutate("bd-inprog", func(i *types.Issue) { i.Status = types.StatusInProgress }),
		mutate("bd-blocked", func(i *types.Issue) { i.Status = types.StatusBlocked }),
		mutate("bd-defer", func(i *types.Issue) { i.Status = types.StatusDeferred }),
		mutate("bd-eph", func(i *types.Issue) { i.Ephemeral = true }),
		pollutionFixtureIssue("bd-open"),
		mutate("bd-nohist", func(i *types.Issue) { i.NoHistory = true }),
	}

	reported := map[string]bool{}
	for _, p := range DetectTestPollution(issues) {
		reported[p.Issue.ID] = true
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

// TestPollutionCandidateFilter pins the eligibility rules issue by issue.
func TestPollutionCandidateFilter(t *testing.T) {
	epic := pollutionFixtureIssue("bd-epic")
	epic.IssueType = types.TypeEpic
	mutate := func(f func(*types.Issue)) *types.Issue {
		i := pollutionFixtureIssue("bd-mut")
		f(i)
		return i
	}

	cases := []struct {
		name  string
		issue *types.Issue
		want  bool
	}{
		{"nil", nil, false},
		{"open task", pollutionFixtureIssue("bd-open"), true},
		{"open NoHistory wisp (Ephemeral=false)", mutate(func(i *types.Issue) { i.NoHistory = true }), true},
		{"epic", epic, false},
		{"closed", mutate(func(i *types.Issue) { i.Status = types.StatusClosed }), false},
		{"in_progress", mutate(func(i *types.Issue) { i.Status = types.StatusInProgress }), false},
		{"blocked", mutate(func(i *types.Issue) { i.Status = types.StatusBlocked }), false},
		{"deferred", mutate(func(i *types.Issue) { i.Status = types.StatusDeferred }), false},
		{"ephemeral", mutate(func(i *types.Issue) { i.Ephemeral = true }), false},
	}
	for _, tc := range cases {
		if got := PollutionCandidateFilter(tc.issue); got != tc.want {
			t.Errorf("%s: PollutionCandidateFilter = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// TestPollutionIssueFilter pins the load filter both pollution checks share:
// open, non-epic, non-ephemeral, and uncapped (callers add their own row cap).
func TestPollutionIssueFilter(t *testing.T) {
	filter := PollutionIssueFilter()
	if filter.Status == nil || *filter.Status != types.StatusOpen {
		t.Errorf("Status = %v, want open", filter.Status)
	}
	if len(filter.ExcludeTypes) != 1 || filter.ExcludeTypes[0] != types.TypeEpic {
		t.Errorf("ExcludeTypes = %v, want [epic]", filter.ExcludeTypes)
	}
	if filter.Ephemeral == nil || *filter.Ephemeral {
		t.Errorf("Ephemeral = %v, want pointer to false (excludes ephemeral, keeps NoHistory)", filter.Ephemeral)
	}
	if filter.MaxRows != 0 {
		t.Errorf("MaxRows = %d, want 0 (the shared load adds no cap)", filter.MaxRows)
	}
}

// TestHasTestTitlePrefix pins the prefix matcher exactly as create/export use
// it (isTestIssue): same prefixes, same separators, case-insensitive.
func TestHasTestTitlePrefix(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"test-fix login", true},
		{"Test-quality architecture", true},
		{"TEST-1", true},
		{"test_thing", true},
		{"testy title", false},   // prefix must be a whole word
		{"contest rules", false}, // no prefix at line start
		{"benchmark seed", true},
		{"sample issue", true},
		{"tmp note", true},
		{"temp file", true},
		{"Debug sync hangs", true},
		{"dummy record", true},
		{"Test-quality architecture v2", true},
		{"", false},
	}
	for _, tc := range cases {
		if got := HasTestTitlePrefix(tc.title); got != tc.want {
			t.Errorf("HasTestTitlePrefix(%q) = %t, want %t", tc.title, got, tc.want)
		}
	}
}
