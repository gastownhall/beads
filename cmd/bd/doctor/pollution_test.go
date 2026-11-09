package doctor

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestPollutionSparesLegitimateWork carries GH#5025's report: substantive
// issues about test infrastructure and an open P1 "Test-quality
// architecture" epic, bulk-imported with 30 others in one minute under
// sequential-looking IDs, are real work. The old scorer flagged the epic at
// 1.00 and every test-* infrastructure bug at 0.70.
func TestPollutionSparesLegitimateWork(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	issues := []*types.Issue{
		{ID: "dcr-9f3", Title: "Test-quality architecture", Description: "Restructure the component test suite so each layer owns its fixtures and assertions.", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
		{ID: "dcr-lzse", Title: "test-driver.sh isolation broken — constants.sh clobbers the caller's env", Description: "Sourcing constants.sh from test-driver.sh overwrites exported paths, so suites leak state.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug},
		{ID: "dcr-3t0", Title: "test-agents-md-currency.sh asserts the retired wording", Description: "The script still greps for the pre-rename AGENTS.md heading and fails on current docs.", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeBug},
		{ID: "dcr-cdf.34", Title: "test-planner rule: axe tests must render prop-derived states", Description: "Accessibility checks only render default props, missing states derived from props.", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask},
		{ID: "dcr-dk2n", Title: "test-check-19-spec-a11y-gating.sh scatters advisory residue", Description: "The gating script writes advisory files into the repo root instead of a temp dir.", Status: types.StatusClosed, Priority: 3, IssueType: types.TypeBug},
		{ID: "dcr-3db", Title: "test-shellcheck.sh teeth fixtures use fixed repo paths", Description: "Fixtures hard-code /repo paths, so the teeth checks pass vacuously on other checkouts.", Status: types.StatusClosed, Priority: 3, IssueType: types.TypeBug},
	}
	// A bulk import: 30 more issues in the same minute with sequential IDs.
	// Some carry no description, the old scorer's "sequential ID with
	// minimal description" signal (0.4 + 0.2 + 0.3 for the burst = 0.9).
	for i := 1; i <= 30; i++ {
		description := "Derived from the audit report; see linked findings."
		if i%3 == 0 {
			description = ""
		}
		issues = append(issues, &types.Issue{
			ID:          fmt.Sprintf("dcr-%d", i),
			Title:       fmt.Sprintf("Imported report finding %d", i),
			Description: description,
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeBug,
			CreatedAt:   created,
		})
	}
	for _, issue := range issues {
		issue.CreatedAt = created
	}

	if matches := DetectTestPollution(issues); len(matches) != 0 {
		for _, m := range matches {
			t.Errorf("legitimate issue %s %q matched (score %.2f, reasons %v)", m.Issue.ID, m.Issue.Title, m.Score, m.Reasons)
		}
	}
}

// TestPollutionTitleAndContentRule pins the classifier's inclusion rule on
// isolated, unprotected issues: a test-prefix title needs an empty
// description, a generic test phrase needs a description shorter than 20
// bytes, and any design, acceptance criteria or notes rule the issue out.
func TestPollutionTitleAndContentRule(t *testing.T) {
	t.Parallel()

	const nineteen = "abcdefghijklmnopqrs"
	tests := []struct {
		name        string
		issue       types.Issue
		wantMatch   bool
		wantScore   float64
		wantStrong  bool
		wantReasons []string
	}{
		{name: "prefix, empty description", issue: types.Issue{Title: "test-login"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "prefix, whitespace description", issue: types.Issue{Title: "test-login", Description: " \n\t "}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "prefix, short description", issue: types.Issue{Title: "test-login", Description: "short"}},
		{name: "prefix, substantive description", issue: types.Issue{Title: "test-driver.sh clobbers env", Description: "Sourcing constants.sh overwrites exported paths."}},
		{name: "mixed-case prefix", issue: types.Issue{Title: "TEST_case"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "benchmark prefix", issue: types.Issue{Title: "benchmark-run"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "sample prefix", issue: types.Issue{Title: "sample data"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "tmp prefix", issue: types.Issue{Title: "Tmp-x"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "temp prefix", issue: types.Issue{Title: "temp_x"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "debug prefix", issue: types.Issue{Title: "Debug x"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "dummy prefix", issue: types.Issue{Title: "dummy-x"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "test word without separator", issue: types.Issue{Title: "testing harness"}},
		{name: "ordinary title, empty description", issue: types.Issue{Title: "Fix login"}},
		{name: "generic phrase, empty description", issue: types.Issue{Title: "My Sample Issue"}, wantMatch: true, wantScore: 0.8, wantReasons: []string{"Generic test title", "No description"}},
		{name: "generic phrase, 19-byte description", issue: types.Issue{Title: "an issue for testing", Description: nineteen}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Generic test title", "Very short description"}},
		{name: "generic phrase, 19 bytes after trimming", issue: types.Issue{Title: "an issue for testing", Description: "  " + nineteen + "\n"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Generic test title", "Very short description"}},
		{name: "generic phrase, 20-byte description", issue: types.Issue{Title: "an issue for testing", Description: nineteen + "t"}},
		{name: "prefix and generic phrase, short description", issue: types.Issue{Title: "Test Issue 1", Description: "Another test"}, wantMatch: true, wantScore: 1.1, wantStrong: true, wantReasons: []string{"Title starts with test prefix", "Generic test title", "Very short description"}},
		{name: "prefix and generic phrase, empty description", issue: types.Issue{Title: "test issue"}, wantMatch: true, wantScore: 1.2, wantStrong: true, wantReasons: []string{"Title starts with test prefix", "Generic test title", "No description"}},
		{name: "prefix with design", issue: types.Issue{Title: "test-login", Design: "Use a fake IdP."}},
		{name: "prefix with acceptance criteria", issue: types.Issue{Title: "test-login", AcceptanceCriteria: "Login succeeds."}},
		{name: "prefix with notes", issue: types.Issue{Title: "test-login", Notes: "Seen on CI."}},
		{name: "prefix with blank notes", issue: types.Issue{Title: "test-login", Notes: "  \n"}, wantMatch: true, wantScore: 0.7, wantReasons: []string{"Title starts with test prefix", "No description"}},
		{name: "generic phrase with notes", issue: types.Issue{Title: "test issue 7", Description: "x", Notes: "Seen on CI."}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			issue := tt.issue
			issue.Status = types.StatusOpen
			issue.IssueType = types.TypeTask
			// The verdict must not depend on ID shape or creation time.
			for i, variant := range []struct {
				id      string
				created time.Time
			}{
				{"bd-1", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
				{"bd-x9z", time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)},
			} {
				issue := issue
				issue.ID = variant.id
				issue.CreatedAt = variant.created
				matches := DetectTestPollution([]*types.Issue{&issue})
				if !tt.wantMatch {
					if len(matches) != 0 {
						t.Fatalf("variant %d: matched %+v, want no match", i, matches[0])
					}
					continue
				}
				if len(matches) != 1 {
					t.Fatalf("variant %d: got %d matches, want 1", i, len(matches))
				}
				m := matches[0]
				if m.Issue != &issue {
					t.Errorf("variant %d: match carries a different issue", i)
				}
				if m.Score != tt.wantScore || m.Strong() != tt.wantStrong {
					t.Errorf("variant %d: score %.2f strong %v, want %.2f strong %v", i, m.Score, m.Strong(), tt.wantScore, tt.wantStrong)
				}
				if fmt.Sprint(m.Reasons) != fmt.Sprint(tt.wantReasons) {
					t.Errorf("variant %d: reasons %q, want %q", i, m.Reasons, tt.wantReasons)
				}
			}
		})
	}
}
