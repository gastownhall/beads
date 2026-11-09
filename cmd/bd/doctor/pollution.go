package doctor

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// Test-pollution scoring, shared by `bd doctor --check=pollution` (the
// detailed check and its --clean cleanup) and the "Test Pollution" validation
// gate (CheckTestPollution), so the two checks cannot drift apart again
// (GH#5025: the gate's SQL counted titles the scorer missed and vice versa).
//
// Scoring works in integer tenths of a point and reports the score as a
// float: a hit needs >= 7 tenths (0.7) to be reported at all, and >= 9 tenths
// (0.9) to count as high confidence.

// PollutionResult is one issue the scorer reported as likely test pollution.
type PollutionResult struct {
	Issue   *types.Issue
	Score   float64
	Reasons []string
}

// HighConfidence reports whether the hit is solid enough for --clean deletion
// eligibility. Weaker hits stay review-only.
func (r PollutionResult) HighConfidence() bool {
	return r.Score >= pollutionHighThreshold
}

// Score thresholds, as fractions of a point (the integer-tenths counterparts
// are the pollution*Points constants below).
const (
	pollutionReportThreshold = 0.7
	pollutionHighThreshold   = 0.9
)

// Signal weights and thresholds, in integer tenths of a point.
const (
	pollutionTitlePrefixPoints      = 7
	pollutionSequentialIDPoints     = 4
	pollutionEmptyDescriptionPoints = 2
	pollutionShortDescriptionPoints = 1
	pollutionBulkCreatePoints       = 3
	pollutionGenericTitlePoints     = 5
	pollutionReportPoints           = 7
	pollutionHighPoints             = 9
	pollutionBulkCreateGroupSize    = 10
	pollutionSubstantiveDescription = 20
)

// testTitlePrefixPattern matches common test issue title prefixes. The same
// pattern backs create/export warnings (cmd/bd's isTestIssue), so a title
// flagged at creation is exactly what the scorer treats as a signal.
var testTitlePrefixPattern = regexp.MustCompile(`^(test|benchmark|sample|tmp|temp|debug|dummy)[-_\s]`)

// sequentialIDPattern matches counter-mode IDs (bd-123), which issueops
// generates routinely; a match is not pollution evidence on its own.
var sequentialIDPattern = regexp.MustCompile(`^[a-z]+-\d+$`)

// HasTestTitlePrefix reports whether title starts with a common test-issue
// prefix (test, benchmark, sample, tmp, temp, debug, dummy), case-insensitively.
func HasTestTitlePrefix(title string) bool {
	return testTitlePrefixPattern.MatchString(strings.ToLower(title))
}

// PollutionCandidateFilter reports whether issue is eligible to be scored as
// a pollution candidate at all. Only open, non-epic, non-ephemeral issues are:
// closed rows are history, an epic whose title merely starts with "Test" is
// usually real work (the GH#5025 false positive), and ephemeral wisps are
// operational state. NoHistory rows keep Ephemeral=false and stay eligible.
func PollutionCandidateFilter(issue *types.Issue) bool {
	if issue == nil {
		return false
	}
	return issue.Status == types.StatusOpen && issue.IssueType != types.TypeEpic && !issue.Ephemeral
}

// hasSubstantiveContent reports whether issue carries real work: a
// description of at least pollutionSubstantiveDescription bytes once trimmed,
// or any nonblank prose in Design, AcceptanceCriteria, or Notes. Fixture
// leftovers rarely have any of these; real work almost always does — every
// false positive in GH#5025 had a substantive description.
func hasSubstantiveContent(issue *types.Issue) bool {
	if len(strings.TrimSpace(issue.Description)) >= pollutionSubstantiveDescription {
		return true
	}
	for _, prose := range []string{issue.Design, issue.AcceptanceCriteria, issue.Notes} {
		if strings.TrimSpace(prose) != "" {
			return true
		}
	}
	return false
}

// PollutionIssueFilter returns the search filter both pollution checks load
// their candidates with: open, non-epic, non-ephemeral issues (NoHistory rows
// included — search treats Ephemeral=false as "not ephemeral-flagged").
// The detailed doctor check adds its env-only MaxRows cap on top; the
// validation gate stays uncapped. The scorer repeats the eligibility per
// issue, so a caller passing a superset cannot reintroduce false positives.
func PollutionIssueFilter() types.IssueFilter {
	open := types.StatusOpen
	nonEphemeral := false
	return types.IssueFilter{
		Status:       &open,
		ExcludeTypes: []types.IssueType{types.TypeEpic},
		Ephemeral:    &nonEphemeral,
	}
}

// DetectTestPollution scores issues and returns the ones that look like test
// pollution, highest signal first in input order. Callers that already
// loaded a filtered set still get the eligibility guarantees: the filter is
// repeated here so a caller passing a superset cannot reintroduce the
// false positives (GH#5025).
func DetectTestPollution(issues []*types.Issue) []PollutionResult {
	var results []PollutionResult

	// Group issues by creation time to detect rapid succession.
	issuesByMinute := make(map[int64][]*types.Issue)
	for _, issue := range issues {
		minute := issue.CreatedAt.Unix() / 60
		issuesByMinute[minute] = append(issuesByMinute[minute], issue)
	}

	for _, issue := range issues {
		if !PollutionCandidateFilter(issue) {
			continue
		}

		// Substantive content vetoes every signal: an issue with a real
		// description or any prose fields is someone's work, not residue.
		if hasSubstantiveContent(issue) {
			continue
		}

		points := 0
		var reasons []string

		title := strings.ToLower(issue.Title)

		// Test-prefixed title (strong signal).
		if HasTestTitlePrefix(title) {
			points += pollutionTitlePrefixPoints
			reasons = append(reasons, "Title starts with test prefix")
		}

		// Sequential ID with minimal description (medium signal).
		if sequentialIDPattern.MatchString(issue.ID) && len(issue.Description) < pollutionSubstantiveDescription {
			points += pollutionSequentialIDPoints
			reasons = append(reasons, "Sequential ID with minimal description")
		}

		// Generic or empty description (weak signal).
		if len(strings.TrimSpace(issue.Description)) == 0 {
			points += pollutionEmptyDescriptionPoints
			reasons = append(reasons, "No description")
		} else if len(issue.Description) < pollutionSubstantiveDescription {
			points += pollutionShortDescriptionPoints
			reasons = append(reasons, "Very short description")
		}

		// Created with many other issues in the same minute (bulk signature).
		minute := issue.CreatedAt.Unix() / 60
		if len(issuesByMinute[minute]) >= pollutionBulkCreateGroupSize {
			points += pollutionBulkCreatePoints
			reasons = append(reasons, fmt.Sprintf("Created with %d other issues in same minute", len(issuesByMinute[minute])-1))
		}

		// Generic test titles.
		if strings.Contains(title, "issue for testing") ||
			strings.Contains(title, "test issue") ||
			strings.Contains(title, "sample issue") {
			points += pollutionGenericTitlePoints
			reasons = append(reasons, "Generic test title")
		}

		if points >= pollutionReportPoints {
			results = append(results, PollutionResult{
				Issue:   issue,
				Score:   float64(points) / 10,
				Reasons: reasons,
			})
		}
	}

	return results
}
