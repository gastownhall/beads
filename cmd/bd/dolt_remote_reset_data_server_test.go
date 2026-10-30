//go:build cgo && integration

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestE2E_ResetDataThroughOwnServerKeepsTheRemotesData runs bd dolt remote
// reset-data on a git-backed remote of a server-mode workspace whose pushes
// run in bd's own auto-started dolt sql-server. The server keeps the store of
// every git remote it has pushed to for its whole life, so deleting the
// remote's Dolt data ref under it and force-pushing (what reset-data does in
// embedded mode) leaves a push that either writes nothing or never finishes,
// and the remote without its data. reset-data must instead finish, leave the
// remote's data in place with every committed issue, leave no temporary
// remote or ref behind, and keep the remote usable: a later push lands, and
// a re-run works. A remote an earlier reset left without its data ref is
// rebuilt the same way. A file:// remote in a bare git repository is a git
// remote only when its URL ends in .git; any other is a Dolt file store
// among the repository's files, which reset-data refuses, as it refuses one
// in a checkout.
//
// The server's store for the remote also caches which table files the remote
// holds, and that cache only grows, so after the data ref moves under it the
// server still takes the pre-reset store's files as present. reset-data
// restarts bd's own server so that it rereads the remote: a Dolt branch
// pushed before the reset and pushed again after it then clones, and every
// table file the remote's manifest names is on the remote. Without the
// restart that push wrote a manifest naming a file the remote lacked in some
// runs, so the leg repeats.
func TestE2E_ResetDataThroughOwnServerKeepsTheRemotesData(t *testing.T) {
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
	tmpDir := t.TempDir()
	stopDoltServerCleanup(t, filepath.Join(tmpDir, ".beads"))
	doltRoot := t.TempDir()
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
	// timeout a stuck server-side push ran into.
	bd := func(args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, bdBinary, args...)
		cmd.Dir = tmpDir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("bd %s did not finish within 2m:\n%s", strings.Join(args, " "), out)
		}
		return string(out), err
	}
	mustBD := func(args ...string) string {
		t.Helper()
		out, err := bd(args...)
		if err != nil {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	// remoteTitles clones the remote with the dolt CLI and lists the issue
	// titles it holds.
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
	remoteTitles := func(remoteURL string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "clone")
		run := dolt
		run(filepath.Dir(dir), "clone", remoteURL, dir)
		return run(dir, "sql", "-r", "csv", "-q", "SELECT title FROM issues ORDER BY title")
	}

	mustBD("init", "--backend", "dolt", "--server", "--prefix", "rd", "--quiet")
	remoteDir := setupBareGitRemote(t)
	remoteURL := "git+file://" + remoteDir
	// Added by the file:// URL a user types for the bare repository: Dolt
	// stores a file URL that ends in .git as git+file://, a git remote, so
	// reset-data takes the temporary-ref route for it.
	mustBD("dolt", "remote", "add", "origin", "file://"+remoteDir)
	if list := mustBD("dolt", "remote", "list"); !strings.Contains(list, remoteURL) {
		t.Fatalf("Dolt did not store origin as %s:\n%s", remoteURL, list)
	}
	mustBD("create", "first issue", "--type", "task")
	mustBD("dolt", "push")
	// A commit since the last push: the server-side force push of main's
	// reset-data then hung until the read timeout.
	mustBD("create", "second issue", "--type", "task")
	dataBefore := lsRemoteRef(t, remoteDir, "refs/dolt/data")
	if dataBefore == "" {
		t.Fatal("push did not create refs/dolt/data")
	}

	checkReset := func(what string) {
		t.Helper()
		out := mustBD("dolt", "remote", "reset-data", "origin", "--yes")
		if !strings.Contains(out, `✓ Remote "origin" data plane reset`) {
			t.Errorf("%s: reset-data output lacks the success line:\n%s", what, out)
		}
		if lsRemoteRef(t, remoteDir, "refs/dolt/data") == "" {
			t.Fatalf("%s: the remote has no refs/dolt/data after reset-data:\n%s", what, out)
		}
		if refs := resetDataRunGit(t, remoteDir, "for-each-ref", "--format=%(refname)", "refs/dolt/"); strings.Contains(refs, "bd-reset-data") {
			t.Errorf("%s: reset-data left its temporary ref on the remote:\n%s", what, refs)
		}
		if list := mustBD("dolt", "remote", "list"); strings.Contains(list, "bd-reset-data") {
			t.Errorf("%s: reset-data left its temporary remote on the server:\n%s", what, list)
		}
	}

	checkReset("first reset")
	if got := lsRemoteRef(t, remoteDir, "refs/dolt/data"); got == dataBefore {
		t.Errorf("refs/dolt/data still at %s: reset-data did not replace the store", got)
	}
	if titles := remoteTitles(remoteURL); !strings.Contains(titles, "first issue") || !strings.Contains(titles, "second issue") {
		t.Fatalf("the reset remote lacks committed issues:\n%s", titles)
	}
	if lsRemoteRef(t, remoteDir, "refs/heads/main") == "" {
		t.Error("reset-data removed the remote's code branch")
	}

	// The server's store for origin follows the moved ref: a later push lands.
	mustBD("create", "third issue", "--type", "task")
	mustBD("dolt", "push")
	if titles := remoteTitles(remoteURL); !strings.Contains(titles, "third issue") {
		t.Fatalf("a push after reset-data did not reach the remote:\n%s", titles)
	}

	// A re-run works.
	checkReset("re-run")

	// A remote an earlier reset left without its data ref (main's delete-
	// then-push failing after the delete) is rebuilt in place.
	resetDataRunGit(t, remoteDir, "update-ref", "-d", "refs/dolt/data")
	checkReset("stranded remote")
	if titles := remoteTitles(remoteURL); !strings.Contains(titles, "third issue") {
		t.Fatalf("the rebuilt remote lacks committed issues:\n%s", titles)
	}

	// missingTables lists the table files the remote's refs/dolt/data
	// manifest names that its tree lacks. The manifest is
	// version:lock:root:gcGen:appendix... then name:count pairs from the
	// sixth field on.
	missingTables := func() []string {
		t.Helper()
		files := map[string]bool{}
		for _, f := range strings.Fields(resetDataRunGit(t, remoteDir, "ls-tree", "--name-only", "refs/dolt/data")) {
			files[strings.TrimSuffix(f, ".darc")] = true
		}
		parts := strings.Split(strings.TrimSpace(resetDataRunGit(t, remoteDir, "show", "refs/dolt/data:manifest")), ":")
		var missing []string
		for i := 5; i < len(parts); i += 2 {
			if !files[parts[i]] {
				missing = append(missing, parts[i])
			}
		}
		return missing
	}
	// A Dolt branch with table files of its own, pushed through the server
	// before a reset (the dolt CLI in the served database hands the
	// statements to bd's server), and again after it.
	dbDir := filepath.Join(tmpDir, ".beads", "dolt", "rd")
	for i := 0; i < 4; i++ {
		branch := fmt.Sprintf("feat%d", i)
		dolt(dbDir, "sql", "-q", fmt.Sprintf(
			"CALL DOLT_CHECKOUT('-b','%[1]s'); CREATE TABLE zz_%[1]s (id int primary key, v longtext); "+
				"INSERT INTO zz_%[1]s VALUES (1, REPEAT('payload-%[1]s-', 20000)), (2, 'row-%[1]s'); "+
				"CALL DOLT_COMMIT('-Am','%[1]s'); CALL DOLT_PUSH('origin','%[1]s'); CALL DOLT_CHECKOUT('main');", branch))
		out := mustBD("dolt", "remote", "reset-data", "origin", "--yes")
		if !strings.Contains(out, "Restarted bd's Dolt server") {
			t.Errorf("%s: reset-data through bd's own server did not restart it:\n%s", branch, out)
		}
		dolt(dbDir, "sql", "-q", fmt.Sprintf("CALL DOLT_PUSH('origin','%s');", branch))
		if missing := missingTables(); len(missing) > 0 {
			t.Fatalf("%s: the remote's manifest names table files it does not hold: %v", branch, missing)
		}
		dir := filepath.Join(t.TempDir(), "clone")
		dolt(filepath.Dir(dir), "clone", "-b", branch, remoteURL, dir)
		if rows := dolt(dir, "sql", "-r", "csv", "-q", fmt.Sprintf("SELECT id FROM zz_%s ORDER BY id", branch)); !strings.Contains(rows, "1") || !strings.Contains(rows, "2") {
			t.Fatalf("%s: the branch pushed after reset-data clones without its rows:\n%s", branch, rows)
		}
	}

	// Any other file:// URL stays file:// in Dolt, which pushes it as a file
	// store written among the files of the bare git repository there, with
	// no Dolt data ref. reset-data must not clear a git repository, so it
	// refuses that remote and leaves it as it was.
	plainDir := filepath.Join(t.TempDir(), "plain")
	resetDataRunGit(t, filepath.Dir(plainDir), "init", "--bare", "-b", "main", plainDir)
	mustBD("dolt", "remote", "add", "plain", "file://"+plainDir)
	mustBD("dolt", "push", "--remote", "plain")
	if _, err := os.Stat(filepath.Join(plainDir, "manifest")); err != nil {
		t.Fatalf("a push to file://%s wrote no Dolt file store there: %v", plainDir, err)
	}
	if lsRemoteRef(t, plainDir, "refs/dolt/data") != "" {
		t.Fatalf("a push to file://%s wrote refs/dolt/data: Dolt took it for a git remote", plainDir)
	}
	if out, err := bd("dolt", "remote", "reset-data", "plain", "--yes"); err == nil || !strings.Contains(out, `cannot clear stored data on remote "plain"`) {
		t.Fatalf("reset-data of a file:// remote in a bare git repository did not refuse it (err %v):\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(plainDir, "manifest")); err != nil || !isBareGitRepoDir(plainDir) {
		t.Errorf("reset-data changed the remote it refused: %v", err)
	}

	// A checkout takes Dolt's file store beside .git and its working files.
	// reset-data took that for a file store and cleared the directory, .git
	// and every working file included, so it refuses that remote too.
	checkoutDir := filepath.Join(t.TempDir(), "checkout")
	resetDataRunGit(t, filepath.Dir(checkoutDir), "init", "-b", "main", checkoutDir)
	if err := os.WriteFile(filepath.Join(checkoutDir, "README.md"), []byte("code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resetDataRunGit(t, checkoutDir, "add", "README.md")
	resetDataRunGit(t, checkoutDir, "-c", "user.name=bd-test", "-c", "user.email=bd-test@example.com",
		"commit", "-m", "code")
	mustBD("dolt", "remote", "add", "checkout", "file://"+checkoutDir)
	mustBD("dolt", "push", "--remote", "checkout")
	if _, err := os.Stat(filepath.Join(checkoutDir, "manifest")); err != nil {
		t.Fatalf("a push to file://%s wrote no Dolt file store there: %v", checkoutDir, err)
	}
	if out, err := bd("dolt", "remote", "reset-data", "checkout", "--yes"); err == nil || !strings.Contains(out, `cannot clear stored data on remote "checkout"`) {
		t.Fatalf("reset-data of a file:// remote in a git checkout did not refuse it (err %v):\n%s", err, out)
	}
	for _, f := range []string{".git", "README.md", "manifest"} {
		if _, err := os.Stat(filepath.Join(checkoutDir, f)); err != nil {
			t.Errorf("reset-data changed the remote it refused: %v", err)
		}
	}
}
