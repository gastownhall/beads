//go:build cgo

package doctor

import (
	"net"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil"
)

// gitHTTPServer serves the bare repositories under root over git's smart
// HTTP protocol (git http-backend) on a loopback port and returns its base
// URL, http://127.0.0.1:<port>.
func gitHTTPServer(t *testing.T, root string) string {
	t.Helper()
	backend := filepath.Join(strings.TrimSpace(runGit(t, root, "--exec-path")), "git-http-backend")
	srv := httptest.NewServer(&cgi.Handler{
		Path: backend,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1",
			"HOME=" + t.TempDir(),
		},
		InheritEnv: []string{"PATH"},
	})
	t.Cleanup(srv.Close)
	return srv.URL
}

// newBareRepoWithBranch makes a bare repository at path holding one commit
// on main, as a repository that hosts project source does. Dolt pushes to a
// git remote only once it has a branch.
func newBareRepoWithBranch(t *testing.T, path string) {
	t.Helper()
	runGit(t, filepath.Dir(path), "init", "-q", "--bare", path)
	seed := t.TempDir()
	runGit(t, seed, "init", "-q")
	runGit(t, seed, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "seed")
	runGit(t, seed, "push", "-q", path, "HEAD:refs/heads/main")
}

// publishDoltData commits a table in a fresh Dolt database and pushes it to
// the bare repository at bare through Dolt's git remote transport, as
// bd dolt push does, and returns the refs/dolt/data commit it wrote.
func publishDoltData(t *testing.T, bare string) string {
	t.Helper()
	testutil.RequireDoltBinary(t)
	home := t.TempDir()
	db := t.TempDir()
	env := append(os.Environ(),
		"HOME="+home, "DOLT_ROOT_PATH="+home, "DOLT_DISABLE_EVENT_FLUSH=1",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(home, "gitconfig"),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	dolt := func(args ...string) {
		t.Helper()
		cmd := exec.Command("dolt", args...) // #nosec G204 -- test-only, fixed dolt CLI arguments
		cmd.Dir = db
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt %v: %v\n%s", args, err, out)
		}
	}
	dolt("config", "--global", "--add", "metrics.disabled", "true")
	dolt("init", "--name", "t", "--email", "t@example.com")
	dolt("sql", "-q", "CREATE TABLE issues (id varchar(32) PRIMARY KEY, title text); INSERT INTO issues VALUES ('t-1', 'published');")
	dolt("add", ".")
	dolt("commit", "-m", "publish beads data")
	// Dolt stores a local path ending in .git as a git+file:// remote.
	dolt("remote", "add", "origin", bare)
	dolt("push", "origin", "main")
	return strings.TrimSpace(runGit(t, bare, "rev-parse", "--verify", "refs/dolt/data"))
}

// lsRemoteDoltData returns the refs/dolt/data line git sees at url, or "".
func lsRemoteDoltData(t *testing.T, url string) string {
	t.Helper()
	cmd := gitCommand("ls-remote", url, "refs/dolt/data")
	cmd.Env = append(os.Environ(), "no_proxy=*", "NO_PROXY=*")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote %s: %v\n%s", url, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestDoctorGitBackedOriginTopology runs the three doctor checks GH#6150
// names against a server-mode workspace wired the way bd wires git-backed
// sync: git origin and the SQL Dolt remote origin name the same repository
// (http:// and git+http:// over a loopback git http-backend), sync.remote is
// set, and the repository carries real Dolt data under refs/dolt/data. The
// origin check is OK without advice to remove origin or go local-only, and
// both federation checks see no peer: origin is the sync remote, not one.
// With one more remote both count exactly that peer, the remotesapi
// listener's state and the peer's reachability reported apart from it.
func TestDoctorGitBackedOriginTopology(t *testing.T) {
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available")
	}

	gitRoot := t.TempDir()
	published := filepath.Join(gitRoot, "published.git")
	newBareRepoWithBranch(t, published)
	dataCommit := publishDoltData(t, published)
	fresh := filepath.Join(gitRoot, "fresh.git")
	newBareRepoWithBranch(t, fresh)
	baseURL := gitHTTPServer(t, gitRoot)

	// workspace is the GH#6150 topology for the repository repoName on the
	// git server, plus extra SQL remotes. The remotesapi port is closed.
	workspace := func(t *testing.T, repoName string, extra ...storage.RemoteInfo) string {
		t.Helper()
		gitOrigin := baseURL + "/" + repoName
		doltOrigin := "git+" + gitOrigin
		if doltremote.CanonicalForComparison(gitOrigin) != doltremote.CanonicalForComparison(doltOrigin) {
			t.Fatalf("fixture: git origin %s and Dolt origin %s do not name the same repository", gitOrigin, doltOrigin)
		}
		remotes := append([]storage.RemoteInfo{{Name: "origin", URL: doltOrigin}}, extra...)
		repo, beadsDir := probeOnlyServerWorkspaceWithURLs(t, port, remotes...)
		t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
		clearResolveBeadsDirCache()
		t.Cleanup(clearResolveBeadsDirCache)
		runGit(t, repo, "init", "-q")
		runGit(t, repo, "config", "core.hooksPath", ".git/hooks")
		runGit(t, repo, "remote", "add", "origin", gitOrigin)
		// What bd dolt remote add origin <url> persists.
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("sync:\n  remote: "+doltOrigin+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return repo
	}

	assertOriginOK := func(t *testing.T, repo string) {
		t.Helper()
		check := CheckDoltRemoteGitOrigin(repo)
		if check.Status != StatusOK || check.Fix != "" {
			t.Errorf("Dolt Remote vs Git Origin: got %s %q (detail %q, fix %q), want ok with no fix",
				check.Status, check.Message, check.Detail, check.Fix)
		}
		assertNoSyncDisablingAdvice(t, check)
	}

	for _, tc := range []struct {
		name, repo string
	}{
		{"origin carrying refs/dolt/data", "published.git"},
		{"origin before its first push", "fresh.git"},
	} {
		t.Run(tc.name+": zero peers", func(t *testing.T) {
			if tc.repo == "published.git" {
				if got := lsRemoteDoltData(t, baseURL+"/published.git"); !strings.HasPrefix(got, dataCommit) {
					t.Fatalf("fixture: refs/dolt/data at the git origin = %q, want %s", got, dataCommit)
				}
			} else if got := lsRemoteDoltData(t, baseURL+"/fresh.git"); got != "" {
				t.Fatalf("fixture: fresh origin already has refs/dolt/data: %q", got)
			}
			repo := workspace(t, tc.repo)
			assertOriginOK(t, repo)
			if check := CheckFederationRemotesAPI(repo); check.Status != StatusOK || check.Message != "N/A (no federation peers configured)" {
				t.Errorf("Federation remotesapi: got %s %q (%s), want ok %q",
					check.Status, check.Message, check.Detail, "N/A (no federation peers configured)")
			}
			if check := CheckFederationPeerConnectivity(repo); check.Status != StatusOK || check.Message != "No federation peers configured (only origin remote)" {
				t.Errorf("Peer Connectivity: got %s %q (%s), want ok %q",
					check.Status, check.Message, check.Detail, "No federation peers configured (only origin remote)")
			}
		})
	}

	for _, tc := range []struct {
		name         string
		listenerOpen bool
	}{
		{"remotesapi listener closed", false},
		{"remotesapi listener open", true},
	} {
		t.Run("origin carrying refs/dolt/data plus one peer, "+tc.name, func(t *testing.T) {
			peer := storage.RemoteInfo{Name: "peer1", URL: "http://127.0.0.1:" + strconv.Itoa(closedLocalPort(t)) + "/beads"}
			repo := workspace(t, "published.git", peer)
			if tc.listenerOpen {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ln.Close() })
				t.Setenv("BEADS_DOLT_REMOTESAPI_PORT", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
			}
			remotesAPIPort := os.Getenv("BEADS_DOLT_REMOTESAPI_PORT")
			assertOriginOK(t, repo)

			check := CheckFederationRemotesAPI(repo)
			wantStatus, wantMessage := StatusError, "remotesapi port "+remotesAPIPort+" not accessible"
			if tc.listenerOpen {
				wantStatus, wantMessage = StatusOK, "Port "+remotesAPIPort+" accessible"
			}
			if check.Status != wantStatus || check.Message != wantMessage || !strings.HasSuffix(check.Detail, "1 peers configured") {
				t.Errorf("Federation remotesapi: got %s %q (%q), want %s %q with Detail ending %q",
					check.Status, check.Message, check.Detail, wantStatus, wantMessage, "1 peers configured")
			}

			// The peer is counted once whether or not it is reachable.
			check = CheckFederationPeerConnectivity(repo)
			if check.Message != "1 peers reachable" && check.Message != "1/1 peers unreachable" {
				t.Errorf("Peer Connectivity: got %s %q (%s), want exactly one peer, reachable or not",
					check.Status, check.Message, check.Detail)
			}
		})
	}
}
