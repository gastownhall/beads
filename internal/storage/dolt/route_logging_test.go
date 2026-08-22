package dolt

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPushPullLogSQLRouteForFileRemote is part of the RED coverage for
// be-9i0yq.2 item 2: prepareCLIRouteForGitProtocol and its siblings
// (store.go ~3554-3609) decide CLI-vs-SQL routing silently -- nothing in the
// push/pull path says which one ran. A file:// remote is not git protocol,
// so both Push and Pull take the SQL route (see the "Local file:// pulls
// intentionally stay on the SQL path" comment on pullTransportReporting):
// that route decision must be discoverable from the log for a real
// Push/Pull call, without reading source to find out.
func TestPushPullLogSQLRouteForFileRemote(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	if _, err := store.db.ExecContext(ctx, "CREATE TABLE route_log_marker (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("create marker table: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'test: route log marker')"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	remoteURL := "file://" + filepath.Join(tmpDir, "route-log-remote")
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	store.remote = "origin"
	store.branch = "main"

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	if err := store.Push(ctx); err != nil {
		t.Fatalf("Push over a file:// remote should succeed via the SQL route: %v", err)
	}
	if err := store.Pull(ctx); err != nil {
		t.Fatalf("Pull over a file:// remote should succeed via the SQL route: %v", err)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "dolt push route: SQL") {
		t.Fatalf("Push over a file:// (non-git-protocol) remote did not log taking the SQL route; got log:\n%s", logged)
	}
	if !strings.Contains(logged, "dolt pull route: SQL") {
		t.Fatalf("Pull over a file:// (non-git-protocol) remote did not log taking the SQL route; got log:\n%s", logged)
	}
	if strings.Contains(logged, "route: CLI") {
		t.Fatalf("a file:// remote should never take the CLI route, but the log claims it did:\n%s", logged)
	}
}

// TestPushPullLogCLIRouteForGitProtocolRemote is the RED companion for the
// other side of item 2: a git-protocol-scheme remote must make Push/Pull log
// the CLI route, distinguishable from the file:// SQL route proven above.
//
// The remote points at a local path that does not exist. git+file:// keeps
// the whole exchange on the local filesystem, so the dolt subprocess fails
// fast instead of risking a DNS/network hang -- appropriate here because this
// test is about the route DECISION, which is logged before the subprocess
// ever runs, not about a successful transfer. Push/Pull are expected to
// return an error; only the log line is under test.
func TestPushPullLogCLIRouteForGitProtocolRemote(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)

	store, err := New(ctx, &Config{
		Path:            tmpDir,
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
		t.Skip("this store has no local CLI database to route through (external-server client?); CLI routing cannot be constructed")
	}

	remoteURL := "git+file://" + filepath.Join(tmpDir, "nonexistent-git-remote")
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL); err != nil {
		t.Fatalf("add remote: %v", err)
	}
	store.remote = "origin"
	store.branch = "main"

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	if err := store.Push(ctx); err == nil {
		t.Fatalf("Push against a nonexistent git+file:// target should fail (route logging is under test, not transfer success)")
	}
	if err := store.Pull(ctx); err == nil {
		t.Fatalf("Pull against a nonexistent git+file:// target should fail (route logging is under test, not transfer success)")
	}

	logged := logBuf.String()
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
