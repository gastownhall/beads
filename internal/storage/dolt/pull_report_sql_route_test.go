package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/types"
)

// TestPullTransportReportsWhetherAnythingMerged is the end-to-end proof for
// ga-bq9zd: on the SQL pull route, "I merged the peer's commits" and "there was
// nothing to merge" are now different RETURN VALUES, not two spellings of nil.
//
// Both are still successes. That is the point and it is the control: the fix
// adds information, it does not turn no-op pulls into errors. A change that
// made the second pull fail would look like it caught something and would
// break every idle `bd sync` in the product.
//
// The fixture keeps everything inside the sql-server's own filesystem — a
// file:// remote the server creates on push, and a peer database it clones from
// that remote — so it does not care whether that server is a container or a
// host process. The peer is a real second database advancing the remote, not a
// stub: without it the first pull would have nothing to merge and the subject
// assertion would be indistinguishable from the control.
func TestPullTransportReportsWhetherAnythingMerged(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)
	peerDB := dbName + "_peer"

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
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", peerDB))
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	}()

	mustExec := func(stage, query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	// A table of our own, so the assertion "the peer's row arrived" does not
	// depend on any beads-schema sweeping/ignore behaviour.
	mustExec("configure issue prefix", "REPLACE INTO config (`key`, value) VALUES ('issue_prefix', 'pulltest')")
	mustExec("stage issue prefix", "CALL DOLT_ADD('config')")
	mustExec("commit issue prefix", "CALL DOLT_COMMIT('-m', 'test: configure issue prefix')")
	mustExec("create marker", "CREATE TABLE pull_report_marker (id INT PRIMARY KEY, v VARCHAR(64))")
	mustExec("stage", "CALL DOLT_ADD('-A')")
	mustExec("commit", "CALL DOLT_COMMIT('-m', 'test: pull report marker')")
	localIssue := &types.Issue{
		ID:        "pull-preserves-local-event",
		Title:     "pull preserves local event",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, localIssue, "tester"); err != nil {
		t.Fatalf("create local issue/event: %v", err)
	}

	// file:// inside the server's filesystem; Dolt creates it on push.
	remoteURL := "file://" + filepath.Join(tmpDir, "pull-report-remote")
	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
	mustExec("push", "CALL DOLT_PUSH('origin', 'main')")

	store.remote = "origin"
	store.branch = "main"

	var eventsBefore, ignoredVersionBefore int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", localIssue.ID).Scan(&eventsBefore); err != nil {
		t.Fatalf("count local events before pull: %v", err)
	}
	if eventsBefore == 0 {
		t.Fatal("local event precondition missing")
	}
	if err := store.db.QueryRowContext(ctx, "SELECT MAX(version) FROM ignored_schema_migrations").Scan(&ignoredVersionBefore); err != nil {
		t.Fatalf("read ignored migration cursor before pull: %v", err)
	}

	// The peer: a real clone of the remote, on the same server.
	mustExec("clone peer", "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB)
	peer := openPeerDB(t, store.connStr, peerDB)
	defer peer.Close()

	mustPeerExec := func(stage, query string, args ...any) {
		t.Helper()
		if _, err := peer.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("peer %s: %v", stage, err)
		}
	}
	mustPeerExec("insert", "INSERT INTO pull_report_marker VALUES (1, 'from peer')")
	mustPeerExec("stage", "CALL DOLT_ADD('-A')")
	mustPeerExec("commit", "CALL DOLT_COMMIT('-m', 'peer: add marker row')")
	mustPeerExec("push", "CALL DOLT_PUSH('origin', 'main')")

	// SUBJECT: a pull with real work to do reports that it merged.
	merged, _, err := store.pullTransportReporting(ctx, "origin")
	if err != nil {
		t.Fatalf("pull with work to do failed: %v", err)
	}
	t.Logf("merged pull reported: %+v", merged)

	// Control for the subject: the pull must actually have delivered the row.
	// Without this, a report saying "merged" could be reporting a merge that
	// never happened, and the case would pass on a lie.
	var got string
	if err := store.db.QueryRowContext(ctx,
		"SELECT v FROM pull_report_marker WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("control broken: the pull did not deliver the peer's row: %v", err)
	}
	if got != "from peer" {
		t.Fatalf("control broken: marker = %q, want %q", got, "from peer")
	}
	assertLocalPullStatePreserved(t, ctx, store, localIssue.ID, eventsBefore, ignoredVersionBefore)
	if !merged.Reported {
		t.Fatalf("the SQL pull route returned no engine report at all (%+v); the DOLT_PULL/DOLT_MERGE row is still being discarded", merged)
	}
	if !merged.Merged {
		t.Errorf("a pull that demonstrably merged the peer's commit reported Merged=false (message %q)", merged.Message)
	}

	// CONTROL: the immediately repeated pull has genuinely nothing to merge.
	// It must still SUCCEED — and now say so.
	noop, _, err := store.pullTransportReporting(ctx, "origin")
	if err != nil {
		t.Fatalf("control broken: a pull with nothing to merge must still succeed, got: %v", err)
	}
	t.Logf("no-op pull reported: %+v", noop)
	if !noop.Reported {
		t.Fatalf("no-op pull returned no engine report (%+v)", noop)
	}
	if noop.Merged {
		t.Errorf("a pull with nothing to merge reported Merged=true (message %q)", noop.Message)
	}
	assertLocalPullStatePreserved(t, ctx, store, localIssue.ID, eventsBefore, ignoredVersionBefore)

	// The server-owned route fetches first and returns before merge preparation
	// when this containment check says the refreshed tracking ref is already in
	// main. Exercise that exact branch against the real no-op remote and prove
	// it cannot clear either local-only table.
	serverNoop, handled, err := store.serverOwnedPullNoop(ctx, "origin", "")
	if err != nil {
		t.Fatalf("server-owned no-op probe: %v", err)
	}
	if !handled || !serverNoop.Reported || serverNoop.Merged {
		t.Fatalf("server-owned no-op = (%+v, handled=%v), want reported non-merge", serverNoop, handled)
	}
	assertLocalPullStatePreserved(t, ctx, store, localIssue.ID, eventsBefore, ignoredVersionBefore)

	// Error-path regression for the production data-loss incident. Both clones
	// edit the same ordinary table row, forcing an unresolved content conflict
	// after pull has snapshotted and checked out the ignored working-set plane.
	// The merge transaction rolls back; the snapshots must then be restored on
	// the same pinned connection in a fresh transaction.
	mustExec("insert local conflict", "INSERT INTO pull_report_marker VALUES (2, 'local')")
	mustExec("stage local conflict", "CALL DOLT_ADD('-A')")
	mustExec("commit local conflict", "CALL DOLT_COMMIT('-m', 'local: conflicting marker')")
	mustPeerExec("insert peer conflict", "INSERT INTO pull_report_marker VALUES (2, 'peer')")
	mustPeerExec("stage peer conflict", "CALL DOLT_ADD('-A')")
	mustPeerExec("commit peer conflict", "CALL DOLT_COMMIT('-m', 'peer: conflicting marker')")
	mustPeerExec("push peer conflict", "CALL DOLT_PUSH('origin', 'main')")
	if _, _, err := store.pullTransportReporting(ctx, "origin"); err == nil {
		t.Fatal("conflicting pull unexpectedly succeeded")
	}
	assertLocalPullStatePreserved(t, ctx, store, localIssue.ID, eventsBefore, ignoredVersionBefore)

	// The whole point, stated as one assertion: the two outcomes are now
	// distinguishable at the call site. Before this change both were nil.
	if merged.Merged == noop.Merged {
		t.Fatalf("a merged pull and a no-op pull are still indistinguishable: both reported Merged=%v", merged.Merged)
	}
}

func assertLocalPullStatePreserved(t *testing.T, ctx context.Context, store *DoltStore, issueID string, wantEvents, wantIgnoredVersion int) {
	t.Helper()
	var events, ignoredVersion int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ?", issueID).Scan(&events); err != nil {
		t.Fatalf("count local events after pull: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT MAX(version) FROM ignored_schema_migrations").Scan(&ignoredVersion); err != nil {
		t.Fatalf("read ignored migration cursor after pull: %v", err)
	}
	if events != wantEvents || ignoredVersion != wantIgnoredVersion {
		t.Fatalf("pull changed local-only state: events=%d (want %d), ignored cursor=%d (want %d)", events, wantEvents, ignoredVersion, wantIgnoredVersion)
	}
}

// openPeerDB opens a single-connection *sql.DB against another database on the
// same server as the store, by reusing the store's DSN with the name swapped.
func openPeerDB(t *testing.T, connStr, dbName string) *sql.DB {
	t.Helper()
	cfg, err := mysql.ParseDSN(connStr)
	if err != nil {
		t.Fatalf("parse store DSN: %v", err)
	}
	cfg.DBName = dbName
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatalf("open peer database %q: %v", dbName, err)
	}
	db.SetMaxOpenConns(1)
	return db
}
