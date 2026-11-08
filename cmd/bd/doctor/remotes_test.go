package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

// remoteCheckWorkspace is a Dolt server-mode workspace in a git repository
// whose origin is originURL (none when empty), with sync.remote set to
// syncRemote in config.yaml (unset when empty). It returns the repo root.
func remoteCheckWorkspace(t *testing.T, originURL, syncRemote string) string {
	t.Helper()
	root := serverModeWorkspace(t, "")
	clearResolveBeadsDirCache()
	t.Cleanup(clearResolveBeadsDirCache)
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "core.hooksPath", ".git/hooks")
	if originURL != "" {
		runGit(t, root, "remote", "add", "origin", originURL)
	}
	if syncRemote != "" {
		cfg := "sync:\n  remote: " + syncRemote + "\n"
		if err := os.WriteFile(filepath.Join(root, ".beads", "config.yaml"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// stubSQLRemotes makes CheckDoltRemoteGitOrigin read remotes (or err) as the
// workspace's SQL remotes.
func stubSQLRemotes(t *testing.T, err error, remotes ...storage.RemoteInfo) {
	t.Helper()
	orig := querySQLRemotesForDoctor
	querySQLRemotesForDoctor = func(string) ([]storage.RemoteInfo, error) {
		return remotes, err
	}
	t.Cleanup(func() { querySQLRemotesForDoctor = orig })
}

// assertNoSyncDisablingAdvice fails when check tells the user to remove
// origin, to go local-only, or repeats the secret embedded in a git URL.
func assertNoSyncDisablingAdvice(t *testing.T, check DoctorCheck) {
	t.Helper()
	all := check.Message + "\n" + check.Detail + "\n" + check.Fix
	for _, bad := range []string{"local-only", "remote remove origin", "remove origin", "s3cret"} {
		if strings.Contains(all, bad) {
			t.Errorf("check output contains %q:\n%s", bad, all)
		}
	}
}

// TestCheckDoltRemoteGitOrigin pins how doctor classifies the Dolt remotes
// whose URL matches the git origin (GH#6150).
func TestCheckDoltRemoteGitOrigin(t *testing.T) {
	// The Dolt remote named origin over a git transport is the default
	// git-backed sync remote bd wires up, which keeps its data under
	// refs/dolt/data on the same repository. It is OK with no Fix, whatever
	// URL form either side uses and whatever sync.remote says.
	canonical := []struct {
		name, gitOrigin, doltOrigin, syncRemote string
	}{
		{"https and git+https", "https://github.com/org/repo.git", "git+https://github.com/org/repo.git", "origin"},
		{"https and git+https without .git", "https://github.com/org/repo.git", "git+https://github.com/org/repo", ""},
		{"scp and git+ssh", "git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git", ""},
		{"ssh and git+ssh", "ssh://git@github.com/org/repo.git", "git+ssh://git@github.com/org/repo.git", ""},
		{"scp on both sides", "git@github.com:org/repo.git", "git@github.com:org/repo.git", ""},
		{"git daemon", "git://127.0.0.1:9418/repo.git", "git://127.0.0.1:9418/repo.git", ""},
		{"http and git+http", "http://127.0.0.1:3000/repo.git", "git+http://127.0.0.1:3000/repo.git", ""},
		{"credentials and host case", "https://bob:s3cret@GitHub.com/org/repo.git/", "git+https://github.com/org/repo.git", ""},
		{"sync.remote URL form", "https://github.com/org/repo.git", "git+https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"sync.remote names another remote", "https://github.com/org/repo.git", "git+https://github.com/org/repo.git", "peer"},
	}
	for _, tc := range canonical {
		t.Run("canonical origin: "+tc.name, func(t *testing.T) {
			root := remoteCheckWorkspace(t, tc.gitOrigin, tc.syncRemote)
			stubSQLRemotes(t, nil,
				storage.RemoteInfo{Name: "origin", URL: tc.doltOrigin},
				storage.RemoteInfo{Name: "peer", URL: "http://127.0.0.1:50051/beads"})
			check := CheckDoltRemoteGitOrigin(root)
			if check.Status != StatusOK || check.Fix != "" {
				t.Fatalf("got %s %q (detail %q, fix %q), want ok with no fix", check.Status, check.Message, check.Detail, check.Fix)
			}
			assertNoSyncDisablingAdvice(t, check)
		})
	}

	// A remote under another name that matches the git origin is a federation
	// peer, not the default sync remote, so it is reported, alone or beside a
	// valid git-backed origin, which is never among the affected names. The
	// advice is about that alias only, whatever sync.remote says.
	aliases := []struct {
		name, syncRemote string
		remotes          []storage.RemoteInfo
		wantAffected     string
		wantOriginOK     bool
	}{
		{
			name:         "alias alone",
			remotes:      []storage.RemoteInfo{{Name: "backup", URL: "git+https://github.com/org/repo.git"}},
			wantAffected: "backup",
		},
		{
			name:       "alias named by sync.remote",
			syncRemote: "backup",
			remotes: []storage.RemoteInfo{
				{Name: "origin", URL: "git+https://github.com/org/repo.git"},
				{Name: "backup", URL: "git+https://github.com/org/repo.git"},
			},
			wantAffected: "backup",
			wantOriginOK: true,
		},
		{
			name: "aliases beside the git-backed origin",
			remotes: []storage.RemoteInfo{
				{Name: "origin", URL: "git+https://bob:s3cret@github.com/org/repo.git"},
				{Name: "backup", URL: "git+https://github.com/org/repo"},
				{Name: "mirror", URL: "https://github.com/org/repo.git"},
				{Name: "peer", URL: "http://127.0.0.1:50051/beads"},
			},
			wantAffected: "backup, mirror",
			wantOriginOK: true,
		},
	}
	for _, tc := range aliases {
		t.Run(tc.name, func(t *testing.T) {
			root := remoteCheckWorkspace(t, "https://github.com/org/repo.git", tc.syncRemote)
			stubSQLRemotes(t, nil, tc.remotes...)
			check := CheckDoltRemoteGitOrigin(root)
			if check.Status != StatusWarning {
				t.Fatalf("got %s %q, want a warning about %s", check.Status, check.Message, tc.wantAffected)
			}
			_, affected, _ := strings.Cut(check.Message, "match the git origin URL: ")
			if affected != tc.wantAffected {
				t.Errorf("affected names = %q (message %q), want %q", affected, check.Message, tc.wantAffected)
			}
			if !strings.Contains(check.Fix, tc.wantAffected) || !strings.Contains(check.Fix, "sync through origin") {
				t.Errorf("fix = %q, want advice to review %s and sync through origin", check.Fix, tc.wantAffected)
			}
			if strings.Contains(check.Detail, "conflict") {
				t.Errorf("detail = %q claims a conflict the alias need not cause", check.Detail)
			}
			if tc.wantOriginOK != strings.Contains(check.Detail, "origin is the git-backed sync remote and needs no change") {
				t.Errorf("detail = %q, want the git-backed origin called fine: %v", check.Detail, tc.wantOriginOK)
			}
			assertNoSyncDisablingAdvice(t, check)
		})
	}

	// Outcomes that do not depend on how a match is classified.
	t.Run("no Dolt remote matches", func(t *testing.T) {
		root := remoteCheckWorkspace(t, "https://github.com/org/repo.git", "")
		stubSQLRemotes(t, nil, storage.RemoteInfo{Name: "origin", URL: "git+https://github.com/org/other.git"})
		check := CheckDoltRemoteGitOrigin(root)
		if check.Status != StatusOK || check.Message != "No Dolt remote matches git origin" {
			t.Fatalf("got %s %q, want ok %q", check.Status, check.Message, "No Dolt remote matches git origin")
		}
	})
	t.Run("native file remote and git+file transport do not match", func(t *testing.T) {
		root := remoteCheckWorkspace(t, "file:///srv/git/repo.git", "")
		stubSQLRemotes(t, nil, storage.RemoteInfo{Name: "origin", URL: "git+file:///srv/git/repo.git"})
		check := CheckDoltRemoteGitOrigin(root)
		if check.Status != StatusOK || check.Message != "No Dolt remote matches git origin" {
			t.Fatalf("got %s %q, want ok %q", check.Status, check.Message, "No Dolt remote matches git origin")
		}
	})
	t.Run("no git origin", func(t *testing.T) {
		root := remoteCheckWorkspace(t, "", "")
		stubSQLRemotes(t, nil, storage.RemoteInfo{Name: "origin", URL: "git+https://github.com/org/repo.git"})
		check := CheckDoltRemoteGitOrigin(root)
		if check.Status != StatusOK || check.Message != "No git origin configured" {
			t.Fatalf("got %s %q, want ok %q", check.Status, check.Message, "No git origin configured")
		}
	})
	t.Run("SQL remotes unavailable", func(t *testing.T) {
		root := remoteCheckWorkspace(t, "https://github.com/org/repo.git", "")
		stubSQLRemotes(t, errors.New("connection refused"))
		check := CheckDoltRemoteGitOrigin(root)
		if check.Status != StatusOK || !strings.HasPrefix(check.Message, "Could not query Dolt remotes") {
			t.Fatalf("got %s %q, want ok %q", check.Status, check.Message, "Could not query Dolt remotes ...")
		}
	})
	t.Run("not a Dolt backend", func(t *testing.T) {
		root := remoteCheckWorkspace(t, "https://github.com/org/repo.git", "")
		if err := (&configfile.Config{Backend: "sqlite"}).Save(filepath.Join(root, ".beads")); err != nil {
			t.Fatal(err)
		}
		stubSQLRemotes(t, nil, storage.RemoteInfo{Name: "backup", URL: "https://github.com/org/repo.git"})
		check := CheckDoltRemoteGitOrigin(root)
		if check.Status != StatusOK || check.Message != "N/A (not using Dolt backend)" {
			t.Fatalf("got %s %q, want ok %q", check.Status, check.Message, "N/A (not using Dolt backend)")
		}
	})
}
