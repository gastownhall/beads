//go:build cgo

package main

import (
	"os"
	"os/exec"
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
