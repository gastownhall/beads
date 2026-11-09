package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// pollutionReasons joins a match's reasons for table comparisons.
func pollutionReasons(m PollutionMatch) string {
	return strings.Join(m.Reasons, "; ")
}

func TestPollutionClassifyPrefixTitleRequiresEmptyDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		title       string
		description string
		wantMatch   bool
		wantScore   float64
		wantReasons string
	}{
		{name: "test prefix, empty description", title: "test-something", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "mixed-case prefix, empty description", title: "Test-Quality stub", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "uppercase prefix, empty description", title: "BENCHMARK_run", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "benchmark separator underscore", title: "benchmark_run", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "sample separator space", title: "Sample throwaway stub", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "tmp prefix", title: "tmp throwaway", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "temp prefix", title: "temp scratch row", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "debug prefix", title: "debug probe", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "dummy prefix", title: "dummy_row", description: "", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "whitespace-only description counts as empty", title: "test-something", description: "   \n\t", wantMatch: true, wantScore: 0.7, wantReasons: "Title starts with a test-style prefix; No description"},
		{name: "prefix with any description is real work", title: "test-driver.sh isolation broken", description: "constants.sh writes a fixed shared path", wantMatch: false},
		{name: "prefix with short description is still real work", title: "test stub", description: "short", wantMatch: false},
		{name: "word containing prefix but not at start", title: "attendant row", description: "", wantMatch: false},
		{name: "prefix without separator", title: "testing the harness", description: "", wantMatch: false},
		{name: "ordinary title, empty description", title: "Real feature request", description: "", wantMatch: false},
	}

	for _, tt := range tests {
		issue := &types.Issue{ID: "tst-1", Title: tt.title, Description: tt.description, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		matches := ClassifyTestPollution([]*types.Issue{issue})
		if !tt.wantMatch {
			if len(matches) != 0 {
				t.Errorf("%s: ClassifyTestPollution(%q) = %v, want no match", tt.name, tt.title, matches)
			}
			continue
		}
		if len(matches) != 1 {
			t.Errorf("%s: ClassifyTestPollution(%q) returned %d matches, want 1", tt.name, tt.title, len(matches))
			continue
		}
		m := matches[0]
		if m.Score != tt.wantScore {
			t.Errorf("%s: score = %.2f, want %.2f", tt.name, m.Score, tt.wantScore)
		}
		if got := pollutionReasons(m); got != tt.wantReasons {
			t.Errorf("%s: reasons = %q, want %q", tt.name, got, tt.wantReasons)
		}
		if PollutionScoreHighBucket(m) {
			t.Errorf("%s: score %.2f must stay in the possible bucket", tt.name, m.Score)
		}
	}
}

func TestPollutionClassifyGenericPhraseRequiresThinDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		title       string
		description string
		wantMatch   bool
		wantScore   float64
		wantReasons string
	}{
		{name: "generic phrase, no description", title: "a test issue I made", description: "", wantMatch: true, wantScore: 0.8, wantReasons: "Generic test-issue title; No description"},
		{name: "issue for testing, no description", title: "made an issue for testing", description: "", wantMatch: true, wantScore: 0.8, wantReasons: "Generic test-issue title; No description"},
		{name: "sample issue, no description", title: "some Sample Issue here", description: "", wantMatch: true, wantScore: 0.8, wantReasons: "Generic test-issue title; No description"},
		{name: "generic phrase, 19-byte description", title: "a test issue I made", description: "1234567890123456789", wantMatch: true, wantScore: 0.7, wantReasons: "Generic test-issue title; Very short description"},
		{name: "generic phrase, 20-byte description", title: "a test issue I made", description: "12345678901234567890", wantMatch: false},
		{name: "generic phrase, whitespace-trimmed to empty", title: "a test issue I made", description: "  ", wantMatch: true, wantScore: 0.8, wantReasons: "Generic test-issue title; No description"},
		{name: "generic phrase with substantive description", title: "a test issue I made", description: "These rows back the regression corpus for issue #42.", wantMatch: false},
		{name: "prefix and generic phrase, no description", title: "test issue", description: "", wantMatch: true, wantScore: 1.2, wantReasons: "Title starts with a test-style prefix; Generic test-issue title; No description"},
		{name: "prefix and generic phrase, short description", title: "test issue again", description: "stub", wantMatch: true, wantScore: 1.1, wantReasons: "Title starts with a test-style prefix; Generic test-issue title; Very short description"},
		{name: "generic phrase away from start", title: "Investigate the test issue left in staging", description: "19 bytes ok", wantMatch: true, wantScore: 0.7, wantReasons: "Generic test-issue title; Very short description"},
	}

	for _, tt := range tests {
		issue := &types.Issue{ID: "tst-2", Title: tt.title, Description: tt.description, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		matches := ClassifyTestPollution([]*types.Issue{issue})
		if !tt.wantMatch {
			if len(matches) != 0 {
				t.Errorf("%s: ClassifyTestPollution(%q) = %v, want no match", tt.name, tt.title, matches)
			}
			continue
		}
		if len(matches) != 1 {
			t.Errorf("%s: ClassifyTestPollution(%q) returned %d matches, want 1", tt.name, tt.title, len(matches))
			continue
		}
		m := matches[0]
		if m.Score != tt.wantScore {
			t.Errorf("%s: score = %.2f, want %.2f", tt.name, m.Score, tt.wantScore)
		}
		if got := pollutionReasons(m); got != tt.wantReasons {
			t.Errorf("%s: reasons = %q, want %q", tt.name, got, tt.wantReasons)
		}
	}
}

func TestPollutionClassifyAuthoredFieldsExcludeMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		issue *types.Issue
	}{
		{name: "design excludes prefix match", issue: &types.Issue{ID: "tst-3", Title: "test-thing", Description: "", Design: "Plan the cleanup", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}},
		{name: "acceptance criteria exclude generic match", issue: &types.Issue{ID: "tst-4", Title: "a test issue", Description: "19 bytes exactly!", AcceptanceCriteria: "- [ ] gone", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}},
		{name: "notes exclude prefix match", issue: &types.Issue{ID: "tst-5", Title: "tmp entry", Description: "", Notes: "kept deliberately", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}},
		{name: "whitespace-only authored fields do not exclude", issue: &types.Issue{ID: "tst-6", Title: "tmp entry", Description: "", Design: "  ", Notes: "\n", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}},
	}

	for _, tt := range tests[:len(tests)-1] {
		if matches := ClassifyTestPollution([]*types.Issue{tt.issue}); len(matches) != 0 {
			t.Errorf("%s: ClassifyTestPollution matched %s %q, want no match", tt.name, tt.issue.ID, tt.issue.Title)
		}
	}

	// Whitespace-only authored fields keep the match (content checks trim).
	whitespace := tests[len(tests)-1].issue
	matches := ClassifyTestPollution([]*types.Issue{whitespace})
	if len(matches) != 1 {
		t.Fatalf("whitespace-only authored fields: %d matches, want 1", len(matches))
	}
}

func TestPollutionClassifyIgnoresIDShapeAndTiming(t *testing.T) {
	t.Parallel()

	// A sequential-looking ID and a same-minute cohort are import
	// signatures, not pollution evidence; they neither add score nor
	// exclude a match.
	seqID := &types.Issue{ID: "abc-123", Title: "test stub", Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	plainID := &types.Issue{ID: "tst-xyz", Title: "test stub", Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	matches := ClassifyTestPollution([]*types.Issue{seqID, plainID})
	if len(matches) != 2 {
		t.Fatalf("ClassifyTestPollution returned %d matches, want 2", len(matches))
	}
	for _, m := range matches {
		if m.Score != 0.7 {
			t.Errorf("%s: score = %.2f, want 0.7 (ID shape must not add evidence)", m.Issue.ID, m.Score)
		}
		if got := pollutionReasons(m); got != "Title starts with a test-style prefix; No description" {
			t.Errorf("%s: reasons = %q, want only title/content reasons", m.Issue.ID, got)
		}
	}

	// An ordinary title on a sequential ID never matches on ID shape alone.
	ordinary := &types.Issue{ID: "abc-456", Title: "Imported report finding", Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if matches := ClassifyTestPollution([]*types.Issue{ordinary}); len(matches) != 0 {
		t.Errorf("ordinary title with sequential ID matched, want no match")
	}
}

func TestPollutionHighBucketSplit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		score float64
		want  bool
	}{
		{0.7, false},
		{0.8, false},
		{0.89, false},
		{0.9, true},
		{1.2, true},
	}
	for _, tt := range tests {
		m := PollutionMatch{Score: tt.score}
		if got := PollutionScoreHighBucket(m); got != tt.want {
			t.Errorf("PollutionScoreHighBucket(%.2f) = %v, want %v", tt.score, got, tt.want)
		}
	}
}

func TestIsTestTitleMatchesPrefixesCaseInsensitively(t *testing.T) {
	t.Parallel()

	tests := []struct {
		title string
		want  bool
	}{
		{"test-something", true},
		{"TEST-something", true},
		{"Sample issue", true},
		{"Real feature request", false},
		{"attempt something", false},
		{"testing the harness", false},
	}
	for _, tt := range tests {
		if got := IsTestTitle(tt.title); got != tt.want {
			t.Errorf("IsTestTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

func TestPollutionClassifyExcludesProtectedFields(t *testing.T) {
	t.Parallel()

	externalRef := "gh-9"
	base := func() *types.Issue {
		return &types.Issue{ID: "tst-9", Title: "test leftover", Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}

	tests := []struct {
		name  string
		mutcr func(*types.Issue)
	}{
		{name: "closed status", mutcr: func(i *types.Issue) { i.Status = types.StatusClosed }},
		{name: "epic type", mutcr: func(i *types.Issue) { i.IssueType = types.TypeEpic }},
		{name: "pinned flag", mutcr: func(i *types.Issue) { i.Pinned = true }},
		{name: "pinned status", mutcr: func(i *types.Issue) { i.Status = types.StatusPinned }},
		{name: "template", mutcr: func(i *types.Issue) { i.IsTemplate = true }},
		{name: "external ref", mutcr: func(i *types.Issue) { i.ExternalRef = &externalRef }},
		{name: "source system", mutcr: func(i *types.Issue) { i.SourceSystem = "gh-importer" }},
	}

	for _, tt := range tests {
		issue := base()
		tt.mutcr(issue)
		if matches := ClassifyTestPollution([]*types.Issue{issue}); len(matches) != 0 {
			t.Errorf("%s: ClassifyTestPollution matched %s, want no match (protected issue)", tt.name, issue.ID)
		}
	}

	// The unprotected baseline still matches, so the exclusions above are
	// what made the difference.
	if matches := ClassifyTestPollution([]*types.Issue{base()}); len(matches) != 1 {
		t.Errorf("unprotected baseline: %d matches, want 1", len(matches))
	}

	// A blank external ref is not protection: only a nonblank one is.
	blankRef := base()
	spaces := "  "
	blankRef.ExternalRef = &spaces
	if matches := ClassifyTestPollution([]*types.Issue{blankRef}); len(matches) != 1 {
		t.Errorf("blank external ref: %d matches, want 1 (a blank ref does not protect)", len(matches))
	}

	// Other statuses stay eligible under the same rule; the classifier is
	// not an open/in-progress allowlist.
	inProgress := base()
	inProgress.Status = types.StatusInProgress
	if matches := ClassifyTestPollution([]*types.Issue{inProgress}); len(matches) != 1 {
		t.Errorf("in-progress issue: %d matches, want 1 (only closed/pinned are protected statuses)", len(matches))
	}
}

// fakePollutionEvidence records every call so tests can pin the batch shape:
// which IDs were queried, how many times, and which method errored.
type fakePollutionEvidence struct {
	outgoing   map[string][]*types.Dependency
	incoming   map[string][]*types.Dependency
	comments   map[string]int
	outErr     error
	inErr      error
	commentErr error

	outCalls    [][]string
	inCalls     [][]string
	commentCall [][]string
}

func (f *fakePollutionEvidence) GetDependencyRecordsForIssues(_ context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	f.outCalls = append(f.outCalls, append([]string(nil), issueIDs...))
	if f.outErr != nil {
		return nil, f.outErr
	}
	return f.outgoing, nil
}

func (f *fakePollutionEvidence) GetDependentRecordsForIssues(_ context.Context, targetIDs []string) (map[string][]*types.Dependency, error) {
	f.inCalls = append(f.inCalls, append([]string(nil), targetIDs...))
	if f.inErr != nil {
		return nil, f.inErr
	}
	return f.incoming, nil
}

func (f *fakePollutionEvidence) GetCommentCounts(_ context.Context, issueIDs []string) (map[string]int, error) {
	f.commentCall = append(f.commentCall, append([]string(nil), issueIDs...))
	if f.commentErr != nil {
		return nil, f.commentErr
	}
	return f.comments, nil
}

func pollutionCandidate(title, id string) *types.Issue {
	return &types.Issue{ID: id, Title: title, Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

func TestPollutionDetectExcludesConnectedAndCommentedIssues(t *testing.T) {
	t.Parallel()

	candidates := []*types.Issue{
		pollutionCandidate("test candidate outgoing", "cand-out"),
		pollutionCandidate("test candidate incoming", "cand-in"),
		pollutionCandidate("test candidate commented", "cand-com"),
		pollutionCandidate("test candidate isolated", "cand-iso"),
	}
	// An ineligible issue in the input must never reach the evidence seam.
	ineligible := pollutionCandidate("test candidate with description", "cand-desc")
	ineligible.Description = "Substantive description makes this real work."

	evidence := &fakePollutionEvidence{
		outgoing: map[string][]*types.Dependency{
			"cand-out": {{IssueID: "cand-out", DependsOnID: "some-other", Type: types.DepBlocks}},
		},
		incoming: map[string][]*types.Dependency{
			// Any edge type protects, not only blocking ones.
			"cand-in": {{IssueID: "some-other", DependsOnID: "cand-in", Type: types.DepRelated}},
		},
		comments: map[string]int{"cand-com": 2},
	}

	matches, err := DetectTestPollution(context.Background(), evidence, append(candidates, ineligible))
	if err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}
	if len(matches) != 1 || matches[0].Issue.ID != "cand-iso" {
		var got []string
		for _, m := range matches {
			got = append(got, m.Issue.ID)
		}
		t.Fatalf("DetectTestPollution matched %v, want [cand-iso]", got)
	}

	// Every evidence query carried exactly the preliminary candidate IDs,
	// once, and never the ineligible row.
	want := []string{"cand-out", "cand-in", "cand-com", "cand-iso"}
	for name, calls := range map[string][][]string{
		"outgoing": evidence.outCalls,
		"incoming": evidence.inCalls,
		"comments": evidence.commentCall,
	} {
		if len(calls) != 1 {
			t.Errorf("%s: %d calls, want 1", name, len(calls))
			continue
		}
		if strings.Join(calls[0], ",") != strings.Join(want, ",") {
			t.Errorf("%s: called with %v, want %v", name, calls[0], want)
		}
	}
}

func TestPollutionDetectEvidenceErrorsReturnNoMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		evidence  *fakePollutionEvidence
		wantInErr string
	}{
		{name: "outgoing error", evidence: &fakePollutionEvidence{outErr: errors.New("out boom")}, wantInErr: "querying outgoing dependencies: out boom"},
		{name: "incoming error", evidence: &fakePollutionEvidence{inErr: errors.New("in boom")}, wantInErr: "querying incoming dependencies: in boom"},
		{name: "comment error", evidence: &fakePollutionEvidence{commentErr: errors.New("com boom")}, wantInErr: "querying comment counts: com boom"},
	}
	for _, tt := range tests {
		// One candidate would match on classification; the evidence error
		// must keep it out of the results entirely.
		matches, err := DetectTestPollution(context.Background(), tt.evidence, []*types.Issue{pollutionCandidate("test candidate", "cand-1")})
		if err == nil {
			t.Errorf("%s: DetectTestPollution error = nil, want failure", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantInErr) {
			t.Errorf("%s: error = %q, want it to contain %q", tt.name, err.Error(), tt.wantInErr)
		}
		if len(matches) != 0 {
			t.Errorf("%s: %d matches escaped an evidence error, want none", tt.name, len(matches))
		}
	}
}

func TestPollutionDetectSkipsEvidenceWithoutCandidates(t *testing.T) {
	t.Parallel()

	// No eligible candidates: the store must not be queried at all.
	ineligible := []*types.Issue{
		pollutionCandidate("test candidate with description", "cand-desc"),
	}
	ineligible[0].Description = "Substantive description makes this real work."

	evidence := &fakePollutionEvidence{}
	matches, err := DetectTestPollution(context.Background(), evidence, ineligible)
	if err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("DetectTestPollution matched %d, want 0", len(matches))
	}
	if len(evidence.outCalls)+len(evidence.inCalls)+len(evidence.commentCall) != 0 {
		t.Errorf("evidence queried %d times with no candidates, want 0", len(evidence.outCalls)+len(evidence.inCalls)+len(evidence.commentCall))
	}
}
