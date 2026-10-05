package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

// GH#4927: prime's git probes must not depend on BEADS_DIR / RepoContext.
//
// buildRepoContext (internal/beads/context.go) can fail when FindBeadsDir()
// returns "" (context.go:107, e.g. BEADS_DIR doesn't exist). The pre-fix bug
// collapsed that GetRepoContext() error into "no git remote" / "ephemeral
// branch". This test poisons BEADS_DIR with a nonexistent path and verifies
// (a) that GetRepoContext() actually fails via FindBeadsDir()=="", and (b)
// that both probes the fix repairs -- primeHasGitRemote and isEphemeralBranch
// -- are unaffected by it.
//
// The probe assertions below use t.Error rather than t.Fatal so a regression
// in one probe does not mask the state of the others.
func TestPrimeGitProbes_IndependentOfBeadsDir(t *testing.T) {
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
	run("git", "config", "commit.gpgsign", "false")

	// gitDirHasRemote is the test-only oracle described in prime.go; these two
	// assertions check that the fixture itself is built correctly, not the fix.
	if gitDirHasRemote(dir) {
		t.Fatal("expected no remote after init")
	}

	run("git", "remote", "add", "origin", "https://example.invalid/repo.git")
	if !gitDirHasRemote(dir) {
		t.Fatal("expected remote after git remote add origin")
	}

	// A committed branch that has a remote but no upstream -- the state in
	// which the branch genuinely is ephemeral. The fixed branch name avoids
	// depending on init.defaultBranch.
	run("git", "commit", "-q", "--allow-empty", "-m", "fixture")
	run("git", "checkout", "-q", "-B", "fixture-branch")

	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	// CWD-based probe: chdir into the fixture repo
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// BEADS_DIR points at a path that simply doesn't exist on disk — this is
	// the FindBeadsDir()=="" failure mode (context.go:107). CWD is the
	// fixture repo (no .beads anywhere in its ancestry), so the walk in
	// FindBeadsDir also comes up empty.
	t.Setenv("BEADS_DIR", filepath.Join(dir, "does-not-exist", ".beads"))
	beads.ResetCaches()
	git.ResetCaches()
	if _, err := beads.GetRepoContext(); err == nil || !strings.Contains(err.Error(), "no .beads directory found") {
		t.Fatalf("expected GetRepoContext() to fail via FindBeadsDir()==\"\", got err=%v", err)
	}

	if !gitCWDHasRemote() {
		t.Error("gitCWDHasRemote should see origin even with a nonexistent BEADS_DIR set")
	}

	// The two probes the fix actually repairs. Both route through primeGitCmd,
	// so both regress if its GetRepoContext() fallback is removed.
	if !primeHasGitRemote() {
		t.Error("primeHasGitRemote should see origin even with a nonexistent BEADS_DIR set")
	}

	// isEphemeralBranch, both polarities under the same poisoned BEADS_DIR.
	// The branch has no upstream yet, so "ephemeral" is the correct answer
	// here; this control is what proves the assertion below is reading git
	// rather than returning a constant.
	if !isEphemeralBranch() {
		t.Error("isEphemeralBranch should report ephemeral while the branch has no upstream")
	}

	// Configure a real upstream -- entirely local, the remote is never
	// contacted -- and the same probe must flip. Pre-fix it stayed true here,
	// because the GetRepoContext() failure was itself reported as "ephemeral".
	run("git", "update-ref", "refs/remotes/origin/fixture-branch", "HEAD")
	run("git", "branch", "--set-upstream-to=origin/fixture-branch", "fixture-branch")
	if isEphemeralBranch() {
		t.Error("isEphemeralBranch should see the configured upstream even with a nonexistent BEADS_DIR set")
	}
}
