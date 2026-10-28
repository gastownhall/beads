//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// GH#6484: Dolt's git blobstore force-publishes a visible info branch — a
// single DOLT_REMOTE.md marker — on the git remote after every successful
// write. In bd's default layout the git-hosted Dolt remote is the user's
// code repo, so that branch becomes a checkout-able HEAD trap: a fetch
// materializes origin/__dolt_remote_info__, and one stray checkout of it
// strips every tracked file from the working tree. bd must keep the branch
// off by default and honor Dolt's own opt-in, DOLT_REMOTE_INFO_BRANCH.

const (
	// doltRemoteInfoBranchEnv is Dolt's override for the visible info
	// branch name (store/blobstore/git_refs.go: InfoBranchEnvVar), pinned
	// locally the way dolt_remote_reset_data.go pins the ref: set to empty
	// it disables the branch entirely. internal/doltinfoenv keeps a pin
	// test against Dolt's constant.
	doltRemoteInfoBranchEnv = "DOLT_REMOTE_INFO_BRANCH"
	// doltRemoteInfoBranchName is the default short branch name Dolt
	// publishes (store/blobstore/git_refs.go: DefaultInfoBranch).
	doltRemoteInfoBranchName = "__dolt_remote_info__"
)

// riEnvDefault is bdEnv with any inherited DOLT_REMOTE_INFO_BRANCH dropped,
// so the default-publication tests are deterministic on a host that exports
// it. The default case is "the variable absent from bd's environment".
func riEnvDefault(dir string) []string {
	return envWithout(bdEnv(dir), doltRemoteInfoBranchEnv)
}

// riEnvOptIn is bdEnv with DOLT_REMOTE_INFO_BRANCH pinned to name, the
// documented opt-in.
func riEnvOptIn(dir, name string) []string {
	return append(riEnvDefault(dir), doltRemoteInfoBranchEnv+"="+name)
}

// riRun runs bd with an explicit environment and returns its combined
// output. Callers decide whether an error is the condition under test.
func riRun(t *testing.T, bd, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// riRunOK runs bd and fatals on failure.
func riRunOK(t *testing.T, bd, dir string, env []string, args ...string) {
	t.Helper()
	if out, err := riRun(t, bd, dir, env, args...); err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// riCreate runs "bd create --json" with an explicit environment and returns
// the parsed issue.
func riCreate(t *testing.T, bd, dir string, env []string, args ...string) *types.Issue {
	t.Helper()
	out, err := riRun(t, bd, dir, env, append([]string{"create", "--json"}, args...)...)
	if err != nil {
		t.Fatalf("bd create %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return parseIssueJSON(t, []byte(out))
}

// TestEmbeddedDoltPushDoesNotPublishRemoteInfoBranch pins the new default:
// with DOLT_REMOTE_INFO_BRANCH absent from bd's environment, neither
// `bd dolt push` nor the auto-push after a write publishes the info branch
// on the git-hosted Dolt remote (GH#6484).
func TestEmbeddedDoltPushDoesNotPublishRemoteInfoBranch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ri")
	env := riEnvDefault(dir)
	remoteDir := setupBareGitRemote(t)
	riRunOK(t, bd, dir, env, "dolt", "remote", "add", "origin", "git+file://"+remoteDir)

	riCreate(t, bd, dir, env, "first issue", "--type", "task")
	riRunOK(t, bd, dir, env, "dolt", "push")
	if lsRemoteRef(t, remoteDir, "refs/dolt/data") == "" {
		t.Fatal("bd dolt push did not publish refs/dolt/data on the git remote")
	}
	if got := lsRemoteRef(t, remoteDir, "refs/heads/"+doltRemoteInfoBranchName); got != "" {
		t.Fatalf("bd dolt push published %s on the git remote (%s); the info branch must be off by default",
			doltRemoteInfoBranchName, got)
	}

	// The auto-push after a write must not publish it either.
	riRunOK(t, bd, dir, env, "config", "set", "dolt.auto-push", "true")
	dataBefore := lsRemoteRef(t, remoteDir, "refs/dolt/data")
	riCreate(t, bd, dir, env, "second issue", "--type", "task")
	if after := lsRemoteRef(t, remoteDir, "refs/dolt/data"); after == "" || after == dataBefore {
		t.Errorf("auto-push after create did not move refs/dolt/data (before %q, after %q)", dataBefore, after)
	}
	if got := lsRemoteRef(t, remoteDir, "refs/heads/"+doltRemoteInfoBranchName); got != "" {
		t.Errorf("auto-push published %s on the git remote (%s)", doltRemoteInfoBranchName, got)
	}
}

// TestEmbeddedDoltPushHonorsExplicitRemoteInfoBranch pins the opt-in: with
// DOLT_REMOTE_INFO_BRANCH set to a name, `bd dolt push` publishes that
// branch — still the single DOLT_REMOTE.md marker — and never the default
// name.
func TestEmbeddedDoltPushHonorsExplicitRemoteInfoBranch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ri")
	env := riEnvOptIn(dir, "dolt-info")
	remoteDir := setupBareGitRemote(t)
	riRunOK(t, bd, dir, env, "dolt", "remote", "add", "origin", "git+file://"+remoteDir)

	riCreate(t, bd, dir, env, "first issue", "--type", "task")
	riRunOK(t, bd, dir, env, "dolt", "push")
	if lsRemoteRef(t, remoteDir, "refs/heads/dolt-info") == "" {
		t.Fatal("bd dolt push with DOLT_REMOTE_INFO_BRANCH=dolt-info did not publish refs/heads/dolt-info")
	}
	if lsRemoteRef(t, remoteDir, "refs/heads/"+doltRemoteInfoBranchName) != "" {
		t.Errorf("bd dolt push published %s despite the explicit override", doltRemoteInfoBranchName)
	}
	// The opt-in branch stays what Dolt writes: a single-file marker tree.
	tree := strings.TrimSpace(resetDataRunGit(t, remoteDir, "ls-tree", "refs/heads/dolt-info"))
	lines := strings.Split(tree, "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "DOLT_REMOTE.md") {
		t.Errorf("refs/heads/dolt-info tree = %q, want the single DOLT_REMOTE.md marker", tree)
	}
}

// TestEmbeddedWritesNeverCheckOutDoltRemoteInfoBranch is the GH#6484 layout
// end to end: the git origin doubles as the git-hosted Dolt remote, the info
// branch is published (opt-in) and fetched so origin/__dolt_remote_info__
// exists in the user's clone, then ordinary writes and a batch of six failed
// creates run. The working repo's HEAD must never move to the info branch
// and its tracked files must survive — the reported failure stripped every
// tracked file from disk.
func TestEmbeddedWritesNeverCheckOutDoltRemoteInfoBranch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ri")
	env := riEnvOptIn(dir, doltRemoteInfoBranchName)

	// A real working branch with a tracked file.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resetDataRunGit(t, dir, "add", "tracked.txt")
	resetDataRunGit(t, dir, "commit", "-m", "tracked file")
	resetDataRunGit(t, dir, "checkout", "-b", "work")

	// The git origin is also the Dolt remote (git-hosted Dolt remote).
	remoteDir := setupBareGitRemote(t)
	resetDataRunGit(t, dir, "remote", "add", "origin", remoteDir)
	resetDataRunGit(t, dir, "push", "origin", "work")
	riRunOK(t, bd, dir, env, "dolt", "remote", "add", "origin", "git+file://"+remoteDir)
	riRunOK(t, bd, dir, env, "config", "set", "dolt.auto-push", "true")
	seed := riCreate(t, bd, dir, env, "seed issue", "--type", "task")
	riRunOK(t, bd, dir, env, "dolt", "push")
	if lsRemoteRef(t, remoteDir, "refs/heads/"+doltRemoteInfoBranchName) == "" {
		t.Fatal("opt-in push did not publish " + doltRemoteInfoBranchName + " on the git remote")
	}
	// The user's clone now sees origin/__dolt_remote_info__, as in the report.
	resetDataRunGit(t, dir, "fetch", "origin")

	// Ordinary writes, then a batch of six failed creates.
	riRunOK(t, bd, dir, env, "export", "-o", filepath.Join(t.TempDir(), "out.jsonl"))
	riCreate(t, bd, dir, env, "second issue", "--type", "task")
	riRunOK(t, bd, dir, env, "update", seed.ID, "--status", "in_progress")
	for i := 0; i < 6; i++ {
		if out, err := riRun(t, bd, dir, env, "create", "child", "--parent", "ri-doesnotexist"); err == nil {
			t.Fatalf("create with missing parent unexpectedly succeeded:\n%s", out)
		}
	}

	if got := strings.TrimSpace(resetDataRunGit(t, dir, "branch", "--show-current")); got != "work" {
		t.Errorf("HEAD moved: current branch = %q, want %q", got, "work")
	}
	if _, err := os.Stat(filepath.Join(dir, "tracked.txt")); err != nil {
		t.Errorf("tracked file stripped from working tree: %v", err)
	}
	if reflog := resetDataRunGit(t, dir, "reflog", "--format=%gs"); strings.Contains(reflog, doltRemoteInfoBranchName) {
		t.Errorf("HEAD reflog records a checkout of %s:\n%s", doltRemoteInfoBranchName, reflog)
	}
	if out := resetDataRunGit(t, dir, "for-each-ref", "refs/heads/"+doltRemoteInfoBranchName); strings.TrimSpace(out) != "" {
		t.Errorf("local branch refs/heads/%s was created in the working repo: %s", doltRemoteInfoBranchName, out)
	}
}
