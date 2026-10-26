package tracker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// mockMatchKeyTracker is a tracker whose identifiers (the last path segment)
// repeat across repos, so it qualifies its pull match key by repo.
type mockMatchKeyTracker struct {
	*mockTracker
}

// MatchKey keys https://test.test/{repo}/{n} as {repo}#{n}, case-insensitively.
func (m *mockMatchKeyTracker) MatchKey(ref string) string {
	parts := strings.Split(strings.TrimPrefix(strings.ToLower(ref), "https://test.test/"), "/")
	if len(parts) != 2 {
		return ""
	}
	return parts[0] + "#" + parts[1]
}

// TestEnginePullPrefersMatchKeyer pins the engine's fallback match (taken when
// the exact external_ref lookup misses) to MatchKeyer when the tracker has
// one, and to ExtractIdentifier when it does not.
func TestEnginePullPrefersMatchKeyer(t *testing.T) {
	tests := []struct {
		name        string
		matchKeyer  bool
		localRef    string
		pulledURL   string
		wantCreated int
		wantUpdated int
	}{
		{name: "match keyer: same identifier in another repo creates", matchKeyer: true,
			localRef: "https://test.test/repo-a/7", pulledURL: "https://test.test/repo-b/7", wantCreated: 1},
		{name: "match keyer: same key under another spelling updates", matchKeyer: true,
			localRef: "https://test.test/REPO-A/7", pulledURL: "https://test.test/repo-a/7", wantUpdated: 1},
		{name: "no match keyer: falls back to ExtractIdentifier",
			localRef: "https://test.test/repo-a/7", pulledURL: "https://test.test/repo-b/7", wantUpdated: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &engineUOWState{
				issues: map[string]*types.Issue{
					"bd-a7": {
						ID:          "bd-a7",
						Title:       "Local issue 7",
						Status:      types.StatusOpen,
						IssueType:   types.TypeTask,
						Priority:    2,
						ExternalRef: strPtr(tt.localRef),
					},
				},
				configs: map[string]string{"issue_prefix": "bd"},
			}
			base := newMockTracker("test")
			base.issues = []TrackerIssue{{
				ID:         "7",
				Identifier: "7",
				URL:        tt.pulledURL,
				Title:      "Remote issue 7",
				UpdatedAt:  time.Now(),
			}}
			var tr IssueTracker = base
			if tt.matchKeyer {
				tr = &mockMatchKeyTracker{mockTracker: base}
			}

			result, err := NewEngine(tr, NewUOWStore(&engineUOWProvider{state: state}), "test-actor").
				Sync(context.Background(), SyncOptions{Pull: true})
			if err != nil {
				t.Fatalf("Sync() error: %v", err)
			}
			if result.PullStats.Created != tt.wantCreated || result.PullStats.Updated != tt.wantUpdated {
				t.Errorf("pull stats = {Created:%d Updated:%d}, want {Created:%d Updated:%d}",
					result.PullStats.Created, result.PullStats.Updated, tt.wantCreated, tt.wantUpdated)
			}
			wantTitle := "Local issue 7"
			if tt.wantUpdated == 1 {
				wantTitle = "Remote issue 7"
			}
			if got := state.issues["bd-a7"].Title; got != wantTitle {
				t.Errorf("bd-a7 title = %q, want %q", got, wantTitle)
			}
		})
	}
}
