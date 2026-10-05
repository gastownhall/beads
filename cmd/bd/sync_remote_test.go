package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	internalbeads "github.com/steveyegge/beads/internal/beads"
	internalgit "github.com/steveyegge/beads/internal/git"
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
		{"C:/Users/alice/repos/beads.git", "git+C:/Users/alice/repos/beads.git"},
		{`D:\repos\beads.git`, `git+D:\repos\beads.git`},

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

func TestCommitBeadsConfigSkipsGitHooks(t *testing.T) {
	repo := t.TempDir()

	// Pin bd's repo resolution to this temp repo. commitBeadsConfig resolves the
	// target repo via GetRepoContext -> FindBeadsDir, which walks UP the
	// directory tree. When the test runs from a checkout nested inside another
	// beads repo (e.g. a verification worktree under the coordination repo),
	// FindBeadsDir escapes the temp repo and resolves to the OUTER repo's
	// .beads; commitBeadsConfig then commits against the wrong repository and
	// fails (observed as `git commit` exit 128 and "branch has no commits yet").
	// os.Chdir below is not enough because resolution is not purely CWD-based.
	// BEADS_DIR pins it deterministically regardless of where the test runs.
	t.Setenv("BEADS_DIR", filepath.Join(repo, ".beads"))

	// Isolate git config and pin the initial branch so the commit never depends
	// on the developer's ~/.gitconfig or the ambient init.defaultBranch.
	gitHome := t.TempDir()
	t.Setenv("HOME", gitHome)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(gitHome, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	runGitForCommitConfigTest(t, repo, "init", "--initial-branch=main")
	runGitForCommitConfigTest(t, repo, "config", "user.email", "test@example.com")
	runGitForCommitConfigTest(t, repo, "config", "user.name", "Test User")

	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("sync:\n  remote: git+https://example.com/repo.git\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	hooksDir := filepath.Join(repo, ".git", "hooks")
	hookMarker := filepath.Join(repo, "hook-ran")
	hook := "#!/bin/sh\n" +
		"echo hook-ran > " + shellQuoteForTest(hookMarker) + "\n" +
		"exit 42\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	internalbeads.ResetCaches()
	internalgit.ResetCaches()
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
		internalbeads.ResetCaches()
		internalgit.ResetCaches()
	})

	commitBeadsConfig("bd: update sync.remote")

	if _, err := os.Stat(hookMarker); !os.IsNotExist(err) {
		t.Fatalf("pre-commit hook ran during internal config commit")
	}
	out := runGitForCommitConfigTest(t, repo, "log", "-1", "--format=%s")
	if got := strings.TrimSpace(out); got != "bd: update sync.remote" {
		t.Fatalf("commit subject = %q, want %q", got, "bd: update sync.remote")
	}
}

func runGitForCommitConfigTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// TestRedactRemoteURL pins the credential scrubbing applied to every remote URL
// bd echoes into an error, a hint, a log line or JSON. CI commonly configures
// sync.remote as https://x-access-token:<token>@github.com/org/repo.git.
func TestRedactRemoteURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "https token is dropped whole",
			in:   "https://x-access-token:ghp_secret@github.com/org/repo.git",
			want: "https://github.com/org/repo.git",
		},
		{
			name: "git+https token is dropped whole",
			in:   "git+https://user:pass@github.com/org/repo.git",
			want: "git+https://github.com/org/repo.git",
		},
		{
			name: "http basic auth is dropped whole",
			in:   "http://user:pass@myserver:7007/mydb",
			want: "http://myserver:7007/mydb",
		},
		{
			// The conventional SSH account selector is not a secret, and the
			// ls-remote hint is useless without it.
			name: "ssh user is kept",
			in:   "git+ssh://git@github.com/org/repo.git",
			want: "git+ssh://git@github.com/org/repo.git",
		},
		{
			name: "ssh password is dropped, user kept",
			in:   "ssh://alice:hunter2@my-dolt.example.com/org/db",
			want: "ssh://alice@my-dolt.example.com/org/db",
		},
		{
			name: "scp style is untouched",
			in:   "git@github.com:org/repo.git",
			want: "git@github.com:org/repo.git",
		},
		{name: "no userinfo", in: "https://github.com/org/repo.git", want: "https://github.com/org/repo.git"},
		{name: "dolt native", in: "dolthub://myorg/mydb", want: "dolthub://myorg/mydb"},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactRemoteURL(tt.in); got != tt.want {
				t.Errorf("redactRemoteURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestScrubURLCredentials covers the redaction of credential-bearing URLs that
// arrive embedded in free-form text — most importantly a `git ls-remote` stderr
// line that echoes the tokenized remote. Credential-free diagnostics must pass
// through untouched so the probe error keeps git's actual complaint (#5743).
func TestScrubURLCredentials(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			// The contingent leak Finding 2 guards: a git build that echoes the
			// full tokenized URL. The token must go; host and path must stay.
			name: "tokenized git stderr is scrubbed, host and path kept",
			in:   "fatal: unable to access 'https://x-access-token:ghp_TESTTOKEN@github.com/org/repo.git/': The requested URL returned error: 403",
			want: "fatal: unable to access 'https://github.com/org/repo.git/': The requested URL returned error: 403",
		},
		{
			name: "ssh password embedded in text is dropped, user kept",
			in:   "note: not cloning ssh://alice:hunter2@dolt.example.com/org/db right now",
			want: "note: not cloning ssh://alice@dolt.example.com/org/db right now",
		},
		{
			name: "two credential URLs in one line are both scrubbed",
			in:   "https://u:p@a.example.com/x and https://v:q@b.example.com/y",
			want: "https://a.example.com/x and https://b.example.com/y",
		},
		{
			// The common case: modern git already prints host-only, and DNS/repo
			// errors carry no userinfo. Nothing must be altered.
			name: "credential-free git stderr is unchanged",
			in:   "fatal: repository 'https://github.com/org/repo.git/' not found",
			want: "fatal: repository 'https://github.com/org/repo.git/' not found",
		},
		{
			name: "plain diagnostic without a URL is unchanged",
			in:   "fatal: Could not read from remote repository.",
			want: "fatal: Could not read from remote repository.",
		},
		{name: "empty", in: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scrubURLCredentials(tt.in); got != tt.want {
				t.Errorf("scrubURLCredentials(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
