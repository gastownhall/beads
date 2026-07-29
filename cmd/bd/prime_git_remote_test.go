package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

// GH#4927: git remote probe must not depend on BEADS_DIR / RepoContext.
//
// buildRepoContext (internal/beads/context.go) can fail two different ways:
// FindBeadsDir() returning "" (context.go:107, e.g. BEADS_DIR doesn't exist)
// and isPathInSafeBoundary rejecting an existing BEADS_DIR (context.go:112,
// SEC-003). The pre-fix bug collapsed a GetRepoContext() error of either
// shape into "no git remote" / "ephemeral branch". This test poisons
// BEADS_DIR with one example of each shape and, for each, verifies (a) that
// GetRepoContext() actually fails via the failure mode the case claims to
// exercise, and (b) that gitCWDHasRemote/primeHasGitRemote are unaffected by
// either.
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

	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	// Case 1: BEADS_DIR points at a real directory under a rejected prefix
	// (SEC-003's unsafePrefixes includes "/var"), seeded with a beads.db so
	// FindBeadsDir() actually returns it instead of falling through — this is
	// what makes GetRepoContext() fail via isPathInSafeBoundary specifically,
	// not via FindBeadsDir()=="".
	boundaryRejectDir := filepath.Join("/var", "tmp", "beads-poison-"+strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(boundaryRejectDir, 0o755); err != nil {
		t.Skipf("cannot create fixture dir under %s: %v", boundaryRejectDir, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(boundaryRejectDir) })
	if err := os.WriteFile(filepath.Join(boundaryRejectDir, "beads.db"), nil, 0o644); err != nil {
		t.Fatalf("failed to seed poison beads.db: %v", err)
	}

	t.Setenv("BEADS_DIR", boundaryRejectDir)
	beads.ResetCaches()
	git.ResetCaches()
	if _, err := beads.GetRepoContext(); err == nil || !strings.Contains(err.Error(), "unsafe location") {
		t.Fatalf("expected GetRepoContext() to reject %q via isPathInSafeBoundary, got err=%v", boundaryRejectDir, err)
	}

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
		t.Fatal("gitCWDHasRemote should see origin even with a boundary-rejected BEADS_DIR set")
	}
	// And primeHasGitRemote uses the same path
	if !primeHasGitRemote() {
		t.Fatal("primeHasGitRemote should see origin even with a boundary-rejected BEADS_DIR set")
	}

	// Case 2: BEADS_DIR points at a path that simply doesn't exist on disk —
	// this is the FindBeadsDir()=="" failure mode (context.go:107). CWD is
	// now the fixture repo (no .beads anywhere in its ancestry), so the walk
	// in FindBeadsDir also comes up empty.
	t.Setenv("BEADS_DIR", filepath.Join(dir, "does-not-exist", ".beads"))
	beads.ResetCaches()
	git.ResetCaches()
	if _, err := beads.GetRepoContext(); err == nil || !strings.Contains(err.Error(), "no .beads directory found") {
		t.Fatalf("expected GetRepoContext() to fail via FindBeadsDir()==\"\", got err=%v", err)
	}

	if !gitCWDHasRemote() {
		t.Fatal("gitCWDHasRemote should see origin even with a nonexistent BEADS_DIR set")
	}
	if !primeHasGitRemote() {
		t.Fatal("primeHasGitRemote should see origin even with a nonexistent BEADS_DIR set")
	}
}
