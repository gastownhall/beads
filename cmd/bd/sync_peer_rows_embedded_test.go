//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// syncPeerRowsComments returns the issue's comment texts as `bd comments
// --json` lists them, sorted.
func syncPeerRowsComments(t *testing.T, bd, dir, issueID string) []string {
	t.Helper()
	cmd := exec.Command(bd, "comments", issueID, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd comments %s --json in %s: %v\nstdout:\n%s\nstderr:\n%s", issueID, dir, err, stdout.String(), stderr.String())
	}
	var comments []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &comments); err != nil {
		t.Fatalf("parse bd comments --json: %v\n%s", err, stdout.String())
	}
	texts := make([]string, 0, len(comments))
	for _, c := range comments {
		texts = append(texts, c.Text)
	}
	sort.Strings(texts)
	return texts
}

// TestEmbeddedSyncMergeCommitKeepsPeerRows (GH#7757, AC-6) is the reporter's
// two-clone scenario through the bd binary: clone A comments on an issue,
// changes its status and sets a user kv key; clone B comments on the same
// issue and renames it; then `bd sync` runs in A, B, A. Both clones and a
// fresh clone of the remote must end with both comments and the kv value, and
// the merge commit B's sync wrote (the commit its push published) must itself
// hold both comments, the kv row and the field-merged issue, not just B's
// working set.
func TestEmbeddedSyncMergeCommitKeepsPeerRows(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)

	bareDir := filepath.Join(t.TempDir(), "origin.git")
	runGitForBootstrapTest(t, "", "init", "--bare", "--initial-branch=main", bareDir)
	remoteURL := "file://" + bareDir

	dirA := t.TempDir()
	initGitRepoAt(t, dirA)
	runGitForBootstrapTest(t, dirA, "branch", "-M", "main")
	runGitForBootstrapTest(t, dirA, "remote", "add", "origin", remoteURL)
	runGitForBootstrapTest(t, dirA, "commit", "--allow-empty", "-m", "init")
	runGitForBootstrapTest(t, dirA, "push", "-u", "origin", "main")
	runBDInit(t, bd, dirA, "--prefix", "sp", "--skip-hooks", "--skip-agents")
	issueID := bdCreateSilent(t, bd, dirA, "shared issue", "--type", "task")
	bdDolt(t, bd, dirA, "push")

	cloneOfRemote := func() string {
		t.Helper()
		dir := t.TempDir()
		runGitForBootstrapTest(t, dir, "init", "-b", "main")
		runGitForBootstrapTest(t, dir, "remote", "add", "origin", remoteURL)
		bdBootstrap(t, bd, dir, "--yes")
		return dir
	}
	dirB := cloneOfRemote()

	bdComment(t, bd, dirA, issueID, "comment from A")
	bdUpdate(t, bd, dirA, issueID, "--status", "in_progress")
	bdKV(t, bd, dirA, "set", "peerkey", "fromA")

	bdComment(t, bd, dirB, issueID, "comment from B")
	bdUpdate(t, bd, dirB, issueID, "--title", "renamed on B")

	for _, step := range []struct{ name, dir string }{{"A", dirA}, {"B", dirB}, {"A", dirA}} {
		bdCommand(t, bd, step.dir, "sync")
	}

	want := []string{"comment from A", "comment from B"}
	for _, c := range []struct{ name, dir string }{{"A", dirA}, {"B", dirB}, {"fresh clone", cloneOfRemote()}} {
		if got := syncPeerRowsComments(t, bd, c.dir, issueID); !reflect.DeepEqual(got, want) {
			t.Errorf("clone %s comments = %v, want %v", c.name, got, want)
		}
		if got := strings.TrimSpace(bdKV(t, bd, c.dir, "get", "peerkey")); got != "fromA" {
			t.Errorf("clone %s kv peerkey = %q, want %q", c.name, got, "fromA")
		}
	}

	withEmbeddedConn(t, filepath.Join(dirB, ".beads"), func(conn *sql.Conn) {
		ctx := t.Context()
		rows, err := conn.QueryContext(ctx,
			"SELECT commit_hash FROM dolt_commit_ancestors GROUP BY commit_hash HAVING COUNT(*) = 2")
		if err != nil {
			t.Fatalf("find merge commits: %v", err)
		}
		var merges []string
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				t.Fatalf("scan merge commit: %v", err)
			}
			merges = append(merges, h)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate merge commits: %v", err)
		}
		_ = rows.Close()
		if len(merges) != 1 {
			t.Fatalf("clone B holds %d merge commit(s) %v, want the one its sync wrote", len(merges), merges)
		}
		merge := merges[0]

		// AS OF takes no bind parameter; the hash comes from dolt itself.
		asOf := " AS OF '" + merge + "'"
		crows, err := conn.QueryContext(ctx, "SELECT text FROM comments"+asOf+" WHERE issue_id = ? ORDER BY text", issueID)
		if err != nil {
			t.Fatalf("read comments in the merge commit: %v", err)
		}
		var inMerge []string
		for crows.Next() {
			var s string
			if err := crows.Scan(&s); err != nil {
				t.Fatalf("scan merge-commit comment: %v", err)
			}
			inMerge = append(inMerge, s)
		}
		if err := crows.Err(); err != nil {
			t.Fatalf("iterate merge-commit comments: %v", err)
		}
		_ = crows.Close()
		if !reflect.DeepEqual(inMerge, want) {
			t.Errorf("B's merge commit comments = %v, want %v: the merge commit dropped a clone's comment", inMerge, want)
		}

		var kv string
		if err := conn.QueryRowContext(ctx, "SELECT value FROM config"+asOf+" WHERE `key` = 'kv.peerkey'").Scan(&kv); err != nil {
			t.Errorf("read kv.peerkey in B's merge commit (the merge commit does not carry it): %v", err)
		} else if kv != "fromA" {
			t.Errorf("B's merge commit kv.peerkey = %q, want %q", kv, "fromA")
		}
		var title, status string
		if err := conn.QueryRowContext(ctx, "SELECT title, status FROM issues"+asOf+" WHERE id = ?", issueID).Scan(&title, &status); err != nil {
			t.Fatalf("read the issue in B's merge commit: %v", err)
		}
		if title != "renamed on B" || status != "in_progress" {
			t.Errorf("B's merge commit issue = (%q, %q), want (%q, %q)", title, status, "renamed on B", "in_progress")
		}
	})
}
