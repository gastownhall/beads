package doctor

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// Test pollution scoring shared by the validate gate (CheckTestPollution) and
// the detailed `bd doctor --check=pollution` command, so their counts cannot
// drift apart (GH#5025).

// PollutionResult is one issue the pollution scorer reported.
type PollutionResult struct {
	Issue   *types.Issue
	Score   float64
	Reasons []string
}

// Scores are summed in integer tenths so the band edges are exact.
const (
	titleSignalPoints = 6 // test title prefix, or generic test phrase
	reportPoints      = 7 // Score 0.7: reported for review
	highPoints        = 9 // Score 0.9: high confidence, eligible for cleanup
)

// HighConfidence reports whether the hit scored in the high-confidence band.
func (p PollutionResult) HighConfidence() bool {
	return p.Score >= float64(highPoints)/10
}

// testPrefixPattern matches common test issue title prefixes.
var testPrefixPattern = regexp.MustCompile(`^(test|benchmark|sample|tmp|temp|debug|dummy)[-_\s]`)

// HasTestTitlePrefix reports whether title starts with a common test prefix.
// bd create and bd export use it on its own; the pollution scorer treats it
// as one signal among several.
func HasTestTitlePrefix(title string) bool {
	return testPrefixPattern.MatchString(strings.ToLower(title))
}

// genericTestTitle reports whether the lowercased title contains a generic
// test phrase such as "test issue".
func genericTestTitle(title string) bool {
	return strings.Contains(title, "issue for testing") ||
		strings.Contains(title, "test issue") ||
		strings.Contains(title, "sample issue")
}

// PollutionCandidateFilter selects the issues the pollution scorer considers.
// Callers add their own row cap; DetectTestPollution applies the same
// eligibility again, so a caller that loads a superset gets the same result.
// Ephemeral=false still loads NoHistory rows, which stay eligible.
func PollutionCandidateFilter() types.IssueFilter {
	open := types.StatusOpen
	persistent := false
	return types.IssueFilter{
		Status:       &open,
		ExcludeTypes: []types.IssueType{types.TypeEpic},
		Ephemeral:    &persistent,
	}
}

// pollutionCandidate reports whether issue may be scored at all: only open,
// non-ephemeral issues that are not epics. Closed issues, epics and active or
// parked work are never pollution (GH#5025).
func pollutionCandidate(issue *types.Issue) bool {
	return issue.Status == types.StatusOpen && issue.IssueType != types.TypeEpic && !issue.Ephemeral
}

// minAuthoredDescription is the trimmed description length, in bytes, at
// which an issue counts as real work whatever its title says.
const minAuthoredDescription = 20

// hasAuthoredContent reports whether issue carries prose someone wrote: a
// substantive description or any nonblank design, acceptance criteria or
// notes. Such an issue is never pollution, so cleanup cannot delete it.
func hasAuthoredContent(issue *types.Issue) bool {
	return len(strings.TrimSpace(issue.Description)) >= minAuthoredDescription ||
		strings.TrimSpace(issue.Design) != "" ||
		strings.TrimSpace(issue.AcceptanceCriteria) != "" ||
		strings.TrimSpace(issue.Notes) != ""
}

// DetectTestPollution scores issues for test pollution and returns those at
// or above the reporting threshold, in input order. Only the issue's own
// title and prose count: counter-mode IDs and same-minute batch creation are
// how ordinary work and imports look, not evidence of a test run (GH#5025).
func DetectTestPollution(issues []*types.Issue) []PollutionResult {
	var results []PollutionResult
	for _, issue := range issues {
		if !pollutionCandidate(issue) || hasAuthoredContent(issue) {
			continue
		}
		points := 0
		var reasons []string

		title := strings.ToLower(issue.Title)

		// Title signals: either one alone is review-only (medium); both
		// are high confidence. Reasons keep their historical order:
		// prefix, description, generic title.
		if testPrefixPattern.MatchString(title) {
			points += titleSignalPoints
			reasons = append(reasons, "Title starts with test prefix")
		}

		// Description signal (weak). hasAuthoredContent has already
		// skipped any issue whose description is substantive.
		if strings.TrimSpace(issue.Description) == "" {
			points += 2
			reasons = append(reasons, "No description")
		} else {
			points++
			reasons = append(reasons, "Very short description")
		}

		if genericTestTitle(title) {
			points += titleSignalPoints
			reasons = append(reasons, "Generic test title")
		}

		if points >= reportPoints {
			results = append(results, PollutionResult{
				Issue:   issue,
				Score:   float64(points) / 10,
				Reasons: reasons,
			})
		}
	}

	return results
}

// pollutionSearcher is the one store read CheckTestPollution needs.
type pollutionSearcher interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
}

// checkTestPollutionWithStore is CheckTestPollution on an open store. The
// gate reads every candidate with no row cap, so its count is the scorer's.
func checkTestPollutionWithStore(ctx context.Context, store pollutionSearcher) DoctorCheck {
	issues, err := store.SearchIssues(ctx, "", PollutionCandidateFilter())
	if err != nil {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  StatusWarning,
			Message: "N/A (query failed)",
		}
	}

	count := len(DetectTestPollution(issues))
	if count == 0 {
		return DoctorCheck{
			Name:    "Test Pollution",
			Status:  "ok",
			Message: "No test pollution detected",
		}
	}

	return DoctorCheck{
		Name:    "Test Pollution",
		Status:  "warning",
		Message: fmt.Sprintf("%d potential test issue(s) detected", count),
		Detail:  "Test issues may have leaked into production database",
		Fix:     "Run 'bd doctor --check=pollution' to review and clean test issues",
	}
}
