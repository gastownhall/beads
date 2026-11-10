//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedSyncMergeCommitKeepsPeerRows is the GH#7757 end-to-end
// regression: two isolated clones of one bare git remote diverge — clone A
// comments, changes the status, and sets a user-kv key on an issue while
// clone B comments on and renames the same issue — and bd sync runs A/B/A.
// Every settlement route must conclude with the full merge result, so both
// clones, a fresh reader that bootstraps from the remote, and B's merge
// commit itself all carry the peer rows: both comments, the kv value, A's
// status, and B's title. The failure this pins is a conclusion that commits
// HEAD's copy of cleanly merged tables left unstaged by a conflicted merge —
// the push then reverts the peer's rows on the remote (the GH#7811 class),
// and the first-syncing clone loses its own comment.
func TestEmbeddedSyncMergeCommitKeepsPeerRows(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	bareDir := filepath.Join(t.TempDir(), "origin.git")
	runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	remoteURL := "file://" + bareDir

	// Clone A creates the issue and publishes the baseline the other clones
	// share, exactly as the git-origin bootstrap fixture does.
	cloneA := t.TempDir()
	initGitRepoAt(t, cloneA)
	runGitForBootstrapTest(t, cloneA, "branch", "-M", "main")
	runGitForBootstrapTest(t, cloneA, "remote", "add", "origin", remoteURL)
	runGitForBootstrapTest(t, cloneA, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, cloneA, "push", "-u", "origin", "main")
	runBDInit(t, bd, cloneA, "--prefix", "smc", "--skip-hooks", "--skip-agents")
	// The Dolt remote is the git origin itself (the dedicated-data-repo
	// layout the refusal guards against is not this test's), so consent to
	// the exact-URL match and get sync.remote persisted for bd sync.
	bdDolt(t, bd, cloneA, "remote", "add", "origin", remoteURL, "--allow-git-origin")
	issue := bdCreate(t, bd, cloneA, "Shared sync issue", "--type", "task")
	bdDolt(t, bd, cloneA, "push")

	// Clone B bootstraps from the remote, so its history shares A's and the
	// later pull settles as a two-parent merge rather than failing to find
	// a common ancestor.
	cloneB := t.TempDir()
	runGitForBootstrapTest(t, cloneB, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneB, "remote", "add", "origin", remoteURL)
	if out := bdBootstrap(t, bd, cloneB, "--yes"); !strings.Contains(out, "clone from remote") {
		t.Fatalf("expected bootstrap to clone B from the remote, got:\n%s", out)
	}

	// A comments, changes status, and sets a user-kv key; B comments on and
	// renames the same issue. The issue row diverges on disjoint cells
	// (status on A, title on B), the comment rows are additive, and the kv
	// row is A's alone — the exact shape whose cleanly merged tables a bad
	// conclusion leaves unstaged.
	bdComment(t, bd, cloneA, issue.ID, "comment from A")
	bdUpdate(t, bd, cloneA, issue.ID, "--status", "in_progress")
	bdKvSet(t, bd, cloneA, "kvtest", "hello")
	bdComment(t, bd, cloneB, issue.ID, "comment from B")
	bdUpdate(t, bd, cloneB, issue.ID, "--title", "renamed on B")

	// Sync A/B/A: A publishes its divergence, B's pull settles it as a merge
	// commit and pushes, A pulls the settled result.
	bdSync(t, bd, cloneA)
	bdSync(t, bd, cloneB)
	bdSync(t, bd, cloneA)

	// Both clones keep every peer row.
	for name, clone := range map[string]string{"A": cloneA, "B": cloneB} {
		if !hasPeerRows(t, bd, clone, issue.ID) {
			t.Errorf("clone %s lost peer rows across the A/B/A sync", name)
		}
	}

	// A fresh reader of the remote keeps them too: the pushed merge result,
	// not a revert of the first-syncing clone's rows.
	cloneC := t.TempDir()
	runGitForBootstrapTest(t, cloneC, "init", "-b", "main")
	runGitForBootstrapTest(t, cloneC, "remote", "add", "origin", remoteURL)
	if out := bdBootstrap(t, bd, cloneC, "--yes"); !strings.Contains(out, "clone from remote") {
		t.Fatalf("expected bootstrap to clone the fresh reader from the remote, got:\n%s", out)
	}
	if !hasPeerRows(t, bd, cloneC, issue.ID) {
		t.Error("a fresh reader of the remote lost peer rows the sync published")
	}

	// B's merge commit itself already contains the peer rows, not a
	// working set that later commands repaired.
	assertMergeCommitKeepsPeerRows(t, cloneB, issue.ID)
}

// bdKvSet runs "bd kv set <key> <value>" in dir, retrying on flock
// contention, and fatals on failure.
func bdKvSet(t *testing.T, bd, dir, key, value string) {
	t.Helper()
	out, err := bdRunWithFlockRetry(t, bd, dir, "kv", "set", key, value)
	if err != nil {
		t.Fatalf("bd kv set %s %s failed: %v\n%s", key, value, err, out)
	}
}

// bdSync runs "bd sync" in dir, retrying on flock contention, and fatals on
// failure — a halted merge or an exhausted push race is a test failure, not
// a state this regression tolerates.
func bdSync(t *testing.T, bd, dir string) {
	t.Helper()
	out, err := bdRunWithFlockRetry(t, bd, dir, "sync")
	if err != nil {
		t.Fatalf("bd sync in %s failed: %v\n%s", dir, err, out)
	}
}

// hasPeerRows reports whether the workspace in dir sees both comments, the
// kv value, A's status, and B's title on the issue, printing each miss.
func hasPeerRows(t *testing.T, bd, dir, issueID string) bool {
	t.Helper()
	ok := true

	out := bdComments(t, bd, dir, issueID, "--json")
	for _, want := range []string{"comment from A", "comment from B"} {
		if !strings.Contains(out, want) {
			t.Errorf("bd comments in %s is missing %q:\n%s", dir, want, out)
			ok = false
		}
	}

	kvOut, err := bdRunWithFlockRetry(t, bd, dir, "kv", "get", "kvtest")
	if err != nil || strings.TrimSpace(string(kvOut)) != "hello" {
		t.Errorf("bd kv get kvtest in %s = %q (err %v), want %q", dir, strings.TrimSpace(string(kvOut)), err, "hello")
		ok = false
	}

	showOut, err := bdRunWithFlockRetry(t, bd, dir, "show", issueID, "--json")
	if err != nil {
		t.Errorf("bd show %s in %s failed: %v\n%s", issueID, dir, err, showOut)
		return false
	}
	shown := parseIssueJSON(t, showOut)
	if shown.Title != "renamed on B" {
		t.Errorf("issue title in %s = %q, want %q (B's rename)", dir, shown.Title, "renamed on B")
		ok = false
	}
	if shown.Status != "in_progress" {
		t.Errorf("issue status in %s = %q, want %q (A's status change)", dir, shown.Status, "in_progress")
		ok = false
	}
	return ok
}

// assertMergeCommitKeepsPeerRows reads the newest two-parent commit of the
// cloned database in dir — the merge B's sync settled — from the embedded
// engine and asserts the peer comment and kv rows are already part of it.
func assertMergeCommitKeepsPeerRows(t *testing.T, dir, issueID string) {
	t.Helper()
	beadsDir := filepath.Join(dir, ".beads")
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatalf("read %s metadata.json: %v", dir, err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse %s metadata.json: %v", dir, err)
	}
	database, _ := meta["dolt_database"].(string)
	if database == "" {
		t.Fatalf("no dolt_database in %s metadata.json:\n%s", dir, data)
	}

	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL on %s: %v", dir, err)
	}
	defer func() { _ = cleanup() }()

	// The newest two-parent commit is B's settled merge.
	var mergeHash string
	if err := db.QueryRowContext(t.Context(),
		"SELECT a.commit_hash FROM dolt_commit_ancestors a JOIN dolt_log l ON a.commit_hash = l.commit_hash "+
			"WHERE a.parent_index = 1 ORDER BY l.commit_order DESC LIMIT 1").Scan(&mergeHash); err != nil {
		t.Fatalf("find the merge commit in %s: %v", dir, err)
	}
	if mergeHash == "" {
		t.Fatalf("no two-parent merge commit in %s: the sync never merged", dir)
	}

	//nolint:gosec // mergeHash is a commit hash this database produced
	var peerComment int
	if err := db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM comments AS OF '"+mergeHash+"' WHERE issue_id = ? AND text = 'comment from A'",
		issueID).Scan(&peerComment); err != nil {
		t.Fatalf("read comments AS OF the merge commit: %v", err)
	}
	if peerComment != 1 {
		t.Errorf("merge commit %s holds %d copies of A's comment, want 1: the peer row is missing from the merge result itself", mergeHash, peerComment)
	}

	//nolint:gosec // mergeHash is a commit hash this database produced
	var kvValue string
	if err := db.QueryRowContext(t.Context(),
		"SELECT value FROM config AS OF '"+mergeHash+"' WHERE `key` = 'kv.kvtest'").Scan(&kvValue); err != nil {
		t.Fatalf("read config AS OF the merge commit: %v", err)
	}
	if kvValue != "hello" {
		t.Errorf("merge commit %s holds kv.kvtest = %q, want %q: A's kv row is missing from the merge result itself", mergeHash, kvValue, "hello")
	}
}
