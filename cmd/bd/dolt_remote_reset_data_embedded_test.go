//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Integration tests for bd-fs6k3: bd dolt remote reset-data — replace a
// remote's data plane in place after a history squash. Run with
// BEADS_TEST_EMBEDDED_DOLT=1.

// resetDataRunGit runs a git command in dir, fatal on failure.
func resetDataRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// setupBareGitRemote creates a bare git repo seeded with an initial commit
// on main (Dolt's git blobstore requires an existing branch) and returns
// its path.
func setupBareGitRemote(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	remoteDir := filepath.Join(base, "remote.git")
	resetDataRunGit(t, base, "init", "--bare", "-b", "main", remoteDir)

	seedDir := filepath.Join(base, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resetDataRunGit(t, seedDir, "init", "-b", "main")
	resetDataRunGit(t, seedDir, "-c", "user.name=bd-test", "-c", "user.email=bd-test@example.com",
		"commit", "--allow-empty", "-m", "init")
	resetDataRunGit(t, seedDir, "push", remoteDir, "main")
	return remoteDir
}

// lsRemoteRef returns the hash the bare repo has for ref, or "" if absent.
func lsRemoteRef(t *testing.T, remoteDir, ref string) string {
	t.Helper()
	out := resetDataRunGit(t, remoteDir, "ls-remote", remoteDir, ref)
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func TestDoltRemoteResetData_GitBacked(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rd")

	remoteDir := setupBareGitRemote(t)
	remoteURL := "git+file://" + remoteDir

	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "origin", remoteURL); err != nil {
		t.Fatalf("bd dolt remote add failed: %v\nstderr:\n%s", err, stderr)
	}
	bdCreate(t, bd, dir, "seed issue", "--type", "task")
	if stdout, stderr, err := bdDoltSeparate(t, bd, dir, "push"); err != nil {
		t.Fatalf("bd dolt push failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	dataBefore := lsRemoteRef(t, remoteDir, "refs/dolt/data")
	if dataBefore == "" {
		t.Fatal("push did not create refs/dolt/data on the git remote")
	}

	// Non-interactive without --yes: refuse, mention --yes, touch nothing.
	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "reset-data", "origin"); err == nil {
		t.Fatalf("expected reset-data without --yes to exit non-zero\nstderr:\n%s", stderr)
	} else if !strings.Contains(stderr, "--yes") {
		t.Errorf("refusal should mention --yes, got:\n%s", stderr)
	}
	if got := lsRemoteRef(t, remoteDir, "refs/dolt/data"); got != dataBefore {
		t.Fatalf("refused reset-data still changed refs/dolt/data: %q -> %q", dataBefore, got)
	}

	// With --yes: delete the data refs and rebuild the store via force-push.
	stdout, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "reset-data", "origin", "--yes")
	if err != nil {
		t.Fatalf("bd dolt remote reset-data --yes failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "refs/dolt/data") {
		t.Errorf("expected deleted-refs report in output, got:\n%s", stdout)
	}
	if got := lsRemoteRef(t, remoteDir, "refs/dolt/data"); got == "" {
		t.Error("refs/dolt/data missing after reset-data; force-push should have rebuilt it")
	}
	// Code branches are untouched.
	if got := lsRemoteRef(t, remoteDir, "refs/heads/main"); got == "" {
		t.Error("refs/heads/main disappeared from the git remote")
	}

	// Re-run is idempotent: refs exist again, so they are deleted and rebuilt.
	if stdout, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "reset-data", "origin", "--yes"); err != nil {
		t.Fatalf("second reset-data failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
}

// TestEmbeddedDoltRemoteResetData_FileRemoteInGitRepoRefused: Dolt makes a
// file URL a git remote only when it ends in .git (stored as git+file://);
// any other file:// URL is a native Dolt file store, which Dolt writes among
// the files of a git repository at that path. reset-data took a checkout
// holding such a store for a file store and cleared it, .git and every
// working file included, and over a bare repository it reported a reset that
// left every old table file in place. It must refuse both, with the
// replace-the-remote hint, and leave each directory as it was. These are the
// refusal legs of #7902's TestE2E_ResetDataThroughOwnServerKeepsTheRemotesData,
// run in embedded mode: the classification runs before any push, in either
// mode. The TestEmbedded prefix puts it in CI's embedded cmd shards
// (.github/scripts/embedded-test-shard.sh).
func TestEmbeddedDoltRemoteResetData_FileRemoteInGitRepoRefused(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rd")
	bdCreate(t, bd, dir, "seed issue", "--type", "task")

	mustDolt := func(args ...string) {
		t.Helper()
		if stdout, stderr, err := bdDoltSeparate(t, bd, dir, args...); err != nil {
			t.Fatalf("bd dolt %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout, stderr)
		}
	}
	listing := func(path string) string {
		t.Helper()
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return strings.Join(names, " ")
	}
	refused := func(name string) {
		t.Helper()
		stdout, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "reset-data", name, "--yes")
		if err == nil {
			t.Fatalf("reset-data of file:// remote %q in a git repository exited 0\nstdout:\n%s\nstderr:\n%s", name, stdout, stderr)
		}
		for _, want := range []string{`cannot clear stored data on remote "` + name + `"`, "git repository", "fresh URL"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("reset-data refusal for %q lacks %q:\n%s", name, want, stderr)
			}
		}
	}

	// A bare repository: Dolt writes its file store among the repository's
	// files and no Dolt data ref.
	plainDir := filepath.Join(t.TempDir(), "plain")
	resetDataRunGit(t, filepath.Dir(plainDir), "init", "--bare", "-b", "main", plainDir)
	mustDolt("remote", "add", "plain", "file://"+plainDir)
	mustDolt("push", "--remote", "plain")
	if _, err := os.Stat(filepath.Join(plainDir, "manifest")); err != nil {
		t.Fatalf("a push to file://%s wrote no Dolt file store there: %v", plainDir, err)
	}
	if lsRemoteRef(t, plainDir, "refs/dolt/data") != "" {
		t.Fatalf("a push to file://%s wrote refs/dolt/data: Dolt took it for a git remote", plainDir)
	}
	plainBefore := listing(plainDir)
	refused("plain")
	if got := listing(plainDir); got != plainBefore || !isBareGitRepoDir(plainDir) {
		t.Errorf("reset-data changed the bare repository it refused:\nbefore: %s\nafter:  %s", plainBefore, got)
	}

	// A checkout: the file store lands beside .git and the working files.
	checkoutDir := filepath.Join(t.TempDir(), "checkout")
	resetDataRunGit(t, filepath.Dir(checkoutDir), "init", "-b", "main", checkoutDir)
	if err := os.WriteFile(filepath.Join(checkoutDir, "README.md"), []byte("code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resetDataRunGit(t, checkoutDir, "add", "README.md")
	resetDataRunGit(t, checkoutDir, "-c", "user.name=bd-test", "-c", "user.email=bd-test@example.com",
		"commit", "-m", "code")
	mustDolt("remote", "add", "checkout", "file://"+checkoutDir)
	mustDolt("push", "--remote", "checkout")
	if _, err := os.Stat(filepath.Join(checkoutDir, "manifest")); err != nil {
		t.Fatalf("a push to file://%s wrote no Dolt file store there: %v", checkoutDir, err)
	}
	checkoutBefore := listing(checkoutDir)
	refused("checkout")
	if got := listing(checkoutDir); got != checkoutBefore {
		t.Errorf("reset-data changed the checkout it refused:\nbefore: %s\nafter:  %s", checkoutBefore, got)
	}
	for _, f := range []string{".git", "README.md", "manifest"} {
		if _, err := os.Stat(filepath.Join(checkoutDir, f)); err != nil {
			t.Errorf("reset-data changed the checkout it refused: %v", err)
		}
	}
	if log := resetDataRunGit(t, checkoutDir, "log", "--format=%s"); !strings.Contains(log, "code") {
		t.Errorf("the checkout's history is gone after the refused reset-data:\n%s", log)
	}
}

func TestDoltRemoteResetData_UnknownRemote(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rd")

	_, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "reset-data", "nosuch", "--yes")
	if err == nil {
		t.Fatalf("expected reset-data on unknown remote to exit non-zero\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "not configured") {
		t.Errorf("expected 'not configured' in stderr, got:\n%s", stderr)
	}
}

func TestDoltRemoteResetData_CloudRemoteRefused(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rd")

	if _, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "add", "cloud", "gs://bucket/db"); err != nil {
		t.Fatalf("bd dolt remote add failed: %v\nstderr:\n%s", err, stderr)
	}
	_, stderr, err := bdDoltSeparate(t, bd, dir, "remote", "reset-data", "cloud", "--yes")
	if err == nil {
		t.Fatalf("expected reset-data on gs:// remote to exit non-zero\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "fresh URL") {
		t.Errorf("expected replace-the-remote guidance in stderr, got:\n%s", stderr)
	}
}
