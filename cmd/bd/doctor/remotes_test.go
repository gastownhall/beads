package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// Tests for CheckDoltRemoteGitOrigin (GH#6150): the git-backed Dolt origin —
// the remote named origin whose URL uses a Git transport — is the canonical
// sync transport bd itself wires (refs/dolt/data on the git origin), so URL
// equality with the git origin is not a collision for it. What still warns is
// a non-origin remote pointing there (an alias with an unexpected role) and
// an origin remote that matches the git origin URL without a Git transport.
//
// The tests stub querySQLRemotesForDoctor, the seam CheckDoltRemoteGitOrigin
// reads remotes through, and use a real temp git repo for the origin URL, so
// they run without a Dolt server. They must not run in parallel: they replace
// a package seam and reset the beads-dir resolution cache.

// stubSQLRemotes points querySQLRemotesForDoctor at a fixed remote list for
// the test's duration.
func stubSQLRemotes(t *testing.T, remotes []storage.RemoteInfo) {
	t.Helper()
	orig := querySQLRemotesForDoctor
	querySQLRemotesForDoctor = func(string) ([]storage.RemoteInfo, error) {
		return remotes, nil
	}
	t.Cleanup(func() { querySQLRemotesForDoctor = orig })
}

// initGitRepoWithOrigin makes repoPath a git repository whose origin remote
// points at originURL. Doctor reads the origin URL through git itself
// (gitOriginRemoteURL), so the repository has to be real.
func initGitRepoWithOrigin(t *testing.T, repoPath, originURL string) {
	t.Helper()
	if err := os.MkdirAll(repoPath, 0o750); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, repoPath, "init", "-q")
	runGitIn(t, repoPath, "remote", "add", "origin", originURL)
}

// runGitIn runs a git command in dir and fatals on error, with the fixture
// flags of gitCommand (no auto maintenance).
func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitCommand(args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// TestCheckDoltRemoteGitOrigin_CanonicalGitOriginIsOK reproduces GH#6150: a
// Dolt remote named origin at the canonically equivalent Git-protocol URL of
// the git origin is the canonical git-backed sync topology, and doctor must
// report OK with no fix — never advising removal or dolt.local-only, which
// would disable the cross-machine sync path the remote exists for.
func TestCheckDoltRemoteGitOrigin_CanonicalGitOriginIsOK(t *testing.T) {
	for _, tc := range []struct {
		name      string
		gitOrigin string
		doltURL   string
	}{
		{"git+https", "https://github.com/org/repo.git", "git+https://github.com/org/repo.git"},
		{"scp origin with git+ssh remote", "git@github.com:org/repo.git", "git+ssh://git@github.com/org/repo.git"},
		{"credentials and host case fold", "https://user:tok@GitHub.com/org/repo.git/", "git+https://github.com/org/repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := serverModeWorkspace(t, "")
			clearResolveBeadsDirCache()
			t.Cleanup(clearResolveBeadsDirCache)
			initGitRepoWithOrigin(t, root, tc.gitOrigin)
			stubSQLRemotes(t, []storage.RemoteInfo{{Name: "origin", URL: tc.doltURL}})

			check := CheckDoltRemoteGitOrigin(root)
			if check.Status != StatusOK {
				t.Errorf("status = %q, want %q for the canonical git-backed origin; message=%q detail=%q",
					check.Status, StatusOK, check.Message, check.Detail)
			}
			if check.Fix != "" {
				t.Errorf("fix = %q, want empty for the canonical git-backed origin", check.Fix)
			}
		})
	}

	t.Run("remote at another repository does not warn", func(t *testing.T) {
		root := serverModeWorkspace(t, "")
		clearResolveBeadsDirCache()
		t.Cleanup(clearResolveBeadsDirCache)
		initGitRepoWithOrigin(t, root, "https://github.com/org/repo.git")
		stubSQLRemotes(t, []storage.RemoteInfo{{Name: "origin", URL: "https://doltremoteapi.dolthub.com/org/repo"}})

		check := CheckDoltRemoteGitOrigin(root)
		if check.Status != StatusOK || check.Message != "No Dolt remote matches git origin" {
			t.Errorf("unrelated remote: got %s %q, want ok %q", check.Status, check.Message, "No Dolt remote matches git origin")
		}
	})
}

// TestCheckDoltRemoteGitOrigin_AliasMatchWarnsAboutAliasOnly covers a
// non-origin remote pointing at the git origin repository: a federation peer
// with an unexpected role for that URL. The warning must name the alias,
// never the valid git-backed origin, and its guidance must neither remove
// the git-backed origin nor disable sync.
func TestCheckDoltRemoteGitOrigin_AliasMatchWarnsAboutAliasOnly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		remotes []storage.RemoteInfo
	}{
		{"alias alone", []storage.RemoteInfo{
			{Name: "backup", URL: "https://github.com/org/repo.git"},
		}},
		{"alias beside the canonical git-backed origin", []storage.RemoteInfo{
			{Name: "origin", URL: "git+https://github.com/org/repo.git"},
			{Name: "backup", URL: "git+https://github.com/org/repo.git"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := serverModeWorkspace(t, "")
			clearResolveBeadsDirCache()
			t.Cleanup(clearResolveBeadsDirCache)
			initGitRepoWithOrigin(t, root, "https://github.com/org/repo.git")
			stubSQLRemotes(t, tc.remotes)

			check := CheckDoltRemoteGitOrigin(root)
			if check.Status != StatusWarning {
				t.Fatalf("status = %q, want %q; message=%q", check.Status, StatusWarning, check.Message)
			}
			if !strings.Contains(check.Message, "backup") {
				t.Errorf("message %q must name the alias", check.Message)
			}
			if strings.Contains(check.Message, "origin,") || strings.HasSuffix(check.Message, "origin") {
				t.Errorf("message %q must exclude the valid git-backed origin from the affected names", check.Message)
			}
			if strings.Contains(check.Fix, "local-only") {
				t.Errorf("fix %q must not advise disabling sync (dolt.local-only)", check.Fix)
			}
			if strings.Contains(check.Fix, "remote remove origin") {
				t.Errorf("fix %q must not advise removing the git-backed origin", check.Fix)
			}
		})
	}

	t.Run("sync.remote naming the alias does not change classification", func(t *testing.T) {
		root := serverModeWorkspace(t, "")
		clearResolveBeadsDirCache()
		t.Cleanup(clearResolveBeadsDirCache)
		initGitRepoWithOrigin(t, root, "https://github.com/org/repo.git")
		beadsDir := filepath.Join(root, ".beads")
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := config.SetYamlConfigInDir(beadsDir, "sync.remote", "https://github.com/org/repo.git"); err != nil {
			t.Fatalf("persist sync.remote: %v", err)
		}
		stubSQLRemotes(t, []storage.RemoteInfo{{Name: "backup", URL: "https://github.com/org/repo.git"}})

		check := CheckDoltRemoteGitOrigin(root)
		if check.Status != StatusWarning || !strings.Contains(check.Message, "backup") {
			t.Errorf("alias matching sync.remote: got %s %q, want a warning naming the alias", check.Status, check.Message)
		}
		if strings.Contains(check.Fix, "local-only") {
			t.Errorf("fix %q must not advise disabling sync", check.Fix)
		}
	})
}

// TestCheckDoltRemoteGitOrigin_OriginWrongTransport covers a Dolt remote
// named origin that canonically matches the git origin URL without using a
// Git transport: raw http(s), file and cloud URLs address a Dolt remote
// server or native storage, not the git repository, so origin is not the
// git-backed sync remote its name suggests. The warning must be about the
// transport and advise configuring origin for Git sync — never local-only.
func TestCheckDoltRemoteGitOrigin_OriginWrongTransport(t *testing.T) {
	// Only raw http(s) both canonicalizes to the git origin form and misses
	// the Git-transport classifier: ssh:// and SCP forms are Git transports,
	// and file or cloud URLs never canonicalize to an https origin at all.
	for _, tc := range []struct {
		gitOrigin string
		doltURL   string
	}{
		{"https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"https://GitHub.com/org/repo", "https://user@github.com/org/repo.git"},
		{"http://localhost:3000/org/repo.git", "http://localhost:3000/org/repo"},
	} {
		t.Run(tc.doltURL, func(t *testing.T) {
			root := serverModeWorkspace(t, "")
			clearResolveBeadsDirCache()
			t.Cleanup(clearResolveBeadsDirCache)
			initGitRepoWithOrigin(t, root, tc.gitOrigin)
			stubSQLRemotes(t, []storage.RemoteInfo{{Name: "origin", URL: tc.doltURL}})

			check := CheckDoltRemoteGitOrigin(root)
			if check.Status != StatusWarning {
				t.Fatalf("status = %q, want %q; message=%q", check.Status, StatusWarning, check.Message)
			}
			if !strings.Contains(check.Message, "origin") {
				t.Errorf("message %q must name origin", check.Message)
			}
			if !strings.Contains(check.Detail, "transport") {
				t.Errorf("detail %q must explain the transport problem", check.Detail)
			}
			if !strings.Contains(check.Fix, "git+") {
				t.Errorf("fix %q must recommend configuring origin for Git-backed sync (a git+ URL)", check.Fix)
			}
			if strings.Contains(check.Fix, "local-only") {
				t.Errorf("fix %q must not advise disabling sync", check.Fix)
			}
		})
	}

	// The exemption must come from canonical equality plus a Git transport,
	// never from a lax comparison: a git+file:// remote does not match a
	// file:// git origin, so that pair stays a no-match, not a canonical OK.
	t.Run("git+file remote does not match a file:// origin", func(t *testing.T) {
		root := serverModeWorkspace(t, "")
		clearResolveBeadsDirCache()
		t.Cleanup(clearResolveBeadsDirCache)
		initGitRepoWithOrigin(t, root, "file:///srv/org/repo.git")
		stubSQLRemotes(t, []storage.RemoteInfo{{Name: "origin", URL: "git+file:///srv/org/repo.git"}})

		check := CheckDoltRemoteGitOrigin(root)
		if check.Status != StatusOK || check.Message != "No Dolt remote matches git origin" {
			t.Errorf("file:// vs git+file://: got %s %q, want ok %q", check.Status, check.Message, "No Dolt remote matches git origin")
		}
	})
}
