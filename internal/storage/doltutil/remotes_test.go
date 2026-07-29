package doltutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"has space", "'has space'"},
		{"semi;colon", "'semi;colon'"},
		{"pipe|char", "'pipe|char'"},
		{"$(cmd)", "'$(cmd)'"},
		{"`cmd`", "'`cmd`'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := ShellQuote(tt.input); got != tt.want {
				t.Errorf("ShellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsSSHURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		// SSH URLs
		{"git+ssh://git@github.com/org/repo.git", true},
		{"ssh://git@github.com/org/repo.git", true},
		{"git@github.com:org/repo.git", true},
		{"git+ssh://github.com/org/repo", true},
		{"ssh://user@host:2222/path", true},
		{"git@bitbucket.org:team/repo.git", true},

		// Non-SSH URLs
		{"https://dolthub.com/org/repo", false},
		{"http://localhost:50051/repo", false},
		{"aws://[table:bucket]/db", false},
		{"gs://bucket/db", false},
		{"file:///local/path", false},
		{"/absolute/local/path", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := IsSSHURL(tt.url); got != tt.want {
				t.Errorf("IsSSHURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestIsGitProtocolURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		// SSH URLs (subset of git protocol)
		{"git+ssh://git@github.com/org/repo.git", true},
		{"ssh://git@github.com/org/repo.git", true},
		{"git@github.com:org/repo.git", true},

		// Git-over-HTTPS (the bug: these were not detected before)
		{"git+https://github.com/user/repo.git", true},
		{"git+https://github.com/org/private-repo.git", true},

		// Git-over-HTTP
		{"git+http://localhost:3000/user/repo.git", true},

		// Local git transport
		{"git+file:///tmp/repo.git", true},

		// Plain git protocol
		{"git://github.com/org/repo.git", true},

		// Non-git-protocol URLs (native Dolt remotes — fast, no CLI needed)
		{"https://dolthub.com/org/repo", false},
		{"https://doltremoteapi.dolthub.com/org/repo", false},
		{"http://localhost:50051/repo", false},
		{"aws://[table:bucket]/db", false},
		{"gs://bucket/db", false},
		{"file:///local/path", false},
		{"/absolute/local/path", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := IsGitProtocolURL(tt.url); got != tt.want {
				t.Errorf("IsGitProtocolURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestRemoteURLsMatchNormalizesGitURLs(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
		ok   bool
	}{
		{"exact", "git+https://github.com/org/repo.git", "git+https://github.com/org/repo.git", true},
		{"https normalizes", "https://github.com/org/repo.git", "git+https://github.com/org/repo.git", true},
		{"ssh normalizes", "ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git", true},
		{"scp normalizes", "git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git", true},
		{"file native and git transport differ", "git+file:///tmp/repo.git", "file:///tmp/repo.git", false},
		{"file native and git transport differ reversed", "file:///tmp/repo.git", "git+file:///tmp/repo.git", false},
		{"unknown git prefix differs", "git+foo://host/repo", "foo://host/repo", false},
		{"different", "git+https://github.com/org/other.git", "git+https://github.com/org/repo.git", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RemoteURLsMatch(tt.got, tt.want); got != tt.ok {
				t.Errorf("RemoteURLsMatch(%q, %q) = %v, want %v", tt.got, tt.want, got, tt.ok)
			}
		})
	}
}

// listCLIRemotesTimeout must pick the aggressive 2s cap only for a directory
// that lacks .dolt/repo_state.json (the known broken multi-DB server-root
// case) and a generous 30s cap for anything that looks like a real Dolt
// repo, so a slow-but-valid `dolt remote -v` is never SIGKILLed and
// misread as "remote absent" by callers like FindCLIRemote (review
// should-fix, 2026-07-24).
func TestListCLIRemotesTimeout(t *testing.T) {
	t.Run("missing .dolt directory entirely uses the broken-root cap", func(t *testing.T) {
		if got := listCLIRemotesTimeout(t.TempDir()); got != listCLIRemotesTimeoutBroken {
			t.Errorf("listCLIRemotesTimeout = %v, want %v", got, listCLIRemotesTimeoutBroken)
		}
	})

	t.Run("has .dolt but no repo_state.json uses the broken-root cap", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := listCLIRemotesTimeout(dir); got != listCLIRemotesTimeoutBroken {
			t.Errorf("listCLIRemotesTimeout = %v, want %v", got, listCLIRemotesTimeoutBroken)
		}
	})

	t.Run("has repo_state.json uses the generous healthy-repo cap", func(t *testing.T) {
		dir := t.TempDir()
		doltDir := filepath.Join(dir, ".dolt")
		if err := os.MkdirAll(doltDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(doltDir, "repo_state.json"), []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := listCLIRemotesTimeout(dir); got != listCLIRemotesTimeoutHealthy {
			t.Errorf("listCLIRemotesTimeout = %v, want %v", got, listCLIRemotesTimeoutHealthy)
		}
	})
}
