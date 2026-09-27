package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/gitenv"
)

func TestGetIdentityIgnoresInheritedGitRouting(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	t.Setenv("BEADS_IDENTITY", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = home-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, decoy := t.TempDir(), t.TempDir()
	for _, repo := range []string{target, decoy} {
		runConfigProbeGit(t, repo, "init", "--quiet")
	}
	runConfigProbeGit(t, target, "config", "--local", "user.name", "target-user")
	runConfigProbeGit(t, decoy, "config", "--local", "user.name", "decoy-user")
	t.Chdir(target)
	if err := Initialize(); err != nil {
		t.Fatal(err)
	}
	Set("identity", "")
	poisonConfig := filepath.Join(t.TempDir(), "poison.gitconfig")
	if err := os.WriteFile(poisonConfig, []byte("[user]\n\tname = injected-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		env        map[string]string
		clearLocal bool
		want       string
	}{
		{name: "repository", env: map[string]string{"GIT_DIR": filepath.Join(decoy, ".git")}, want: "target-user"},
		{name: "inline config", env: map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "user.name", "GIT_CONFIG_VALUE_0": "injected-user"}, want: "target-user"},
		{name: "global config", env: map[string]string{"GIT_CONFIG_GLOBAL": poisonConfig}, clearLocal: true, want: "home-user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.clearLocal {
				runConfigProbeGit(t, target, "config", "--local", "--unset", "user.name")
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if got := GetIdentity(""); got != tc.want {
				t.Errorf("GetIdentity() = %q, want %q from the intended config", got, tc.want)
			}
			Set("identity", "configured-user")
			t.Cleanup(func() { Set("identity", "") })
			if got := GetIdentity("flag-user"); got != "flag-user" {
				t.Errorf("flag identity = %q", got)
			}
			if got := GetIdentity(""); got != "configured-user" {
				t.Errorf("configured identity = %q", got)
			}
		})
	}
	t.Run("missing Git retains hostname fallback", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		want, err := os.Hostname()
		if err != nil || want == "" {
			t.Fatalf("hostname fixture: %q, %v", want, err)
		}
		if got := GetIdentity(""); got != want {
			t.Errorf("hostname identity = %q, want %q", got, want)
		}
	})
}

func TestSecretGitTrackingIgnoresInheritedGitRouting(t *testing.T) {
	target, decoy := t.TempDir(), t.TempDir()
	for _, repo := range []string{target, decoy} {
		runConfigProbeGit(t, repo, "init", "--quiet")
	}
	tracked := filepath.Join(target, "config.yaml")
	untracked := filepath.Join(target, "untracked.yaml")
	for _, path := range []string{tracked, untracked} {
		if err := os.WriteFile(path, []byte("json: false\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runConfigProbeGit(t, target, "add", "--", tracked)
	for _, tc := range []struct {
		name string
		env  map[string]string
	}{
		{name: "repository", env: map[string]string{"GIT_DIR": filepath.Join(decoy, ".git")}},
		{name: "index", env: map[string]string{"GIT_INDEX_FILE": filepath.Join(decoy, "missing-index")}},
		{name: "config", env: map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.worktree", "GIT_CONFIG_VALUE_0": decoy}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if !isGitTracked(tracked) {
				t.Error("routing hid the tracked file in the intended repository")
			}
			if isGitTracked(untracked) {
				t.Error("untracked file reported as tracked")
			}
			if err := checkSecretGitTracked(tracked, "linear.api_key"); err == nil || !strings.Contains(err.Error(), "refusing to write secret key") {
				t.Errorf("tracked secret refusal = %v", err)
			}
			if err := checkSecretGitTracked(untracked, "linear.api_key"); err != nil {
				t.Errorf("untracked secret refused: %v", err)
			}
			if err := checkSecretGitTracked(tracked, "no-db"); err != nil {
				t.Errorf("non-secret key refused: %v", err)
			}
		})
	}

	// The scrub alone narrows the probe to upward discovery from the config
	// file's directory. A detached work tree — GIT_DIR plus GIT_WORK_TREE with
	// no in-tree .git beside the config — is reachable only through the
	// inherited context, so without the inherited fallback the guard reports
	// "untracked" and writes the secret into a tracked file.
	t.Run("legitimate routing", func(t *testing.T) {
		gitDir := filepath.Join(t.TempDir(), "repo.git")
		if err := os.MkdirAll(gitDir, 0o750); err != nil {
			t.Fatal(err)
		}
		runConfigProbeGit(t, gitDir, "init", "--bare", "--quiet")
		work := t.TempDir()
		beadsDir := filepath.Join(work, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatal(err)
		}
		detachedTracked := filepath.Join(beadsDir, "config.yaml")
		detachedUntracked := filepath.Join(beadsDir, "untracked.yaml")
		for _, path := range []string{detachedTracked, detachedUntracked} {
			if err := os.WriteFile(path, []byte("json: false\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		runConfigProbeGit(t, work, "--git-dir", gitDir, "--work-tree", work, "add", "--", detachedTracked)

		t.Setenv("GIT_DIR", gitDir)
		t.Setenv("GIT_WORK_TREE", work)
		// Guard the fixture: the scrubbed probe must not see this repository at
		// all, or the fallback below would be satisfied by discovery instead.
		if isGitTrackedWithEnv(detachedTracked, gitenv.ScrubRouting(os.Environ())) {
			t.Fatal("scrubbed discovery reached the detached work tree; fixture cannot exercise the fallback")
		}
		if !isGitTracked(detachedTracked) {
			t.Error("legitimate routing lost the tracked file; the secret guard would write into it")
		}
		if isGitTracked(detachedUntracked) {
			t.Error("untracked file reported as tracked")
		}
		if err := checkSecretGitTracked(detachedTracked, "linear.api_key"); err == nil || !strings.Contains(err.Error(), "refusing to write secret key") {
			t.Errorf("tracked secret refusal = %v", err)
		}
		if err := checkSecretGitTracked(detachedUntracked, "linear.api_key"); err != nil {
			t.Errorf("untracked secret refused: %v", err)
		}
	})
}

// Fixture setup uses the same routing boundary before any per-case poison is set.
func runConfigProbeGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitenv.ScrubRouting(os.Environ())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, output)
	}
}
