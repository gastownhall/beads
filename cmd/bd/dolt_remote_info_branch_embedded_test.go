//go:build cgo

package main

import (
	"os"
	"os/exec"
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
