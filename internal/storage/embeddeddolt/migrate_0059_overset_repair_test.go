//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrateRepairsIsBlockedOverSetBy0059 pins gastownhall/beads#7037.
// On engines before go-mysql-server#3899 (dolt < 2.4.0, and bd's embedded
// engine before that pin) migration 0059's recursive step dropped its
// d.type = 'parent-child' filter (dolthub/dolt#11886), so stores that crossed
// it hold is_blocked = 1 on issues that are not blocked: they vanish from
// bd ready. 0059's bytes are frozen, so a store already past it must be
// repaired by a later migration. Seed a v59 store in the shape the buggy
// engine left behind and migrate it to the latest version: the over-set flag
// must be cleared, and genuinely blocked issues must stay blocked.
func TestEmbeddedMigrateRepairsIsBlockedOverSetBy0059(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 59)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	// ov-related's only edge is relates-to an open issue: never blocked, but
	// the buggy 0059 propagated blockedness across it. ov-child inherits a
	// real block through parent-child and must stay blocked.
	rows := []stampedIssue{
		{id: "ov-blocker", status: "open", stamp: historicalStamp(1)},
		{id: "ov-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
		{id: "ov-child", status: "open", stamp: historicalStamp(3), wantBlocked: true},
		{id: "ov-related", status: "open", stamp: historicalStamp(4)},
	}
	insertStampedIssues(t, ctx, conn, "issues", rows, true)
	insertIssueEdges(t, ctx, conn, [][3]string{
		{"ov-blocked", "ov-blocker", "blocks"},
		{"ov-child", "ov-blocked", "parent-child"},
		{"ov-related", "ov-blocked", "relates-to"},
	})
	if _, err := conn.ExecContext(ctx, "UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id = 'ov-related'"); err != nil {
		t.Fatalf("over-set is_blocked as 0059 did on dolt < 2.4.0: %v", err)
	}
	commitFixture(t, ctx, conn, "test: v59 store crossed 0059 on an engine with dolt#11886")

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	for _, r := range rows {
		if got := readIsBlocked(t, ctx, conn, "issues", r.id); got != r.wantBlocked {
			t.Errorf("issues %s: is_blocked = %v after upgrading past 0059, want %v", r.id, got, r.wantBlocked)
		}
	}
}

// TestEmbeddedMigrate0075RepairIsJournalNeutralAndOracleClean pins the rest of
// AC-1 for gastownhall/beads#7037 on the cursor shape real affected stores
// hold: a converged v74 main schema whose issue flags are already corrupt.
// The repair the migration pair runs must correct the flags (so the false
// blocker is ready work again, the genuine ones stay blocked), leave every
// row's updated_at where history put it, consume the durable obligation key
// it recorded, and do all of that WITHOUT emitting a journal row or moving
// the seq: a consumer tailing the journal must be able to learn that the
// migration is journal-neutral and refresh its readiness snapshot by other
// means (see the upgrade notes), not discover the flips as events.
func TestEmbeddedMigrate0075RepairIsJournalNeutralAndOracleClean(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 74)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	// A real store at main 74 holds a fully-caught-up ignored plane: the two
	// series advance together on every open. MigrateUpTo seeds the main plane
	// alone, and the issueops write paths below assume the local shape the
	// ignored series builds (lease columns, wisp split targets), so catch the
	// clone-local plane up to just below this repair's marker first — the
	// same position a clone whose main history arrives by pull holds.
	if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 31); err != nil {
		t.Fatalf("MigrateIgnoredUpTo: %v", err)
	}

	rows := []stampedIssue{
		{id: "ov-blocker", status: "open", stamp: historicalStamp(1)},
		{id: "ov-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
		{id: "ov-child", status: "open", stamp: historicalStamp(3), wantBlocked: true},
		{id: "ov-related", status: "open", stamp: historicalStamp(4)},
		{id: "ov-free", status: "open", stamp: historicalStamp(5)},
	}
	insertStampedIssues(t, ctx, conn, "issues", rows, true)
	insertIssueEdges(t, ctx, conn, [][3]string{
		{"ov-blocked", "ov-blocker", "blocks"},
		{"ov-child", "ov-blocked", "parent-child"},
		{"ov-related", "ov-blocked", "relates-to"},
	})

	// The over-set itself is 0059's deed, and a migration never journaled it;
	// set it the way the historical migration did, with a plain UPDATE.
	if _, err := conn.ExecContext(ctx, "UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id = 'ov-related'"); err != nil {
		t.Fatalf("over-set is_blocked as 0059 did on dolt < 2.4.0: %v", err)
	}

	// The journal rows a real store holds before this upgrade come from
	// ordinary user mutations, not from the migration that planted the
	// corruption: mint one through the journaled write path on an issue
	// outside the blocked graph, so the store holds a row a consumer could
	// already have tailed. The repair must correct the flags without
	// emitting a row or moving the seq.
	insertStampedIssues(t, ctx, conn, "issues", []stampedIssue{
		{id: "ov-watched", status: "open", stamp: historicalStamp(6)},
	}, false)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	unscope := issueops.ScopeEventsJournalTransaction(tx, true)
	if err := issueops.MergeMetadataInTx(ctx, tx, "ov-watched", "watched-by",
		json.RawMessage(`"consumer-cursor"`), "test"); err != nil {
		t.Fatalf("MergeMetadataInTx: %v", err)
	}
	unscope()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit journaled mutation: %v", err)
	}
	commitFixture(t, ctx, conn, "test: v74 store holds 0059-era over-set flags")

	journalCount, journalMaxSeq := readJournalState(t, ctx, conn)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	for _, r := range rows {
		if got := readIsBlocked(t, ctx, conn, "issues", r.id); got != r.wantBlocked {
			t.Errorf("issues %s: is_blocked = %v after the 0075/0032 repair, want %v", r.id, got, r.wantBlocked)
		}
		requireStamp(t, ctx, conn, "issues", r.id, r.stamp)
	}
	if corrected, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
		t.Fatalf("counting is_blocked inconsistencies: %v", err)
	} else if corrected != 0 {
		t.Errorf("full recompute still corrects %d row(s) after the migration; the repair left the store inconsistent", corrected)
	}

	gotCount, gotMaxSeq := readJournalState(t, ctx, conn)
	if gotCount != journalCount || gotMaxSeq != journalMaxSeq {
		t.Errorf("journal changed during the repair: %d row(s), max seq %d before MigrateUp; %d row(s), max seq %d after (the migration repair must not emit journal rows)",
			journalCount, journalMaxSeq, gotCount, gotMaxSeq)
	}

	var keyRows int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_repair_owed_0075'").Scan(&keyRows); err != nil {
		t.Fatalf("counting repair obligation key: %v", err)
	}
	if keyRows != 0 {
		t.Errorf("is_blocked_repair_owed_0075 still holds %d row(s) after the repair completed; the obligation must be consumed", keyRows)
	}
}

// readJournalState returns the journal's row count and highest seq, the two
// numbers a migration-repair pass must leave exactly where it found them.
func readJournalState(t *testing.T, ctx context.Context, conn *sql.Conn) (count int, maxSeq int64) {
	t.Helper()
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(MAX(seq), 0) FROM bd_events_journal").Scan(&count, &maxSeq); err != nil {
		t.Fatalf("reading journal state: %v", err)
	}
	return count, maxSeq
}
