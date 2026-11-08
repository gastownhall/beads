//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// These tests pin the NORMAL pull path (versioncontrolops.Pull, what the
// embedded store's Pull and bd sync run) over GH#6368: a clone whose events
// table moved onto the ignore plane (migration 0062) pulling a remote whose
// history still tracks it. The engine refuses that merge as a stomp of the
// ignored working table before it checks whether anything needs merging. The
// direct-merge pins in merge_events_flip_test.go keep that refusal for
// MergeAndSettle; these pin what the pull wrapper makes of it.

// seedFlippedStoreBehindRemote builds a store whose pre-flip history (a
// committed bead and a committed events row) is published to a fresh file://
// remote, then migrates through 0062 locally and writes one more, unversioned,
// events row. The remote is a strict ancestor of HEAD: the issue's measured
// state, local ahead and remote ahead by zero.
func seedFlippedStoreBehindRemote(t *testing.T, ctx context.Context, conn *sql.Conn) (remoteTip string) {
	t.Helper()
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('pull-1', 'pull seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('pull-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-seed-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed committed events row')")

	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	mustDrain(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', 'file://"+remoteDir+"')")
	mustDrain(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	remoteTip = scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")

	migrateThroughFlip(t, ctx, conn)
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('pull-1', 'updated', 'migrator', '2026-07-30 00:00:02', 'evt-local-1');
`)
	return remoteTip
}

// assertPullLeftStoreIntact checks that a pull changed nothing local: HEAD,
// the ignored events rows, merge state, and the branch set.
func assertPullLeftStoreIntact(t *testing.T, ctx context.Context, conn *sql.Conn, headBefore string, eventsBefore []string) {
	t.Helper()
	if got := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1"); got != headBefore {
		t.Fatalf("pull moved HEAD: %s -> %s", headBefore, got)
	}
	if got := eventRows(t, ctx, conn); !slices.Equal(got, eventsBefore) {
		t.Fatalf("pull changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, got)
	}
	if scalarBool(t, ctx, conn, "SELECT is_merging FROM dolt_merge_status") {
		t.Fatal("pull left a merge open")
	}
	if got := branchNames(t, ctx, conn); !slices.Equal(got, []string{"main"}) {
		t.Fatalf("pull left branches %v, want only main", got)
	}
}

// TestEmbeddedPullIgnoredTableAncestor is GH#6368's measured state through the
// normal pull: the fetched remote tip is contained in HEAD, so the pull is a
// no-op and must succeed with HEAD, the local ignored rows, and merge state
// untouched. The ordinary (non-force) push then brings the remote level, and
// the next pull — the equal-tip case — succeeds too. Before the fix the first
// pull failed with the engine's stomp refusal on every run, so sync never
// reached the push.
func TestEmbeddedPullIgnoredTableAncestor(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	remoteTip := seedFlippedStoreBehindRemote(t, ctx, conn)

	headBefore := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	eventsBefore := eventRows(t, ctx, conn)
	if len(eventsBefore) != 2 {
		t.Fatalf("fixture events rows = %v, want the committed seed row and one local ignored row", eventsBefore)
	}
	if headBefore == remoteTip {
		t.Fatal("fixture HEAD equals the remote tip; want the remote strictly behind")
	}

	if err := versioncontrolops.Pull(ctx, conn, "origin", "main", ""); err != nil {
		t.Fatalf("pull of a remote tip HEAD already contains = %v; want a no-op success", err)
	}
	assertPullLeftStoreIntact(t, ctx, conn, headBefore, eventsBefore)

	if err := versioncontrolops.Push(ctx, conn, "origin", "main", ""); err != nil {
		t.Fatalf("non-force push after the no-op pull: %v", err)
	}
	mustDrain(t, ctx, conn, "CALL DOLT_FETCH('origin', 'main')")
	if got := scalarString(t, ctx, conn, "SELECT DOLT_HASHOF('origin/main')"); got != headBefore {
		t.Fatalf("remote tip after push = %s, want HEAD %s", got, headBefore)
	}

	if err := versioncontrolops.Pull(ctx, conn, "origin", "main", ""); err != nil {
		t.Fatalf("second pull, remote level with HEAD = %v; want success", err)
	}
	assertPullLeftStoreIntact(t, ctx, conn, headBefore, eventsBefore)
}
