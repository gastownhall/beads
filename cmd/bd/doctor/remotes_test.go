package doctor

import (
	"os"
	"strings"
	"testing"

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
