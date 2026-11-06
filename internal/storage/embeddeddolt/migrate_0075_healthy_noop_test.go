//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrate0075HealthyStoreNoOpsThroughReopen pins the healthy-store
// half of the is_blocked repair (gastownhall/beads#7037): a store whose flags
// were never wrong must come out of the 0075/0032 upgrade observably
// unchanged. The repair still runs on it — 0075 cannot know a store is
// healthy without the recompute it obligates — so the pass mints the
// obligation key, corrects zero rows, and consumes the key, all without
// touching a flag, a timestamp, or the journal. The key's consumption must be
// PUBLISHED, not merely deleted from the working set (an unpublished
// deletion would resurface as owed on any open that reads HEAD), and a
// reopen must find no work at all: no second repair commit, no journal
// movement, no flag churn.
//
// Each withRawConn opens and closes its own engine handle on the data
// directory, so the second block is a true reopen: the migration state the
// first pass published is everything the second handle can see.
func TestEmbeddedMigrate0075HealthyStoreNoOpsThroughReopen(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 74)

	// Healthy graph: hn-blocked really is blocked by hn-blocker, hn-related
	// only relates to it and is clear, hn-watched sits outside the graph and
	// carries the journal row a consumer could already have tailed. A real
	// store at main 74 also holds a fully-caught-up ignored plane, and the
	// journaled mint needs the clone-local shape that series builds (lease
	// columns, wisp split targets).
	rows := []stampedIssue{
		{id: "hn-blocker", status: "open", stamp: historicalStamp(1)},
		{id: "hn-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
		{id: "hn-related", status: "open", stamp: historicalStamp(3)},
		{id: "hn-watched", status: "open", stamp: historicalStamp(4)},
	}
	var journalCount int
	var journalMaxSeq int64
	var openCommits int
	// The journaled mint is an ordinary user mutation, so it re-stamps
	// hn-watched's updated_at — that stamp (not the fixture's historical one)
	// is the baseline the migration must preserve.
	var watchedStamp time.Time
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 31); err != nil {
			t.Fatalf("MigrateIgnoredUpTo: %v", err)
		}
		insertStampedIssues(t, ctx, conn, "issues", rows, true)
		insertIssueEdges(t, ctx, conn, [][3]string{
			{"hn-blocked", "hn-blocker", "blocks"},
			{"hn-related", "hn-blocked", "relates-to"},
		})
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		unscope := issueops.ScopeEventsJournalTransaction(tx, true)
		if err := issueops.MergeMetadataInTx(ctx, tx, "hn-watched", "watched-by",
			json.RawMessage(`"consumer-cursor"`), "test"); err != nil {
			t.Fatalf("MergeMetadataInTx: %v", err)
		}
		unscope()
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit journaled mutation: %v", err)
		}
		commitFixture(t, ctx, conn, "test: healthy v74 store")
		journalCount, journalMaxSeq = readJournalState(t, ctx, conn)
		watchedStamp = readUpdatedAt(t, ctx, conn, "issues", "hn-watched")

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp: %v", err)
		}

		for _, r := range rows {
			if got := readIsBlocked(t, ctx, conn, "issues", r.id); got != r.wantBlocked {
				t.Errorf("issues %s: is_blocked = %v after the healthy-store pass, want %v", r.id, got, r.wantBlocked)
			}
			if r.id != "hn-watched" {
				requireStamp(t, ctx, conn, "issues", r.id, r.stamp)
			}
		}
		requireStamp(t, ctx, conn, "issues", "hn-watched", watchedStamp)
		if corrected, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
			t.Fatalf("counting is_blocked inconsistencies: %v", err)
		} else if corrected != 0 {
			t.Errorf("full recompute corrects %d row(s) on a healthy store", corrected)
		}
		if gotCount, gotMaxSeq := readJournalState(t, ctx, conn); gotCount != journalCount || gotMaxSeq != journalMaxSeq {
			t.Errorf("journal moved during the healthy-store pass: %d row(s), max seq %d before MigrateUp; %d row(s), max seq %d after",
				journalCount, journalMaxSeq, gotCount, gotMaxSeq)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 75 {
			t.Fatalf("main cursor = %d after MigrateUp, want 75", got)
		}
		if !ignoredCursorHas(t, ctx, conn, 32) {
			t.Fatal("ignored cursor does not record 0032 after the healthy-store pass")
		}
		requireKeyConsumedAtHead(t, ctx, conn)
		openCommits = scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")
	})

	// Reopen: no work is pending, so the pass must not exist — no repair
	// commit, no journal movement, no flag churn, and the consumed key stays
	// consumed at HEAD.
	withRawConn(t, dataDir, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (reopen): %v", err)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log"); got != openCommits {
			t.Errorf("reopen minted %d commit(s) (dolt_log %d -> %d) on a healthy, current store",
				got-openCommits, openCommits, got)
		}
		for _, r := range rows {
			if got := readIsBlocked(t, ctx, conn, "issues", r.id); got != r.wantBlocked {
				t.Errorf("issues %s: is_blocked = %v after reopen, want %v", r.id, got, r.wantBlocked)
			}
			if r.id != "hn-watched" {
				requireStamp(t, ctx, conn, "issues", r.id, r.stamp)
			}
		}
		requireStamp(t, ctx, conn, "issues", "hn-watched", watchedStamp)
		if gotCount, gotMaxSeq := readJournalState(t, ctx, conn); gotCount != journalCount || gotMaxSeq != journalMaxSeq {
			t.Errorf("journal moved during reopen: %d row(s), max seq %d; want %d, %d",
				gotCount, gotMaxSeq, journalCount, journalMaxSeq)
		}
		requireKeyConsumedAtHead(t, ctx, conn)
	})
}

// requireKeyConsumedAtHead asserts the 0075 obligation key is gone from the
// working set AND from committed history. A working-set-only deletion would
// read as consumed here-and-now but resurface as owed on any open that reads
// HEAD, so both reads must agree on zero.
func requireKeyConsumedAtHead(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	for _, scope := range []string{"", " AS OF 'HEAD'"} {
		var keyRows int
		if err := conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM metadata"+scope+" WHERE `key` = 'is_blocked_repair_owed_0075'").Scan(&keyRows); err != nil {
			t.Fatalf("counting repair obligation key (scope %q): %v", scope, err)
		}
		if keyRows != 0 {
			t.Errorf("is_blocked_repair_owed_0075 holds %d row(s) (scope %q) after the healthy-store pass; the obligation must be consumed and published", keyRows, scope)
		}
	}
}
