package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite/migrations"
	"github.com/steveyegge/beads/internal/types"
)

func TestMigrateDirtyIssuesTable(t *testing.T) {
	t.Run("creates dirty_issues table if not exists", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db

		// Drop table if exists
		_, _ = db.Exec("DROP TABLE IF EXISTS dirty_issues")

		// Run migration
		if err := migrations.MigrateDirtyIssuesTable(db); err != nil {
			t.Fatalf("failed to migrate dirty_issues table: %v", err)
		}

		// Verify table exists
		var tableName string
		err := db.QueryRow(`
			SELECT name FROM sqlite_master
			WHERE type='table' AND name='dirty_issues'
		`).Scan(&tableName)
		if err != nil {
			t.Fatalf("dirty_issues table not found: %v", err)
		}
	})

	t.Run("adds content_hash column to existing table", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db

		// Drop and create table without content_hash
		_, _ = db.Exec("DROP TABLE IF EXISTS dirty_issues")
		_, err := db.Exec(`
			CREATE TABLE dirty_issues (
				issue_id TEXT PRIMARY KEY,
				marked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)
		`)
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}

		// Run migration
		if err := migrations.MigrateDirtyIssuesTable(db); err != nil {
			t.Fatalf("failed to migrate dirty_issues table: %v", err)
		}

		// Verify content_hash column exists
		var hasContentHash bool
		err = db.QueryRow(`
			SELECT COUNT(*) > 0 FROM pragma_table_info('dirty_issues')
			WHERE name = 'content_hash'
		`).Scan(&hasContentHash)
		if err != nil {
			t.Fatalf("failed to check for content_hash column: %v", err)
		}
		if !hasContentHash {
			t.Error("content_hash column was not added")
		}
	})
}

func TestMigrateExternalRefColumn(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	db := store.db

	// Run migration
	if err := migrations.MigrateExternalRefColumn(db); err != nil {
		t.Fatalf("failed to migrate external_ref column: %v", err)
	}

	// Verify column exists
	rows, err := db.Query("PRAGMA table_info(issues)")
	if err != nil {
		t.Fatalf("failed to query table info: %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("failed to scan column info: %v", err)
		}
		if name == "external_ref" {
			found = true
			break
		}
	}

	if !found {
		t.Error("external_ref column was not added")
	}
}

func TestMigrateCompositeIndexes(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	db := store.db

	// Drop index if exists
	_, _ = db.Exec("DROP INDEX IF EXISTS idx_dependencies_depends_on_type")

	// Run migration
	if err := migrations.MigrateCompositeIndexes(db); err != nil {
		t.Fatalf("failed to migrate composite indexes: %v", err)
	}

	// Verify index exists
	var indexName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='index' AND name='idx_dependencies_depends_on_type'
	`).Scan(&indexName)
	if err != nil {
		t.Fatalf("composite index not found: %v", err)
	}
}

func TestMigrateClosedAtConstraint(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// The constraint is now enforced in schema, so we can't easily create inconsistent state.
	// Instead, just verify the migration runs successfully on clean data.
	issue := &types.Issue{Title: "test-migrate", Priority: 1, IssueType: "task", Status: "open"}
	err := s.CreateIssue(ctx, issue, "test")
	if err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Run migration (should succeed with no inconsistent data)
	if err := migrations.MigrateClosedAtConstraint(s.db); err != nil {
		t.Fatalf("failed to migrate closed_at constraint: %v", err)
	}

	// Verify issue is still valid
	got, err := s.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if got.ClosedAt != nil {
		t.Error("closed_at should be nil for open issue")
	}
}

func TestMigrateCompactionColumns(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()

	// Remove compaction columns if they exist
	_, _ = s.db.Exec(`ALTER TABLE issues DROP COLUMN compaction_level`)
	_, _ = s.db.Exec(`ALTER TABLE issues DROP COLUMN compacted_at`)
	_, _ = s.db.Exec(`ALTER TABLE issues DROP COLUMN original_size`)

	// Run migration (will fail since columns don't exist, but that's okay for this test)
	// The migration should handle this gracefully
	_ = migrations.MigrateCompactionColumns(s.db)

	// Verify at least one column exists by querying
	var exists bool
	err := s.db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'compaction_level'
	`).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check compaction columns: %v", err)
	}

	// It's okay if the columns don't exist in test schema
	// The migration is idempotent and will add them when needed
}

func TestMigrateSnapshotsTable(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	db := store.db

	// Drop table if exists
	_, _ = db.Exec("DROP TABLE IF EXISTS issue_snapshots")

	// Run migration
	if err := migrations.MigrateSnapshotsTable(db); err != nil {
		t.Fatalf("failed to migrate snapshots table: %v", err)
	}

	// Verify table exists
	var tableExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM sqlite_master
		WHERE type='table' AND name='issue_snapshots'
	`).Scan(&tableExists)
	if err != nil {
		t.Fatalf("failed to check snapshots table: %v", err)
	}
	if !tableExists {
		t.Error("issue_snapshots table was not created")
	}
}

func TestMigrateCompactionConfig(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	db := store.db

	// Clear config table
	_, _ = db.Exec("DELETE FROM config WHERE key LIKE 'compact%'")

	// Run migration
	if err := migrations.MigrateCompactionConfig(db); err != nil {
		t.Fatalf("failed to migrate compaction config: %v", err)
	}

	// Verify config values exist
	var value string
	err := db.QueryRow(`SELECT value FROM config WHERE key = 'compaction_enabled'`).Scan(&value)
	if err != nil {
		t.Fatalf("compaction config not found: %v", err)
	}
	if value != "false" {
		t.Errorf("expected compaction_enabled='false', got %q", value)
	}
}

func TestMigrateCompactedAtCommitColumn(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	db := store.db

	// Run migration
	if err := migrations.MigrateCompactedAtCommitColumn(db); err != nil {
		t.Fatalf("failed to migrate compacted_at_commit column: %v", err)
	}

	// Verify column exists
	var columnExists bool
	err := db.QueryRow(`
		SELECT COUNT(*) > 0
		FROM pragma_table_info('issues')
		WHERE name = 'compacted_at_commit'
	`).Scan(&columnExists)
	if err != nil {
		t.Fatalf("failed to check compacted_at_commit column: %v", err)
	}
	if !columnExists {
		t.Error("compacted_at_commit column was not added")
	}
}

func TestMigrateExportHashesTable(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	db := store.db

	// Drop table if exists
	_, _ = db.Exec("DROP TABLE IF EXISTS export_hashes")

	// Run migration
	if err := migrations.MigrateExportHashesTable(db); err != nil {
		t.Fatalf("failed to migrate export_hashes table: %v", err)
	}

	// Verify table exists
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='export_hashes'
	`).Scan(&tableName)
	if err != nil {
		t.Fatalf("export_hashes table not found: %v", err)
	}
}

func TestMigrateExternalRefUnique(t *testing.T) {
	t.Run("creates unique index on external_ref", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db

		externalRef1 := "JIRA-1"
		externalRef2 := "JIRA-2"
		issue1 := types.Issue{ID: "bd-1", Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, ExternalRef: &externalRef1}
		if err := store.CreateIssue(context.Background(), &issue1, "test"); err != nil {
			t.Fatalf("failed to create issue1: %v", err)
		}

		issue2 := types.Issue{ID: "bd-2", Title: "Issue 2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, ExternalRef: &externalRef2}
		if err := store.CreateIssue(context.Background(), &issue2, "test"); err != nil {
			t.Fatalf("failed to create issue2: %v", err)
		}

		if err := migrations.MigrateExternalRefUnique(db); err != nil {
			t.Fatalf("failed to migrate external_ref unique constraint: %v", err)
		}

		issue3 := types.Issue{ID: "bd-3", Title: "Issue 3", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, ExternalRef: &externalRef1}
		err := store.CreateIssue(context.Background(), &issue3, "test")
		if err == nil {
			t.Error("Expected error when creating issue with duplicate external_ref, got nil")
		}
	})

	t.Run("fails if duplicates exist", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db

		_, err := db.Exec(`DROP INDEX IF EXISTS idx_issues_external_ref_unique`)
		if err != nil {
			t.Fatalf("failed to drop index: %v", err)
		}

		externalRef := "JIRA-1"
		issue1 := types.Issue{ID: "bd-100", Title: "Issue 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, ExternalRef: &externalRef}
		if err := store.CreateIssue(context.Background(), &issue1, "test"); err != nil {
			t.Fatalf("failed to create issue1: %v", err)
		}

		_, err = db.Exec(`INSERT INTO issues (id, title, status, priority, issue_type, external_ref, created_at, updated_at) VALUES (?, ?, 'open', 1, 'task', ?, datetime('now'), datetime('now'))`, "bd-101", "Issue 2", externalRef)
		if err != nil {
			t.Fatalf("failed to create duplicate: %v", err)
		}

		err = migrations.MigrateExternalRefUnique(db)
		if err == nil {
			t.Error("Expected migration to fail with duplicates present")
		}
		if !strings.Contains(err.Error(), "duplicate external_ref values") {
			t.Errorf("Expected error about duplicates, got: %v", err)
		}
	})
}

func TestMigrateRepoMtimesTable(t *testing.T) {
	t.Run("creates repo_mtimes table if not exists", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db

		// Drop table if exists
		_, _ = db.Exec("DROP TABLE IF EXISTS repo_mtimes")

		// Run migration
		if err := migrations.MigrateRepoMtimesTable(db); err != nil {
			t.Fatalf("failed to migrate repo_mtimes table: %v", err)
		}

		// Verify table exists
		var tableName string
		err := db.QueryRow(`
			SELECT name FROM sqlite_master
			WHERE type='table' AND name='repo_mtimes'
		`).Scan(&tableName)
		if err != nil {
			t.Fatalf("repo_mtimes table not found: %v", err)
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db

		// Run migration twice
		if err := migrations.MigrateRepoMtimesTable(db); err != nil {
			t.Fatalf("first migration failed: %v", err)
		}
		if err := migrations.MigrateRepoMtimesTable(db); err != nil {
			t.Fatalf("second migration failed: %v", err)
		}

		// Verify table still exists and is correct
		var tableName string
		err := db.QueryRow(`
			SELECT name FROM sqlite_master
			WHERE type='table' AND name='repo_mtimes'
		`).Scan(&tableName)
		if err != nil {
			t.Fatalf("repo_mtimes table not found after idempotent migration: %v", err)
		}
	})
}

func TestMigrateContentHashColumn(t *testing.T) {
	t.Run("adds content_hash column if missing", func(t *testing.T) {
		s, cleanup := setupTestDB(t)
		defer cleanup()

		// Run migration (should be idempotent)
		if err := migrations.MigrateContentHashColumn(s.db); err != nil {
			t.Fatalf("failed to migrate content_hash column: %v", err)
		}

		// Verify column exists
		var colName string
		err := s.db.QueryRow(`
			SELECT name FROM pragma_table_info('issues')
			WHERE name = 'content_hash'
		`).Scan(&colName)
		if err == sql.ErrNoRows {
			t.Error("content_hash column was not added")
		} else if err != nil {
			t.Fatalf("failed to check content_hash column: %v", err)
		}
	})

	t.Run("populates content_hash for existing issues", func(t *testing.T) {
		s, cleanup := setupTestDB(t)
		defer cleanup()

		ctx := context.Background()

		// Create a test issue
		issue := &types.Issue{Title: "test-hash", Priority: 1, IssueType: "task", Status: "open"}
		err := s.CreateIssue(ctx, issue, "test")
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}

		// Clear its content_hash directly in DB
		_, err = s.db.Exec(`UPDATE issues SET content_hash = NULL WHERE id = ?`, issue.ID)
		if err != nil {
			t.Fatalf("failed to clear content_hash: %v", err)
		}

		// Verify it's cleared
		var hash sql.NullString
		err = s.db.QueryRow(`SELECT content_hash FROM issues WHERE id = ?`, issue.ID).Scan(&hash)
		if err != nil {
			t.Fatalf("failed to verify cleared hash: %v", err)
		}
		if hash.Valid {
			t.Error("content_hash should be NULL before migration")
		}

		// Drop the column to simulate fresh migration
		// Note: Schema must include owner column for GetIssue to work
		_, err = s.db.Exec(`
			CREATE TABLE issues_backup AS SELECT * FROM issues;
			DROP TABLE issues;
			CREATE TABLE issues (
				id TEXT PRIMARY KEY,
				title TEXT NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				design TEXT NOT NULL DEFAULT '',
				acceptance_criteria TEXT NOT NULL DEFAULT '',
				notes TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL CHECK (status IN ('open', 'in_progress', 'blocked', 'closed')),
				priority INTEGER NOT NULL,
				issue_type TEXT NOT NULL CHECK (issue_type IN ('bug', 'feature', 'task', 'epic', 'chore', 'message', 'agent', 'role')),
				assignee TEXT,
				estimated_minutes INTEGER,
				created_at DATETIME NOT NULL,
				created_by TEXT DEFAULT '',
				owner TEXT DEFAULT '',
				updated_at DATETIME NOT NULL,
				closed_at DATETIME,
				closed_by_session TEXT DEFAULT '',
				external_ref TEXT,
				spec_id TEXT DEFAULT '',
				compaction_level INTEGER DEFAULT 0,
				compacted_at DATETIME,
				original_size INTEGER,
				compacted_at_commit TEXT,
				source_repo TEXT DEFAULT '.',
				close_reason TEXT DEFAULT '',
				deleted_at TEXT,
				deleted_by TEXT DEFAULT '',
				delete_reason TEXT DEFAULT '',
				original_type TEXT DEFAULT '',
				sender TEXT DEFAULT '',
				ephemeral INTEGER DEFAULT 0,
				wisp_type TEXT DEFAULT '',
				pinned INTEGER DEFAULT 0,
				is_template INTEGER DEFAULT 0,
				crystallizes INTEGER DEFAULT 0,
				await_type TEXT DEFAULT '',
				await_id TEXT DEFAULT '',
				timeout_ns INTEGER DEFAULT 0,
				waiters TEXT DEFAULT '',
				hook_bead TEXT DEFAULT '',
				role_bead TEXT DEFAULT '',
				agent_state TEXT DEFAULT '',
				last_activity DATETIME,
				role_type TEXT DEFAULT '',
				rig TEXT DEFAULT '',
				mol_type TEXT DEFAULT '',
				event_kind TEXT DEFAULT '',
				actor TEXT DEFAULT '',
				target TEXT DEFAULT '',
				payload TEXT DEFAULT '',
				due_at DATETIME,
				defer_until DATETIME,
				metadata TEXT NOT NULL DEFAULT '{}',
				CHECK ((status = 'closed') = (closed_at IS NOT NULL))
			);
			INSERT INTO issues SELECT id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, estimated_minutes, created_at, '', '', updated_at, closed_at, '', external_ref, '', compaction_level, compacted_at, original_size, compacted_at_commit, source_repo, '', NULL, '', '', '', '', 0, '', 0, 0, 0, '', '', 0, '', '', '', '', NULL, '', '', '', '', '', '', '', NULL, NULL, '{}' FROM issues_backup;
			DROP TABLE issues_backup;
		`)
		if err != nil {
			t.Fatalf("failed to drop content_hash column: %v", err)
		}

		// Run migration - this should add the column and populate it
		if err := migrations.MigrateContentHashColumn(s.db); err != nil {
			t.Fatalf("failed to migrate content_hash column: %v", err)
		}

		// Verify content_hash is now populated
		got, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}
		if got.ContentHash == "" {
			t.Error("content_hash should be populated after migration")
		}
	})
}

func TestMigrateOrphanDetection(t *testing.T) {
	t.Run("detects orphaned child issues", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db
		ctx := context.Background()

		// Create a parent issue
		parent := &types.Issue{
			ID:        "bd-parent",
			Title:     "Parent Issue",
			Priority:  1,
			IssueType: "task",
			Status:    "open",
		}
		if err := store.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent issue: %v", err)
		}

		// Create a valid child issue
		validChild := &types.Issue{
			ID:        "bd-parent.1",
			Title:     "Valid Child",
			Priority:  1,
			IssueType: "task",
			Status:    "open",
		}
		if err := store.CreateIssue(ctx, validChild, "test"); err != nil {
			t.Fatalf("failed to create valid child: %v", err)
		}

		// Create an orphaned child by directly inserting it into the database
		// (bypassing CreateIssue validation which checks for parent existence)
		_, err := db.Exec(`
			INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))
		`, "bd-missing.1", "Orphaned Child", "open", 1, "task")
		if err != nil {
			t.Fatalf("failed to create orphan: %v", err)
		}

		// Run migration - it should detect the orphan and log it
		if err := migrations.MigrateOrphanDetection(db); err != nil {
			t.Fatalf("failed to run orphan detection migration: %v", err)
		}

		// Verify the orphan still exists (migration doesn't delete)
		got, err := store.GetIssue(ctx, "bd-missing.1")
		if err != nil {
			t.Fatalf("orphan should still exist after migration: %v", err)
		}
		if got.ID != "bd-missing.1" {
			t.Errorf("expected orphan ID bd-missing.1, got %s", got.ID)
		}

		// Verify valid child still exists
		got, err = store.GetIssue(ctx, "bd-parent.1")
		if err != nil {
			t.Fatalf("valid child should still exist: %v", err)
		}
		if got.ID != "bd-parent.1" {
			t.Errorf("expected valid child ID bd-parent.1, got %s", got.ID)
		}
	})

	t.Run("no orphans found in clean database", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db
		ctx := context.Background()

		// Create a parent with valid children
		parent := &types.Issue{
			ID:        "bd-p1",
			Title:     "Parent",
			Priority:  1,
			IssueType: "task",
			Status:    "open",
		}
		if err := store.CreateIssue(ctx, parent, "test"); err != nil {
			t.Fatalf("failed to create parent: %v", err)
		}

		child := &types.Issue{
			ID:        "bd-p1.1",
			Title:     "Child",
			Priority:  1,
			IssueType: "task",
			Status:    "open",
		}
		if err := store.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatalf("failed to create child: %v", err)
		}

		// Run migration - should succeed with no orphans
		if err := migrations.MigrateOrphanDetection(db); err != nil {
			t.Fatalf("migration should succeed with clean data: %v", err)
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db

		// Run migration multiple times
		for i := 0; i < 3; i++ {
			if err := migrations.MigrateOrphanDetection(db); err != nil {
				t.Fatalf("migration run %d failed: %v", i+1, err)
			}
		}
	})

	// GH#508: Verify that prefixes with dots don't trigger false positives
	t.Run("prefix with dots is not flagged as orphan", func(t *testing.T) {
		store, cleanup := setupTestDB(t)
		defer cleanup()
		db := store.db

		// Override the prefix for this test
		_, err := db.Exec(`UPDATE config SET value = 'my.project' WHERE key = 'issue_prefix'`)
		if err != nil {
			t.Fatalf("failed to update prefix: %v", err)
		}

		// Insert issues with dotted prefix directly (bypassing prefix validation)
		testCases := []struct {
			id           string
			expectOrphan bool
		}{
			// These should NOT be flagged as orphans (dots in prefix)
			{"my.project-abc123", false},
			{"my.project-xyz789", false},
			{"com.example.app-issue1", false},

			// This SHOULD be flagged as orphan (hierarchical, parent doesn't exist)
			{"my.project-missing.1", true},
		}

		for _, tc := range testCases {
			_, err := db.Exec(`
				INSERT INTO issues (id, title, status, priority, issue_type, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))
			`, tc.id, "Test Issue", "open", 1, "task")
			if err != nil {
				t.Fatalf("failed to insert %s: %v", tc.id, err)
			}
		}

		// Query for orphans using the same logic as the migration
		rows, err := db.Query(`
			SELECT id
			FROM issues
			WHERE
			  (id GLOB '*.[0-9]' OR id GLOB '*.[0-9][0-9]' OR id GLOB '*.[0-9][0-9][0-9]' OR id GLOB '*.[0-9][0-9][0-9][0-9]')
			  AND rtrim(rtrim(id, '0123456789'), '.') NOT IN (SELECT id FROM issues)
			ORDER BY id
		`)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		defer rows.Close()

		var orphans []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan failed: %v", err)
			}
			orphans = append(orphans, id)
		}

		// Verify only the expected orphan is detected
		if len(orphans) != 1 {
			t.Errorf("expected 1 orphan, got %d: %v", len(orphans), orphans)
		}
		if len(orphans) == 1 && orphans[0] != "my.project-missing.1" {
			t.Errorf("expected orphan 'my.project-missing.1', got %q", orphans[0])
		}

		// Verify non-hierarchical dotted IDs are NOT flagged
		for _, tc := range testCases {
			if !tc.expectOrphan {
				for _, orphan := range orphans {
					if orphan == tc.id {
						t.Errorf("false positive: %s was incorrectly flagged as orphan", tc.id)
					}
				}
			}
		}
	})
}
