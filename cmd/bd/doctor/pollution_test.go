package doctor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
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

	matches, err := DetectTestPollution(context.Background(), &fakePollutionEvidence{}, issues)
	if err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}
	if len(matches) != 0 {
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
				matches, err := DetectTestPollution(context.Background(), &fakePollutionEvidence{}, []*types.Issue{&issue})
				if err != nil {
					t.Fatalf("variant %d: DetectTestPollution: %v", i, err)
				}
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

// fakePollutionEvidence is a PollutionEvidence whose rows, counts and errors
// a test sets, and which records the IDs each lookup received.
type fakePollutionEvidence struct {
	outgoing    map[string][]*types.Dependency
	incoming    map[string][]*types.Dependency
	comments    map[string]int
	outgoingErr error
	incomingErr error
	commentsErr error
	calls       map[string][][]string
}

func (f *fakePollutionEvidence) record(method string, ids []string) {
	if f.calls == nil {
		f.calls = map[string][][]string{}
	}
	f.calls[method] = append(f.calls[method], slices.Clone(ids))
}

func (f *fakePollutionEvidence) GetDependencyRecordsForIssues(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	f.record("outgoing", ids)
	return f.outgoing, f.outgoingErr
}

func (f *fakePollutionEvidence) GetDependentRecordsForIssues(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	f.record("incoming", ids)
	return f.incoming, f.incomingErr
}

func (f *fakePollutionEvidence) GetCommentCounts(_ context.Context, ids []string) (map[string]int, error) {
	f.record("comments", ids)
	return f.comments, f.commentsErr
}

// pollutionCandidate returns an issue the title and content rule matches.
func pollutionCandidate(id string) *types.Issue {
	return &types.Issue{ID: id, Title: "test-" + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
}

func matchIDs(matches []PollutionMatch) []string {
	var ids []string
	for _, m := range matches {
		ids = append(ids, m.Issue.ID)
	}
	return ids
}

// TestPollutionExcludesProtectedIssues pins that an otherwise matching issue
// is never a pollution match when it is closed, an epic, pinned, a template
// or owned by another tracker or system; other statuses stay eligible.
func TestPollutionExcludesProtectedIssues(t *testing.T) {
	t.Parallel()

	ref := "gh-9"
	blank := "  "
	tests := []struct {
		name      string
		mutate    func(*types.Issue)
		wantMatch bool
	}{
		{name: "open", mutate: func(*types.Issue) {}, wantMatch: true},
		{name: "in progress", mutate: func(i *types.Issue) { i.Status = types.StatusInProgress }, wantMatch: true},
		{name: "blocked", mutate: func(i *types.Issue) { i.Status = types.StatusBlocked }, wantMatch: true},
		{name: "deferred", mutate: func(i *types.Issue) { i.Status = types.StatusDeferred }, wantMatch: true},
		{name: "blank external ref", mutate: func(i *types.Issue) { i.ExternalRef = &blank }, wantMatch: true},
		{name: "closed", mutate: func(i *types.Issue) { i.Status = types.StatusClosed }},
		{name: "epic", mutate: func(i *types.Issue) { i.IssueType = types.TypeEpic }},
		{name: "pinned flag", mutate: func(i *types.Issue) { i.Pinned = true }},
		{name: "pinned status", mutate: func(i *types.Issue) { i.Status = types.StatusPinned }},
		{name: "template", mutate: func(i *types.Issue) { i.IsTemplate = true }},
		{name: "external ref", mutate: func(i *types.Issue) { i.ExternalRef = &ref }},
		{name: "source system", mutate: func(i *types.Issue) { i.SourceSystem = "jira" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			issue := pollutionCandidate("bd-1")
			tt.mutate(issue)
			evidence := &fakePollutionEvidence{}
			matches, err := DetectTestPollution(context.Background(), evidence, []*types.Issue{issue})
			if err != nil {
				t.Fatalf("DetectTestPollution: %v", err)
			}
			if got := len(matches) == 1; got != tt.wantMatch {
				t.Fatalf("matched = %v, want %v", got, tt.wantMatch)
			}
			if !tt.wantMatch && len(evidence.calls) != 0 {
				t.Fatalf("evidence lookups %v for a protected issue, want none", evidence.calls)
			}
		})
	}
}

// TestPollutionExcludesConnectedIssues pins that any raw dependency row in
// either direction, whatever its type and whether or not its other end
// resolves, and any comment rule a candidate out.
func TestPollutionExcludesConnectedIssues(t *testing.T) {
	t.Parallel()

	dep := func(from, to string, typ types.DependencyType) *types.Dependency {
		return &types.Dependency{IssueID: from, DependsOnID: to, Type: typ}
	}
	tests := []struct {
		name     string
		evidence fakePollutionEvidence
	}{
		{name: "outgoing blocks", evidence: fakePollutionEvidence{outgoing: map[string][]*types.Dependency{"bd-1": {dep("bd-1", "bd-9", types.DepBlocks)}}}},
		{name: "outgoing parent-child", evidence: fakePollutionEvidence{outgoing: map[string][]*types.Dependency{"bd-1": {dep("bd-1", "bd-9", types.DepParentChild)}}}},
		{name: "outgoing related", evidence: fakePollutionEvidence{outgoing: map[string][]*types.Dependency{"bd-1": {dep("bd-1", "bd-9", types.DepRelated)}}}},
		{name: "outgoing external", evidence: fakePollutionEvidence{outgoing: map[string][]*types.Dependency{"bd-1": {dep("bd-1", "external:other:cap", types.DepBlocks)}}}},
		{name: "outgoing unresolved", evidence: fakePollutionEvidence{outgoing: map[string][]*types.Dependency{"bd-1": {dep("bd-1", "gone-404", types.DepRelated)}}}},
		{name: "incoming blocks", evidence: fakePollutionEvidence{incoming: map[string][]*types.Dependency{"bd-1": {dep("bd-9", "bd-1", types.DepBlocks)}}}},
		{name: "incoming parent-child", evidence: fakePollutionEvidence{incoming: map[string][]*types.Dependency{"bd-1": {dep("bd-9", "bd-1", types.DepParentChild)}}}},
		{name: "incoming discovered-from", evidence: fakePollutionEvidence{incoming: map[string][]*types.Dependency{"bd-1": {dep("bd-9", "bd-1", types.DepDiscoveredFrom)}}}},
		{name: "comment", evidence: fakePollutionEvidence{comments: map[string]int{"bd-1": 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			evidence := tt.evidence
			issues := []*types.Issue{pollutionCandidate("bd-1"), pollutionCandidate("bd-2")}
			matches, err := DetectTestPollution(context.Background(), &evidence, issues)
			if err != nil {
				t.Fatalf("DetectTestPollution: %v", err)
			}
			if got := matchIDs(matches); !slices.Equal(got, []string{"bd-2"}) {
				t.Fatalf("matches = %v, want [bd-2]", got)
			}
		})
	}
}

// TestPollutionEvidenceLookups pins that evidence is read once per method,
// batched over the preliminary candidates only, and not at all when no
// issue passes the title, content and protection rules.
func TestPollutionEvidenceLookups(t *testing.T) {
	t.Parallel()

	closed := pollutionCandidate("bd-3")
	closed.Status = types.StatusClosed
	issues := []*types.Issue{
		pollutionCandidate("bd-1"),
		{ID: "bd-real", Title: "Fix login", Status: types.StatusOpen, IssueType: types.TypeBug},
		closed,
		pollutionCandidate("bd-2"),
	}
	evidence := &fakePollutionEvidence{}
	if _, err := DetectTestPollution(context.Background(), evidence, issues); err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}
	for _, method := range []string{"outgoing", "incoming", "comments"} {
		if got := evidence.calls[method]; len(got) != 1 || !slices.Equal(got[0], []string{"bd-1", "bd-2"}) {
			t.Errorf("%s lookups = %v, want one call with [bd-1 bd-2]", method, got)
		}
	}

	evidence = &fakePollutionEvidence{}
	matches, err := DetectTestPollution(context.Background(), evidence, []*types.Issue{issues[1], closed})
	if err != nil || len(matches) != 0 {
		t.Fatalf("DetectTestPollution = %v, %v; want no matches", matches, err)
	}
	if len(evidence.calls) != 0 {
		t.Fatalf("evidence lookups %v with no candidates, want none", evidence.calls)
	}
}

// TestPollutionEvidenceErrors pins that a failure in any evidence lookup
// returns an error and no matches, never an unchecked candidate.
func TestPollutionEvidenceErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	for name, evidence := range map[string]*fakePollutionEvidence{
		"outgoing": {outgoingErr: boom},
		"incoming": {incomingErr: boom},
		"comments": {commentsErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			matches, err := DetectTestPollution(context.Background(), evidence, []*types.Issue{pollutionCandidate("bd-1")})
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want %v", err, boom)
			}
			if matches != nil {
				t.Fatalf("matches = %v, want none on error", matchIDs(matches))
			}
		})
	}
}

// fakePollutionStore answers SearchIssues the way the SQL builder does for
// TitleContains (case-insensitive substring) and records each filter.
type fakePollutionStore struct {
	fakePollutionEvidence
	issues    []*types.Issue
	searchErr error
	filters   []types.IssueFilter
}

func (f *fakePollutionStore) SearchIssues(_ context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.filters = append(f.filters, filter)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	if query != "" {
		return nil, fmt.Errorf("unexpected query %q", query)
	}
	var out []*types.Issue
	for _, issue := range f.issues {
		if filter.TitleContains == "" || strings.Contains(strings.ToLower(issue.Title), strings.ToLower(filter.TitleContains)) {
			out = append(out, issue)
		}
	}
	return out, nil
}

// TestPollutionGateMatchesFullScan pins that the full doctor run's Test
// Pollution check, which searches candidate titles, reports the same IDs and
// count as the classifier over every issue (bd doctor --check=pollution):
// mixed case, duplicate search hits, a test- ID with an ordinary title and
// protected or connected issues included.
func TestPollutionGateMatchesFullScan(t *testing.T) {
	t.Parallel()

	ref := "gh-12"
	issues := []*types.Issue{
		{ID: "bd-1", Title: "Test-quality architecture", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "bd-2", Title: "TEST ISSUE for a sample issue", Description: "x", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "bd-3", Title: "Created an Issue For Testing", Status: types.StatusInProgress, IssueType: types.TypeBug},
		{ID: "bd-4", Title: "DUMMY_record", Status: types.StatusOpen, IssueType: types.TypeChore},
		{ID: "test-5", Title: "Ordinary work", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "bd-6", Title: "tmp-scratch", Status: types.StatusClosed, IssueType: types.TypeTask},
		{ID: "bd-7", Title: "temp-thing", ExternalRef: &ref, Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "bd-8", Title: "debug-linked", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "bd-9", Title: "benchmark-commented", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "bd-10", Title: "test-driver.sh clobbers env", Description: "Sourcing constants.sh overwrites exported paths.", Status: types.StatusOpen, IssueType: types.TypeBug},
		{ID: "bd-11", Title: "Sample-run epic", Status: types.StatusOpen, IssueType: types.TypeEpic},
	}
	newStore := func() *fakePollutionStore {
		return &fakePollutionStore{
			fakePollutionEvidence: fakePollutionEvidence{
				incoming: map[string][]*types.Dependency{"bd-8": {{IssueID: "bd-x", DependsOnID: "bd-8", Type: types.DepRelated}}},
				comments: map[string]int{"bd-9": 2},
			},
			issues: issues,
		}
	}
	want := []string{"bd-1", "bd-2", "bd-3", "bd-4"}

	full := newStore()
	all, err := full.SearchIssues(context.Background(), "", types.IssueFilter{})
	if err != nil {
		t.Fatal(err)
	}
	detailed, err := DetectTestPollution(context.Background(), full, all)
	if err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}
	gateStore := newStore()
	gate, err := FindTestPollution(context.Background(), gateStore)
	if err != nil {
		t.Fatalf("FindTestPollution: %v", err)
	}
	detailedIDs, gateIDs := matchIDs(detailed), matchIDs(gate)
	slices.Sort(detailedIDs)
	slices.Sort(gateIDs)
	slices.Sort(want)
	if !slices.Equal(detailedIDs, want) || !slices.Equal(gateIDs, want) {
		t.Fatalf("detailed = %v, gate = %v, want both %v", detailedIDs, gateIDs, want)
	}

	// The searches narrow candidates only: one per test word, title only,
	// no status, type, limit or row cap.
	if len(gateStore.filters) != len(testTitleWords) {
		t.Fatalf("gate ran %d searches, want %d", len(gateStore.filters), len(testTitleWords))
	}
	for i, filter := range gateStore.filters {
		if !reflect.DeepEqual(filter, types.IssueFilter{TitleContains: testTitleWords[i]}) {
			t.Errorf("search %d filter = %+v, want TitleContains %q only", i, filter, testTitleWords[i])
		}
	}
	// Duplicate hits (bd-2 matches "test" and "sample") are classified once.
	if got := gateStore.calls["comments"]; len(got) != 1 || !slices.Equal(slices.Sorted(slices.Values(got[0])), []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-8", "bd-9"}) {
		t.Errorf("evidence lookups %v, want one call with each candidate once", got)
	}

	check := checkTestPollutionWithStore(context.Background(), newStore())
	if check.Status != StatusWarning || !strings.HasPrefix(check.Message, fmt.Sprintf("%d ", len(want))) {
		t.Fatalf("gate check = %q %q, want a warning counting %d", check.Status, check.Message, len(want))
	}
}

// TestPollutionGateSearchesCoverEveryMarker pins the invariant the gate's
// candidate searches rely on: each prefix word and generic phrase the
// classifier recognizes contains a searched word.
func TestPollutionGateSearchesCoverEveryMarker(t *testing.T) {
	t.Parallel()

	markers := slices.Clone(genericTestPhrases)
	for _, word := range []string{"test", "benchmark", "sample", "tmp", "temp", "debug", "dummy"} {
		if !testPrefixPattern.MatchString(word + "-x") {
			t.Fatalf("prefix pattern no longer matches %q", word+"-x")
		}
		markers = append(markers, word)
	}
	for _, marker := range markers {
		if !slices.ContainsFunc(testTitleWords, func(w string) bool { return strings.Contains(marker, w) }) {
			t.Errorf("marker %q contains no searched title word %v", marker, testTitleWords)
		}
	}
}

// TestPollutionGateSearchError pins that a failed candidate search is the
// gate's "query failed" warning, never a clean bill of health.
func TestPollutionGateSearchError(t *testing.T) {
	t.Parallel()

	store := &fakePollutionStore{searchErr: errors.New("boom")}
	if _, err := FindTestPollution(context.Background(), store); err == nil {
		t.Fatal("FindTestPollution succeeded on a failed search")
	}
	check := checkTestPollutionWithStore(context.Background(), store)
	if check.Status != StatusWarning || check.Message != "N/A (query failed)" {
		t.Fatalf("check = %q %q, want warning N/A (query failed)", check.Status, check.Message)
	}

	store = &fakePollutionStore{
		fakePollutionEvidence: fakePollutionEvidence{commentsErr: errors.New("boom")},
		issues:                []*types.Issue{pollutionCandidate("bd-1")},
	}
	check = checkTestPollutionWithStore(context.Background(), store)
	if check.Status != StatusWarning || check.Message != "N/A (query failed)" {
		t.Fatalf("check = %q %q, want warning N/A (query failed)", check.Status, check.Message)
	}
}
