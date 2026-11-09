package doctor

import (
	"context"
	"fmt"
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

// PollutionEvidence is the store surface DetectTestPollution reads to rule
// out connected issues: raw dependency rows in both directions (unresolved
// and external targets included) and comment counts, each batched over the
// candidate IDs.
type PollutionEvidence interface {
	GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error)
	GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error)
	GetCommentCounts(ctx context.Context, issueIDs []string) (map[string]int, error)
}

// DetectTestPollution returns the issues that look like leftovers of test
// runs, in input order. Each issue is judged on its own title and content:
// (a generic test phrase and a description shorter than 20 bytes) or (a
// test-prefix title and no description), with no design, acceptance
// criteria or notes. Creation time and ID shape are not evidence (a bulk
// import creates many issues in one minute, and sequential IDs are
// ordinary).
//
// Closed issues, epics, pinned issues, templates and issues from an
// external tracker or another system are never matched, nor is a candidate
// with any dependency row in either direction or any comment. An evidence
// error returns no matches.
func DetectTestPollution(ctx context.Context, evidence PollutionEvidence, issues []*types.Issue) ([]PollutionMatch, error) {
	var candidates []PollutionMatch
	var ids []string
	for _, issue := range issues {
		if isPollutionProtected(issue) {
			continue
		}
		if m, ok := classifyPollution(issue); ok {
			candidates = append(candidates, m)
			ids = append(ids, issue.ID)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	outgoing, err := evidence.GetDependencyRecordsForIssues(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading dependencies of pollution candidates: %w", err)
	}
	incoming, err := evidence.GetDependentRecordsForIssues(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading dependents of pollution candidates: %w", err)
	}
	comments, err := evidence.GetCommentCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading comments of pollution candidates: %w", err)
	}

	var matches []PollutionMatch
	for _, m := range candidates {
		id := m.Issue.ID
		if len(outgoing[id]) > 0 || len(incoming[id]) > 0 || comments[id] > 0 {
			continue
		}
		matches = append(matches, m)
	}
	return matches, nil
}

// isPollutionProtected reports whether an issue is never a pollution match
// whatever its title: closed work, epics, pinned context, templates, and
// issues another tracker or system owns.
func isPollutionProtected(issue *types.Issue) bool {
	if issue == nil {
		return true
	}
	switch {
	case issue.Status == types.StatusClosed,
		issue.IssueType == types.TypeEpic,
		issue.Pinned || issue.Status == types.StatusPinned,
		issue.IsTemplate,
		issue.ExternalRef != nil && strings.TrimSpace(*issue.ExternalRef) != "",
		strings.TrimSpace(issue.SourceSystem) != "":
		return true
	}
	return false
}

// classifyPollution applies the title and content rule to one issue.
func classifyPollution(issue *types.Issue) (PollutionMatch, bool) {
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

	// A test prefix alone is an ordinary way to name test-infrastructure
	// work, so it needs an empty description; a generic test phrase may
	// carry a few bytes. Any authored design, acceptance criteria or notes
	// mean someone wrote the issue up.
	eligible := (generic && len(description) < shortDescriptionBytes) || (prefix && description == "")
	if !eligible || hasAuthoredDetail(issue) {
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

// hasAuthoredDetail reports whether the issue carries any nonblank design,
// acceptance criteria or notes.
func hasAuthoredDetail(issue *types.Issue) bool {
	for _, field := range []string{issue.Design, issue.AcceptanceCriteria, issue.Notes} {
		if strings.TrimSpace(field) != "" {
			return true
		}
	}
	return false
}
