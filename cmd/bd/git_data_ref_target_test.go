package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// targetTestGit runs git in dir with a repository-local hooks path, so a
// developer's global core.hooksPath cannot reach the fixture.
func targetTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=.git/hooks",
		"GIT_AUTHOR_NAME=bd-test", "GIT_AUTHOR_EMAIL=bd-test@example.com",
		"GIT_COMMITTER_NAME=bd-test", "GIT_COMMITTER_EMAIL=bd-test@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bareRepoWithMain is a bare repository whose default branch main holds one
// commit, the shape of a code repository a data ref could collide with.
func bareRepoWithMain(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "code.git")
	targetTestGit(t, base, "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(base, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	targetTestGit(t, seed, "init", "-b", "main")
	targetTestGit(t, seed, "commit", "--allow-empty", "-m", "init")
	targetTestGit(t, seed, "push", bare, "main")
	return bare
}

// The probe reads the remote's default branch and whether the ref exists.
func TestProbeGitDataRefTarget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bare := bareRepoWithMain(t)
	url := "git+file://" + bare

	got, err := probeGitDataRefTarget(url, "refs/heads/main")
	if err != nil || !got.Exists || !got.IsHead {
		t.Fatalf("main: %+v, %v; want existing default branch", got, err)
	}
	got, err = probeGitDataRefTarget(url, "refs/heads/issue-data")
	if err != nil || got.Exists || got.IsHead {
		t.Fatalf("absent branch: %+v, %v", got, err)
	}
	targetTestGit(t, bare, "branch", "issue-data", "main")
	got, err = probeGitDataRefTarget(url, "refs/heads/issue-data")
	if err != nil || !got.Exists || got.IsHead {
		t.Fatalf("existing branch: %+v, %v; want existing, not the default", got, err)
	}
	got, err = probeGitDataRefTarget(url, "refs/dolt/units/team-12542")
	if err != nil || got.Exists {
		t.Fatalf("absent custom ref: %+v, %v", got, err)
	}
	if _, err := probeGitDataRefTarget("git+file://"+filepath.Join(t.TempDir(), "missing.git"), "refs/heads/x"); err == nil {
		t.Fatal("an unreachable repository must be an error, not a clean probe")
	}
}

// The guard: the default branch is refused, an existing branch or tag needs
// yes (or a prompt, which a missing terminal turns into a refusal naming
// --yes), an absent ref and a ref outside refs/heads and refs/tags pass
// without a prompt, and a non-git URL is never probed.
func TestGuardGitDataRefTarget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	bare := bareRepoWithMain(t)
	targetTestGit(t, bare, "branch", "issue-data", "main")
	targetTestGit(t, bare, "tag", "v1", "main")
	url := "git+file://" + bare

	prev := remoteAddStdinIsTerminal
	remoteAddStdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { remoteAddStdinIsTerminal = prev })
	noPrompt := func(string) bool { t.Fatal("prompt must not be shown here"); return false }

	if _, err := guardGitDataRefTarget(url, "refs/heads/main", true, noPrompt); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("default branch must be refused even with yes: %v", err)
	}
	for _, ref := range []string{"refs/heads/issue-data", "refs/tags/v1"} {
		if _, err := guardGitDataRefTarget(url, ref, false, noPrompt); err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("%s exists: without a terminal and without yes it must be refused naming --yes: %v", ref, err)
		}
		if canceled, err := guardGitDataRefTarget(url, ref, true, noPrompt); err != nil || canceled {
			t.Fatalf("%s exists with yes: %v %v", ref, canceled, err)
		}
	}
	for _, ref := range []string{"refs/heads/beads-data", "refs/dolt/units/team-12542", ""} {
		if canceled, err := guardGitDataRefTarget(url, ref, false, noPrompt); err != nil || canceled {
			t.Fatalf("%q: %v %v", ref, canceled, err)
		}
	}
	// With a terminal, the prompt decides.
	remoteAddStdinIsTerminal = func() bool { return true }
	if canceled, err := guardGitDataRefTarget(url, "refs/heads/issue-data", false, func(string) bool { return false }); err != nil || !canceled {
		t.Fatalf("declined prompt: canceled=%v err=%v", canceled, err)
	}
	if canceled, err := guardGitDataRefTarget(url, "refs/heads/issue-data", false, func(string) bool { return true }); err != nil || canceled {
		t.Fatalf("accepted prompt: canceled=%v err=%v", canceled, err)
	}
	// A non-git URL is not probed at all (an unreachable one would error).
	if canceled, err := guardGitDataRefTarget("dolthub://org/repo", "refs/heads/main", false, noPrompt); err != nil || canceled {
		t.Fatalf("non-git URL: %v %v", canceled, err)
	}
}

// ensureDoltRemoteGuarded runs the guard only when a remote is created or
// replaced, never for a re-add that changes nothing.
func TestEnsureDoltRemoteGuardedRunsGuardOnWritesOnly(t *testing.T) {
	ctx := t.Context()
	calls := 0
	guard := func() (bool, error) { calls++; return false, nil }
	st := &fakeDoltRemoteAddStore{}
	const url = "git+file:///srv/ledgers.git"

	if _, err := ensureDoltRemoteGuarded(ctx, st, "origin", url, "refs/heads/issue-data", true, nil, guard); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("create: guard ran %d times, want 1", calls)
	}
	if _, err := ensureDoltRemoteGuarded(ctx, st, "origin", url, "refs/heads/issue-data", true, nil, guard); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("unchanged re-add: guard ran %d times, want still 1", calls)
	}
	if _, err := ensureDoltRemoteGuarded(ctx, st, "origin", url, "refs/heads/other", true, nil, guard); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("replace: guard ran %d times, want 2", calls)
	}
	refusing := func() (bool, error) { return false, os.ErrPermission }
	if _, err := ensureDoltRemoteGuarded(ctx, st, "backup", url, "refs/heads/main", true, nil, refusing); err == nil {
		t.Fatal("a refusing guard must stop the add")
	}
	if _, found := findDoltRemote(st.remotes, "backup"); found {
		t.Fatal("a refused add must not create the remote")
	}
	canceling := func() (bool, error) { return true, nil }
	res, err := ensureDoltRemoteGuarded(ctx, st, "backup", url, "refs/heads/x", false, nil, canceling)
	if err != nil || !res.Canceled {
		t.Fatalf("canceled guard: %+v %v", res, err)
	}
}
