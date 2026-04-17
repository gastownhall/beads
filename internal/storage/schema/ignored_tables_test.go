package schema

import (
	"strings"
	"testing"
)

func TestIgnoredTableDDL(t *testing.T) {
	ddl := IgnoredTableDDL()
	if len(ddl) == 0 {
		t.Fatal("IgnoredTableDDL returned no statements")
	}

	combined := strings.Join(ddl, "\n")

	// Verify all expected tables are referenced.
	for _, table := range []string{
		"local_metadata", "repo_mtimes", "wisps",
		"wisp_labels", "wisp_dependencies", "wisp_events", "wisp_comments",
	} {
		if !strings.Contains(combined, table) {
			t.Errorf("IgnoredTableDDL missing reference to table %q", table)
		}
	}

	// Verify columns added by later migrations are present (the bug that
	// motivated this refactor: started_at was missing from the Go constant).
	for _, col := range []string{"started_at", "no_history"} {
		if !strings.Contains(combined, col) {
			t.Errorf("IgnoredTableDDL missing column %q — migration not included?", col)
		}
	}

	// Verify the wisp_events created_at index is present.
	if !strings.Contains(combined, "idx_wisp_events_created_at") {
		t.Error("IgnoredTableDDL missing idx_wisp_events_created_at index")
	}
}

func TestReadMigrationSQL(t *testing.T) {
	sql := ReadMigrationSQL(20)
	if !strings.Contains(sql, "CREATE TABLE") {
		t.Error("migration 0020 should contain CREATE TABLE")
	}
	if !strings.Contains(sql, "wisps") {
		t.Error("migration 0020 should reference wisps table")
	}
}

func TestReadMigrationSQL_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for non-existent migration")
		}
	}()
	ReadMigrationSQL(9999)
}

func TestSplitStatements(t *testing.T) {
	sql := "CREATE TABLE foo (id INT);\nALTER TABLE foo ADD COLUMN bar INT;\n"
	stmts := splitStatements(sql)
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d: %v", len(stmts), stmts)
	}
}

func TestSplitStatements_StripsComments(t *testing.T) {
	sql := "-- This is a comment\nCREATE TABLE foo (id INT);\n"
	stmts := splitStatements(sql)
	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(stmts))
	}
	if strings.Contains(stmts[0], "--") {
		t.Error("comment not stripped from statement")
	}
}
