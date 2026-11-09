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
