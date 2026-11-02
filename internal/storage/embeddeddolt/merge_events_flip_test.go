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
// until it ships these tests pin what is bd's to own while this clone's
// ignored working events table is present: the engine refuses the merge as
// a stomp of that table before any merge opens, and the ordinary merge/pull
// path RETAINS that failure with no history damage and every local ignored
// events row intact.
//
// Migrating the peer's history first avoids that pair altogether: once it has
// gone through the same migration, both sides of the three-way have deleted
// the versioned table, and the ordinary merge takes it. That is the recovery
// docs/recovery/sync-failures.md gives after a non-fast-forward rejection.
//
// The refusal pins cover only that stomp shape. Once the working events
// table is gone (DOLT_CHECKOUT('--','events') deletes it), the same merge
// opens with an events schema conflict that dolt_conflicts does not list and
// SettleMerge does not check, so MergeAndSettle returns nil and leaves the
// merge open; nothing here covers that shape.

// seedFlippedStoreWithUnpulledPeer builds the #6727 fixture: a store whose
// main has migrated through 0062 (events on the ignore plane, one local
// unversioned row) and a divergent pre-flip branch holding one new bead and
// one more committed events row. It returns the active branch name and the
// peer branch's tip commit. The working set it leaves is clean, as Pull's
// CommitPending leaves it before every merge.
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
	migrateThroughFlip(t, ctx, conn)
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('merge-1', 'updated', 'migrator', '2026-07-30 00:00:02', 'evt-local-1');
`)
	return mainBranch, peerTip
}

// migrateThroughFlip migrates the checked-out branch through 0062. MigrateUpTo
// stops before MigrateUp's closing commit, leaving dolt_ignore and
// schema_migrations modified; commit them as MigrateUp does.
func migrateThroughFlip(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	if _, err := schema.MigrateUpTo(ctx, conn, 62); err != nil {
		t.Fatalf("MigrateUpTo(62): %v", err)
	}
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'schema: apply migrations')")
}

// TestEmbeddedMergeAcrossEventsFlipWithoutBridge runs the ordinary embedded
// pull/merge path (PullFrom's MergeAndSettle) against the divergent
// pre-flip peer. With no driver-owned retired-events bridge available the
// merge MUST keep failing with the engine's events stomp refusal and leave
// the store exactly as it was: HEAD unmoved, the peer's commit and beads
// absent, the local ignored events rows intact, no open merge, no recovery
// branches. A nil error here would be the worst outcome — a silent success
// over a commit the engine refuses, wedging the store.
//
// The merge starts on a clean working set, as it does after Pull's
// CommitPending, so the refused merge takes abortMerge's DOLT_RESET('--hard')
// fallback; the rows-intact assertion pins that the fallback keeps the
// ignored working events table.
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
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_status"); got != 0 {
		t.Fatalf("dolt_status rows before the merge = %d, want 0: a dirty working set skips abortMerge's hard-reset fallback", got)
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

// TestEmbeddedMergeAcrossEventsFlipFromAnAncestor pins the push-first triage
// of the events recovery runbook. The engine checks for stomped working
// tables before it checks whether there is anything to merge, so a flipped
// store refuses even a pre-flip commit its own history already contains (a
// remote that is merely behind this clone), with no peer work involved. A
// fast-forward push moves that ref to this clone's HEAD, and merging it is
// then up to date.
func TestEmbeddedMergeAcrossEventsFlipFromAnAncestor(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	mainBranch, _ := seedFlippedStoreWithUnpulledPeer(t, ctx, conn)

	// The remote as this clone last pulled it: the pre-flip commit the peer
	// branched from, an ancestor of HEAD.
	preFlip := scalarString(t, ctx, conn, "SELECT DOLT_MERGE_BASE('"+mainBranch+"', 'unpulled')")
	mustDrain(t, ctx, conn, "CALL DOLT_BRANCH('behind', '"+preFlip+"')")
	headBefore := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	eventsBefore := eventRows(t, ctx, conn)

	err := versioncontrolops.MergeAndSettle(ctx, conn, "behind")
	if err == nil || !strings.Contains(err.Error(), "stomped by merge") || !strings.Contains(err.Error(), "events") {
		t.Fatalf("merging the pre-flip ancestor = %v; want the engine's events stomp refusal", err)
	}
	if got := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1"); got != headBefore {
		t.Fatalf("refused merge moved HEAD: %s -> %s", headBefore, got)
	}
	if got := eventRows(t, ctx, conn); !slices.Equal(got, eventsBefore) {
		t.Fatalf("refused merge changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, got)
	}

	// The push: a fast-forward moves the remote, and so the ref the next pull
	// merges, to this clone's HEAD.
	mustDrain(t, ctx, conn, "CALL DOLT_BRANCH('-f', 'behind', '"+mainBranch+"')")
	if err := versioncontrolops.MergeAndSettle(ctx, conn, "behind"); err != nil {
		t.Fatalf("merging a ref at HEAD after the push must be up to date: %v", err)
	}
	if got := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1"); got != headBefore {
		t.Fatalf("up-to-date merge moved HEAD: %s -> %s", headBefore, got)
	}
	if got := eventRows(t, ctx, conn); !slices.Equal(got, eventsBefore) {
		t.Fatalf("up-to-date merge changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, got)
	}
}

// TestEmbeddedMergeAcrossEventsFlipAfterPeerMigrates pins the recovery the
// events runbook gives after a non-fast-forward rejection: the peer's history
// goes through the same migration first (on the peer, or on a fresh clone of
// the remote, which pushes it), and the ordinary merge then takes it. Both
// sides of the three-way have deleted the versioned events table by then, so
// nothing is stomped and nothing conflicts: the peer's commits and bead
// arrive in a two-parent merge, events stays off HEAD, and the local ignored
// events rows are untouched. The peer's own audit rows stay in its working
// set, because events rows do not sync after 0062.
func TestEmbeddedMergeAcrossEventsFlipAfterPeerMigrates(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMainSchemaAt(t, ctx, 61)
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	mainBranch, peerTip := seedFlippedStoreWithUnpulledPeer(t, ctx, conn)
	eventsBefore := eventRows(t, ctx, conn)

	err := versioncontrolops.MergeAndSettle(ctx, conn, "unpulled")
	if err == nil || !strings.Contains(err.Error(), "stomped by merge") || !strings.Contains(err.Error(), "events") {
		t.Fatalf("merging the un-migrated peer = %v; want the engine's events stomp refusal", err)
	}

	// The peer's history is migrated where it lives, as `bd migrate schema`
	// there would, and that is what the next pull merges.
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('unpulled')")
	migrateThroughFlip(t, ctx, conn)
	peerMigrated := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	mustDrain(t, ctx, conn, "CALL DOLT_CHECKOUT('"+mainBranch+"')")

	if err := versioncontrolops.MergeAndSettle(ctx, conn, "unpulled"); err != nil {
		t.Fatalf("merging the migrated peer history must succeed: %v", err)
	}
	head := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = ?", head); got != 2 {
		t.Fatalf("merge result HEAD has %d parents, want 2 (a merge commit)", got)
	}
	for _, commit := range []string{peerTip, peerMigrated} {
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log WHERE commit_hash = ?", commit); got != 1 {
			t.Fatalf("peer commit %s missing from the merged ancestry", commit)
		}
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id IN ('merge-1', 'peer-1')"); got != 2 {
		t.Fatalf("beads after the merge = %d, want 2 (merge-1 and peer-1)", got)
	}
	if merging := scalarBool(t, ctx, conn, "SELECT is_merging FROM dolt_merge_status"); merging {
		t.Fatal("merge of the migrated peer history left a merge open")
	}
	for _, query := range []string{
		"SELECT COUNT(*) FROM dolt_conflicts",
		"SELECT COUNT(*) FROM dolt_schema_conflicts",
		"SELECT COUNT(*) FROM dolt_status",
	} {
		if got := scalarInt(t, ctx, conn, query); got != 0 {
			t.Fatalf("%s = %d after the merge, want 0", query, got)
		}
	}
	if got := eventRows(t, ctx, conn); !slices.Equal(got, eventsBefore) {
		t.Fatalf("merge changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, got)
	}
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM events AS OF 'HEAD'").Scan(new(int)); err == nil {
		t.Fatal("events exists at HEAD after the merge; want it kept off committed history")
	}
	execFrozenGuard(t, ctx, conn, `
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('peer-1', 'updated', 'migrator', '2026-07-30 00:00:03', 'evt-after-recovery');
`)
	assertEventsAbsentFromStatus(t, ctx, conn)
}

// TestEmbeddedMergeAfterEventsFlipOrdinary pins that the retained failure is
// specific to the retired-events shape: peer history created AFTER the flip
// — no versioned events anywhere in the three-way — merges through the same
// path as ordinary work, and event writes keep working. Both sides commit
// after the flip, so the merge is a true three-way merge (a two-parent
// commit), the path a pull between two migrated clones takes, not a
// fast-forward.
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
	// This clone's own post-flip work, so main has moved since postflip
	// branched and the merge cannot fast-forward.
	execFrozenGuard(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('local-1', 'local post-flip work', '', '', '', '', 'open', 2, 'task');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: local post-flip work')")
	eventsBefore := eventRows(t, ctx, conn)

	if err := versioncontrolops.MergeAndSettle(ctx, conn, "postflip"); err != nil {
		t.Fatalf("post-flip peer must merge ordinarily: %v", err)
	}
	head := scalarString(t, ctx, conn, "SELECT commit_hash FROM dolt_log LIMIT 1")
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = ?", head); got != 2 {
		t.Fatalf("merge result HEAD has %d parents, want 2 (a three-way merge commit, not a fast-forward)", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM issues WHERE id IN ('post-1', 'local-1')"); got != 2 {
		t.Fatalf("beads from both sides after the merge = %d, want 2 (post-1 and local-1)", got)
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
