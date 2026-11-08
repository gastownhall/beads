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

// These tests pin the embedded side of gastownhall/beads#6368: a migrated
// store (main migration 0062, events on the dolt_ignored working plane) with
// local ignored events rows pulls from a remote whose tip is already contained
// in HEAD. The engine refuses that merge with the stomped-tables diagnostic
// before it ever compares histories — the pull is a content no-op, but it died
// as a generic exit 1, which `bd sync` reported as an ordinary failure and
// retried forever. The pull wrapper must recognize the proven no-op and
// report pull success, with the successful-pull maintenance (the is_blocked
// recompute and its pending-repair marker) running as for any pull. The
// sync-level outcome that follows (status ok, exit 0) is pinned in
// cmd/bd/sync_test.go.

// ignoredPullFixture builds the #6368 no-op shape on the database the seeded
// schema lives in: a pre-flip committed bead and events row pushed to a fresh
// file:// remote, then the flip, then local ignored events rows. It returns
// the beads dir, the remote URL, and the working events rows at the end of the
// fixture.
func ignoredPullFixture(t *testing.T, ctx context.Context) (beadsDir, remoteURL string, events []string) {
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

// requireIgnoredPullPreconditions asserts, on a freshly opened store session,
// the state the pull runs from: the local ignored events rows are still there
// and the tracked working set is still clean.
func requireIgnoredPullPreconditions(t *testing.T, ctx context.Context, dataDir string, events []string) {
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

// TestEmbeddedPullIgnoredTableAncestor is the #6368 no-op, end to end over a
// file:// remote: the pull must succeed without changing HEAD, the ignored
// events rows, or the merge state, the ordinary (non-force) push must advance
// the remote, and the second pull — an equal tip, the shape every replica sees
// after the push clears it — must succeed too.
func TestEmbeddedPullIgnoredTableAncestor(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	beadsDir, remoteURL, eventsBefore := ignoredPullFixture(t, ctx)
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	requireIgnoredPullPreconditions(t, ctx, dataDir, eventsBefore)

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

// TestEmbeddedPullIgnoredTableDiverged pins the other half of #6368: the same
// ignored-table refusal on a remote that genuinely diverged — a peer pushed
// work this clone does not have — must NOT be treated as the no-op. The pull
// still fails, but as the typed IgnoredTablesMergeError: the refusal is real
// operator-only work (an operator must reconcile the ignored working rows
// with the incoming history), and `bd sync` (pinned in cmd/bd/sync_test.go)
// reports it as the ignored-table outcome instead of a generic exit 1. The
// failure must also be inert: nothing merged, the head never moved, the
// protected rows are exactly what they were, and the successful-pull
// maintenance (which consumes the pending-recompute marker) did not run.
func TestEmbeddedPullIgnoredTableDiverged(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	beadsDir, remoteURL, eventsBefore := ignoredPullFixture(t, ctx)
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// A peer clones the remote at its pre-flip tip and pushes its own work,
	// so the remote's main leaves this clone's history: divergence, the shape
	// no containment proof can ever excuse.
	peerData := filepath.Join(t.TempDir(), "embeddeddolt")
	if err := os.MkdirAll(peerData, 0o700); err != nil {
		t.Fatalf("create peer data dir: %v", err)
	}
	withRawConn(t, peerData, "", func(peer *sql.Conn) {
		mustExecOn(t, ctx, peer, "CALL DOLT_CLONE(?, 'testdb')", remoteURL)
		mustExecOn(t, ctx, peer, "USE testdb")
		tipBefore := scalarString(t, ctx, peer, "SELECT DOLT_HASHOF('origin/main')")
		execFrozenGuard(t, ctx, peer, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('peer-diverge-1', 'peer diverges the remote', '', '', '', '', 'open', 2, 'task');
`)
		mustDrain(t, ctx, peer, "CALL DOLT_COMMIT('-Am', 'test: peer diverges the remote')")
		mustDrain(t, ctx, peer, "CALL DOLT_PUSH('origin', 'main')")
		// Fixture evidence: the peer's work moved the remote's main off the
		// tip this clone last pushed. Whether the new tip is contained in this
		// clone's history is the local session's question, asserted below once
		// the refused pull has fetched the tracking ref.
		tipAfter := scalarString(t, ctx, peer, "SELECT DOLT_HASHOF('origin/main')")
		if tipAfter == tipBefore {
			t.Fatalf("peer push did not move the remote (tip %s)", tipAfter)
		}
	})

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer closeEmbeddedDoltStore(t, store)
	requireIgnoredPullPreconditions(t, ctx, dataDir, eventsBefore)

	headBefore, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("read HEAD before the pull: %v", err)
	}
	pullErr := store.Pull(ctx)
	if pullErr == nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatal("pull of a diverged remote succeeded; the ignored-table refusal is operator-only work")
	}

	// The typed refusal: names the engine-confirmed ignored table and
	// preserves the engine's original error for the operator.
	var typed *versioncontrolops.IgnoredTablesMergeError
	if !errors.As(pullErr, &typed) {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("pull error is not the typed ignored-tables refusal: %v", pullErr)
	}
	if !slices.Equal(typed.Tables, []string{"events"}) {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("typed refusal names %v, want [events]", typed.Tables)
	}
	if inner := typed.Unwrap(); inner == nil || !strings.Contains(inner.Error(), "local changes would be stomped by merge") {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("typed refusal lost the engine's original error: %v", typed.Unwrap())
	}

	// The failure is inert: the head never moved, no merge opened, the
	// protected rows are exactly what they were, and the peer's work did not
	// arrive.
	headAfter, err := store.GetCurrentCommit(ctx)
	if err != nil {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("read HEAD after the failed pull: %v", err)
	}
	if headAfter != headBefore {
		closeEmbeddedDoltStore(t, store)
		t.Fatalf("failed pull moved HEAD: %s -> %s", headBefore, headAfter)
	}
	closeEmbeddedDoltStore(t, store)

	withRawConn(t, dataDir, "testdb", func(conn *sql.Conn) {
		// Fixture evidence, on the session that refused the merge: the
		// fetched remote tip is genuinely not contained in this clone's
		// history — their merge base is neither tip.
		base := scalarString(t, ctx, conn, "SELECT DOLT_MERGE_BASE('main', 'origin/main')")
		tip := scalarString(t, ctx, conn, "SELECT DOLT_HASHOF('origin/main')")
		if base == tip {
			t.Fatalf("fetched remote tip %s is contained in HEAD (merge base %s); the fixture does not diverge", tip, base)
		}
		if merging := scalarBool(t, ctx, conn, "SELECT is_merging FROM dolt_merge_status"); merging {
			t.Fatal("refused pull left a merge open")
		}
		if got := eventRows(t, ctx, conn); !slices.Equal(got, eventsBefore) {
			t.Fatalf("refused pull changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, got)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = 'peer-diverge-1'"); got != 0 {
			t.Fatalf("refused pull delivered the peer's work anyway (%d rows)", got)
		}
		// The successful-pull maintenance never ran: the pending-recompute
		// marker only a successful pull consumes is still there. A refusal
		// mistaken for a no-op would have eaten it.
		if got := scalarInt(t, ctx, conn,
			"SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_recompute_pending'"); got != 1 {
			t.Fatalf("pending-recompute marker rows = %d after the failed pull, want 1 (no successful-pull maintenance may run)", got)
		}
	})
}
