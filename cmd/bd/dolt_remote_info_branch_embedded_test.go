//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dolthub/dolt/go/store/blobstore"
)

// Integration tests for GH#6484: Dolt publishes a one-file
// refs/heads/__dolt_remote_info__ branch on every git-hosted remote it
// writes to, and bd's default layout aims that write at the user's code
// remote. bd turns the branch off unless the user opts in with
// DOLT_REMOTE_INFO_BRANCH. Run with BEADS_TEST_EMBEDDED_DOLT=1.

var defaultInfoBranchRef = "refs/heads/" + blobstore.DefaultInfoBranch

// infoBranchBDEnv is bdEnv without any inherited DOLT_REMOTE_INFO_BRANCH, so
// bd runs with its own default, plus extra entries.
func infoBranchBDEnv(dir string, extra ...string) []string {
	return append(envWithout(bdEnv(dir), blobstore.InfoBranchEnvVar), extra...)
}

// infoBranchRunBD runs bd in dir with env, fatal on failure. The shared
// helpers (bdCreate, bdDoltSeparate) always use bdEnv and cannot take the
// extra variable, and t.Setenv cannot combine with t.Parallel.
func infoBranchRunBD(t *testing.T, bd, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// infoBranchPushSetup initializes a workspace whose Dolt remote is a
// git+file bare repo that already has a main branch (Dolt refuses a
// branchless git remote), creates one issue and pushes it with env.
func infoBranchPushSetup(t *testing.T, bd string, env func(dir string) []string) (dir, remoteDir string) {
	t.Helper()
	dir, _, _ = bdInit(t, bd, "--prefix", "ib")
	remoteDir = setupBareGitRemote(t)
	infoBranchRunBD(t, bd, dir, env(dir), "dolt", "remote", "add", "origin", "git+file://"+remoteDir)
	infoBranchRunBD(t, bd, dir, env(dir), "create", "seed issue", "--type", "task")
	infoBranchRunBD(t, bd, dir, env(dir), "dolt", "push")
	if lsRemoteRef(t, remoteDir, "refs/dolt/data") == "" {
		t.Fatal("push did not create refs/dolt/data on the git remote")
	}
	return dir, remoteDir
}

func TestEmbeddedDoltPushDoesNotPublishRemoteInfoBranch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, remoteDir := infoBranchPushSetup(t, bd, func(dir string) []string { return infoBranchBDEnv(dir) })

	if got := lsRemoteRef(t, remoteDir, defaultInfoBranchRef); got != "" {
		t.Errorf("bd dolt push published %s (%s) on the git remote", defaultInfoBranchRef, got)
	}

	// The auto-push after a write. BD_DOLT_AUTO_PUSH turns it on for this one
	// call (viper reads BD_<KEY>); `bd dolt push` records no push state, so
	// the auto-push debounce does not skip it.
	dataBefore := lsRemoteRef(t, remoteDir, "refs/dolt/data")
	infoBranchRunBD(t, bd, dir, append(slices.Clone(infoBranchBDEnv(dir)), "BD_DOLT_AUTO_PUSH=true"),
		"create", "second issue", "--type", "task")
	if lsRemoteRef(t, remoteDir, "refs/dolt/data") == dataBefore {
		t.Fatal("auto-push after bd create did not move refs/dolt/data")
	}
	if got := lsRemoteRef(t, remoteDir, defaultInfoBranchRef); got != "" {
		t.Errorf("auto-push published %s (%s) on the git remote", defaultInfoBranchRef, got)
	}
}

func TestEmbeddedDoltPushHonorsExplicitRemoteInfoBranch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	_, remoteDir := infoBranchPushSetup(t, bd, func(dir string) []string {
		return infoBranchBDEnv(dir, blobstore.InfoBranchEnvVar+"=dolt-info")
	})

	if lsRemoteRef(t, remoteDir, "refs/heads/dolt-info") == "" {
		t.Fatalf("bd dolt push with %s=dolt-info did not publish refs/heads/dolt-info", blobstore.InfoBranchEnvVar)
	}
	tree := resetDataRunGit(t, remoteDir, "ls-tree", "--name-only", "refs/heads/dolt-info")
	if got := strings.Fields(tree); !slices.Equal(got, []string{"DOLT_REMOTE.md"}) {
		t.Errorf("refs/heads/dolt-info tree = %q, want only DOLT_REMOTE.md", got)
	}
	if got := lsRemoteRef(t, remoteDir, defaultInfoBranchRef); got != "" {
		t.Errorf("bd dolt push published %s (%s) although %s names another branch",
			defaultInfoBranchRef, got, blobstore.InfoBranchEnvVar)
	}
}

// TestEmbeddedWritesNeverCheckOutDoltRemoteInfoBranch is the GH#6484 report's
// scenario: the git origin doubles as the git-hosted Dolt remote and the
// clone has fetched origin/__dolt_remote_info__. bd writes, successful and
// failed, must never move HEAD to that branch or strip tracked files. The
// workspace opts back into the branch so it exists to be checked out.
func TestEmbeddedWritesNeverCheckOutDoltRemoteInfoBranch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ri")
	env := infoBranchBDEnv(dir, blobstore.InfoBranchEnvVar+"="+blobstore.DefaultInfoBranch)

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
	infoBranchRunBD(t, bd, dir, env, "dolt", "remote", "add", "origin", "git+file://"+remoteDir)
	infoBranchRunBD(t, bd, dir, env, "config", "set", "dolt.auto-push", "true")
	seed := parseIssueJSON(t, []byte(infoBranchRunBD(t, bd, dir, env, "create", "--json", "seed issue", "--type", "task")))
	infoBranchRunBD(t, bd, dir, env, "dolt", "push")
	if lsRemoteRef(t, remoteDir, defaultInfoBranchRef) == "" {
		t.Fatalf("push with %s=%s did not publish %s on the git remote",
			blobstore.InfoBranchEnvVar, blobstore.DefaultInfoBranch, defaultInfoBranchRef)
	}
	// The user's clone now sees origin/__dolt_remote_info__, as in the report.
	resetDataRunGit(t, dir, "fetch", "origin")

	// Ordinary writes, then a batch of six failed creates.
	infoBranchRunBD(t, bd, dir, env, "export", "-o", filepath.Join(t.TempDir(), "out.jsonl"))
	infoBranchRunBD(t, bd, dir, env, "create", "second issue", "--type", "task")
	infoBranchRunBD(t, bd, dir, env, "update", seed.ID, "--status", "in_progress")
	for i := 0; i < 6; i++ {
		cmd := exec.Command(bd, "create", "child", "--parent", "ri-doesnotexist")
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("create with missing parent unexpectedly succeeded:\n%s", out)
		}
	}

	if got := strings.TrimSpace(resetDataRunGit(t, dir, "branch", "--show-current")); got != "work" {
		t.Errorf("HEAD moved: current branch = %q, want %q", got, "work")
	}
	if _, err := os.Stat(filepath.Join(dir, "tracked.txt")); err != nil {
		t.Errorf("tracked file stripped from working tree: %v", err)
	}
	if reflog := resetDataRunGit(t, dir, "reflog", "--format=%gs"); strings.Contains(reflog, blobstore.DefaultInfoBranch) {
		t.Errorf("HEAD reflog records a checkout of %s:\n%s", blobstore.DefaultInfoBranch, reflog)
	}
	if out := resetDataRunGit(t, dir, "for-each-ref", defaultInfoBranchRef); strings.TrimSpace(out) != "" {
		t.Errorf("local branch %s was created in the working repo: %s", defaultInfoBranchRef, out)
	}
}
