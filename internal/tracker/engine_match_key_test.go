package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
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

// mockIdentityTracker gives mockMatchKeyTracker a rename-stable identity: a
// local issue's is its SourceSystem, a pulled issue's "id:" + its tracker ID.
type mockIdentityTracker struct {
	*mockMatchKeyTracker
}

func (m *mockIdentityTracker) LocalIdentityKey(issue *types.Issue) string { return issue.SourceSystem }
func (m *mockIdentityTracker) RemoteIdentityKey(issue *TrackerIssue) string {
	return "id:" + issue.ID
}

// TestEnginePullMatchesIdentityKeyFirst pins the IdentityKeyer contract of the
// pull fallback: a pulled issue matches the local issue with its identity even
// under another match key (a moved issue), and a local issue that has an
// identity is never matched by match key alone.
func TestEnginePullMatchesIdentityKeyFirst(t *testing.T) {
	tests := []struct {
		name        string
		identity    string
		localRef    string
		wantCreated int
		wantUpdated int
	}{
		{name: "same identity under another match key updates", identity: "id:7",
			localRef: "https://test.test/repo-old/7", wantUpdated: 1},
		{name: "other identity under the same match key creates", identity: "id:8",
			localRef: "https://test.test/REPO-A/7", wantCreated: 1},
		{name: "no identity falls back to the match key",
			localRef: "https://test.test/REPO-A/7", wantUpdated: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &engineUOWState{
				issues: map[string]*types.Issue{
					"bd-a7": {
						ID:           "bd-a7",
						Title:        "Local issue 7",
						Status:       types.StatusOpen,
						IssueType:    types.TypeTask,
						Priority:     2,
						ExternalRef:  strPtr(tt.localRef),
						SourceSystem: tt.identity,
					},
				},
				configs: map[string]string{"issue_prefix": "bd"},
			}
			base := newMockTracker("test")
			base.issues = []TrackerIssue{{
				ID:         "7",
				Identifier: "7",
				URL:        "https://test.test/repo-a/7",
				Title:      "Remote issue 7",
				UpdatedAt:  time.Now(),
			}}
			tr := &mockIdentityTracker{mockMatchKeyTracker: &mockMatchKeyTracker{mockTracker: base}}

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

// TestEnginePullGivesLocalIssueToOneRemoteIssue pins that a pull writes at
// most one remote issue to a local issue. The local issue's identity names
// remote issue A7 but its external_ref is remote issue B7's, as on a bead an
// older pull relinked. B7 takes it by the exact ref, and A7, in either
// processing order, gets a new local issue rather than overwriting B7's.
func TestEnginePullGivesLocalIssueToOneRemoteIssue(t *testing.T) {
	remoteA7 := TrackerIssue{ID: "A7", Identifier: "7", URL: "https://test.test/repo-a/7", Title: "Remote A7", UpdatedAt: time.Now()}
	remoteB7 := TrackerIssue{ID: "B7", Identifier: "7", URL: "https://test.test/repo-b/7", Title: "Remote B7", UpdatedAt: time.Now()}
	tests := []struct {
		name   string
		remote []TrackerIssue
	}{
		{name: "exact match first", remote: []TrackerIssue{remoteB7, remoteA7}},
		{name: "identity match first", remote: []TrackerIssue{remoteA7, remoteB7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &engineUOWState{
				issues: map[string]*types.Issue{
					"bd-x": {
						ID:           "bd-x",
						Title:        "Remote B7 (stale)",
						Status:       types.StatusOpen,
						IssueType:    types.TypeTask,
						Priority:     2,
						ExternalRef:  strPtr("https://test.test/repo-b/7"),
						SourceSystem: "id:A7",
					},
				},
				configs: map[string]string{"issue_prefix": "bd"},
			}
			base := newMockTracker("test")
			base.issues = tt.remote
			tr := &mockIdentityTracker{mockMatchKeyTracker: &mockMatchKeyTracker{mockTracker: base}}

			result, err := NewEngine(tr, NewUOWStore(&engineUOWProvider{state: state}), "test-actor").
				Sync(context.Background(), SyncOptions{Pull: true})
			if err != nil {
				t.Fatalf("Sync() error: %v", err)
			}
			if result.PullStats.Created != 1 || result.PullStats.Updated != 1 {
				t.Errorf("pull stats = {Created:%d Updated:%d}, want {Created:1 Updated:1}",
					result.PullStats.Created, result.PullStats.Updated)
			}
			if got := state.issues["bd-x"].Title; got != "Remote B7" {
				t.Errorf("bd-x title = %q, want %q", got, "Remote B7")
			}
		})
	}
}

// TestEnginePullSkipsSharedIdentityKey pins that an identity key two local
// issues hold matches neither of them, so a stale identity cannot pick one:
// both keep the match key fallback a local issue without an identity has.
func TestEnginePullSkipsSharedIdentityKey(t *testing.T) {
	tests := []struct {
		name        string
		pulledURL   string
		wantCreated int
		wantUpdated string // ID of the local issue the pull updates, "" for none
	}{
		{name: "no match key match creates", pulledURL: "https://test.test/repo-z/7", wantCreated: 1},
		{name: "match key still matches", pulledURL: "https://test.test/REPO-Y/7", wantUpdated: "bd-y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &engineUOWState{issues: map[string]*types.Issue{}, configs: map[string]string{"issue_prefix": "bd"}}
			for _, id := range []string{"bd-x", "bd-y"} {
				state.issues[id] = &types.Issue{
					ID:           id,
					Title:        "Local " + id,
					Status:       types.StatusOpen,
					IssueType:    types.TypeTask,
					Priority:     2,
					ExternalRef:  strPtr("https://test.test/repo-" + strings.TrimPrefix(id, "bd-") + "/7"),
					SourceSystem: "id:7",
				}
			}
			base := newMockTracker("test")
			base.issues = []TrackerIssue{{ID: "7", Identifier: "7", URL: tt.pulledURL, Title: "Remote issue 7", UpdatedAt: time.Now()}}
			tr := &mockIdentityTracker{mockMatchKeyTracker: &mockMatchKeyTracker{mockTracker: base}}

			result, err := NewEngine(tr, NewUOWStore(&engineUOWProvider{state: state}), "test-actor").
				Sync(context.Background(), SyncOptions{Pull: true})
			if err != nil {
				t.Fatalf("Sync() error: %v", err)
			}
			wantUpdated := 0
			if tt.wantUpdated != "" {
				wantUpdated = 1
			}
			if result.PullStats.Created != tt.wantCreated || result.PullStats.Updated != wantUpdated {
				t.Errorf("pull stats = {Created:%d Updated:%d}, want {Created:%d Updated:%d}",
					result.PullStats.Created, result.PullStats.Updated, tt.wantCreated, wantUpdated)
			}
			for _, id := range []string{"bd-x", "bd-y"} {
				want := "Local " + id
				if id == tt.wantUpdated {
					want = "Remote issue 7"
				}
				if got := state.issues[id].Title; got != want {
					t.Errorf("%s title = %q, want %q", id, got, want)
				}
			}
		})
	}
}

// TestEngineDependencyResolverUsesMatchKey pins dependency endpoints to the
// match key: with issue 7 of two repos linked locally, an endpoint naming repo
// B's issue under another spelling resolves to repo B's bead, not to the first
// bead whose ExtractIdentifier is "7", and a bare "7" names no repo's issue.
func TestEngineDependencyResolverUsesMatchKey(t *testing.T) {
	state := &engineUOWState{
		issues: map[string]*types.Issue{
			"bd-a7": {ID: "bd-a7", ExternalRef: strPtr("https://test.test/repo-a/7")},
			"bd-b7": {ID: "bd-b7", ExternalRef: strPtr("https://test.test/REPO-B/7")},
		},
		configs: map[string]string{},
	}
	engine := NewEngine(&mockMatchKeyTracker{mockTracker: newMockTracker("test")},
		NewUOWStore(&engineUOWProvider{state: state}), "test-actor")
	resolve, err := engine.dependencyIssueResolver(context.Background(), nil)
	if err != nil {
		t.Fatalf("dependencyIssueResolver: %v", err)
	}

	for endpoint, wantID := range map[string]string{
		"https://test.test/repo-b/7": "bd-b7",
		"7":                          "",
	} {
		got, err := resolve(context.Background(), endpoint, "")
		if err != nil {
			t.Fatalf("resolve(%q): %v", endpoint, err)
		}
		gotID := ""
		if got != nil {
			gotID = got.ID
		}
		if gotID != wantID {
			t.Errorf("resolve(%q) = %q, want %q", endpoint, gotID, wantID)
		}
	}
}

// TestEngineDependencyResolverUsesIdentityKey pins the resolver to the order a
// pull matches issues in: an exact ref first, then the identity key, so an
// endpoint whose issue moved resolves to the local issue holding its identity.
// An identity two local issues hold resolves to neither.
func TestEngineDependencyResolverUsesIdentityKey(t *testing.T) {
	state := &engineUOWState{
		issues: map[string]*types.Issue{
			"bd-a7": {ID: "bd-a7", ExternalRef: strPtr("https://test.test/repo-a/7"), SourceSystem: "id:A7"},
			"bd-b8": {ID: "bd-b8", ExternalRef: strPtr("https://test.test/repo-old/8"), SourceSystem: "id:B8"},
			"bd-c1": {ID: "bd-c1", ExternalRef: strPtr("https://test.test/repo-c/1"), SourceSystem: "id:C"},
			"bd-c2": {ID: "bd-c2", ExternalRef: strPtr("https://test.test/repo-c/2"), SourceSystem: "id:C"},
		},
		configs: map[string]string{},
	}
	engine := NewEngine(&mockIdentityTracker{mockMatchKeyTracker: &mockMatchKeyTracker{mockTracker: newMockTracker("test")}},
		NewUOWStore(&engineUOWProvider{state: state}), "test-actor")
	resolve, err := engine.dependencyIssueResolver(context.Background(), nil)
	if err != nil {
		t.Fatalf("dependencyIssueResolver: %v", err)
	}

	tests := []struct {
		name, endpoint, identity, wantID string
	}{
		{name: "moved issue resolves by identity", endpoint: "https://test.test/repo-new/8", identity: "id:B8", wantID: "bd-b8"},
		{name: "exact ref before another issue's identity", endpoint: "https://test.test/repo-a/7", identity: "id:B8", wantID: "bd-a7"},
		{name: "shared identity resolves to neither", endpoint: "https://test.test/repo-new/9", identity: "id:C"},
		{name: "moved issue without identity", endpoint: "https://test.test/repo-new/8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A URL endpoint nothing matches ends in the store's exact
			// lookup, whose storage.ErrNotFound means the issue was not
			// imported: no local issue and no error.
			got, err := resolve(context.Background(), tt.endpoint, tt.identity)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			gotID := ""
			if got != nil {
				gotID = got.ID
			}
			if gotID != tt.wantID {
				t.Errorf("resolve(%q, %q) = %q, want %q", tt.endpoint, tt.identity, gotID, tt.wantID)
			}
		})
	}
}

// refLookupStore is a pureTestStore whose exact external_ref lookup fails with
// err.
type refLookupStore struct {
	*pureTestStore
	err error
}

func (s *refLookupStore) GetIssueByExternalRef(context.Context, string) (*types.Issue, error) {
	return nil, s.err
}

// TestEngineCreateDependenciesReportsOnlyFailedLookups pins the dependency
// pass for an endpoint named by URL that no local issue holds, which ends in
// the store's exact lookup: storage.ErrNotFound means the pull did not import
// that issue, and the link is skipped with no warning or error count, while
// any other lookup failure is still reported and counted.
func TestEngineCreateDependenciesReportsOnlyFailedLookups(t *testing.T) {
	deps := []DependencyInfo{
		{FromExternalID: "https://test.test/MISSING-1", ToExternalID: "https://test.test/MISSING-2", Type: string(types.DepBlocks)},
	}
	for _, tt := range []struct {
		name      string
		lookupErr error
		wantErrs  int
	}{
		{name: "issue not imported", lookupErr: fmt.Errorf("%w: external_ref https://test.test/MISSING-1", storage.ErrNotFound)},
		{name: "lookup failed", lookupErr: errors.New("connection reset"), wantErrs: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &refLookupStore{pureTestStore: newPureTestStore(), err: tt.lookupErr}
			engine := NewEngine(newMockTracker("test"), store, "test-actor")
			if got := engine.createDependencies(context.Background(), deps); got != tt.wantErrs || len(engine.warnings) != tt.wantErrs {
				t.Errorf("createDependencies = %d errors with warnings %q, want %d of each", got, engine.warnings, tt.wantErrs)
			}
		})
	}
}
