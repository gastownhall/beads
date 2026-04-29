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

// TestCommitBeadsConfigBypassesHooks verifies that the auto-commit
// fired by bd dolt remote add (and any other caller of commitBeadsConfig)
// runs `git commit --no-verify`, so a bd-installed pre-commit hook can't
// re-enter `bd export` and deadlock on the embedded Dolt lock the parent
// is still holding. Mirrors the PR #3457 fix at the bd init bootstrap
// commit site. See GH#3598.
func TestCommitBeadsConfigBypassesHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell hook not portable to Windows")
	}

	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
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

	preCommitPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	preCommit := "#!/bin/sh\necho hook-fired >> .hook-ran\nexit 1\n"
	if err := os.WriteFile(preCommitPath, []byte(preCommit), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(dir)

	commitBeadsConfig("bd: update sync.remote")

	if _, err := os.Stat(filepath.Join(dir, ".hook-ran")); err == nil {
		t.Fatal("expected commitBeadsConfig to bypass git pre-commit hook (GH#3598)")
	}

	logCmd := exec.Command("git", "log", "--oneline", "-n", "1")
	logCmd.Dir = dir
	logOut, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log failed: %v\n%s", err, logOut)
	}
	if !strings.Contains(string(logOut), "bd: update sync.remote") {
		t.Fatalf("expected commit to succeed despite hostile hook, got log: %s", logOut)
	}
}
