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
// ten; the stronger bucket keeps the historical >= 0.9 split. The point
// totals are not calibrated probabilities — they only rank presentation.
const (
	pollutionPrefixPoints    = 4
	pollutionGenericPoints   = 5
	pollutionEmptyDescPoints = 3
	pollutionShortDescPoints = 2
	// pollutionShortDescMaxBytes is the content ceiling for a generic test
	// phrase to qualify an issue (a description this thin is little or no
	// content; anything more is authored work).
	pollutionShortDescMaxBytes = 20
	pollutionHighBucketPoints  = 9
)

// ClassifyTestPollution applies the pollution heuristic to each issue
// independently — of other rows, of ID shape and of creation timing, none of
// which establish test provenance (a sequential ID or a same-minute batch is
// the signature of an import as often as of pollution).
//
// A test-style prefix qualifies an issue only when its description is empty;
// a generic test phrase qualifies only when the description (if any) is
// shorter than pollutionShortDescMaxBytes. Substantive authored fields
// (design, acceptance criteria, notes) exclude a match regardless of title:
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
	// Authored substance anywhere else on the issue excludes the match: a
	// real issue about the test suite usually carries a design or notes,
	// and content checks trim whitespace first.
	if strings.TrimSpace(issue.Design) != "" ||
		strings.TrimSpace(issue.AcceptanceCriteria) != "" ||
		strings.TrimSpace(issue.Notes) != "" {
		return PollutionMatch{}, false
	}

	title := strings.ToLower(issue.Title)
	prefix := testPrefixPattern.MatchString(title)
	generic := hasGenericTestPhrase(title)
	description := strings.TrimSpace(issue.Description)
	emptyDesc := description == ""
	shortDesc := len(description) < pollutionShortDescMaxBytes

	// Explicit eligibility rule; the point totals below are presentation
	// only and never decide inclusion.
	eligible := (prefix && emptyDesc) || (generic && shortDesc)
	if !eligible {
		return PollutionMatch{}, false
	}

	points := 0
	var reasons []string
	if prefix {
		points += pollutionPrefixPoints
		reasons = append(reasons, "Title starts with a test-style prefix")
	}
	if generic {
		points += pollutionGenericPoints
		reasons = append(reasons, "Generic test-issue title")
	}
	if emptyDesc {
		points += pollutionEmptyDescPoints
		reasons = append(reasons, "No description")
	} else {
		points += pollutionShortDescPoints
		reasons = append(reasons, "Very short description")
	}
	return PollutionMatch{Issue: issue, Score: float64(points) / 10, Reasons: reasons}, true
}

// hasGenericTestPhrase reports whether a lowercased title contains one of the
// generic test-issue phrases.
func hasGenericTestPhrase(loweredTitle string) bool {
	return strings.Contains(loweredTitle, "issue for testing") ||
		strings.Contains(loweredTitle, "test issue") ||
		strings.Contains(loweredTitle, "sample issue")
}
