//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

const (
	trackedWispID          = "testdb-wisp-001"
	trackedWispTitle       = "live wisp title"
	trackedWispDescription = "live wisp description"
	trackedCommentID       = "testdb-wisp-comment-001"
	trackedCommentText     = "live wisp comment payload"
)

// trackedIgnoredDirtyFixture builds the gastownhall/beads#5816 reporter's
// state in an isolated embedded database: wisps and wisp_comments are
// dolt-ignored but tracked at HEAD (the historical state of real clones), hold
// live uncommitted rows, and the clone-local ignored cursor has fallen behind
// ignored migration 0024, which touches wisp_comments.
//
// downgradeText makes 0024 perform real DDL on replay instead of its
// already-widened no-op. stage leaves the named dirty tables staged, so a
// migration commit that let staged ignored changes ride along would publish them.
func trackedIgnoredDirtyFixture(t *testing.T, downgradeText bool, stage ...string) (beadsDir, dataDir string) {
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
		"INSERT INTO wisps (id, title, description) VALUES ('"+trackedWispID+"', '"+trackedWispTitle+"', '"+trackedWispDescription+"')",
		"INSERT INTO wisp_comments (id, issue_id, author, text) VALUES ('"+trackedCommentID+"', '"+trackedWispID+"', 'agent', '"+trackedCommentText+"')",
	)
	for _, table := range stage {
		queries = append(queries, "CALL DOLT_ADD('-f', '"+table+"')")
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

// assertNoStagedTrackedWispTables fails when a wisp table delta is still
// staged, where the next commit of any kind would publish it.
func assertNoStagedTrackedWispTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var staged int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status WHERE staged = true AND table_name IN ('wisps', 'wisp_comments')",
	).Scan(&staged); err != nil {
		t.Fatalf("count staged wisp tables: %v", err)
	}
	if staged != 0 {
		t.Fatalf("%d wisp table delta(s) still staged after migration, want 0", staged)
	}
}

func wispCommentsTextType(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var columnType string
	if err := db.QueryRowContext(ctx, `
		SELECT COLUMN_TYPE FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisp_comments' AND COLUMN_NAME = 'text'`,
	).Scan(&columnType); err != nil {
		t.Fatalf("read wisp_comments.text type: %v", err)
	}
	return columnType
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
			beadsDir, dataDir := trackedIgnoredDirtyFixture(t, tc.downgradeText)

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
			if columnType := wispCommentsTextType(t, ctx, db); columnType != "longtext" {
				t.Fatalf("wisp_comments.text = %q, want longtext after 0024", columnType)
			}
		})
	}
}

// trackedWispRowSnapshot renders the fixture's wisp and comment rows, payloads
// and timestamps included, so a migration that rewrote or re-created them
// shows up as a difference rather than passing a row count.
func trackedWispRowSnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var wisp, comment string
	if err := db.QueryRowContext(ctx,
		"SELECT CONCAT_WS('|', id, title, description, CAST(created_at AS CHAR)) FROM wisps WHERE id = ?", trackedWispID,
	).Scan(&wisp); err != nil {
		t.Fatalf("read wisp %s: %v", trackedWispID, err)
	}
	if err := db.QueryRowContext(ctx,
		"SELECT CONCAT_WS('|', id, issue_id, author, text, CAST(created_at AS CHAR)) FROM wisp_comments WHERE id = ?", trackedCommentID,
	).Scan(&comment); err != nil {
		t.Fatalf("read wisp comment %s: %v", trackedCommentID, err)
	}
	return wisp + "\n" + comment
}

// TestEmbeddedIgnoredTrackedDirtyMigrationPreservesRows covers
// gastownhall/beads#5816 AC-2: the migration over live wisp rows keeps them
// byte-for-byte in the working set and never commits them, even when a dirty
// wisp table was staged before the pass, and leaves no wisp delta staged.
func TestEmbeddedIgnoredTrackedDirtyMigrationPreservesRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage []string
	}{
		{name: "unstaged"},
		{name: "pre-staged wisps", stage: []string{"wisps"}},
		{name: "pre-staged wisp_comments", stage: []string{"wisp_comments"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			beadsDir, dataDir := trackedIgnoredDirtyFixture(t, true, tc.stage...)

			db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			before := trackedWispRowSnapshot(t, ctx, db)
			_ = cleanup()

			store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			store.Close()

			db, cleanup, err = embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
			if err != nil {
				t.Fatalf("OpenSQL (post-open): %v", err)
			}
			defer func() { _ = cleanup() }()
			if after := trackedWispRowSnapshot(t, ctx, db); after != before {
				t.Fatalf("wisp rows after migration =\n%s\nwant unchanged\n%s", after, before)
			}
			if columnType := wispCommentsTextType(t, ctx, db); columnType != "longtext" {
				t.Fatalf("wisp_comments.text = %q, want longtext after 0024", columnType)
			}
			assertTrackedWispRowsAbsentAtHead(t, ctx, db)
			assertNoStagedTrackedWispTables(t, ctx, db)
		})
	}
}

const (
	committableParentID = "testdb-parent"
	committableWispID   = "testdb-unrelated-wisp"
)

// committableIgnoredDirtyFixture builds a fully seeded embedded database whose
// committable child_counters table holds an uncommitted row for a durable
// (committed, non-wisp) parent, while the untracked ignored cursor sits at 1,
// so pending ignored migration 0002 touches child_counters. An unrelated
// dirty wisp rides along to prove nothing publishes it. rewindMain also leaves
// the latest main migration pending (it does not touch child_counters, so only
// the ignored-series intersection can refuse).
func committableIgnoredDirtyFixture(t *testing.T, rewindMain bool) (beadsDir, dataDir string) {
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
	parent := &types.Issue{
		ID:        committableParentID,
		Title:     "durable parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "tester"); err != nil {
		store.Close()
		t.Fatalf("CreateIssue(parent): %v", err)
	}
	if _, err := store.CommitAll(ctx, "test: durable parent"); err != nil {
		store.Close()
		t.Fatalf("CommitAll: %v", err)
	}
	store.Close()

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	queries := []string{
		"INSERT INTO child_counters (parent_id, last_child) VALUES ('" + committableParentID + "', 7) " +
			"ON DUPLICATE KEY UPDATE last_child = 7",
		"INSERT INTO wisps (id, title) VALUES ('" + committableWispID + "', 'unrelated wisp')",
		"DELETE FROM ignored_schema_migrations WHERE version > 1",
	}
	if rewindMain {
		queries = append(queries, fmt.Sprintf("DELETE FROM schema_migrations WHERE version >= %d", schema.LatestVersion()))
	}
	for _, q := range queries {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	return beadsDir, dataDir
}

// committableIgnoredDirtySnapshot renders everything the refused pass must
// leave alone: both cursors row by row, HEAD, the dirty child_counters row
// and the unrelated wisp.
func committableIgnoredDirtySnapshot(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		"SELECT CONCAT_WS(':', version, COALESCE(content_hash, '')) FROM schema_migrations ORDER BY version",
		"SELECT CONCAT_WS(':', version, COALESCE(content_hash, '')) FROM ignored_schema_migrations ORDER BY version",
		"SELECT HASHOF('HEAD')",
		"SELECT CONCAT_WS('|', parent_id, last_child) FROM child_counters WHERE parent_id = '" + committableParentID + "'",
		"SELECT CONCAT_WS('|', id, title) FROM wisps WHERE id = '" + committableWispID + "'",
	} {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				t.Fatalf("%s: %v", q, err)
			}
			b.WriteString(s)
			b.WriteByte('\n')
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		b.WriteString("--\n")
	}
	return b.String()
}

// TestEmbeddedIgnoredCommittableDirtyRefusesBeforeMigrating covers
// gastownhall/beads#5816 AC-3: a pending ignored migration that touches dirty
// committable data refuses with the typed *schema.DirtyTablesError (the one
// the working-set-reconcile open tolerates) before any numbered migration
// runs, leaving both cursors, HEAD and the user rows unchanged.
func TestEmbeddedIgnoredCommittableDirtyRefusesBeforeMigrating(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rewindMain bool
	}{
		{name: "ignored series pending"},
		{name: "main series pending too", rewindMain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			beadsDir, dataDir := committableIgnoredDirtyFixture(t, tc.rewindMain)

			db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			before := committableIgnoredDirtySnapshot(t, ctx, db)
			_ = cleanup()

			store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
			if err == nil {
				store.Close()
				t.Fatal("Open = nil, want *schema.DirtyTablesError for dirty child_counters")
			}
			var dirtyErr *schema.DirtyTablesError
			if !errors.As(err, &dirtyErr) {
				t.Fatalf("Open error = %T (%v), want error wrapping *schema.DirtyTablesError", err, err)
			}
			if !slices.Equal(dirtyErr.Tables, []string{"child_counters"}) {
				t.Fatalf("DirtyTablesError.Tables = %v, want [child_counters]", dirtyErr.Tables)
			}

			db, cleanup, err = embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
			if err != nil {
				t.Fatalf("OpenSQL (post-refusal): %v", err)
			}
			defer func() { _ = cleanup() }()
			if after := committableIgnoredDirtySnapshot(t, ctx, db); after != before {
				t.Fatalf("state after refusal =\n%s\nwant unchanged\n%s", after, before)
			}
		})
	}
}

// TestEmbeddedIgnoredCommittableDirtyReconcileRecovers covers
// gastownhall/beads#5816 AC-4: the AC-3 refusal is recoverable in-band. The
// working-set-reconcile open behind "bd dolt commit" warns and opens, its
// CommitAll commits the dirty child_counters row (and only committable data),
// and the next ordinary open converges both series without losing that row
// or publishing the unrelated dirty wisp.
func TestEmbeddedIgnoredCommittableDirtyReconcileRecovers(t *testing.T) {
	ctx := t.Context()
	beadsDir, dataDir := committableIgnoredDirtyFixture(t, false)

	var (
		reconcile *embeddeddolt.EmbeddedDoltStore
		openErr   error
	)
	warning := captureStderr(t, func() {
		reconcile, openErr = embeddeddolt.OpenForWorkingSetReconcile(ctx, beadsDir, "testdb", "main")
	})
	if openErr != nil {
		t.Fatalf("OpenForWorkingSetReconcile = %v, want the DirtyTablesError refusal tolerated", openErr)
	}
	if !strings.Contains(warning, "Warning:") || !strings.Contains(warning, "child_counters") {
		reconcile.Close()
		t.Fatalf("reconcile open stderr = %q, want a warning naming child_counters", warning)
	}
	committed, err := reconcile.CommitAll(ctx, "reconcile working set")
	reconcile.Close()
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !committed {
		t.Fatal("CommitAll committed = false, want the dirty child_counters row committed")
	}

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open (post-commit) = %v, want migrations to converge", err)
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
	// Read through db: a second OpenSQL would wait on the one held here.
	var mainVersion int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&mainVersion); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if mainVersion != schema.LatestVersion() {
		t.Fatalf("schema version = %d, want %d", mainVersion, schema.LatestVersion())
	}
	for _, q := range []string{
		"SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = '" + committableParentID + "'",
		"SELECT last_child FROM child_counters WHERE parent_id = '" + committableParentID + "'",
	} {
		var lastChild int
		if err := db.QueryRowContext(ctx, q).Scan(&lastChild); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if lastChild != 7 {
			t.Fatalf("%s = %d, want 7", q, lastChild)
		}
	}
	var working, atHead int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisps WHERE id = '"+committableWispID+"'").Scan(&working); err != nil {
		t.Fatalf("count working wisp: %v", err)
	}
	// An untracked wisps table is absent from HEAD altogether, which also
	// keeps the row out of it.
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisps AS OF 'HEAD' WHERE id = '"+committableWispID+"'").Scan(&atHead); err != nil &&
		!strings.Contains(err.Error(), "table not found") {
		t.Fatalf("count committed wisp: %v", err)
	}
	if working != 1 || atHead != 0 {
		t.Fatalf("unrelated wisp: working = %d, at HEAD = %d; want 1 and 0 (it must stay local)", working, atHead)
	}
}
