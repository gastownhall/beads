package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeRemoteURL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Dolt-native schemes — returned as-is
		{"dolthub://myorg/beads", "dolthub://myorg/beads"},
		{"file:///tmp/doltdb", "file:///tmp/doltdb"},
		{"aws://[dolt-table:us-east-1]/mydb", "aws://[dolt-table:us-east-1]/mydb"},
		{"gs://my-bucket/mydb", "gs://my-bucket/mydb"},
		{"git+https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"git+ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"git+http://example.com/repo.git", "git+http://example.com/repo.git"},

		// Git URLs — converted to dolt remote format
		{"https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"http://github.com/org/repo.git", "git+http://github.com/org/repo.git"},
		{"ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git"},

		// Dolt remotesapi URLs — also converted (callers that need
		// pass-through for user-provided URLs should skip normalization)
		{"http://myserver:7007/mydb", "git+http://myserver:7007/mydb"},
		{"https://doltremoteapi.example.com/mydb", "git+https://doltremoteapi.example.com/mydb"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeRemoteURL(tt.input)
			if got != tt.want {
				t.Errorf("normalizeRemoteURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// setupHostileHookRepo creates a git repo at a fresh temp dir with
// .beads/config.yaml staged and the named hook scripts installed under
// .git/hooks. Each hook writes its name to a marker file (".<name>-ran")
// and exits 1, so a successful commit proves the hook was bypassed.
// Returns the repo dir.
func setupHostileHookRepo(t *testing.T, hooks ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		// Disable any signing the user may have inherited from their
		// global config — --no-verify does NOT bypass commit.gpgSign,
		// and a missing key would surface as a generic commit failure.
		{"config", "commit.gpgSign", "false"},
		{"config", "tag.gpgSign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("sync.remote: \"file:///tmp/x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, hook := range hooks {
		path := filepath.Join(dir, ".git", "hooks", hook)
		body := "#!/bin/sh\necho fired >> ." + hook + "-ran\nexit 1\n"
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestCommitBeadsConfigBypassesHooks verifies that the auto-commit
// fired by bd dolt remote add (and any other caller of commitBeadsConfig)
// runs `git commit --no-verify`, so neither a bd-installed pre-commit hook
// nor a commit-msg hook can re-enter `bd export` and deadlock on the
// embedded Dolt lock the parent is still holding. Mirrors the PR #3457
// fix at the bd init bootstrap commit site. See GH#3598.
//
// Covers both hook types because `--no-verify` is documented to bypass
// pre-commit AND commit-msg; pinning both prevents a future change from
// silently regressing one while leaving the other guarded.
func TestCommitBeadsConfigBypassesHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell hook not portable to Windows")
	}

	dir := setupHostileHookRepo(t, "pre-commit", "commit-msg")
	t.Chdir(dir)

	commitBeadsConfig("bd: update sync.remote")

	for _, hook := range []string{"pre-commit", "commit-msg"} {
		marker := filepath.Join(dir, "."+hook+"-ran")
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("expected commitBeadsConfig to bypass %s hook (GH#3598)", hook)
		}
	}

	logCmd := exec.Command("git", "log", "--oneline", "-n", "1")
	logCmd.Dir = dir
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log failed: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "bd: update sync.remote") {
		t.Fatalf("expected commit to succeed despite hostile hooks, got log: %s", logOut)
	}
}

// TestCommitBeadsConfigIsIdempotent verifies that calling
// commitBeadsConfig a second time with no further changes is a silent
// no-op (the "nothing to commit" branch). bd dolt remote add can be
// re-invoked with the same URL; a noisy warning each time would be a
// UX regression and a flaky-CI hazard.
func TestCommitBeadsConfigIsIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell hook not portable to Windows")
	}

	dir := setupHostileHookRepo(t)
	t.Chdir(dir)

	commitBeadsConfig("bd: update sync.remote")
	commitBeadsConfig("bd: update sync.remote") // second call: nothing to commit

	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = dir
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log failed: %v\n%s", err, logOut)
	}
	commits := strings.Count(strings.TrimSpace(string(logOut)), "\n") + 1
	if commits != 1 {
		t.Fatalf("expected exactly 1 commit after two calls, got %d:\n%s", commits, logOut)
	}
}
