//go:build cgo

package doctor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltremote"
	"github.com/steveyegge/beads/internal/testutil"
)

// The GH#6150 regression contract: a workspace whose git origin and SQL Dolt
// origin point at the same real repository — with sync.remote configured and
// actual Dolt data published under refs/dolt/data — is the canonical
// git-backed sync topology, and doctor's three affected checks must agree
// about it. These tests need the Dolt test server (the doctor server suite)
// plus the host dolt and git binaries.

// runDoltIn runs a dolt command in dir and fatals on error.
func runDoltIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("dolt", args...) // #nosec G204 -- fixed test fixture commands
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// gitDaemonWithDoltData starts a loopback git daemon serving a bare
// repository that already carries real Dolt data under refs/dolt/data, and
// returns the git:// URL both the git origin and the SQL Dolt origin of the
// regression topology point at. The Dolt data is published by a throwaway
// dolt workspace pushing to the repository over its file:// URL — the same
// transport bd dolt push uses — so refs/dolt/data holds a real Dolt commit,
// not a fabricated ref.
func gitDaemonWithDoltData(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skipf("dolt binary not available: %v", err)
	}

	export := t.TempDir()
	bare := filepath.Join(export, "repo.git")
	runGitIn(t, export, "init", "-q", "--bare", bare)
	// Dolt refuses to push to a git remote without any branch
	// ("initialize the repository ... first"), so seed an empty commit.
	tree := gitOutput(t, bare, "mktree")
	commit := gitOutput(t, bare, "commit-tree", strings.TrimSpace(tree), "-m", "seed")
	runGitIn(t, bare, "update-ref", "refs/heads/main", strings.TrimSpace(commit))

	// Publish real Dolt data to the repository's refs/dolt/data.
	work := t.TempDir()
	runDoltIn(t, work, "init", "--name", "bd", "--email", "bd@example.com")
	runDoltIn(t, work, "sql", "-q", "create table seed (pk int primary key)")
	runDoltIn(t, work, "add", "-A")
	runDoltIn(t, work, "commit", "-m", "seed data")
	runDoltIn(t, work, "remote", "add", "origin", "file://"+bare)
	runDoltIn(t, work, "push", "origin", "main")

	// The regression contract's premise: real Dolt data under refs/dolt/data.
	refs := gitOutput(t, bare, "for-each-ref", "--format=%(refname)", "refs/dolt/data")
	if !strings.Contains(refs, "refs/dolt/data") {
		t.Fatalf("fixture precondition: no refs/dolt/data in %s (dolt push did not publish)", bare)
	}

	// Serve the repository over the git protocol on the loopback, with
	// receive-pack enabled for this test only, so the same URL is a genuine
	// git transport, not just a string the two remotes happen to share.
	port := closedLocalPort(t)
	daemon := exec.Command("git", "daemon", // #nosec G204 -- fixed test fixture command
		"--listen=127.0.0.1", "--port="+strconv.Itoa(port),
		"--export-all", "--enable=receive-pack", "--base-path="+export)
	if err := daemon.Start(); err != nil {
		t.Fatalf("start git daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_, _ = daemon.Process.Wait()
	})
	url := fmt.Sprintf("git://127.0.0.1:%d/repo.git", port)
	waitForAccept(t, "127.0.0.1:"+strconv.Itoa(port))

	// The daemon must actually serve the published Dolt data ref.
	remote := gitOutput(t, t.TempDir(), "ls-remote", url)
	if !strings.Contains(remote, "refs/dolt/data") {
		t.Fatalf("fixture precondition: git ls-remote %s shows no refs/dolt/data:\n%s", url, remote)
	}
	return url
}

// gitOutput runs a git command in dir and returns its combined output,
// fataling on error. mktree and ls-remote read from stdin or the network
// rather than a work tree, so dir only anchors repository-level commands.
// A fixed identity is set so commit-tree never depends on the ambient
// git config (the fixture's HOME is a scratch directory).
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCommand(args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=bd", "GIT_AUTHOR_EMAIL=bd@example.com",
		"GIT_COMMITTER_NAME=bd", "GIT_COMMITTER_EMAIL=bd@example.com")
	if args[0] == "mktree" {
		cmd.Stdin = strings.NewReader("")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// waitForAccept dials addr until it accepts a connection or the deadline
// passes, so the test never races the daemon's listener.
func waitForAccept(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("git daemon at %s did not start accepting connections", addr)
}

// gitBackedOriginWorkspace builds the GH#6150 topology around the repository
// at originURL: a server-mode workspace whose SQL remotes are remotes, a git
// repository whose origin points at originURL, and sync.remote persisted to
// it. It asserts the fixture's premise — the git origin URL and the SQL
// origin URL canonically name the same repository — before returning.
func gitBackedOriginWorkspace(t *testing.T, port int, remotes map[string]string) (repo, beadsDir string) {
	t.Helper()
	dbName := "doctest_origin_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	repo, beadsDir = serverWorkspaceWithRemotes(t, port, dbName, remotes)
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))

	initGitRepoWithOrigin(t, repo, remotes["origin"])
	cfgPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SetYamlConfigInDir(beadsDir, "sync.remote", remotes["origin"]); err != nil {
		t.Fatalf("persist sync.remote: %v", err)
	}

	// Fixture precondition: the SQL origin and the git origin name the same
	// repository, whatever form each URL was stored in.
	sqlRemotes, err := querySQLRemotesForDoctor(beadsDir)
	if err != nil {
		t.Fatalf("read the workspace's SQL remotes: %v", err)
	}
	sqlOrigin := ""
	for _, r := range sqlRemotes {
		if r.Name == "origin" {
			sqlOrigin = r.URL
		}
	}
	if sqlOrigin == "" {
		t.Fatal("fixture precondition: the workspace has no SQL remote named origin")
	}
	if doltremote.CanonicalForComparison(sqlOrigin) != doltremote.CanonicalForComparison(gitOriginRemoteURL(repo)) {
		t.Fatalf("fixture precondition: SQL origin %q and git origin %q are not the same repository", sqlOrigin, gitOriginRemoteURL(repo))
	}
	return repo, beadsDir
}

// TestDoctorGitBackedOriginTopology runs doctor's affected checks against a
// real git-backed origin topology: a git origin and a SQL Dolt origin at the
// same served repository, with real Dolt data published under refs/dolt/data
// and sync.remote configured. The origin-only topology is the canonical one:
// doctor must not recommend removing the origin or disabling sync, and the
// federation checks must count zero peers for it — origin is the sync remote,
// not a federation peer.
func TestDoctorGitBackedOriginTopology(t *testing.T) {
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skipf("dolt binary not available: %v", err)
	}

	originURL := gitDaemonWithDoltData(t)

	t.Run("origin-only topology reports OK origin and zero peers", func(t *testing.T) {
		repo, _ := gitBackedOriginWorkspace(t, port, map[string]string{"origin": originURL})

		originCheck := CheckDoltRemoteGitOrigin(repo)
		if originCheck.Status != StatusOK {
			t.Errorf("origin check = %s %q (%s), want ok: the git-backed origin is the canonical sync transport",
				originCheck.Status, originCheck.Message, originCheck.Detail)
		}
		if strings.Contains(originCheck.Fix, "remote remove") || strings.Contains(originCheck.Fix, "local-only") {
			t.Errorf("origin check fix %q must not remove the origin or disable sync", originCheck.Fix)
		}

		fed := CheckFederationRemotesAPI(repo)
		if fed.Status != StatusOK || fed.Message != "N/A (no federation peers configured)" {
			t.Errorf("federation remotesapi = %s %q (%s), want ok %q",
				fed.Status, fed.Message, fed.Detail, "N/A (no federation peers configured)")
		}

		conn := CheckFederationPeerConnectivity(repo)
		if conn.Status != StatusOK || !strings.Contains(conn.Message, "No federation peers configured") {
			t.Errorf("peer connectivity = %s %q, want ok reporting no federation peers", conn.Status, conn.Message)
		}
	})
}
