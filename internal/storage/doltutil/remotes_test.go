package doltutil

import (
	"os"
	"path/filepath"
	"testing"
)

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

// TestIsGitSyncRemote pins the default git-backed sync remote: named origin,
// with a stored URL that Dolt serves through its git remote factory
// (GH#6150).
func TestIsGitSyncRemote(t *testing.T) {
	tests := []struct {
		name, url string
		want      bool
	}{
		{"origin", "git+https://github.com/org/repo.git", true},
		{"origin", "git+https://github.com/org/repo", true},
		{"origin", "git+ssh://git@github.com/org/repo.git", true},
		{"origin", "git+ssh://git@github.com/./org/repo.git", true},
		{"origin", "git+http://127.0.0.1:3000/repo.git", true},
		{"origin", "git+file:///tmp/repo.git", true},

		// Named origin but stored under a transport that is not git: a
		// remotesapi server, Dolt's own ssh transfer, a native file store,
		// no git:// transport at all, and an scp-style address Dolt cannot
		// parse.
		{"origin", "https://github.com/org/repo.git", false},
		{"origin", "http://localhost:50051/repo", false},
		{"origin", "ssh://git@github.com/org/repo", false},
		{"origin", "file:///tmp/repo.git", false},
		{"origin", "/tmp/repo.git", false},
		{"origin", "git://127.0.0.1/repo.git", false},
		{"origin", "git@github.com:org/repo.git", false},
		{"origin", "", false},

		// A git-backed remote under any other name is a federation peer.
		{"backup", "git+https://github.com/org/repo.git", false},
		{"Origin", "git+https://github.com/org/repo.git", false},
		{"", "git+https://github.com/org/repo.git", false},
	}
	for _, tt := range tests {
		t.Run(tt.name+" "+tt.url, func(t *testing.T) {
			if got := IsGitSyncRemote(tt.name, tt.url); got != tt.want {
				t.Errorf("IsGitSyncRemote(%q, %q) = %v, want %v", tt.name, tt.url, got, tt.want)
			}
		})
	}
}

// TestAddsAsGitBackedRemote pins which URLs Dolt stores git-backed when it
// adds a remote. Each comment is what dolt 2.6.0's dolt remote add stored.
func TestAddsAsGitBackedRemote(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"git+https://github.com/org/repo", true},       // as given
		{"GIT+HTTPS://github.com/org/repo.git", true},   // git+https://github.com/org/repo.git
		{"https://github.com/org/repo.git", true},       // git+https://github.com/org/repo.git
		{"HTTPS://github.com/org/repo.git", true},       // git+https://github.com/org/repo.git
		{"https://github.com/org/repo.git?ref=x", true}, // git+https://github.com/org/repo.git?ref=x
		{"http://127.0.0.1:3000/repo.git", true},        // git+http://127.0.0.1:3000/repo.git
		{"ssh://git@github.com/org/repo.git", true},     // git+ssh://git@github.com/org/repo.git
		{"git@github.com:org/repo.git", true},           // git+ssh://git@github.com/./org/repo.git
		{"github.com:org/repo.git", true},               // git+ssh://github.com/./org/repo.git
		{"github.com/org/repo.git", true},               // git+https://github.com/org/repo.git
		{"file:///srv/git/repo.git", true},              // git+file:///srv/git/repo.git
		{"/srv/git/repo.git", true},                     // git+file:///srv/git/repo.git
		{"./rel/repo.git", true},                        // git+file://<cwd>/rel/repo.git

		{"https://github.com/org/repo", false},      // as given: a remotesapi server
		{"https://github.com/org/repo.git/", false}, // as given
		{"ssh://git@github.com/org/repo", false},    // as given: Dolt's own ssh transfer
		{"git://127.0.0.1:9418/repo.git", false},    // as given: no git:// transport
		{"file:///srv/git/repo", false},             // as given: a native file store
		{"/srv/git/repo", false},                    // https://doltremoteapi.dolthub.com/srv/git/repo
		{"git@github.com:org/repo", false},          // refused: Dolt cannot parse it
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := AddsAsGitBackedRemote(tt.url); got != tt.want {
				t.Errorf("AddsAsGitBackedRemote(%q) = %v, want %v", tt.url, got, tt.want)
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
