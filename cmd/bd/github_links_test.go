//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// lastSyncCountingStore counts writes to github.last_sync so tests can prove
// the relationship pass leaves the engine-owned cursor alone.
type lastSyncCountingStore struct {
	storage.DoltStorage
	mu     sync.Mutex
	writes int
}

func (s *lastSyncCountingStore) SetLocalMetadata(ctx context.Context, key, value string) error {
	if key == "github.last_sync" {
		s.mu.Lock()
		s.writes++
		s.mu.Unlock()
	}
	return s.DoltStorage.SetLocalMetadata(ctx, key, value)
}

func (s *lastSyncCountingStore) lastSyncWrites() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// fakeGitHubLinkServer answers the issue, sub-issue and blocked_by endpoints
// for owner/repo and records relationship POSTs as "<from>/<kind>/<to>".
type fakeGitHubLinkServer struct {
	mu    sync.Mutex
	posts []string
}

func (f *fakeGitHubLinkServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(segments) < 5 || segments[0] != "repos" || segments[3] != "issues" {
			// Repository-wide listings (pull) see no remote issues.
			_ = json.NewEncoder(w).Encode([]github.Issue{})
			return
		}
		number, err := strconv.Atoi(segments[4])
		if err != nil {
			_ = json.NewEncoder(w).Encode([]github.Issue{})
			return
		}
		rest := strings.Join(segments[5:], "/")
		switch {
		case rest == "":
			_ = json.NewEncoder(w).Encode(github.Issue{
				ID:      number + 1000,
				Number:  number,
				Title:   fmt.Sprintf("issue %d", number),
				State:   "open",
				HTMLURL: fmt.Sprintf("https://github.com/owner/repo/issues/%d", number),
			})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]github.Issue{})
		case r.Method == http.MethodPost && (rest == "sub_issues" || rest == "dependencies/blocked_by"):
			var body map[string]int
			_ = json.NewDecoder(r.Body).Decode(&body)
			to := body["sub_issue_id"] + body["issue_id"] - 1000
			f.mu.Lock()
			f.posts = append(f.posts, fmt.Sprintf("%d/%s/%d", number, rest, to))
			f.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.Issue{ID: to + 1000, Number: to})
		default:
			_, _ = io.Copy(io.Discard, r.Body)
			_ = json.NewEncoder(w).Encode(github.Issue{ID: number + 1000, Number: number})
		}
	}
}

func (f *fakeGitHubLinkServer) takePosts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	posts := f.posts
	f.posts = nil
	return posts
}

// setupGitHubLinkSync seeds a store with a task (not epic) parent, its child
// and a blocker, all linked to GitHub, then points the tracker at a fake
// server.
func setupGitHubLinkSync(t *testing.T) (*lastSyncCountingStore, *fakeGitHubLinkServer, []string) {
	t.Helper()
	saveAndRestoreGlobals(t)

	testDBPath := filepath.Join(t.TempDir(), ".beads", "dolt")
	counting := &lastSyncCountingStore{DoltStorage: newTestStore(t, testDBPath)}
	store = counting
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	fake := &fakeGitHubLinkServer{}
	server := httptest.NewServer(fake.handler(t))
	t.Cleanup(server.Close)
	t.Setenv("GITHUB_TOKEN", "token")
	t.Setenv("GITHUB_OWNER", "owner")
	t.Setenv("GITHUB_REPO", "repo")
	t.Setenv("GITHUB_API_URL", server.URL)

	ctx := context.Background()
	mk := func(title, ref string) *types.Issue {
		issue := &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, ExternalRef: &ref}
		if err := counting.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return issue
	}
	parent := mk("task parent", "github:1")
	child := mk("child", "github:2")
	blocker := mk("blocker", "github:3")
	for _, dep := range []*types.Dependency{
		{IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild},
		{IssueID: child.ID, DependsOnID: blocker.ID, Type: types.DepBlocks},
	} {
		if err := counting.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("add dependency: %v", err)
		}
	}
	return counting, fake, []string{parent.ID, child.ID, blocker.ID}
}

func assertGitHubLinkPosts(t *testing.T, got []string) {
	t.Helper()
	want := map[string]bool{
		"1/sub_issues/2":              true, // task parent, not an epic
		"2/dependencies/blocked_by/3": true,
	}
	if len(got) != len(want) {
		t.Fatalf("relationship POSTs = %v, want %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("unexpected relationship POST %q (all: %v)", p, got)
		}
	}
}

// TestGitHubPushRelationshipPassLeavesLastSync pins finding 2 of the #5971
// review: only the engine writes github.last_sync, once per Sync. The
// relationship pass that runs after it must not move the conflict cursor.
func TestGitHubPushRelationshipPassLeavesLastSync(t *testing.T) {
	counting, fake, ids := setupGitHubLinkSync(t)

	oldCtx := githubPushCmd.Context()
	githubPushCmd.SetContext(context.Background())
	t.Cleanup(func() { githubPushCmd.SetContext(oldCtx) })
	if err := runGitHubPush(githubPushCmd, ids); err != nil {
		t.Fatalf("runGitHubPush: %v", err)
	}
	assertGitHubLinkPosts(t, fake.takePosts())
	if got := counting.lastSyncWrites(); got > 1 {
		t.Fatalf("github.last_sync written %d times by bd github push; only the engine may write it", got)
	}
}

func TestGitHubSyncRelationshipPassLeavesLastSync(t *testing.T) {
	counting, fake, _ := setupGitHubLinkSync(t)

	githubSyncPushOnly = true
	t.Cleanup(func() { githubSyncPushOnly = false })
	if err := runGitHubSync(githubSyncCmd, nil); err != nil {
		t.Fatalf("runGitHubSync: %v", err)
	}
	assertGitHubLinkPosts(t, fake.takePosts())
	if got := counting.lastSyncWrites(); got > 1 {
		t.Fatalf("github.last_sync written %d times by bd github sync; only the engine may write it", got)
	}
}

func assertGitHubLinkDryRun(t *testing.T, fake *fakeGitHubLinkServer, out string) {
	t.Helper()
	if posts := fake.takePosts(); len(posts) != 0 {
		t.Fatalf("dry-run made relationship POSTs: %v", posts)
	}
	if strings.Contains(out, "Synced") {
		t.Fatalf("dry-run output claims links were synced:\n%s", out)
	}
	if !strings.Contains(out, "Would sync 2 relationship links") {
		t.Fatalf("dry-run output missing relationship plan count:\n%s", out)
	}
}

func TestGitHubPushDryRunMakesNoRelationshipPosts(t *testing.T) {
	_, fake, ids := setupGitHubLinkSync(t)

	oldCtx := githubPushCmd.Context()
	githubPushCmd.SetContext(context.Background())
	t.Cleanup(func() { githubPushCmd.SetContext(oldCtx) })
	if err := githubPushCmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = githubPushCmd.Flags().Set("dry-run", "false") })

	out := captureStdout(t, func() error { return runGitHubPush(githubPushCmd, ids) })
	assertGitHubLinkDryRun(t, fake, out)
}

func TestGitHubSyncDryRunMakesNoRelationshipPosts(t *testing.T) {
	_, fake, _ := setupGitHubLinkSync(t)

	githubSyncPushOnly, githubSyncDryRun = true, true
	t.Cleanup(func() { githubSyncPushOnly, githubSyncDryRun = false, false })

	out := captureStdout(t, func() error { return runGitHubSync(githubSyncCmd, nil) })
	assertGitHubLinkDryRun(t, fake, out)
}
