//go:build !cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// GH#4927: git remote probe must not depend on BEADS_DIR / RepoContext.
func TestGitDirHasRemote_IndependentOfBeadsDir(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.name", "fixture")
	run("git", "config", "user.email", "fixture@example.com")

	if gitDirHasRemote(dir) {
		t.Fatal("expected no remote after init")
	}

	run("git", "remote", "add", "origin", "https://example.invalid/repo.git")
	if !gitDirHasRemote(dir) {
		t.Fatal("expected remote after git remote add origin")
	}

	// Poison BEADS_DIR to a path under another home prefix — must not affect probe.
	t.Setenv("BEADS_DIR", filepath.Join("/Users", "other-host", "sandboxes", "beads-runtime"))
	// CWD-based probe: chdir into the fixture repo
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	if !gitCWDHasRemote() {
		t.Fatal("gitCWDHasRemote should see origin even with foreign BEADS_DIR set")
	}
	// And primeHasGitRemote uses the same path
	if !primeHasGitRemote() {
		t.Fatal("primeHasGitRemote should see origin even with foreign BEADS_DIR set")
	}
}
