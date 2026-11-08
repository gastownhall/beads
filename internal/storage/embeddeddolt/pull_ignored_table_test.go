//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
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
	// The pull publishes nothing: bringing the remote level is the ordinary
	// push's job (bd sync's next step), never the storage pull's.
	if got := scalarString(t, ctx, conn, "SELECT DOLT_HASHOF('origin/main')"); got != remoteTip {
		t.Fatalf("remote tip after the no-op pull = %s, want it still at %s (the pull must not push)", got, remoteTip)
	}

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

// TestEmbeddedPullIgnoredTableDiverged is the escalation half of GH#6368: the
// remote holds pre-flip peer work this clone does not have, so the merge is
// not a no-op and the pull must still fail — but as an IgnoredTablesMergeError
// naming the ignored table, which bd sync maps to its operator-only exit,
// instead of an anonymous error. The original refusal stays reachable, and the
// store is left exactly as it was: HEAD, the local ignored rows, no peer work,
// no open merge.
func TestEmbeddedPullIgnoredTableDiverged(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	seedFlippedStoreBehindRemote(t, ctx, conn)

	// A peer that never migrated publishes more pre-flip work: a new bead and
	// another row on the still-versioned events table, on top of the remote
	// tip this clone already has.
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('-b', 'peer', 'origin/main')")
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('peer-1', 'peer work', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('pull-1', 'updated', 'laptop', '2026-07-30 00:00:01', 'evt-peer-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: un-migrated peer work')")
	peerTip := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	mustDrain(t, ctx, conn, "CALL DOLT_PUSH('origin', 'peer:main')")
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('main')")
	mustDrain(t, ctx, conn, "CALL DOLT_BRANCH('-D', 'peer')")

	headBefore := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	eventsBefore := eventRows(t, ctx, conn)

	err := versioncontrolops.Pull(ctx, conn, "origin", "main", "")
	if err == nil {
		t.Fatal("pull of divergent pre-flip history succeeded; want the ignored-table refusal")
	}
	var ignoredErr *versioncontrolops.IgnoredTablesMergeError
	if !errors.As(err, &ignoredErr) {
		t.Fatalf("pull error is not an IgnoredTablesMergeError: %v", err)
	}
	if !slices.Equal(ignoredErr.Tables, []string{"events"}) {
		t.Fatalf("IgnoredTablesMergeError.Tables = %v, want [events]", ignoredErr.Tables)
	}
	for _, want := range []string{"stomped by merge", "events"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lost the engine's original refusal (missing %q): %v", want, err)
		}
	}
	if errors.Unwrap(ignoredErr) == nil || errors.Unwrap(ignoredErr).Error() != err.Error() {
		t.Fatalf("Unwrap does not return the original error: %v", errors.Unwrap(ignoredErr))
	}

	assertPullLeftStoreIntact(t, ctx, conn, headBefore, eventsBefore)
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", peerTip); got != 0 {
		t.Fatalf("refused pull put peer tip %s in ancestry", peerTip)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = 'peer-1'"); got != 0 {
		t.Fatalf("peer beads landed by a refused pull = %d, want 0", got)
	}
}

// The tests below drive the same GH#6368 shapes through the public store
// (embeddeddolt.Store.Pull and Push) rather than the raw pull wrapper, so they
// also pin what only the store adds: the auto-commit before the pull and the
// successful-pull maintenance (the is_blocked recompute and its pending-repair
// marker, bd-578h9.11), which must run after a recognized no-op and must not
// run after a refusal. The sync-level outcome is pinned in
// cmd/bd/sync_ignored_table_test.go.

// storeIgnoredPullFixture builds the #6368 no-op shape on the database the seeded
// schema lives in: a pre-flip committed bead and events row pushed to a fresh
// file:// remote, then the flip, then local ignored events rows. It returns
// the beads dir, the remote URL, and the working events rows at the end of the
// fixture.
func storeIgnoredPullFixture(t *testing.T, ctx context.Context) (beadsDir, remoteURL string, events []string) {
	t.Helper()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	beadsDir = filepath.Dir(dataDir)
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL = "file://" + remoteDir

	withRawConn(t, dataDir, "testdb", func(conn *sql.Conn) {
		// Pre-flip committed work: a bead and a committed events row (events
		// is still versioned at main schema 61).
		execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('pull-1', 'pull seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('pull-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-seed-1');
`)
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: pre-flip seed')")
		// The remote as this clone last pulled it: the pre-flip tip.
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
		// The flip: events moves to the ignored plane, and local events rows
		// keep working afterwards — they exist only in the working set, which
		// is exactly what the stomp guard protects.
		migrateThroughFlip(t, ctx, conn)
		// Finish the migration series here, as the field clone did before its
		// next open: a remote-backed database refuses to auto-apply pending
		// schema migrations (#4259), so the store under test must open with
		// none pending.
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (fixture to latest): %v", err)
		}
		// The ignored-lane migrations alter tables (leases) that the seed's
		// blanket baseline commit tracked at HEAD — a fixture artifact: real
		// lineages never commit them, so staging never skips the delta there.
		// Plain staging skips it here (every staging path anti-joins
		// dolt_ignore), and a dirty tracked table is the #4356 wedge, not the
		// #6368 shape this test pins. Commit the delta by force so the working
		// set reaches the clean state the field clone has.
		for _, dirty := range dirtyTableNames(t, ctx, conn) {
			mustDrain(t, ctx, conn, "CALL DOLT_ADD('-f', '"+dirty+"')")
			mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('--skip-empty', '-m', 'test: commit ignored-lane schema delta')")
		}
		execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('pull-1', 'updated', 'migrator', '2026-07-30 00:00:02', 'evt-local-1');
`)
		// A pending is_blocked recompute marker (the record a failed recompute
		// leaves, bd-578h9.11): only the successful-pull maintenance consumes
		// it, so its disappearance proves the no-op pull reached maintenance.
		mustExecOn(t, ctx, conn,
			"INSERT IGNORE INTO metadata (`key`, value) VALUES ('is_blocked_recompute_pending', 'full')")
		mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: seed pending recompute marker')")

		// Fixture evidence: events is engine-confirmed ignored, the tracked
		// working set is clean, and the remote tip is contained in HEAD.
		if got := scalarInt(t, ctx, conn,
			"SELECT COUNT(*) FROM dolt_status_ignored WHERE table_name = 'events' AND ignored"); got != 1 {
			t.Fatalf("dolt_status_ignored lists events %d time(s); the fixture does not reproduce #6368", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status"); got != 0 {
			t.Fatalf("dolt_status rows = %d, want a clean tracked working set: %v", got, dirtyTableNames(t, ctx, conn))
		}
		base := scalarString(t, ctx, conn, "SELECT DOLT_MERGE_BASE('main', 'origin/main')")
		tip := scalarString(t, ctx, conn, "SELECT DOLT_HASHOF('origin/main')")
		if base != tip {
			t.Fatalf("remote tip %s is not contained in HEAD (merge base %s); the fixture is not the no-op shape", tip, base)
		}
		events = eventRows(t, ctx, conn)
	})
	return beadsDir, remoteURL, events
}

// requireStoreIgnoredPullPreconditions asserts, on a freshly opened store session,
// the state the pull runs from: the local ignored events rows are still there
// and the tracked working set is still clean.
func requireStoreIgnoredPullPreconditions(t *testing.T, ctx context.Context, dataDir string, events []string) {
	t.Helper()
	withRawConn(t, dataDir, "testdb", func(conn *sql.Conn) {
		if got := scalarInt(t, ctx, conn,
			"SELECT COUNT(*) FROM dolt_status_ignored WHERE table_name = 'events' AND ignored"); got != 1 {
			t.Fatalf("events stopped being an ignored working table on open (dolt_status_ignored = %d)", got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status"); got != 0 {
			t.Fatalf("dolt_status rows = %d before the pull, want a clean tracked working set", got)
		}
		for _, want := range []string{"evt-seed-1", "evt-local-1"} {
			if !strings.Contains(strings.Join(eventRows(t, ctx, conn), "\n"), want) {
				t.Fatalf("fixture lost local ignored event %s on open: %v", want, eventRows(t, ctx, conn))
			}
		}
	})
}

// TestEmbeddedStorePullIgnoredTableAncestor is the #6368 no-op, end to end over a
// file:// remote: the pull must succeed without changing HEAD, the ignored
// events rows, or the merge state, the ordinary (non-force) push must advance
// the remote, and the second pull — an equal tip, the shape every replica sees
// after the push clears it — must succeed too.
func TestEmbeddedStorePullIgnoredTableAncestor(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	beadsDir, remoteURL, eventsBefore := storeIgnoredPullFixture(t, ctx)
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	requireStoreIgnoredPullPreconditions(t, ctx, dataDir, eventsBefore)

	// The pull: the remote tip is an ancestor of HEAD, so nothing can merge.
	// This is the reported bug — it used to die as a generic
	// "merge origin/main: ... stomped ..." error and bd sync retried forever.
	headBeforePull, err := store.GetCurrentCommit(t.Context())
	if err != nil {
		t.Fatalf("read HEAD before the pull: %v", err)
	}
	if err := store.Pull(ctx); err != nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("pull of a contained remote tip = %v; want the no-op pull to succeed", err)
	}
	got, err := store.GetCurrentCommit(t.Context())
	if err != nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("read HEAD after the pull: %v", err)
	}
	if got != headBeforePull {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("no-op pull moved HEAD: %s -> %s", headBeforePull, got)
	}

	// The recovery this pull makes possible: an ordinary push (never force)
	// moves the remote to this clone's HEAD, clearing the wedge for every
	// replica that pulls it. The push's auto-commit first commits what the
	// pull's recompute left in the working set (the marker deletion — derived
	// state, committed by the next CommitPending in production too), so HEAD
	// may advance here; what matters is the remote lands on this clone's tip.
	if err := store.Push(ctx); err != nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("push after the no-op pull failed: %v", err)
	}
	headAfterPush, err := store.GetCurrentCommit(t.Context())
	if err != nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("read HEAD after the push: %v", err)
	}

	// The second pull: an equal tip, the no-op every other shape of this
	// clone runs into. It must succeed for the same reason the first did,
	// and — with a clean working set now — it must not move HEAD either.
	if err := store.Pull(ctx); err != nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("second pull of the pushed tip = %v; want the equal-tip no-op to succeed", err)
	}
	got, err = store.GetCurrentCommit(t.Context())
	if err != nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("read HEAD after the second pull: %v", err)
	}
	if got != headAfterPush {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("equal-tip pull moved HEAD: %s -> %s", headAfterPush, got)
	}
	closeEmbeddedDoltStore(t, store)

	withRawConn(t, dataDir, "testdb", func(conn *sql.Conn) {
		// The no-op left no merge open and the ignored rows exactly as they
		// were — the stomp guard protected precisely these rows.
		if merging := scalarBool(t, ctx, conn, "SELECT is_merging FROM dolt_merge_status"); merging {
			t.Fatal("no-op pull left a merge open")
		}
		if got := eventRows(t, ctx, conn); !slices.Equal(got, eventsBefore) {
			t.Fatalf("no-op pull changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, got)
		}
		// Successful-pull maintenance ran even though nothing merged: the
		// pending-recompute marker only the maintenance consumes is gone.
		if got := scalarInt(t, ctx, conn,
			"SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_recompute_pending'"); got != 0 {
			t.Fatalf("is_blocked recompute marker survived the no-op pull (%d rows); the successful-pull maintenance did not run", got)
		}
		// The push advanced the remote itself: a fresh clone of it starts at
		// this clone's HEAD.
		cloneData := filepath.Join(t.TempDir(), "embeddeddolt")
		if err := os.MkdirAll(cloneData, 0o700); err != nil {
			t.Fatalf("create clone data dir: %v", err)
		}
		withRawConn(t, cloneData, "", func(clone *sql.Conn) {
			mustExecOn(t, ctx, clone, "CALL DOLT_CLONE(?, 'testdb')", remoteURL)
			mustExecOn(t, ctx, clone, "USE testdb")
			if got := scalarString(t, ctx, clone, "SELECT HASHOF('HEAD')"); got != headAfterPush {
				t.Fatalf("clone of the pushed remote starts at %s, want this clone's HEAD %s", got, headAfterPush)
			}
		})
	})
}
