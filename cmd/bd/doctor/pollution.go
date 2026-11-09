package doctor

import (
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// Test-pollution classification, shared by the detailed `bd doctor
// --check=pollution` report and the doctor Test Pollution gate (GH#5025).
//
// The classifier is deliberately shallow and never a verdict: it flags
// issues whose title looks test-created AND whose content is thin, so that
// title-only work — a real issue about the test harness, a "Test-quality"
// epic, a bulk import — does not score as pollution the way the old additive
// scorer did (it awarded 0.7 for a title prefix alone and +0.3 for a
// same-minute batch, flagging an open P1 epic at 1.00 and offering to delete
// it). Scores below are uncalibrated heuristic strengths for presentation
// only; they confer no deletion privilege and are not probabilities.

// testPrefixPattern matches common test issue title prefixes. Compiled once
// at package level for IsTestTitle.
var testPrefixPattern = regexp.MustCompile(`^(test|benchmark|sample|tmp|temp|debug|dummy)[-_\s]`)

// IsTestTitle reports whether an issue title starts with one of the common
// test-issue prefixes, case-insensitively. This is the same predicate the
// create-time warning, the create description bypass and the export filter
// have always applied (cmd/bd's isTestIssue); on its own it says nothing
// about whether an issue is pollution.
func IsTestTitle(title string) bool {
	return testPrefixPattern.MatchString(strings.ToLower(title))
}

// PollutionMatch is one issue the heuristic classifier flagged as a possible
// leftover test issue. Score is a presentation-only heuristic strength.
type PollutionMatch struct {
	Issue   *types.Issue
	Score   float64
	Reasons []string
}

// Heuristic score points. The rendered score is the point total divided by
// ten; the stronger bucket keeps the historical >= 0.9 split.
const (
	pollutionPrefixPoints     = 4
	pollutionEmptyDescPoints  = 3
	pollutionHighBucketPoints = 9
)

// ClassifyTestPollution applies the pollution heuristic to each issue
// independently — of other rows, of ID shape and of creation timing, none of
// which establish test provenance (a sequential ID or a same-minute batch is
// the signature of an import as often as of pollution).
//
// A test-style prefix qualifies an issue only when its description is empty:
// a titled, described issue about the test harness is real work, not
// pollution.
func ClassifyTestPollution(issues []*types.Issue) []PollutionMatch {
	var matches []PollutionMatch
	for _, issue := range issues {
		if match, ok := classifyPollutionIssue(issue); ok {
			matches = append(matches, match)
		}
	}
	return matches
}

// PollutionScoreHighBucket reports whether a match belongs in the stronger
// presentation bucket (the historical >= 0.9 split).
func PollutionScoreHighBucket(m PollutionMatch) bool {
	return m.Score >= float64(pollutionHighBucketPoints)/10
}

func classifyPollutionIssue(issue *types.Issue) (PollutionMatch, bool) {
	if !IsTestTitle(issue.Title) {
		return PollutionMatch{}, false
	}
	if strings.TrimSpace(issue.Description) != "" {
		return PollutionMatch{}, false
	}

	points := pollutionPrefixPoints + pollutionEmptyDescPoints
	reasons := []string{
		"Title starts with a test-style prefix",
		"No description",
	}
	return PollutionMatch{Issue: issue, Score: float64(points) / 10, Reasons: reasons}, true
}
