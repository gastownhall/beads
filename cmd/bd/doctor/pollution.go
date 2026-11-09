package doctor

import (
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// testPrefixPattern matches the leading words of a test-style title. It is
// applied to the lowercased title.
var testPrefixPattern = regexp.MustCompile(`^(test|benchmark|sample|tmp|temp|debug|dummy)[-_\s]`)

// genericTestPhrases are title phrases, anywhere in the lowercased title, that
// name an issue as a throwaway test issue.
var genericTestPhrases = []string{"issue for testing", "test issue", "sample issue"}

// Heuristic points per signal. A match's Score is its points divided by ten;
// the scores are uncalibrated strengths for ordering output, not
// probabilities, and they decide neither inclusion nor deletion.
const (
	pointsTestPrefix       = 4
	pointsGenericTestTitle = 5
	pointsNoDescription    = 3
	pointsShortDescription = 2

	// strongPollutionPoints splits the "stronger" output group from the
	// "possible" one (the JSON high_confidence/medium_confidence buckets).
	strongPollutionPoints = 9

	// shortDescriptionBytes is the description length, after trimming, below
	// which a description counts as thin.
	shortDescriptionBytes = 20
)

// IsTestTitle reports whether title starts with a test-style word such as
// "test-" or "tmp_". It is the predicate bd create (warning, description
// requirement) and bd export --scrub use; it is not by itself evidence of
// pollution.
func IsTestTitle(title string) bool {
	return testPrefixPattern.MatchString(strings.ToLower(title))
}

// PollutionMatch is one issue the test-pollution heuristic matched.
type PollutionMatch struct {
	Issue   *types.Issue
	Score   float64
	Reasons []string

	points int
}

// Strong reports whether the match belongs to the stronger heuristic group.
func (m PollutionMatch) Strong() bool {
	return m.points >= strongPollutionPoints
}

// DetectTestPollution returns the issues that look like leftovers of test
// runs, in input order. Each issue is judged on its own title and content:
// a test-style title is required, and so is a thin description. Creation
// time and ID shape are not evidence (a bulk import creates many issues in
// one minute, and sequential IDs are ordinary).
func DetectTestPollution(issues []*types.Issue) []PollutionMatch {
	var matches []PollutionMatch
	for _, issue := range issues {
		if m, ok := classifyPollution(issue); ok {
			matches = append(matches, m)
		}
	}
	return matches
}

// classifyPollution applies the title and content rule to one issue.
func classifyPollution(issue *types.Issue) (PollutionMatch, bool) {
	if issue == nil {
		return PollutionMatch{}, false
	}

	title := strings.ToLower(issue.Title)
	prefix := testPrefixPattern.MatchString(title)
	generic := false
	for _, phrase := range genericTestPhrases {
		if strings.Contains(title, phrase) {
			generic = true
			break
		}
	}
	description := strings.TrimSpace(issue.Description)

	if !(prefix || generic) || len(description) >= shortDescriptionBytes {
		return PollutionMatch{}, false
	}

	m := PollutionMatch{Issue: issue}
	if prefix {
		m.points += pointsTestPrefix
		m.Reasons = append(m.Reasons, "Title starts with test prefix")
	}
	if generic {
		m.points += pointsGenericTestTitle
		m.Reasons = append(m.Reasons, "Generic test title")
	}
	if description == "" {
		m.points += pointsNoDescription
		m.Reasons = append(m.Reasons, "No description")
	} else {
		m.points += pointsShortDescription
		m.Reasons = append(m.Reasons, "Very short description")
	}
	m.Score = float64(m.points) / 10
	return m, true
}
