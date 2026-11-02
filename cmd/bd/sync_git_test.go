package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestIsBareGitRepo(t *testing.T) {
	t.Run("returns true in bare repository", func(t *testing.T) {
		repoDir := filepath.Join(t.TempDir(), "bare.git")
		runGitForSyncTest(t, "", "init", "--bare", repoDir)

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if !isBareGitRepo() {
			t.Fatal("isBareGitRepo() = false, want true")
		}
	})

	t.Run("returns false in non-bare repository", func(t *testing.T) {
		repoDir := t.TempDir()
		runGitForSyncTest(t, repoDir, "init")

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if isBareGitRepo() {
			t.Fatal("isBareGitRepo() = true, want false")
		}
	})

	t.Run("returns false outside git repository", func(t *testing.T) {
		nonRepoDir := t.TempDir()

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(nonRepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if isBareGitRepo() {
			t.Fatal("isBareGitRepo() = true, want false")
		}
	})
}

func TestGitOriginGetURL(t *testing.T) {
	t.Run("returns origin URL", func(t *testing.T) {
		repoDir := t.TempDir()
		bareDir := filepath.Join(t.TempDir(), "bare.git")
		runGitForSyncTest(t, "", "init", "--bare", bareDir)
		runGitForSyncTest(t, repoDir, "init")
		runGitForSyncTest(t, repoDir, "remote", "add", "origin", bareDir)

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		url, err := gitOriginGetURL()
		if err != nil {
			t.Fatalf("gitOriginGetURL failed: %v", err)
		}
		if url != bareDir {
			t.Errorf("gitOriginGetURL = %q, want %q", url, bareDir)
		}
	})

	t.Run("returns error for nonexistent remote", func(t *testing.T) {
		repoDir := t.TempDir()
		runGitForSyncTest(t, repoDir, "init")

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		_, err = gitOriginGetURL()
		if err == nil {
			t.Fatal("expected error for nonexistent remote")
		}
	})
}

func TestGitOriginHasDoltDataRef(t *testing.T) {
	t.Run("returns false for nonexistent ref", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "bare.git")
		runGitForSyncTest(t, "", "init", "--bare", bareDir)

		repoDir := t.TempDir()
		runGitForSyncTest(t, repoDir, "init")
		runGitForSyncTest(t, repoDir, "remote", "add", "origin", bareDir)

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if gitOriginHasDoltDataRef() {
			t.Fatal("expected false for nonexistent ref")
		}
	})

	t.Run("returns true for existing ref", func(t *testing.T) {
		bareDir := filepath.Join(t.TempDir(), "bare.git")
		runGitForSyncTest(t, "", "init", "--bare", bareDir)

		// Create a repo, commit, push to create refs/dolt/data
		repoDir := t.TempDir()
		runGitForSyncTest(t, repoDir, "init", "-b", "main")
		runGitForSyncTest(t, repoDir, "config", "user.email", "test@test.com")
		runGitForSyncTest(t, repoDir, "config", "user.name", "Test User")
		runGitForSyncTest(t, repoDir, "commit", "--allow-empty", "-m", "init")
		runGitForSyncTest(t, repoDir, "remote", "add", "origin", bareDir)
		runGitForSyncTest(t, repoDir, "push", "origin", "HEAD:refs/dolt/data")

		oldWd, err := os.Getwd()
		if err != nil {
			t.Fatalf("failed to get cwd: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()
		if err := os.Chdir(repoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}

		if !gitOriginHasDoltDataRef() {
			t.Fatal("expected true for existing ref")
		}
	})
}

func TestGitURLToDoltRemote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"http://github.com/org/repo.git", "git+http://github.com/org/repo.git"},
		{"ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"git+https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"git+ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"C:/Users/alice/repos/beads.git", "git+C:/Users/alice/repos/beads.git"},
		{`D:\repos\beads.git`, `git+D:\repos\beads.git`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := gitURLToDoltRemote(tt.input)
			if got != tt.want {
				t.Errorf("gitURLToDoltRemote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func runGitForSyncTest(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := gitCommand(args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
}

// TestGitRemoteHasDoltDataRefStatusSurfacesGitStderr proves the probe failure a
// user actually sees carries git's own diagnosis. exec.ExitError.Error() is
// only "exit status 128"; git's complaint sits unread in ExitError.Stderr, and
// bootstrap's blocked message is built from this error (#5743).
func TestGitRemoteHasDoltDataRefStatusSurfacesGitStderr(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-a-repo")

	hasData, err := gitRemoteHasDoltDataRefStatus(missing)

	if err == nil {
		t.Fatalf("probe of %q returned hasData=%v and no error; want a failure", missing, hasData)
	}
	msg := err.Error()
	if !strings.Contains(msg, "ls-remote") {
		t.Errorf("error %q does not name the command that failed", msg)
	}
	// Whatever git's exact wording, the message must be more than the bare
	// exit status — that is the whole point of reading ExitError.Stderr.
	if msg == "git ls-remote: exit status 128" {
		t.Errorf("error %q carries no stderr detail; the ExitError.Stderr thread-through regressed", msg)
	}
	if !strings.Contains(msg, "repository") && !strings.Contains(msg, "fatal") {
		t.Errorf("error %q does not look like it carries git's stderr", msg)
	}
}

// The probe is advisory and must not invoke inherited credential prompts.
func TestGitRemoteHasDoltDataRefStatusDisablesAskpass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires a POSIX shell")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$GIT_TERMINAL_PROMPT" != 0 ] || [ -n "$GIT_ASKPASS" ] || [ -n "$SSH_ASKPASS" ]; then
    echo 'fatal: inherited credential prompt is enabled' >&2
    exit 128
fi
printf '0123456789012345678901234567890123456789\trefs/dolt/data\n'
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_ASKPASS", "/inherited/git-askpass")
	t.Setenv("SSH_ASKPASS", "/inherited/ssh-askpass")

	hasData, err := gitRemoteHasDoltDataRefStatus("origin")
	if err != nil || !hasData {
		t.Fatalf("noninteractive probe = (%v, %v), want (true, nil)", hasData, err)
	}
}

// A successful git process can leave a descendant holding its output pipes.
// Incomplete output must remain UNKNOWN, even when stdout contains the ref.
func TestGitRemoteHasDoltDataRefStatusRetainedPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture requires a POSIX shell")
	}
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	script := `#!/bin/sh
/bin/sleep 6 &
printf '%s\n' "$!" > "$PROBE_CHILD_PID"
printf '0123456789012345678901234567890123456789\trefs/dolt/data\n'
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PROBE_CHILD_PID", pidPath)
	t.Cleanup(func() {
		data, err := os.ReadFile(pidPath)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			if child, err := os.FindProcess(pid); err == nil {
				_ = child.Kill()
			}
		}
	})

	hasData, err := gitRemoteHasDoltDataRefStatus("origin")
	if hasData || !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("probe with retained output pipes = (%v, %v), want UNKNOWN with ErrWaitDelay", hasData, err)
	}
}
