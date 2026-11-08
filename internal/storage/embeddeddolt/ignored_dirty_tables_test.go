//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

const (
	trackedWispID      = "testdb-wisp-001"
	trackedWispTitle   = "live wisp title"
	trackedCommentID   = "testdb-wisp-comment-001"
	trackedCommentText = "live wisp comment payload"
)

// trackedIgnoredDirtyFixture builds the gastownhall/beads#5816 reporter's
// state in an isolated embedded database: wisps and wisp_comments are
// dolt-ignored but tracked at HEAD (the historical state of real clones), hold
// live uncommitted rows, and the clone-local ignored cursor has fallen behind
// ignored migration 0024, which touches wisp_comments.
//
// downgradeText makes 0024 perform real DDL on replay instead of its
// already-widened no-op. stageWisps leaves the dirty wisps table staged, so a
// migration commit that let staged ignored changes ride along would publish it.
func trackedIgnoredDirtyFixture(t *testing.T, downgradeText, stageWisps bool) (beadsDir, dataDir string) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	ctx := t.Context()
	beadsDir = filepath.Join(t.TempDir(), ".beads")
	dataDir = filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "testdb"); err != nil {
		store.Close()
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		store.Close()
		t.Fatalf("Commit (init): %v", err)
	}
	store.Close()

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()

	var queries []string
	if downgradeText {
		queries = append(queries, "ALTER TABLE wisp_comments MODIFY COLUMN text TEXT NOT NULL")
	}
	queries = append(queries,
		// Historically tracked wisp tables: only explicit force-staging
		// gets a dolt-ignored table into a commit.
		"CALL DOLT_ADD('-f', 'wisps')",
		"CALL DOLT_ADD('-f', 'wisp_comments')",
		"CALL DOLT_COMMIT('-m', 'test: track wisp tables')",
		"INSERT INTO wisps (id, title) VALUES ('"+trackedWispID+"', '"+trackedWispTitle+"')",
		"INSERT INTO wisp_comments (id, issue_id, author, text) VALUES ('"+trackedCommentID+"', '"+trackedWispID+"', 'agent', '"+trackedCommentText+"')",
	)
	if stageWisps {
		queries = append(queries, "CALL DOLT_ADD('-f', 'wisps')")
	}
	queries = append(queries, "DELETE FROM ignored_schema_migrations WHERE version >= 24")
	for _, q := range queries {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}

	for _, table := range []string{"wisps", "wisp_comments"} {
		var dirty int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_status WHERE table_name = ?", table,
		).Scan(&dirty); err != nil {
			t.Fatalf("dolt_status %s: %v", table, err)
		}
		if dirty == 0 {
			t.Fatalf("fixture: %s is not dirty in dolt_status", table)
		}
	}
	assertTrackedWispRowsAbsentAtHead(t, ctx, db)
	return beadsDir, dataDir
}

func assertTrackedWispRowsAbsentAtHead(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		"SELECT COUNT(*) FROM wisps AS OF 'HEAD' WHERE id = '" + trackedWispID + "'",
		"SELECT COUNT(*) FROM wisp_comments AS OF 'HEAD' WHERE id = '" + trackedCommentID + "'",
	} {
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Fatalf("%s = %d, want 0 (live ignored rows must not be committed)", q, n)
		}
	}
}

func ignoredCursorVersion(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations",
	).Scan(&version); err != nil {
		t.Fatalf("read ignored cursor: %v", err)
	}
	return version
}

// TestEmbeddedIgnoredTrackedDirtyMigrationOpens covers gastownhall/beads#5816
// AC-1: live rows in tracked, dolt-ignored wisp tables are clone-local data
// the migration commit never stages, so a pending ignored migration touching
// them must not refuse an ordinary open.
func TestEmbeddedIgnoredTrackedDirtyMigrationOpens(t *testing.T) {
	for _, tc := range []struct {
		name          string
		downgradeText bool
	}{
		{name: "already widened"},
		{name: "0024 performs real DDL", downgradeText: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			beadsDir, dataDir := trackedIgnoredDirtyFixture(t, tc.downgradeText, false)

			store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
			if err != nil {
				t.Fatalf("Open = %v, want the pending ignored migrations to apply over live wisp rows", err)
			}
			store.Close()

			db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer func() { _ = cleanup() }()
			if got, want := ignoredCursorVersion(t, ctx, db), schema.LatestIgnoredVersion(); got != want {
				t.Fatalf("ignored cursor = %d, want %d", got, want)
			}
			var columnType string
			if err := db.QueryRowContext(ctx, `
				SELECT COLUMN_TYPE FROM INFORMATION_SCHEMA.COLUMNS
				WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_comments' AND COLUMN_NAME = 'text'`,
			).Scan(&columnType); err != nil {
				t.Fatalf("read wisp_comments.text type: %v", err)
			}
			if columnType != "longtext" {
				t.Fatalf("wisp_comments.text = %q, want longtext after 0024", columnType)
			}
		})
	}
}
