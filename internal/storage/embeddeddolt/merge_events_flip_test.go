//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// These tests pin merging ACROSS the bd-red8u events plane flip (#6727): a
// store that ran migration 0062 (events dropped from the versioned plane,
// recreated dolt_ignored) absorbing peer history that still writes to the
// versioned events table. That history pair is a table deletion versus a
// data modification — the engine can neither resolve it row-wise nor commit
// it, and neither --ours nor --theirs can act on a table that has no
// versioned existence on this side. The compatibility operation that would
// absorb such history cleanly belongs to the Dolt driver, not to bd, so
// until it ships these tests pin what is bd's to own: the ordinary
// merge/pull path RETAINS the failure — with guidance naming the shape and
// its recovery, no history damage, no silent success — and the manual
// recovery documented in docs/recovery/sync-failures.md converges on this
// engine.

// seedFlippedStoreWithUnpulledPeer builds the #6727 fixture: a store whose
// main has migrated through 0062 (events on the ignore plane, one local
// unversioned row) and a divergent pre-flip branch holding one new bead and
// one more committed events row. It returns the active branch name and the
// peer branch's tip commit, the commit the recovery must keep in ancestry.
func seedFlippedStoreWithUnpulledPeer(t *testing.T, ctx context.Context, conn *sql.Conn) (mainBranch, peerTip string) {
	t.Helper()

	// Common pre-flip baseline: a committed bead and a committed events row
	// (events is versioned at schema 61).
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('merge-1', 'merge seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('merge-1', 'created', 'tester', '2026-07-30 00:00:00', 'evt-seed-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed committed events row')")
	mainBranch = scalarString(t, ctx, conn, "SELECT active_branch()")

	// The un-pulled clone: ordinary pre-0062 work — a new bead plus another
	// audit row on the still-versioned events table.
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('-b', 'unpulled')")
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('peer-1', 'peer work', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('merge-1', 'updated', 'laptop', '2026-07-30 00:00:01', 'evt-unpulled-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: un-pulled clone work')")
	peerTip = scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('"+mainBranch+"')")

	// The migrator flips events to the ignored plane and keeps writing
	// events locally: unversioned rows no merge may drop.
	if _, err := schema.MigrateUpTo(ctx, conn, 62); err != nil {
		t.Fatalf("MigrateUpTo(62): %v", err)
	}
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('merge-1', 'updated', 'migrator', '2026-07-30 00:00:02', 'evt-local-1');
`)
	return mainBranch, peerTip
}

// TestEmbeddedMergeAcrossEventsFlipWithoutBridge runs the ordinary embedded
// pull/merge path (PullFrom's MergeAndSettle) against the divergent
// pre-flip peer. With no driver-owned retired-events bridge available the
// merge MUST fail, name the shape and its recovery, and leave the store
// exactly as it was: HEAD unmoved, the peer's beads absent, local ignored
// events rows intact, no open merge, no recovery branches. A nil error here
// would be the worst outcome — a silent success over a commit the engine
// refuses, wedging the store.
func TestEmbeddedMergeAcrossEventsFlipWithoutBridge(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	mainBranch, _ := seedFlippedStoreWithUnpulledPeer(t, ctx, conn)

	headBefore := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	localEvents := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events")

	err := versioncontrolops.MergeAndSettle(ctx, conn, "unpulled")
	t.Logf("MergeAndSettle(unpulled) = %v", err)
	if err == nil {
		t.Fatal("merging divergent pre-flip history into a flipped store must fail while no compatibility operation exists; a silent success wedges the store")
	}
	for _, want := range []string{"events", "docs/recovery/sync-failures.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("merge failure missing %q — the aborted merge leaves nothing queryable, so the error itself must carry the recovery:\n%v", want, err)
		}
	}
	if got := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1"); got != headBefore {
		t.Fatalf("failed merge moved HEAD: %s -> %s", headBefore, got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = 'peer-1'"); got != 0 {
		t.Fatalf("peer beads landed by a failed merge = %d, want 0", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got < localEvents {
		t.Fatalf("failed merge dropped local ignored events rows: %d -> %d", localEvents, got)
	}
	if merging := scalarBool(t, ctx, conn, "SELECT is_merging FROM dolt_merge_status"); merging {
		t.Fatalf("failed merge left a merge open (is_merging = %d)", merging)
	}
	for _, b := range branchNames(t, ctx, conn) {
		if b != mainBranch && b != "unpulled" {
			t.Fatalf("failed merge left branch %q behind (have %v)", b, branchNames(t, ctx, conn))
		}
	}
}

// TestEmbeddedMergeAfterEventsFlipOrdinary pins that the retained failure is
// specific to the retired-events shape: peer history created AFTER the flip
// — no versioned events anywhere in the three-way — merges through the same
// path as ordinary work, and event writes keep working.
func TestEmbeddedMergeAfterEventsFlipOrdinary(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	mainBranch, _ := seedFlippedStoreWithUnpulledPeer(t, ctx, conn)

	// A post-flip peer: branches from flipped HEAD, so events is retired on
	// every side of the three-way.
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('-b', 'postflip')")
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('post-1', 'post-flip work', '', '', '', '', 'open', 2, 'task');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: post-flip clone work')")
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('"+mainBranch+"')")

	if err := versioncontrolops.MergeAndSettle(ctx, conn, "postflip"); err != nil {
		t.Fatalf("post-flip peer must merge ordinarily: %v", err)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = 'post-1'"); got != 1 {
		t.Fatalf("post-flip peer bead count = %d, want 1", got)
	}
	assertEventsAbsentFromStatus(t, ctx, conn)
}

// TestEmbeddedEventsFlipManualBridgeRecovery validates, on a disposable
// database, the manual bridge published under
// docs/recovery/sync-failures.md#merge-fails-with-the-tables-events-have-schema-conflicts:
// commit the events deletion on top of the peer's history, so both sides of
// the merge deleted the table. The peer's original commit must stay in
// ancestry with its beads present, events must stay off committed HEAD,
// local ignored events rows must survive, the temporary branch must be
// gone, event writes must work again, and the next ordinary merge must go
// through.
func TestEmbeddedEventsFlipManualBridgeRecovery(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	mainBranch, peerTip := seedFlippedStoreWithUnpulledPeer(t, ctx, conn)

	// The recovery page's starting state: a failed merge attempt. The settle
	// pass aborts it, so the page's abort step is run-but-tolerated here.
	if err := versioncontrolops.MergeAndSettle(ctx, conn, "unpulled"); err == nil {
		t.Fatal("pre-bridge merge must fail (no compatibility operation exists)")
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_MERGE('--abort')"); err != nil {
		t.Logf("merge already aborted by the failed attempt: %v", err)
	}

	// The bridge, exactly as documented (dolt fetch + branch + drop + add +
	// commit + merge + branch -d), expressed as the engine's SQL procedures.
	mustDrain(t, ctx, conn, "CALL DOLT_BRANCH('tmp-events-bridge', 'unpulled')")
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('tmp-events-bridge')")
	execFrozenGuard(t, ctx, conn, "DROP TABLE events")
	// -f: the ignore pattern is database state that survives the checkout to
	// pre-flip history, and plain DOLT_ADD of a dolt_ignore'd table is a
	// silent no-op — 0062's own drop needs the same force.
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-f', 'events')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'bridge: apply the events retirement to incoming history')")
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('"+mainBranch+"')")
	mustDrain(t, ctx, conn, "CALL DOLT_MERGE('tmp-events-bridge')")
	mustDrain(t, ctx, conn, "CALL DOLT_BRANCH('-d', 'tmp-events-bridge')")

	// The peer's work arrived, through its original commit.
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = 'peer-1'"); got != 1 {
		t.Fatalf("peer bead count after bridge = %d, want 1", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = '"+peerTip+"'"); got != 1 {
		t.Fatalf("peer tip %s in post-merge ancestry %d times, want 1", peerTip, got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_schema_conflicts WHERE table_name = 'events'"); got != 0 {
		t.Fatalf("bridge left %d events schema conflict(s)", got)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM events AS OF 'HEAD'").Scan(new(int)); err == nil {
		t.Fatal("events exists at committed HEAD after the bridge; the retirement must hold")
	}
	// Local unversioned rows survived the branch dance.
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got < 2 {
		t.Fatalf("local ignored events rows after bridge = %d, want >= 2 (seed + local)", got)
	}
	for _, b := range branchNames(t, ctx, conn) {
		if b != mainBranch && b != "unpulled" {
			t.Fatalf("bridge left branch %q behind (have %v)", b, branchNames(t, ctx, conn))
		}
	}

	// Event writes work again, and the next ordinary merge goes through.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('peer-1', 'created', 'migrator', '2026-07-30 00:00:03', 'evt-recovered-1');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('-b', 'postflip2')")
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('post-2', 'post-recovery work', '', '', '', '', 'open', 2, 'task');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: post-recovery work')")
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('"+mainBranch+"')")
	if err := versioncontrolops.MergeAndSettle(ctx, conn, "postflip2"); err != nil {
		t.Fatalf("post-recovery ordinary merge: %v", err)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = 'post-2'"); got != 1 {
		t.Fatalf("post-recovery bead count = %d, want 1", got)
	}
}

func scalarBool(t *testing.T, ctx context.Context, conn *sql.Conn, query string) bool {
	t.Helper()
	var v bool
	if err := conn.QueryRowContext(ctx, query).Scan(&v); err != nil {
		t.Fatalf("scalar %q: %v", query, err)
	}
	return v
}

func branchNames(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT name FROM dolt_branches")
	if err != nil {
		t.Fatalf("read dolt_branches: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan branch name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_branches: %v", err)
	}
	return out
}
