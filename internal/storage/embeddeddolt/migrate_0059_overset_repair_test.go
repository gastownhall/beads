//go:build cgo

package embeddeddolt_test

import (
	"testing"

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
	if _, err := conn.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = 'ov-related'"); err != nil {
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
