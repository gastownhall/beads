//go:build cgo && integration

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestServerSyncMergeCommitKeepsPeerRows (GH#7757, AC-6) is the server-mode
// twin of TestEmbeddedSyncMergeCommitKeepsPeerRows: two server-mode clones,
// each with its own auto-started dolt sql-server, share one git remote. Clone
// A comments on an issue, changes its status and sets a user kv key; clone B
// comments on the same issue and renames it; then `bd sync` runs in A, B, A.
// Both clones must end with both comments and the kv value, and a fresh dolt
// clone of the remote must hold them too, in its working tree and in the one
// merge commit (B's sync settled it and its push published it), together
// with the field-merged issue.
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
	bd := func(dir string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bdBinary, args...)
		cmd.Dir = dir
		cmd.Env = env
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if ctx.Err() != nil {
			t.Fatalf("bd %s in %s did not finish within 2m:\n%s%s", strings.Join(args, " "), dir, out, stderr.String())
		}
		if err != nil {
			t.Fatalf("bd %s in %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), dir, err, out, stderr.String())
		}
		return string(out) // stdout only: B's permission warnings go to stderr
	}
	git := func(dir string, args ...string) {
		t.Helper()
		resetDataRunGit(t, dir, args...)
	}

	base := t.TempDir()
	bareDir := filepath.Join(base, "remote.git")
	git(base, "init", "--bare", "-b", "main", bareDir)
	remoteURL := "file://" + bareDir

	dirA := filepath.Join(base, "a")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, dirA)
	git(dirA, "branch", "-M", "main")
	git(dirA, "remote", "add", "origin", remoteURL)
	git(dirA, "commit", "--allow-empty", "-m", "init")
	git(dirA, "push", "-u", "origin", "main")
	stopDoltServerCleanup(t, filepath.Join(dirA, ".beads"))
	bd(dirA, "init", "--backend", "dolt", "--server", "--prefix", "sp", "--quiet", "--skip-agents", "--skip-hooks")
	issueID := strings.TrimSpace(bd(dirA, "create", "shared issue", "--type", "task", "--silent"))
	// bd init committed .beads to git; publish it so clone B finds the
	// workspace.
	git(dirA, "push", "origin", "main")
	bd(dirA, "dolt", "push")

	dirB := filepath.Join(base, "b")
	git(base, "clone", remoteURL, dirB)
	git(dirB, "config", "user.email", "test@test.com")
	git(dirB, "config", "user.name", "Test")
	git(dirB, "config", "core.hooksPath", ".git/hooks")
	stopDoltServerCleanup(t, filepath.Join(dirB, ".beads"))
	// metadata.json (from git) names server mode: bootstrap clones the
	// database from the remote into B's own server.
	bd(dirB, "bootstrap", "--yes")
	bd(dirB, "show", issueID)

	bd(dirA, "comments", "add", issueID, "comment from A")
	bd(dirA, "update", issueID, "--status", "in_progress")
	bd(dirA, "kv", "set", "peerkey", "fromA")

	bd(dirB, "comments", "add", issueID, "comment from B")
	bd(dirB, "update", issueID, "--title", "renamed on B")

	bd(dirA, "sync")
	bd(dirB, "sync")
	bd(dirA, "sync")

	want := []string{"comment from A", "comment from B"}
	for _, c := range []struct{ name, dir string }{{"A", dirA}, {"B", dirB}} {
		var comments []struct {
			Text string `json:"text"`
		}
		out := bd(c.dir, "comments", issueID, "--json")
		if err := json.Unmarshal([]byte(out), &comments); err != nil {
			t.Fatalf("parse clone %s bd comments --json: %v\n%s", c.name, err, out)
		}
		var got []string
		for _, cm := range comments {
			got = append(got, cm.Text)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("clone %s comments = %v, want %v", c.name, got, want)
		}
		if got := strings.TrimSpace(bd(c.dir, "kv", "get", "peerkey")); got != "fromA" {
			t.Errorf("clone %s kv peerkey = %q, want %q", c.name, got, "fromA")
		}
	}

	// A fresh reader of the remote, through the dolt CLI.
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
	reader := filepath.Join(t.TempDir(), "reader")
	dolt(filepath.Dir(reader), "clone", "git+file://"+bareDir, reader)
	query := func(q string) []string {
		t.Helper()
		lines := strings.Split(strings.TrimSpace(dolt(reader, "sql", "-r", "csv", "-q", q)), "\n")
		return lines[1:] // drop the CSV header
	}

	merges := query("SELECT commit_hash FROM dolt_commit_ancestors GROUP BY commit_hash HAVING COUNT(*) = 2")
	if len(merges) != 1 {
		t.Fatalf("the remote holds %d merge commit(s) %v, want the one B's sync wrote", len(merges), merges)
	}
	for _, root := range []struct{ name, asOf string }{{"remote HEAD", ""}, {"B's merge commit", " AS OF '" + merges[0] + "'"}} {
		if got := query("SELECT text FROM comments" + root.asOf + " WHERE issue_id = '" + issueID + "' ORDER BY text"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s comments = %v, want %v", root.name, got, want)
		}
		if got := query("SELECT value FROM config" + root.asOf + " WHERE `key` = 'kv.peerkey'"); !reflect.DeepEqual(got, []string{"fromA"}) {
			t.Errorf("%s kv.peerkey = %v, want [fromA]", root.name, got)
		}
		if got := query("SELECT title, status FROM issues" + root.asOf + " WHERE id = '" + issueID + "'"); !reflect.DeepEqual(got, []string{"renamed on B,in_progress"}) {
			t.Errorf("%s issue = %v, want [renamed on B,in_progress]", root.name, got)
		}
	}
}
