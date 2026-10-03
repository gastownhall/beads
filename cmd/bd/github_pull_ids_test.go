//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// depAddingGitHubTracker is the real GitHub tracker with a field mapper that
// also reports "#2 blocks on #1", so the pull has a dependency between two
// issues it creates in the same run. The GitHub mapper itself emits none.
type depAddingGitHubTracker struct {
	*github.Tracker
}

func (t depAddingGitHubTracker) FieldMapper() tracker.FieldMapper {
	return depAddingFieldMapper{t.Tracker.FieldMapper()}
}

type depAddingFieldMapper struct {
	tracker.FieldMapper
}

func (m depAddingFieldMapper) IssueToBeads(ti *tracker.TrackerIssue) *tracker.IssueConversion {
	conv := m.FieldMapper.IssueToBeads(ti)
	if conv != nil && ti.Identifier == "2" {
		conv.Dependencies = append(conv.Dependencies, tracker.DependencyInfo{
			FromExternalID: "2",
			ToExternalID:   "1",
			Type:           string(types.DepBlocks),
		})
	}
	return conv
}

func newGitHubIssuesServer(t *testing.T) *httptest.Server {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	issues := []github.Issue{
		{ID: 1001, Number: 1, Title: "Remote parent", Body: "created on GitHub", State: "open",
			CreatedAt: &now, UpdatedAt: &now, HTMLURL: "https://github.com/acme/widgets/issues/1"},
		{ID: 1002, Number: 2, Title: "Remote child", Body: "also created on GitHub", State: "open",
			CreatedAt: &now, UpdatedAt: &now, HTMLURL: "https://github.com/acme/widgets/issues/2"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/widgets/issues" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	t.Cleanup(server.Close)
	return server
}

// runGitHubPullForIDTest pulls the two mock issues into a fresh store through
// the production GitHub pull hooks and returns the store and pulled beads.
func runGitHubPullForIDTest(t *testing.T) (context.Context, *types.Issue, *types.Issue) {
	t.Helper()
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
	store = testStore

	server := newGitHubIssuesServer(t)
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_OWNER", "acme")
	t.Setenv("GITHUB_REPO", "widgets")
	t.Setenv("GITHUB_API_URL", server.URL)

	gt := &github.Tracker{}
	if err := gt.Init(ctx, testStore); err != nil {
		t.Fatalf("init GitHub tracker: %v", err)
	}
	engine := tracker.NewEngine(depAddingGitHubTracker{gt}, testStore, "test-actor")
	engine.PullHooks = buildGitHubPullHooks(ctx)

	result, err := engine.Sync(ctx, tracker.SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.Stats.Created != 2 {
		t.Fatalf("created = %d, want 2 (stats %+v)", result.Stats.Created, result.Stats)
	}

	pulled := make([]*types.Issue, 2)
	for i := range pulled {
		ref := fmt.Sprintf("https://github.com/acme/widgets/issues/%d", i+1)
		issue, err := testStore.GetIssueByExternalRef(ctx, ref)
		if err != nil || issue == nil {
			t.Fatalf("GetIssueByExternalRef(%s) = %v, %v", ref, issue, err)
		}
		pulled[i] = issue
	}

	deps, err := testStore.GetDependencies(ctx, pulled[1].ID)
	if err != nil {
		t.Fatalf("GetDependencies(%s): %v", pulled[1].ID, err)
	}
	if len(deps) != 1 || deps[0].ID != pulled[0].ID {
		t.Fatalf("dependencies of %s = %v, want [%s]", pulled[1].ID, issueIDs(deps), pulled[0].ID)
	}
	return ctx, pulled[0], pulled[1]
}

func TestGitHubPullAssignsRegularBeadIDs(t *testing.T) {
	saveAndRestoreGlobals(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	_ = config.Initialize()
	config.Set("issue-prefix", "")

	ctx, parent, child := runGitHubPullForIDTest(t)

	// A bead created the way `bd create` does it (no ID, store mints one)
	// defines the regular format the pulled beads must share.
	local := &types.Issue{Title: "Local", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, local, "test-actor"); err != nil {
		t.Fatalf("create local issue: %v", err)
	}
	regularID := regexp.MustCompile(`^test-[0-9a-z]{3,8}$`)
	for _, id := range []string{local.ID, parent.ID, child.ID} {
		if !regularID.MatchString(id) {
			t.Errorf("ID %q does not match regular bead ID format %s", id, regularID)
		}
	}
}

func TestGitHubPullHonorsYAMLIssuePrefix(t *testing.T) {
	saveAndRestoreGlobals(t)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	_ = config.Initialize()
	// config.yaml wins over the database's issue_prefix (GH#2469).
	config.Set("issue-prefix", "yml")

	_, parent, child := runGitHubPullForIDTest(t)

	regularID := regexp.MustCompile(`^yml-[0-9a-z]{3,8}$`)
	for _, id := range []string{parent.ID, child.ID} {
		if !regularID.MatchString(id) {
			t.Errorf("ID %q does not match %s", id, regularID)
		}
	}
}

func TestPullIssueIDHookLeavesIDForStore(t *testing.T) {
	saveAndRestoreGlobals(t)
	store = nil
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	_ = config.Initialize()

	config.Set("issue-prefix", "")
	issue := &types.Issue{Title: "no yaml prefix"}
	if err := pullIssueIDHook(context.Background())(context.Background(), issue); err != nil {
		t.Fatal(err)
	}
	if issue.ID != "" || issue.PrefixOverride != "" {
		t.Fatalf("ID = %q, PrefixOverride = %q; want both empty", issue.ID, issue.PrefixOverride)
	}

	config.Set("issue-prefix", "yml-")
	issue = &types.Issue{Title: "yaml prefix"}
	if err := pullIssueIDHook(context.Background())(context.Background(), issue); err != nil {
		t.Fatal(err)
	}
	if issue.ID != "" || issue.PrefixOverride != "yml" {
		t.Fatalf("ID = %q, PrefixOverride = %q; want empty ID and override %q", issue.ID, issue.PrefixOverride, "yml")
	}

	existing := &types.Issue{ID: "keep-me", Title: "already linked"}
	if err := pullIssueIDHook(context.Background())(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	if existing.ID != "keep-me" || existing.PrefixOverride != "" {
		t.Fatalf("existing issue changed: ID = %q, PrefixOverride = %q", existing.ID, existing.PrefixOverride)
	}
}
