//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

func TestAggregateRevisionMigrationBackfillsIssuesAndWisps(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dir := seedMainSchemaAt(t, ctx, 65)
	conn, closeConn := openPinnedConn(t, ctx, dir)
	defer closeConn()

	for _, table := range []string{"issues", "wisps"} {
		execFrozenGuard(t, ctx, conn,
			fmt.Sprintf("INSERT INTO %s (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, row_lock) VALUES ('backfill-%s', 'legacy', '', '', '', '', 'open', 2, 'task', 0)", table, table))
	}
	mustDrain(t, ctx, conn, "CALL DOLT_ADD('-A')")
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'test: seed legacy aggregate revisions')")
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	for _, table := range []string{"issues", "wisps"} {
		var rowLock int64
		if err := conn.QueryRowContext(ctx, "SELECT row_lock FROM "+table+" WHERE id = ?", "backfill-"+table).Scan(&rowLock); err != nil {
			t.Fatalf("read %s row_lock: %v", table, err)
		}
		if rowLock == 0 {
			t.Fatalf("%s legacy row_lock remained zero", table)
		}
	}
}
