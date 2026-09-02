// Package github provides client and data types for the GitHub REST API.
package github

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestDefaultGitHubMappingConfig verifies the default configuration has proper mappings.
func TestDefaultGitHubMappingConfig(t *testing.T) {
	config := DefaultMappingConfig()

	// Verify priority mappings exist
	if len(config.PriorityMap) == 0 {
		t.Error("PriorityMap is empty, want default priority mappings")
	}
	if p, ok := config.PriorityMap["high"]; !ok || p != 1 {
		t.Errorf("PriorityMap[\"high\"] = %d, want 1", p)
	}

	// Verify state mappings exist
	if len(config.StateMap) == 0 {
		t.Error("StateMap is empty, want default state mappings")
	}
	if s, ok := config.StateMap["open"]; !ok || s != "open" {
		t.Errorf("StateMap[\"open\"] = %q, want \"open\"", s)
	}
	if s, ok := config.StateMap["closed"]; !ok || s != "closed" {
		t.Errorf("StateMap[\"closed\"] = %q, want \"closed\"", s)
	}

	// Verify type mappings exist
	if len(config.LabelTypeMap) == 0 {
		t.Error("LabelTypeMap is empty, want default type mappings")
	}
	if typ, ok := config.LabelTypeMap["bug"]; !ok || typ != "bug" {
		t.Errorf("LabelTypeMap[\"bug\"] = %q, want \"bug\"", typ)
	}
}

// TestPriorityFromLabels verifies parsing priority::* labels.
func TestPriorityFromLabels(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		name         string
		labels       []string
		wantPriority int
	}{
		{
			name:         "priority::critical",
			labels:       []string{"priority::critical", "bug"},
			wantPriority: 0,
		},
		{
			name:         "priority::high",
			labels:       []string{"priority::high"},
			wantPriority: 1,
		},
		{
			name:         "priority::medium",
			labels:       []string{"priority::medium", "type::feature"},
			wantPriority: 2,
		},
		{
			name:         "priority::low",
			labels:       []string{"priority::low"},
			wantPriority: 3,
		},
		{
			name:         "no priority label defaults to medium",
			labels:       []string{"bug", "backend"},
			wantPriority: 2,
		},
		{
			name:         "empty labels defaults to medium",
			labels:       []string{},
			wantPriority: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := priorityFromLabels(tt.labels, config)
			if got != tt.wantPriority {
				t.Errorf("priorityFromLabels(%v) = %d, want %d", tt.labels, got, tt.wantPriority)
			}
		})
	}
}

// TestStatusFromLabelsAndState verifies status determination from labels and GitHub state.
func TestStatusFromLabelsAndState(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		name       string
		labels     []string
		state      string
		wantStatus string
	}{
		{
			name:       "status::in_progress overrides open state",
			labels:     []string{"status::in_progress"},
			state:      "open",
			wantStatus: "in_progress",
		},
		{
			name:       "status::blocked",
			labels:     []string{"status::blocked", "bug"},
			state:      "open",
			wantStatus: "blocked",
		},
		{
			name:       "status::deferred",
			labels:     []string{"status::deferred"},
			state:      "open",
			wantStatus: "deferred",
		},
		{
			name:       "closed state wins over status labels",
			labels:     []string{"status::in_progress"},
			state:      "closed",
			wantStatus: "closed",
		},
		{
			name:       "open state without status label",
			labels:     []string{"bug"},
			state:      "open",
			wantStatus: "open",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusFromLabelsAndState(tt.labels, tt.state, config)
			if got != tt.wantStatus {
				t.Errorf("statusFromLabelsAndState(%v, %q) = %q, want %q", tt.labels, tt.state, got, tt.wantStatus)
			}
		})
	}
}

// TestTypeFromLabels verifies parsing type::* labels.
func TestTypeFromLabels(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		name     string
		labels   []string
		wantType string
	}{
		{
			name:     "type::bug",
			labels:   []string{"type::bug", "priority::high"},
			wantType: "bug",
		},
		{
			name:     "type::feature",
			labels:   []string{"type::feature"},
			wantType: "feature",
		},
		{
			name:     "type::task",
			labels:   []string{"type::task"},
			wantType: "task",
		},
		{
			name:     "type::epic",
			labels:   []string{"type::epic"},
			wantType: "epic",
		},
		{
			name:     "type::chore",
			labels:   []string{"type::chore"},
			wantType: "chore",
		},
		{
			name:     "type::decision",
			labels:   []string{"type::decision"},
			wantType: "decision",
		},
		{
			name:     "type::spike",
			labels:   []string{"type::spike"},
			wantType: "spike",
		},
		{
			name:     "type::story",
			labels:   []string{"type::story"},
			wantType: "story",
		},
		{
			name:     "type::milestone",
			labels:   []string{"type::milestone"},
			wantType: "milestone",
		},
		{
			name:     "enhancement maps to feature",
			labels:   []string{"type::enhancement"},
			wantType: "feature",
		},
		{
			name:     "bare bug label (no prefix)",
			labels:   []string{"bug"},
			wantType: "bug",
		},
		{
			name:     "no type label defaults to task",
			labels:   []string{"priority::high"},
			wantType: "task",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := typeFromLabels(tt.labels, config)
			if got != tt.wantType {
				t.Errorf("typeFromLabels(%v) = %q, want %q", tt.labels, got, tt.wantType)
			}
		})
	}
}

// TestGitHubIssueToBeads verifies full conversion from GitHub Issue to beads Issue.
func TestGitHubIssueToBeads(t *testing.T) {
	config := DefaultMappingConfig()

	createdAt := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2024, 1, 16, 14, 0, 0, 0, time.UTC)

	ghIssue := &Issue{
		ID:     123456,
		Number: 42,
		Title:  "Fix authentication bug",
		Body:   "Users cannot log in with SSO",
		State:  "open",
		Labels: []Label{
			{Name: "type::bug"},
			{Name: "priority::high"},
			{Name: "status::in_progress"},
		},
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
		Assignee: &User{
			ID:    101,
			Login: "jdoe",
			Name:  "John Doe",
		},
		User: &User{
			ID:    102,
			Login: "alice",
			Name:  "Alice Smith",
		},
		HTMLURL: "https://github.com/org/repo/issues/42",
	}

	conversion := GitHubIssueToBeads(ghIssue, config)
	if conversion == nil {
		t.Fatal("GitHubIssueToBeads returned nil")
	}

	issue := conversion.Issue
	if issue == nil {
		t.Fatal("conversion.Issue is nil")
	}

	// Verify basic fields
	if issue.Title != "Fix authentication bug" {
		t.Errorf("Title = %q, want %q", issue.Title, "Fix authentication bug")
	}
	if issue.Description != "Users cannot log in with SSO" {
		t.Errorf("Description = %q, want %q", issue.Description, "Users cannot log in with SSO")
	}

	// Verify external reference
	if issue.ExternalRef == nil || *issue.ExternalRef != "https://github.com/org/repo/issues/42" {
		t.Errorf("ExternalRef = %v, want GitHub URL", issue.ExternalRef)
	}

	// Verify mapped fields
	if issue.IssueType != types.TypeBug {
		t.Errorf("IssueType = %q, want %q", issue.IssueType, types.TypeBug)
	}
	if issue.Priority != 1 {
		t.Errorf("Priority = %d, want 1 (high)", issue.Priority)
	}
	if issue.Status != types.StatusInProgress {
		t.Errorf("Status = %q, want %q", issue.Status, types.StatusInProgress)
	}

	// Verify assignee
	if issue.Assignee != "jdoe" {
		t.Errorf("Assignee = %q, want \"jdoe\"", issue.Assignee)
	}
}

// TestGitHubIssueToBeads_ClosedIssue verifies closed issues are converted correctly.
func TestGitHubIssueToBeads_ClosedIssue(t *testing.T) {
	config := DefaultMappingConfig()

	closedAt := time.Date(2024, 1, 17, 10, 0, 0, 0, time.UTC)
	ghIssue := &Issue{
		ID:       100,
		Number:   10,
		Title:    "Closed task",
		State:    "closed",
		ClosedAt: &closedAt,
		Labels:   []Label{{Name: "status::in_progress"}}, // Should be overridden by closed state
		HTMLURL:  "https://github.com/org/repo/issues/10",
	}

	conversion := GitHubIssueToBeads(ghIssue, config)
	issue := conversion.Issue
	if issue == nil {
		t.Fatal("conversion.Issue is nil")
	}

	if issue.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q (closed state overrides labels)", issue.Status, types.StatusClosed)
	}
}

// TestBeadsIssueToGitHubFields verifies conversion from beads Issue to GitHub update fields.
func TestBeadsIssueToGitHubFields(t *testing.T) {
	config := DefaultMappingConfig()

	beadsIssue := &types.Issue{
		ID:          "bd-abc123",
		Title:       "New feature request",
		Description: "Add dark mode support",
		IssueType:   types.TypeFeature,
		Priority:    1, // High
		Status:      types.StatusInProgress,
		Assignee:    "jdoe",
	}

	fields := BeadsIssueToGitHubFields(beadsIssue, config)

	// Verify title and body
	if fields["title"] != "New feature request" {
		t.Errorf("fields[\"title\"] = %v, want \"New feature request\"", fields["title"])
	}
	body, ok := fields["body"].(string)
	if !ok {
		t.Fatalf("fields[\"body\"] is not string")
	}
	if !strings.Contains(body, "Add dark mode support") {
		t.Errorf("body = %q, want original description", body)
	}
	if !strings.Contains(body, "<!-- bd: bd-abc123 sync=github-v1 -->") {
		t.Errorf("body = %q, want hidden bd ID marker", body)
	}

	// Verify labels include type, priority, and status
	labels, ok := fields["labels"].([]string)
	if !ok {
		t.Fatalf("fields[\"labels\"] is not []string")
	}

	hasType := false
	hasPriority := false
	hasStatus := false
	for _, l := range labels {
		if l == "type::feature" {
			hasType = true
		}
		if l == "priority::high" {
			hasPriority = true
		}
		if l == "status::in_progress" {
			hasStatus = true
		}
	}
	if !hasType {
		t.Errorf("labels missing type::feature, got %v", labels)
	}
	if !hasPriority {
		t.Errorf("labels missing priority::high, got %v", labels)
	}
	if !hasStatus {
		t.Errorf("labels missing status::in_progress, got %v", labels)
	}

	// Verify state is "open" for in-progress
	if fields["state"] != "open" {
		t.Errorf("fields[\"state\"] = %v, want \"open\"", fields["state"])
	}
}

// TestBeadsIssueToGitHubFields_ClosedState verifies state is set for closed issues.
func TestBeadsIssueToGitHubFields_ClosedState(t *testing.T) {
	config := DefaultMappingConfig()

	closedIssue := &types.Issue{
		Title:  "Completed task",
		Status: types.StatusClosed,
	}

	fields := BeadsIssueToGitHubFields(closedIssue, config)

	if fields["state"] != "closed" {
		t.Errorf("fields[\"state\"] = %v, want \"closed\"", fields["state"])
	}
}

func TestRenderGitHubIssueBodyIncludesDependencyTasklists(t *testing.T) {
	const targetRepo = "https://github.com/org/repo"
	blockerRef := "https://github.com/org/repo/issues/12"
	closedRef := "https://github.com/org/repo/issues/56"
	crossRepoRef := "https://github.com/other/elsewhere/issues/9"
	dependentRef := "https://github.com/org/repo/issues/34"
	issue := &types.Issue{
		ID:          "bd-target",
		Description: "target body",
	}
	deps := []*types.IssueWithDependencyMetadata{
		{
			Issue: types.Issue{
				ID:          "bd-blocker",
				Title:       "blocking issue",
				ExternalRef: &blockerRef,
			},
			DependencyType: types.DepBlocks,
		},
		{
			Issue: types.Issue{
				ID:          "bd-done",
				Title:       "finished blocker",
				Status:      types.StatusClosed,
				ExternalRef: &closedRef,
			},
			DependencyType: types.DepBlocks,
		},
		{
			Issue: types.Issue{
				ID:          "bd-far",
				Title:       "cross-repo blocker",
				ExternalRef: &crossRepoRef,
			},
			DependencyType: types.DepBlocks,
		},
	}
	dependents := []*types.IssueWithDependencyMetadata{
		{
			Issue: types.Issue{
				ID:          "bd-dependent",
				Title:       "dependent issue",
				ExternalRef: &dependentRef,
			},
			DependencyType: types.DepBlocks,
		},
	}

	body := RenderGitHubIssueBody(issue, deps, dependents, targetRepo)

	for _, want := range []string{
		"target body",
		"<!-- bd: bd-target sync=github-v1 -->",
		"### Dependencies",
		"- [ ] #12 — blocking issue (`bd-blocker`, blocked by)",
		// Closed issues render checked so the regenerated tasklist reflects
		// their state instead of resetting them to open.
		"- [x] #56 — finished blocker (`bd-done`, blocked by)",
		// Cross-repo refs keep the full URL: a bare #9 would resolve against
		// org/repo, pointing at an unrelated issue.
		"- [ ] https://github.com/other/elsewhere/issues/9 — cross-repo blocker (`bd-far`, blocked by)",
		// Neutral heading: incoming edges are not necessarily blocking.
		"### Dependents",
		"- [ ] #34 — dependent issue (`bd-dependent`, blocks)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Blocked by this issue") {
		t.Fatalf("body still uses the old 'Blocked by this issue' heading:\n%s", body)
	}
	if strings.Contains(body, "- [ ] #9 ") {
		t.Fatalf("cross-repo ref was shortened to a same-repo issue number:\n%s", body)
	}
}

// TestRenderGitHubIssueBodyStaysUnderBodyLimit pins the degrade-gracefully
// behavior: an issue with enough synthetic dependency/dependent edges to push
// the fully-rendered body over GitHub's body limit gets the tasklist sections
// dropped, keeping the description and the canonical footer (including the
// ID line Strip/Extract require) intact and the body under the limit.
func TestRenderGitHubIssueBodyStaysUnderBodyLimit(t *testing.T) {
	issue := &types.Issue{
		ID:          "bd-target",
		Description: "target body",
	}

	makeEdges := func(n int, prefix string) []*types.IssueWithDependencyMetadata {
		edges := make([]*types.IssueWithDependencyMetadata, 0, n)
		for i := 0; i < n; i++ {
			edges = append(edges, &types.IssueWithDependencyMetadata{
				Issue: types.Issue{
					ID:    fmt.Sprintf("bd-%s-%d", prefix, i),
					Title: fmt.Sprintf("synthetic %s edge number %d with a longer descriptive title to pad size", prefix, i),
				},
				DependencyType: types.DepBlocks,
			})
		}
		return edges
	}
	deps := makeEdges(1000, "dep")
	dependents := makeEdges(1000, "dependent")

	unbounded := buildGitHubIssueBody(issue, issue.Description, deps, dependents, "")
	if len(unbounded) <= githubBodyLimit {
		t.Fatalf("test setup did not exceed githubBodyLimit: got %d bytes, want > %d", len(unbounded), githubBodyLimit)
	}

	body := RenderGitHubIssueBody(issue, deps, dependents, "")

	if len(body) > githubBodyLimit {
		t.Fatalf("body exceeds githubBodyLimit: got %d bytes, want <= %d", len(body), githubBodyLimit)
	}
	if !strings.Contains(body, "target body") {
		t.Fatalf("body dropped user description:\n%s", body)
	}
	if !strings.Contains(body, "<!-- bd: bd-target sync=github-v1 -->") {
		t.Fatalf("body missing canonical ID line:\n%s", body)
	}
	if strings.Contains(body, "### Dependencies") || strings.Contains(body, "### Dependents") {
		t.Fatalf("body still contains tasklist sections after degrading:\n%s", body)
	}
	if got := ExtractBDIDFromGitHubSyncBlock(body); got != "bd-target" {
		t.Fatalf("ExtractBDIDFromGitHubSyncBlock() = %q, want bd-target", got)
	}
	if got := StripGitHubSyncBlock(body); got != "target body" {
		t.Fatalf("StripGitHubSyncBlock() = %q, want %q", got, "target body")
	}
}

// TestRenderGitHubIssueBodyUnderLimitUnchanged pins that a normal small issue
// is unaffected by the body-limit degrade path.
func TestRenderGitHubIssueBodyUnderLimitUnchanged(t *testing.T) {
	issue := &types.Issue{ID: "bd-target", Description: "small body"}
	deps := []*types.IssueWithDependencyMetadata{
		{
			Issue:          types.Issue{ID: "bd-dep", Title: "a dependency"},
			DependencyType: types.DepBlocks,
		},
	}

	body := RenderGitHubIssueBody(issue, deps, nil, "")
	want := buildGitHubIssueBody(issue, issue.Description, deps, nil, "")
	if body != want {
		t.Fatalf("small body was altered by the limit check:\n got: %q\nwant: %q", body, want)
	}
	if !strings.Contains(body, "### Dependencies") {
		t.Fatalf("small body lost its tasklist section:\n%s", body)
	}
}

// TestGitHubTaskTargetRepoScoping pins the shortening rule: only refs exactly
// inside the target repo become #<number>; unknown targets, foreign repos, and
// decorated URLs keep the full URL.
func TestGitHubTaskTargetRepoScoping(t *testing.T) {
	ref := func(s string) *types.Issue {
		return &types.Issue{ID: "bd-x", ExternalRef: &s}
	}
	cases := []struct {
		name   string
		issue  *types.Issue
		target string
		want   string
	}{
		{name: "same repo shortens", issue: ref("https://github.com/org/repo/issues/12"), target: "https://github.com/org/repo", want: "#12"},
		{name: "case-insensitive repo match", issue: ref("https://github.com/Org/Repo/issues/12"), target: "https://github.com/org/repo", want: "#12"},
		{name: "other repo keeps URL", issue: ref("https://github.com/other/elsewhere/issues/12"), target: "https://github.com/org/repo", want: "https://github.com/other/elsewhere/issues/12"},
		{name: "unknown target keeps URL", issue: ref("https://github.com/org/repo/issues/12"), target: "", want: "https://github.com/org/repo/issues/12"},
		{name: "decorated URL keeps URL", issue: ref("https://github.com/org/repo/issues/12#issuecomment-1"), target: "https://github.com/org/repo", want: "https://github.com/org/repo/issues/12#issuecomment-1"},
		{name: "no ref falls back to bead ID", issue: &types.Issue{ID: "bd-x"}, target: "https://github.com/org/repo", want: "`bd-x`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := githubTaskTarget(tc.issue, tc.target); got != tc.want {
				t.Fatalf("githubTaskTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGitHubIssueToBeadsStripsGeneratedSyncBlock(t *testing.T) {
	config := DefaultMappingConfig()
	ghIssue := &Issue{
		ID:     123,
		Number: 7,
		Title:  "Synced issue",
		Body: "User-authored body\n\n<!-- bd-github-sync: start -->\n" +
			"<!-- bd: bd-abc sync=github-v1 -->\n" +
			"## Beads\n\nSource: `bd-abc`\n" +
			"<!-- bd-github-sync: end -->\n",
		State:   "open",
		HTMLURL: "https://github.com/org/repo/issues/7",
	}

	conversion := GitHubIssueToBeads(ghIssue, config)
	if conversion.Issue.ID != "bd-abc" {
		t.Fatalf("ID = %q, want bd ID from hidden marker", conversion.Issue.ID)
	}
	if conversion.Issue.Description != "User-authored body" {
		t.Fatalf("Description = %q, want generated sync block stripped", conversion.Issue.Description)
	}
}

// TestGitHubSyncBlockRoundTrip pins the framing invariants:
//
//	Strip(Render(D)) == D
//	Render(Strip(Render(D))) == Render(D)
//
// across adversarial descriptions. Render must preserve the description
// byte-for-byte and Strip must remove exactly what Render appended (or
// nothing at all).
func TestGitHubSyncBlockRoundTrip(t *testing.T) {
	hostileDeps := []*types.IssueWithDependencyMetadata{
		{
			Issue: types.Issue{
				ID:    "bd-hostile",
				Title: "x <!-- bd-github-sync: end --> y",
			},
			DependencyType: types.DepBlocks,
		},
		{
			Issue: types.Issue{
				ID:    "bd-hostile2",
				Title: "<!-- bd: bd-evil sync=github-v1 --> <!-- bd-github-sync: start -->",
			},
			DependencyType: types.DepBlocks,
		},
	}

	cases := []struct {
		name string
		desc string
		deps []*types.IssueWithDependencyMetadata
	}{
		{name: "empty description", desc: ""},
		{name: "plain text", desc: "hello world"},
		{name: "trailing newline preserved", desc: "hello\n"},
		{name: "unpaired start marker", desc: "text\n<!-- bd-github-sync: start -->\nMY IMPORTANT NOTE"},
		{name: "unpaired end marker", desc: "text\n<!-- bd-github-sync: end -->\nafter"},
		{name: "nested markers", desc: "<!-- bd-github-sync: start -->\n<!-- bd-github-sync: start -->\ninner\n<!-- bd-github-sync: end -->"},
		{name: "truncated block", desc: "text\n\n<!-- bd-github-sync: start -->\n## Beads\ntruncated tail"},
		{name: "paired markers quoted in user text", desc: "quoting a synced body:\n<!-- bd-github-sync: start -->\n## Beads\nSource: `bd-old`\n<!-- bd-github-sync: end -->\nafter text"},
		{name: "description that is itself a rendered body", desc: "base\n\n<!-- bd-github-sync: start -->\n<!-- bd: bd-old sync=github-v1 -->\n## Beads\n\nSource: `bd-old`\n<!-- bd-github-sync: end -->"},
		{name: "markers inline in prose", desc: "see <!-- bd-github-sync: start --> inline and <!-- bd-github-sync: end --> here"},
		{name: "CRLF line endings", desc: "line1\r\nline2\r\n"},
		{name: "leading whitespace before markers", desc: "  <!-- bd-github-sync: start -->\n  <!-- bd-github-sync: end -->"},
		{name: "hostile dependency titles", desc: "user text", deps: hostileDeps},
		{name: "hostile dependency titles with marker-bearing description", desc: "text\n<!-- bd-github-sync: start -->\ndangling", deps: hostileDeps},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := &types.Issue{ID: "bd-target", Description: tc.desc}
			rendered := RenderGitHubIssueBody(issue, tc.deps, nil, "")

			if got := StripGitHubSyncBlock(rendered); got != tc.desc {
				t.Fatalf("Strip(Render(D)) != D\n got: %q\nwant: %q\nrendered: %q", got, tc.desc, rendered)
			}
			rerendered := RenderGitHubIssueBody(&types.Issue{ID: "bd-target", Description: StripGitHubSyncBlock(rendered)}, tc.deps, nil, "")
			if rerendered != rendered {
				t.Fatalf("Render(Strip(Render(D))) != Render(D)\n got: %q\nwant: %q", rerendered, rendered)
			}
		})
	}
}

// TestRenderGitHubIssueBodyRefusesUnpairedMarkers pins the refusal behavior:
// a description with malformed line-anchored markers is returned unchanged
// with no block appended, so a later strip can never span stray markers.
func TestRenderGitHubIssueBodyRefusesUnpairedMarkers(t *testing.T) {
	desc := "text\n<!-- bd-github-sync: start -->\nMY IMPORTANT NOTE"
	got := RenderGitHubIssueBody(&types.Issue{ID: "bd-target", Description: desc}, nil, nil, "")
	if got != desc {
		t.Fatalf("Render over unpaired start = %q, want description unchanged", got)
	}
	if strings.Contains(got, "sync=github-v1") {
		t.Fatalf("Render appended a block over an unpaired marker: %q", got)
	}
}

// TestRenderGitHubIssueBodyEscapesHostileDependencyTitles verifies marker
// strings in interpolated dependency titles are escaped and can neither
// terminate the block early nor inject a bd ID line.
func TestRenderGitHubIssueBodyEscapesHostileDependencyTitles(t *testing.T) {
	deps := []*types.IssueWithDependencyMetadata{
		{
			Issue: types.Issue{
				ID:    "bd-hostile",
				Title: "x <!-- bd-github-sync: end --> y <!-- bd: bd-evil sync=github-v1 -->",
			},
			DependencyType: types.DepBlocks,
		},
	}
	rendered := RenderGitHubIssueBody(&types.Issue{ID: "bd-target", Description: "user text"}, deps, nil, "")

	if strings.Contains(rendered, "<!-- bd-github-sync: end --> y") {
		t.Fatalf("hostile title left unescaped end marker in body:\n%s", rendered)
	}
	if got := ExtractBDIDFromGitHubSyncBlock(rendered); got != "bd-target" {
		t.Fatalf("ExtractBDIDFromGitHubSyncBlock() = %q, want bd-target (hostile title must not inject an ID)", got)
	}
	if got := StripGitHubSyncBlock(rendered); got != "user text" {
		t.Fatalf("Strip() = %q, want %q (hostile title must not terminate block early)", got, "user text")
	}
}

// TestStripGitHubSyncBlockRequiresCanonicalIDLine pins the fix for a body
// that merely ENDS with a well-formed, paired marker block that is not the
// generated footer (e.g. a user pasted an example of a synced body as the
// last paragraph of their own description, before the bead was ever synced).
// Without a canonical ID line inside it, the block must not be mistaken for
// Render's output and stripped, or an imported description would be silently
// truncated on first pull.
func TestStripGitHubSyncBlockRequiresCanonicalIDLine(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "trailing paired block without ID line",
			body: "user description ending with an example:\n\n" +
				"<!-- bd-github-sync: start -->\n## Beads\n\nSource: `bd-example`\n<!-- bd-github-sync: end -->",
		},
		{
			name: "trailing paired block with non-canonical id line",
			body: "user description ending with an example:\n\n" +
				"<!-- bd-github-sync: start -->\n<!-- bd: foo -->\n## Beads\n<!-- bd-github-sync: end -->",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripGitHubSyncBlock(tc.body); got != tc.body {
				t.Fatalf("StripGitHubSyncBlock(%q) = %q, want unchanged (no canonical ID line, not generated)", tc.body, got)
			}
			if got := ExtractBDIDFromGitHubSyncBlock(tc.body); got != "" {
				t.Fatalf("ExtractBDIDFromGitHubSyncBlock(%q) = %q, want empty (no canonical ID line)", tc.body, got)
			}
		})
	}
}

// TestExtractBDIDFromGitHubSyncBlockStrict pins the scoped extractor: only a
// line-anchored ID line carrying the sync=github-v1 token inside the single
// well-formed trailing block may bind a GitHub issue to a bead. Bare
// hand-placed markers (the #4307 suggestion adopted by pointer-only installs)
// are deliberately inert: the failure mode must be "no ID found", never
// "wrong ID found".
func TestExtractBDIDFromGitHubSyncBlockStrict(t *testing.T) {
	block := func(id string) string {
		return "<!-- bd-github-sync: start -->\n<!-- bd: " + id + " sync=github-v1 -->\n## Beads\n\nSource: `" + id + "`\n<!-- bd-github-sync: end -->"
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "well-formed trailing block", body: "user text\n\n" + block("bd-real"), want: "bd-real"},
		{name: "empty body", body: "", want: ""},
		{name: "bare legacy marker", body: "body\n\n<!-- bd: bd_0x2c-tkg7 -->", want: ""},
		{name: "two bare markers", body: "body\n\n<!-- bd: br-zbb -->\n<!-- bd: br-zbb.2 -->", want: ""},
		{name: "bare marker before valid block", body: "<!-- bd: bd-victim -->\n\n" + block("bd-real"), want: "bd-real"},
		{name: "marker in prose before block", body: "mentions <!-- bd: bd-victim sync=github-v1 --> inline\n\n" + block("bd-real"), want: "bd-real"},
		{name: "block missing sync token", body: "body\n\n<!-- bd-github-sync: start -->\n<!-- bd: bd-x -->\n<!-- bd-github-sync: end -->", want: ""},
		{name: "block not trailing", body: block("bd-x") + "\ntrailing user text", want: ""},
		{name: "unpaired start only", body: "<!-- bd-github-sync: start -->\n<!-- bd: bd-x sync=github-v1 -->", want: ""},
		{name: "indented markers are not a block", body: "  <!-- bd-github-sync: start -->\n<!-- bd: bd-x sync=github-v1 -->\n  <!-- bd-github-sync: end -->", want: ""},
		{name: "CRLF block", body: "user\r\n\r\n<!-- bd-github-sync: start -->\r\n<!-- bd: bd-real sync=github-v1 -->\r\n## Beads\r\n<!-- bd-github-sync: end -->\r\n", want: "bd-real"},
		{name: "malformed multi-id line", body: "<!-- bd-github-sync: start -->\n<!-- bd: bd-a bd-b sync=github-v1 -->\n<!-- bd-github-sync: end -->", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractBDIDFromGitHubSyncBlock(tc.body); got != tc.want {
				t.Fatalf("ExtractBDIDFromGitHubSyncBlock(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestGitHubIssueToBeadsValidatesIssuePrefix verifies an embedded bead ID
// with a foreign prefix is discarded rather than becoming a local primary key
// or binding an unrelated bead.
func TestGitHubIssueToBeadsValidatesIssuePrefix(t *testing.T) {
	body := func(id string) string {
		return "User body\n\n<!-- bd-github-sync: start -->\n<!-- bd: " + id + " sync=github-v1 -->\n## Beads\n<!-- bd-github-sync: end -->"
	}
	gh := func(id string) *Issue {
		return &Issue{ID: 1, Number: 7, Title: "t", Body: body(id), State: "open", HTMLURL: "https://github.com/org/repo/issues/7"}
	}

	config := DefaultMappingConfig()
	config.IssuePrefix = "bd"

	if conv := GitHubIssueToBeads(gh("bd-real"), config); conv.Issue.ID != "bd-real" {
		t.Fatalf("matching prefix: ID = %q, want bd-real", conv.Issue.ID)
	}
	if conv := GitHubIssueToBeads(gh("evil-1"), config); conv.Issue.ID != "" {
		t.Fatalf("foreign prefix: ID = %q, want discarded", conv.Issue.ID)
	}
	if conv := GitHubIssueToBeads(gh("bdx-1"), config); conv.Issue.ID != "" {
		t.Fatalf("prefix must match up to the dash: ID = %q, want discarded", conv.Issue.ID)
	}
}

func TestBeadsIssueToGitHubFieldsPreservesPreRenderedDependencyTasklists(t *testing.T) {
	config := DefaultMappingConfig()
	body := "body\n\n<!-- bd-github-sync: start -->\n" +
		"<!-- bd: bd-target sync=github-v1 -->\n" +
		"## Beads\n\nSource: `bd-target`\n\n" +
		"### Dependencies\n" +
		"- [ ] #12 — blocking issue (`bd-blocker`, blocked by)\n" +
		"<!-- bd-github-sync: end -->"
	fields := BeadsIssueToGitHubFields(&types.Issue{
		ID:          "bd-target",
		Title:       "target",
		Description: body,
	}, config)
	if fields["body"] != body {
		t.Fatalf("body was re-rendered without dependency tasklists:\n%v", fields["body"])
	}
}

// TestFilterNonScopedLabels verifies that non-scoped labels are preserved.
func TestFilterNonScopedLabels(t *testing.T) {
	labels := []string{
		"type::bug",
		"priority::high",
		"status::in_progress",
		"backend",
		"needs-review",
		"urgent",
	}

	filtered := filterNonScopedLabels(labels)

	expected := []string{"backend", "needs-review", "urgent"}
	if len(filtered) != len(expected) {
		t.Fatalf("filterNonScopedLabels returned %d labels, want %d", len(filtered), len(expected))
	}

	for i, l := range filtered {
		if l != expected[i] {
			t.Errorf("filtered[%d] = %q, want %q", i, l, expected[i])
		}
	}
}

// TestPriorityToLabel verifies conversion from beads priority to GitHub label value.
func TestPriorityToLabel(t *testing.T) {
	tests := []struct {
		priority int
		want     string
	}{
		{0, "critical"},
		{1, "high"},
		{2, "medium"},
		{3, "low"},
		{4, "none"},
		{-1, "medium"},
		{5, "medium"},
		{100, "medium"},
	}

	for _, tt := range tests {
		got := priorityToLabel(tt.priority)
		if got != tt.want {
			t.Errorf("priorityToLabel(%d) = %q, want %q", tt.priority, got, tt.want)
		}
	}
}

// TestMappingConfigUsesTypesConstants verifies that DefaultMappingConfig uses
// the exported mapping constants from types.go, ensuring single source of truth.
func TestMappingConfigUsesTypesConstants(t *testing.T) {
	config := DefaultMappingConfig()

	// Priority mappings should match types.go PriorityMapping
	for key, want := range PriorityMapping {
		if got := config.PriorityMap[key]; got != want {
			t.Errorf("PriorityMap[%q] = %d, want %d (from PriorityMapping)", key, got, want)
		}
	}

	// Status mappings should match types.go StatusMapping
	if s := config.StateMap["open"]; s != StatusMapping["open"] {
		t.Errorf("StateMap[\"open\"] = %q, want %q", s, StatusMapping["open"])
	}
	if s := config.StateMap["closed"]; s != StatusMapping["closed"] {
		t.Errorf("StateMap[\"closed\"] = %q, want %q", s, StatusMapping["closed"])
	}

	// Type mappings should match types.go typeMapping
	for key, want := range typeMapping {
		if got := config.LabelTypeMap[key]; got != want {
			t.Errorf("LabelTypeMap[%q] = %q, want %q (from typeMapping)", key, got, want)
		}
	}
}
