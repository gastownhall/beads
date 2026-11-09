package doctor

import (
	"fmt"
	"strings"
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

// TestHasSubstantiveContent pins the substantive-content veto exactly: the
// description threshold is 20 trimmed bytes, whitespace does not count, and
// each prose field (Design, AcceptanceCriteria, Notes) counts on its own.
func TestHasSubstantiveContent(t *testing.T) {
	mk := func(mutate func(*types.Issue)) *types.Issue {
		i := pollutionFixtureIssue("bd-veto")
		if mutate != nil {
			mutate(i)
		}
		return i
	}
	cases := []struct {
		name  string
		issue *types.Issue
		want  bool
	}{
		{"no content at all", mk(nil), false},
		{"description of 19 bytes", mk(func(i *types.Issue) { i.Description = strings.Repeat("x", 19) }), false},
		{"description of 20 bytes", mk(func(i *types.Issue) { i.Description = strings.Repeat("x", 20) }), true},
		{"description padded past 20 but 19 once trimmed", mk(func(i *types.Issue) { i.Description = "  " + strings.Repeat("x", 19) + "  " }), false},
		{"description padded past 20, still 20 once trimmed", mk(func(i *types.Issue) { i.Description = "   " + strings.Repeat("x", 20) + "  " }), true},
		{"whitespace-only description", mk(func(i *types.Issue) { i.Description = " \n\t " }), false},
		{"design", mk(func(i *types.Issue) { i.Design = "Step 1: reproduce; step 2: fix" }), true},
		{"acceptance criteria", mk(func(i *types.Issue) { i.AcceptanceCriteria = "gate stays green" }), true},
		{"notes", mk(func(i *types.Issue) { i.Notes = "tracked in the ledger" }), true},
		{"blank prose fields", mk(func(i *types.Issue) { i.Design = " \n\t"; i.AcceptanceCriteria = "\t "; i.Notes = "  " }), false},
	}
	for _, tc := range cases {
		if got := hasSubstantiveContent(tc.issue); got != tc.want {
			t.Errorf("%s: hasSubstantiveContent = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// TestDetectTestPollution_BulkCreateNotEvidence pins AC-4 at the scorer: a
// counter-ID issue with an empty description — alone or in a same-minute
// batch of twelve — is not reported, and no reported reason ever mentions
// the ID or the timestamp.
func TestDetectTestPollution_BulkCreateNotEvidence(t *testing.T) {
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
	issues = append(issues, pollutionFixtureIssue("bd-fixture"))

	for _, p := range DetectTestPollution(issues) {
		if p.Issue.ID == "bd-fixture" {
			for _, reason := range p.Reasons {
				if strings.Contains(reason, "Sequential ID") || strings.Contains(reason, "same minute") {
					t.Errorf("reported reason %q cites ID or timing evidence", reason)
				}
			}
			continue
		}
		t.Errorf("counter-ID issue %s reported on ID/bulk evidence alone (score %.2f, reasons %v)",
			p.Issue.ID, p.Score, p.Reasons)
	}
	if got := DetectTestPollution(issues); len(got) != 1 || got[0].Issue.ID != "bd-fixture" {
		t.Errorf("want only the prefixed fixture reported, got %d hit(s)", len(got))
	}
}

// TestDetectTestPollution_ScoreWeights pins AC-5's exact weights and the
// confidence split they produce.
func TestDetectTestPollution_ScoreWeights(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name        string
		title       string
		description string
		wantScore   float64
		wantHigh    bool
	}{
		{"prefixed title, empty description", "Debug sync hangs", "", 0.8, false},
		{"prefixed generic title, empty description", "Test issue", "", 1.4, true},
		{"prefixed title, short description", "tmp leftover", "short", 0.7, false},
		{"prefixed generic title, short description", "test issue", "x", 1.3, true},
		{"unprefixed title, empty description (not reported)", "Imported work item", "", 0, false},
	}
	for _, tc := range cases {
		issue := &types.Issue{
			ID:          "bd-w",
			Title:       tc.title,
			Description: tc.description,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
			CreatedAt:   created,
		}
		hits := DetectTestPollution([]*types.Issue{issue})
		if tc.wantScore == 0 {
			if len(hits) != 0 {
				t.Errorf("%s: reported with score %.2f, want unreported", tc.name, hits[0].Score)
			}
			continue
		}
		if len(hits) != 1 {
			t.Errorf("%s: got %d hit(s), want 1", tc.name, len(hits))
			continue
		}
		if hits[0].Score != tc.wantScore {
			t.Errorf("%s: score %.2f, want %.2f (reasons %v)", tc.name, hits[0].Score, tc.wantScore, hits[0].Reasons)
		}
		if got := hits[0].HighConfidence(); got != tc.wantHigh {
			t.Errorf("%s: HighConfidence = %t, want %t", tc.name, got, tc.wantHigh)
		}
	}
}
