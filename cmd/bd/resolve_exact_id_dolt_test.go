//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestResolveAndGetFromStoreFindsWispOnlyID pins that an id stored only in
// the wisps table is found by the direct read, through GetIssue's own
// fallback from issues to wisps.
func TestResolveAndGetFromStoreFindsWispOnlyID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))

	wisp := &types.Issue{Title: "Wisp only", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := s.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("create wisp: %v", err)
	}
	var inWisps, inIssues int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", wisp.ID).Scan(&inWisps); err != nil {
		t.Fatalf("count wisps row: %v", err)
	}
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", wisp.ID).Scan(&inIssues); err != nil {
		t.Fatalf("count issues row: %v", err)
	}
	if inWisps != 1 || inIssues != 0 {
		t.Fatalf("test setup: %s has %d wisps rows and %d issues rows, want 1 and 0", wisp.ID, inWisps, inIssues)
	}

	result, err := resolveAndGetFromStore(ctx, s, wisp.ID, false)
	if err != nil {
		t.Fatalf("resolveAndGetFromStore(%s): %v", wisp.ID, err)
	}
	if result.ResolvedID != wisp.ID || result.Issue.ID != wisp.ID || result.Issue.Title != "Wisp only" || !result.Issue.Ephemeral {
		t.Fatalf("result = %s %+v, want the wisp %s", result.ResolvedID, result.Issue, wisp.ID)
	}
}

// TestResolveAndGetFromStoreOverlappingIDReturnsDurableBody pins the body for
// an id present in both issues and wisps. The resolver's merged search prefers
// the wisp row but hands back only its id, and the GetIssue that follows
// reads the durable row first, so the command has always received the durable
// body. The direct read is that same GetIssue and returns the same body.
func TestResolveAndGetFromStoreOverlappingIDReturnsDurableBody(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))

	const id = "test-ovl1"
	if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: "Durable copy", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "test"); err != nil {
		t.Fatalf("create durable row: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx, "INSERT INTO wisps (id, title) VALUES (?, ?)", id, "Wisp copy"); err != nil {
		t.Fatalf("insert overlapping wisps row: %v", err)
	}

	result, err := resolveAndGetFromStore(ctx, s, id, false)
	if err != nil {
		t.Fatalf("resolveAndGetFromStore(%s): %v", id, err)
	}
	if result.ResolvedID != id || result.Issue.Title != "Durable copy" {
		t.Fatalf("result = %s %q, want %s with the durable body", result.ResolvedID, result.Issue.Title, id)
	}
}

// renameAfterReadStore stands in for a concurrent writer: right after the
// first GetIssue of each issue it renames that issue, as another bd process
// could between two reads of one command. It also counts the reads id
// resolution makes, so a test can see which ones a command still sends.
type renameAfterReadStore struct {
	storage.DoltStorage

	t        *testing.T
	renamed  map[string]bool
	gets     map[string]int
	searches int
}

func (s *renameAfterReadStore) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	issue, err := s.DoltStorage.GetIssue(ctx, id)
	s.gets[id]++
	if err == nil && !s.renamed[issue.ID] {
		s.renamed[issue.ID] = true
		if uerr := s.DoltStorage.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": issue.Title + " renamed"}, "other-writer"); uerr != nil {
			s.t.Fatalf("concurrent rename of %s: %v", issue.ID, uerr)
		}
	}
	return issue, err
}

func (s *renameAfterReadStore) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.searches++
	return s.DoltStorage.SearchIssues(ctx, query, filter)
}

// useStoreForDepCommand points the dep commands' globals at s and restores them
// when the test ends.
func useStoreForDepCommand(t *testing.T, s storage.DoltStorage, testDB string) {
	t.Helper()
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	savedRootCtx, savedJSONOutput, savedActor := rootCtx, jsonOutput, actor
	t.Cleanup(func() {
		rootCtx, jsonOutput, actor = savedRootCtx, savedJSONOutput, savedActor
		readonlyMode = false
	})
	store = s
	dbPath = testDB
	rootCtx = context.Background()
	jsonOutput = false
	readonlyMode = false
	actor = "test-actor"

	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

func createTitledIssue(t *testing.T, s storage.DoltStorage, title string) string {
	t.Helper()
	issue := &types.Issue{Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := s.CreateIssue(context.Background(), issue, "test"); err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	return issue.ID
}

// TestDepAddTitlesComeFromResolution pins where dep add's confirmation line
// gets its titles: from the issues the resolution read, with no further read.
// The reuse spans transactions, so the line shows the titles as of the
// resolution: a rename another writer commits after that read is not shown.
// Each issue is read once (one GetIssue, no resolver search).
func TestDepAddTitlesComeFromResolution(t *testing.T) {
	testDB := filepath.Join(t.TempDir(), ".beads", "beads.db")
	s := newTestStore(t, testDB)
	from := createTitledIssue(t, s, "Dependent issue")
	to := createTitledIssue(t, s, "Prerequisite issue")

	wrapped := &renameAfterReadStore{DoltStorage: s, t: t, renamed: map[string]bool{}, gets: map[string]int{}}
	useStoreForDepCommand(t, wrapped, testDB)

	out := captureStdout(t, func() error { return depAddCmd.RunE(depAddCmd, []string{from, to}) })
	want := from + " (Dependent issue) depends on " + to + " (Prerequisite issue)"
	if !strings.Contains(out, want) {
		t.Fatalf("dep add printed %q, want it to contain %q", out, want)
	}
	if wrapped.searches != 0 || wrapped.gets[from] != 1 || wrapped.gets[to] != 1 {
		t.Fatalf("reads: %d resolver searches, GetIssue %v; want no search and one GetIssue per issue", wrapped.searches, wrapped.gets)
	}
}

// TestDepBlocksTitlesComeFromResolution is the same pin for `bd dep <a>
// --blocks <b>`, whose confirmation line used to read both titles again after
// the write.
func TestDepBlocksTitlesComeFromResolution(t *testing.T) {
	testDB := filepath.Join(t.TempDir(), ".beads", "beads.db")
	s := newTestStore(t, testDB)
	blocker := createTitledIssue(t, s, "Blocking issue")
	blocked := createTitledIssue(t, s, "Blocked issue")

	wrapped := &renameAfterReadStore{DoltStorage: s, t: t, renamed: map[string]bool{}, gets: map[string]int{}}
	useStoreForDepCommand(t, wrapped, testDB)

	blocksFlag := depCmd.Flags().Lookup("blocks")
	t.Cleanup(func() {
		_ = blocksFlag.Value.Set("")
		blocksFlag.Changed = false
	})
	if err := depCmd.Flags().Set("blocks", blocked); err != nil {
		t.Fatalf("set --blocks: %v", err)
	}

	out := captureStdout(t, func() error { return depCmd.RunE(depCmd, []string{blocker}) })
	want := blocker + " (Blocking issue) blocks " + blocked + " (Blocked issue)"
	if !strings.Contains(out, want) {
		t.Fatalf("dep --blocks printed %q, want it to contain %q", out, want)
	}
	if wrapped.searches != 0 || wrapped.gets[blocker] != 1 || wrapped.gets[blocked] != 1 {
		t.Fatalf("reads: %d resolver searches, GetIssue %v; want no search and one GetIssue per issue", wrapped.searches, wrapped.gets)
	}
}

// TestDepAddShowsRoutedTargetTitle pins a visible change: a target found
// through routes.jsonl in another rig is now named with its title. The title
// used to be read from the local store, where the routed issue does not
// exist, so the line showed the bare id.
//
// NOTE: This test uses os.Chdir and cannot run in parallel with other tests.
func TestDepAddShowsRoutedTargetTitle(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")
	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "gt")
	if err := rigStore.CreateIssue(ctx, &types.Issue{
		ID: "gt-target1", Title: "Routed dep target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create rig issue: %v", err)
	}
	// Release the rig store before routing reopens it.
	rigStore.Close()
	if err := townStore.CreateIssue(ctx, &types.Issue{
		ID: "hq-source1", Title: "Local source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create town issue: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rig"}`), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	useStoreForDepCommand(t, townStore, townDBPath)
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	out := captureStdout(t, func() error { return depAddCmd.RunE(depAddCmd, []string{"hq-source1", "gt-target1"}) })
	want := "hq-source1 (Local source) depends on gt-target1 (Routed dep target)"
	if !strings.Contains(out, want) {
		t.Fatalf("dep add printed %q, want it to contain %q", out, want)
	}
}
