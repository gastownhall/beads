//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"slices"
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
// merge/pull path RETAINS the engine's failure with no history damage, no
// silent success and every local ignored events row intact.

// seedFlippedStoreWithUnpulledPeer builds the #6727 fixture: a store whose
// main has migrated through 0062 (events on the ignore plane, one local
// unversioned row) and a divergent pre-flip branch holding one new bead and
// one more committed events row. It returns the active branch name and the
// peer branch's tip commit.
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
// merge MUST keep failing on events and leave the store exactly as it was:
// HEAD unmoved, the peer's commit and beads absent, the local ignored events
// rows intact, no open merge, no recovery branches. A nil error here would
// be the worst outcome — a silent success over a commit the engine refuses,
// wedging the store.
func TestEmbeddedMergeAcrossEventsFlipWithoutBridge(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	mainBranch, peerTip := seedFlippedStoreWithUnpulledPeer(t, ctx, conn)

	headBefore := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	eventsBefore := eventRows(t, ctx, conn)
	for _, id := range []string{"evt-seed-1", "evt-local-1"} {
		if !slices.ContainsFunc(eventsBefore, func(row string) bool { return strings.HasPrefix(row, id+"|") }) {
			t.Fatalf("fixture lost local ignored event %s before the merge: %v", id, eventsBefore)
		}
	}

	err := versioncontrolops.MergeAndSettle(ctx, conn, "unpulled")
	t.Logf("MergeAndSettle(unpulled) = %v", err)
	if err == nil {
		t.Fatal("merging divergent pre-flip history into a flipped store must fail while no compatibility operation exists; a silent success wedges the store")
	}
	// The engine's own refusal: the ignored working events table would be
	// stomped by the peer's versioned one.
	for _, want := range []string{"stomped by merge", "events"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("merge failure missing %q; want the engine's original events stomp refusal:\n%v", want, err)
		}
	}
	if got := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1"); got != headBefore {
		t.Fatalf("failed merge moved HEAD: %s -> %s", headBefore, got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", peerTip); got != 0 {
		t.Fatalf("failed merge put peer tip %s in ancestry", peerTip)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = 'peer-1'"); got != 0 {
		t.Fatalf("peer beads landed by a failed merge = %d, want 0", got)
	}
	if got := eventRows(t, ctx, conn); !slices.Equal(got, eventsBefore) {
		t.Fatalf("failed merge changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, got)
	}
	if merging := scalarBool(t, ctx, conn, "SELECT is_merging FROM dolt_merge_status"); merging {
		t.Fatal("failed merge left a merge open")
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
	eventsBefore := eventRows(t, ctx, conn)

	if err := versioncontrolops.MergeAndSettle(ctx, conn, "postflip"); err != nil {
		t.Fatalf("post-flip peer must merge ordinarily: %v", err)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id = 'post-1'"); got != 1 {
		t.Fatalf("post-flip peer bead count = %d, want 1", got)
	}
	if got := eventRows(t, ctx, conn); !slices.Equal(got, eventsBefore) {
		t.Fatalf("ordinary merge changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, got)
	}
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('post-1', 'created', 'migrator', '2026-07-30 00:00:03', 'evt-post-1');
`)
	assertEventsAbsentFromStatus(t, ctx, conn)
}

// eventRows returns every working events row, ordered by id, as
// "id|issue_id|event_type|actor|created_at".
func eventRows(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT CONCAT_WS('|', id, issue_id, event_type, actor, created_at) FROM events ORDER BY id")
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatalf("scan events row: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate events: %v", err)
	}
	return out
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
