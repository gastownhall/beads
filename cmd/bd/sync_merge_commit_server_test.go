//go:build cgo && integration

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestServerSyncMergeCommitKeepsPeerRows is the server-mode twin of
// TestEmbeddedSyncMergeCommitKeepsPeerRows (GH#7757): two isolated
// server-mode workspaces, each served by bd's own auto-started dolt
// sql-server, diverge on the same issue — A comments, changes the status,
// and sets a user-kv key while B comments and renames — and bd sync runs
// A/B/A. The server's merge settlement concludes inside one transaction
// (settleMergeInTx), so a conclusion that records HEAD's copy of cleanly
// merged tables left unstaged by the conflicted merge publishes a revert of
// the peer's rows on push. Both workspaces, a fresh dolt-CLI reader of the
// remote, and B's merge commit itself must instead carry both comments, the
// kv value, A's status, and B's title.
func TestServerSyncMergeCommitKeepsPeerRows(t *testing.T) {
	testutil.RequireDoltBinary(t)
	if runtime.GOOS == windowsOS {
		t.Skip("repo-local dolt server integration test not supported on windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		t.Skip("dolt not on PATH")
	}

	bdBinary := buildLifecycleTestBinary(t)
	const database = "ssm"
	env := append(os.Environ(),
		"BEADS_TEST_MODE=",
		"GT_ROOT=",
		"BEADS_DOLT_AUTO_START=",
		"BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_PORT=",
		"BEADS_DOLT_SHARED_SERVER=",
		"GIT_TERMINAL_PROMPT=0",
		"SSH_ASKPASS=",
		"GIT_ASKPASS=",
	)
	// bd runs every command under a deadline far below the 5-minute read
	// timeout a stuck server-side push ran into (the reset-data lane's rule).
	run := func(dir string, args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bdBinary, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("bd %s did not finish within 2m:\n%s", strings.Join(args, " "), out)
		}
		return string(out), err
	}
	must := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(dir, args...)
		if err != nil {
			t.Fatalf("bd %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
		return out
	}

	// Workspace A creates the issue and publishes the baseline.
	dirA := t.TempDir()
	stopDoltServerCleanup(t, filepath.Join(dirA, ".beads"))
	remoteDir := setupBareGitRemote(t)
	remoteURL := "git+file://" + remoteDir
	must(dirA, "init", "--backend", "dolt", "--server", "--prefix", database, "--quiet")
	must(dirA, "dolt", "remote", "add", "origin", remoteURL)
	issueID := strings.TrimSpace(must(dirA, "create", "Shared sync issue", "--type", "task", "--silent"))
	if issueID == "" {
		t.Fatal("bd create --silent printed no issue id")
	}
	must(dirA, "dolt", "push")

	// Workspace B bootstraps from the remote as a server-mode workspace: its
	// .beads names dolt_mode server before bootstrap runs, so the clone lands
	// in the data directory B's own server will serve, and its history shares
	// A's for the later merge.
	dirB := t.TempDir()
	stopDoltServerCleanup(t, filepath.Join(dirB, ".beads"))
	runGitForBootstrapTest(t, dirB, "init", "-b", "main")
	runGitForBootstrapTest(t, dirB, "remote", "add", "origin", "file://"+remoteDir)
	beadsB := filepath.Join(dirB, ".beads")
	// 0700, the permission bd recommends for .beads: a looser mode makes
	// every bd command print a warning on stdout that assertions read.
	if err := os.MkdirAll(beadsB, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"` + database + `"}`)
	if err := os.WriteFile(filepath.Join(beadsB, "metadata.json"), meta, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := must(dirB, "bootstrap", "--yes"); !strings.Contains(out, "clone from remote") {
		t.Fatalf("expected bootstrap to clone B from the remote, got:\n%s", out)
	}

	// A comments, changes status, and sets a user-kv key; B comments on and
	// renames the same issue — disjoint issue cells plus additive comment rows,
	// the shape whose cleanly merged tables a bad conclusion leaves unstaged.
	must(dirA, "comment", issueID, "comment from A")
	must(dirA, "update", issueID, "--status", "in_progress")
	must(dirA, "kv", "set", "kvtest", "hello")
	must(dirB, "comment", issueID, "comment from B")
	must(dirB, "update", issueID, "--title", "renamed on B")

	// Sync A/B/A: B's pull settles A's divergent push as a merge commit.
	must(dirA, "sync")
	must(dirB, "sync")
	must(dirA, "sync")

	// Both workspaces keep every peer row.
	for name, dir := range map[string]string{"A": dirA, "B": dirB} {
		if !serverHasPeerRows(t, run, dir, issueID) {
			t.Errorf("workspace %s lost peer rows across the A/B/A sync", name)
		}
	}

	// A fresh reader of the remote — a plain dolt clone — keeps them too: the
	// pushed merge result, not a revert of the first-syncing clone's rows.
	doltRoot := t.TempDir()
	dolt := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(doltBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "DOLT_ROOT_PATH="+doltRoot)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dolt %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	dolt(doltRoot, "config", "--global", "--add", "user.name", "bd-test")
	dolt(doltRoot, "config", "--global", "--add", "user.email", "bd-test@example.com")
	readerDir := filepath.Join(t.TempDir(), "reader")
	dolt(filepath.Dir(readerDir), "clone", remoteURL, readerDir)
	remote := dolt(readerDir, "sql", "-r", "csv", "-q",
		"SELECT (SELECT COUNT(*) FROM comments WHERE issue_id = '"+issueID+"' AND text = 'comment from A'), "+
			"(SELECT COUNT(*) FROM comments WHERE issue_id = '"+issueID+"' AND text = 'comment from B'), "+
			"(SELECT value FROM config WHERE `key` = 'kv.kvtest'), "+
			"(SELECT title FROM issues WHERE id = '"+issueID+"'), "+
			"(SELECT status FROM issues WHERE id = '"+issueID+"')")
	want := "1,1,hello,renamed on B,in_progress"
	if !strings.Contains(remote, want) {
		t.Errorf("a fresh dolt-CLI reader of the remote saw %q, want a row %q", strings.TrimSpace(remote), want)
	}

	// B's merge commit itself already contains the peer rows, read over B's
	// own running server.
	serverAssertMergeCommitKeepsPeerRows(t, beadsB, database, issueID)
}

// serverHasPeerRows reports whether the server-mode workspace in dir sees
// both comments, the kv value, A's status, and B's title on the issue,
// printing each miss.
func serverHasPeerRows(t *testing.T, run func(string, ...string) (string, error), dir, issueID string) bool {
	t.Helper()
	ok := true

	comments, err := run(dir, "comments", issueID, "--json")
	if err != nil {
		t.Errorf("bd comments %s in %s failed: %v\n%s", issueID, dir, err, comments)
		return false
	}
	for _, want := range []string{"comment from A", "comment from B"} {
		if !strings.Contains(comments, want) {
			t.Errorf("bd comments in %s is missing %q:\n%s", dir, want, comments)
			ok = false
		}
	}

	kvOut, err := run(dir, "kv", "get", "kvtest")
	if err != nil || strings.TrimSpace(kvOut) != "hello" {
		t.Errorf("bd kv get kvtest in %s = %q (err %v), want %q", dir, strings.TrimSpace(kvOut), err, "hello")
		ok = false
	}

	showOut, err := run(dir, "show", issueID, "--json")
	if err != nil {
		t.Errorf("bd show %s in %s failed: %v\n%s", issueID, dir, err, showOut)
		return false
	}
	shown := parseIssueJSON(t, []byte(showOut))
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

// serverAssertMergeCommitKeepsPeerRows reads the newest two-parent commit of
// database on the dolt server bd runs for beadsDir — the merge B's sync
// settled — and asserts the peer comment and kv rows are already part of it.
func serverAssertMergeCommitKeepsPeerRows(t *testing.T, beadsDir, database, issueID string) {
	t.Helper()
	state, err := doltserver.IsRunning(beadsDir)
	if err != nil {
		t.Fatalf("doltserver.IsRunning(%s): %v", beadsDir, err)
	}
	if state == nil || !state.Running || state.Port == 0 {
		t.Fatalf("bd's own server for %s is not running: %+v", beadsDir, state)
	}
	conn, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/%s", state.Port, database))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var mergeHash string
	if err := conn.QueryRow(
		"SELECT a.commit_hash FROM dolt_commit_ancestors a JOIN dolt_log l ON a.commit_hash = l.commit_hash " +
			"WHERE a.parent_index = 1 ORDER BY l.commit_order DESC LIMIT 1").Scan(&mergeHash); err != nil {
		t.Fatalf("find the merge commit on %s: %v", beadsDir, err)
	}
	if mergeHash == "" {
		t.Fatalf("no two-parent merge commit in %s: the sync never merged", beadsDir)
	}

	//nolint:gosec // mergeHash is a commit hash this database produced
	var peerComment int
	if err := conn.QueryRow(
		"SELECT COUNT(*) FROM comments AS OF '" + mergeHash + "' WHERE issue_id = ? AND text = 'comment from A'",
		issueID).Scan(&peerComment); err != nil {
		t.Fatalf("read comments AS OF the merge commit: %v", err)
	}
	if peerComment != 1 {
		t.Errorf("merge commit %s holds %d copies of A's comment, want 1: the peer row is missing from the merge result itself", mergeHash, peerComment)
	}

	//nolint:gosec // mergeHash is a commit hash this database produced
	var kvValue string
	if err := conn.QueryRow(
		"SELECT value FROM config AS OF '" + mergeHash + "' WHERE `key` = 'kv.kvtest'").Scan(&kvValue); err != nil {
		t.Fatalf("read config AS OF the merge commit: %v", err)
	}
	if kvValue != "hello" {
		t.Errorf("merge commit %s holds kv.kvtest = %q, want %q: A's kv row is missing from the merge result itself", mergeHash, kvValue, "hello")
	}
}
