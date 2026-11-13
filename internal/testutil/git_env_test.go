package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// wrapperGitEnv is what tools/bazel/test_env.sh and scripts/ci/lib/test-env.sh
// export for every test. The location scrub must leave all of it in place.
var wrapperGitEnv = []string{
	"GIT_CEILING_DIRECTORIES=/sandbox/srcdir:/sandbox/tmp",
	"GIT_CONFIG_GLOBAL=/sandbox/gitconfig",
	"GIT_CONFIG_NOSYSTEM=1",
}

func TestWithoutGitRepoLocationEnvKeepsWrapperGitControls(t *testing.T) {
	env := append([]string{
		"GIT_DIR=/host/.git/worktrees/wt",
		"GIT_WORK_TREE=/host",
		"GIT_COMMON_DIR=/host/.git",
		"GIT_INDEX_FILE=/host/.git/worktrees/wt/index",
		"GIT_OBJECT_DIRECTORY=/host/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/host/.git/objects",
		"GIT_NAMESPACE=host",
		"GIT_CONFIG=/host/.git/config",
		"HOME=/sandbox/home",
	}, wrapperGitEnv...)

	got := WithoutGitRepoLocationEnv(env)
	want := append([]string{"HOME=/sandbox/home"}, wrapperGitEnv...)
	if !slices.Equal(got, want) {
		t.Fatalf("WithoutGitRepoLocationEnv:\n got %q\nwant %q", got, want)
	}
}

func TestUnsetGitRepoLocationEnvKeepsWrapperGitControls(t *testing.T) {
	for _, key := range gitRepoLocationKeys {
		t.Setenv(key, "/host/"+key)
	}
	for _, entry := range wrapperGitEnv {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}

	if err := UnsetGitRepoLocationEnv(); err != nil {
		t.Fatal(err)
	}
	for _, key := range gitRepoLocationKeys {
		if value, ok := os.LookupEnv(key); ok {
			t.Errorf("%s=%q survived UnsetGitRepoLocationEnv", key, value)
		}
	}
	for _, entry := range wrapperGitEnv {
		key, want, _ := strings.Cut(entry, "=")
		if got := os.Getenv(key); got != want {
			t.Errorf("%s=%q after UnsetGitRepoLocationEnv, want %q kept", key, got, want)
		}
	}
}

// TestForceRepoLocalHooksPathIgnoresLeakedGitDir pins the write target: with
// GIT_DIR and GIT_WORK_TREE naming another repository, or GIT_CONFIG naming
// another config file, the hooksPath write must still land in repoDir and leave
// the other config untouched.
func TestForceRepoLocalHooksPathIgnoresLeakedGitDir(t *testing.T) {
	decoy, target := t.TempDir(), t.TempDir()
	for _, dir := range []string{decoy, target} {
		cmd := exec.Command("git", "init", "--quiet", dir)
		cmd.Env = WithoutGitRepoLocationEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v\n%s", dir, err, out)
		}
	}
	decoyConfig := filepath.Join(decoy, ".git", "config")
	before, err := os.ReadFile(decoyConfig)
	if err != nil {
		t.Fatal(err)
	}
	decoyFile := filepath.Join(t.TempDir(), "leaked.gitconfig")
	const decoyFileBefore = "[user]\n\tname = decoy\n"
	if err := os.WriteFile(decoyFile, []byte(decoyFileBefore), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)
	t.Setenv("GIT_CONFIG", decoyFile)
	if err := ForceRepoLocalHooksPath(target); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(decoyConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("leaked GIT_DIR routed the hooksPath write into %s:\n%s", decoyConfig, after)
	}
	if got, err := os.ReadFile(decoyFile); err != nil || string(got) != decoyFileBefore {
		t.Fatalf("leaked GIT_CONFIG routed the hooksPath write into %s (err %v):\n%s", decoyFile, err, got)
	}
	get := exec.Command("git", "config", "--file", filepath.Join(target, ".git", "config"), "--get", "core.hooksPath")
	out, err := get.CombinedOutput()
	if err != nil {
		t.Fatalf("read target core.hooksPath: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != ".git/hooks" {
		t.Fatalf("target core.hooksPath=%q, want %q", got, ".git/hooks")
	}
}
