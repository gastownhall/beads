//go:build integration

package dolt

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestPushPullLogCLIRouteForGitProtocolRemote is the RED companion for the
// other side of be-9i0yq.2 item 2: a git-protocol-scheme remote must make
// Push/Pull log the CLI route, distinguishable from the file:// SQL route
// proven by TestPushPullLogSQLRouteForFileRemote (route_logging_test.go).
//
// This test needs a store with its own local, CLI-accessible .dolt
// directory. Plain New() with no ServerHost/ServerPort resolves to a client
// of the suite's shared test server (testmain_test.go), which runs inside a
// Docker container mounting only its own /var/lib/dolt volume — so that
// store's CLIDir() is "" and hasCLIDatabase() is false, and CLI routing can
// never be constructed there (be-9i0yq.2 review, round 1: the test always
// self-skipped). startLocalDoltServer (git_remote_test.go) instead gives
// this store its own on-disk database on this process's filesystem, the
// same arrangement TestGitRemoteEmbeddedPushPull uses.
//
// The remote points at a local path that does not exist. git+file:// keeps
// the whole exchange on the local filesystem, so the dolt subprocess fails
// fast instead of risking a DNS/network hang -- appropriate here because this
// test is about the route DECISION, which is logged before the subprocess
// ever runs, not about a successful transfer. Push/Pull are expected to
// return an error; only the log line is under test.
func TestPushPullLogCLIRouteForGitProtocolRemote(t *testing.T) {
	testutil.RequireDoltBinary(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	doltDir := t.TempDir()
	serverPort, stopServer := startLocalDoltServer(t, doltDir)
	defer stopServer()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dbName := uniqueTestDBName(t)
	store, err := New(ctx, &Config{
		Path:            doltDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      serverPort,
		ServerUser:      "root",
		AutoStart:       false,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	if store.CLIDir() == "" || !store.hasCLIDatabase() {
		t.Fatalf("local-server store should have its own CLI-accessible database; CLIDir=%q hasCLIDatabase=%v", store.CLIDir(), store.hasCLIDatabase())
	}

	remoteURL := "git+file://" + filepath.Join(doltDir, "nonexistent-git-remote")
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	store.remote = "origin"
	store.branch = "main"

	// captureRouteLog (route_logging_test.go) swaps os.Stderr and debug's
	// verbose flag, both process-global: this test must never gain t.Parallel.
	var pushErr, pullErr error
	logged := captureRouteLog(t, func() {
		pushErr = store.Push(ctx)
		pullErr = store.Pull(ctx)
	})
	if pushErr == nil {
		t.Fatalf("Push against a nonexistent git+file:// target should fail (route logging is under test, not transfer success)")
	}
	if pullErr == nil {
		t.Fatalf("Pull against a nonexistent git+file:// target should fail (route logging is under test, not transfer success)")
	}

	if !strings.Contains(logged, "dolt push route: CLI") {
		t.Fatalf("Push over a git+file:// (git-protocol) remote did not log taking the CLI route; got log:\n%s", logged)
	}
	if !strings.Contains(logged, "dolt pull route: CLI") {
		t.Fatalf("Pull over a git+file:// (git-protocol) remote did not log taking the CLI route; got log:\n%s", logged)
	}
	if strings.Contains(logged, "route: SQL") {
		t.Fatalf("a git-protocol remote should never take the SQL route, but the log claims it did:\n%s", logged)
	}
}
