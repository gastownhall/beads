//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestEmbeddedImportClosedChildWhoseParentWasDeleted(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}

	ctx := context.Background()
	beadsDir := t.TempDir()
	store, err := embeddeddolt.Open(ctx, beadsDir, "beads", "main")
	if err != nil {
		t.Fatalf("open embedded Dolt store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue prefix: %v", err)
	}

	// A valid export can retain a closed child after its parent was deleted.
	// The unrelated row proves a counter reconciliation failure does not roll
	// back the rest of the import batch.
	jsonlContent := `{"id":"test-ordinary","title":"Ordinary issue","type":"task","status":"open","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-01T00:00:00Z"}
{"id":"test-deleted-parent.7","title":"Closed orphan child","type":"task","status":"closed","priority":2,"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z","closed_at":"2025-01-02T00:00:00Z"}
`
	jsonlPath := filepath.Join(t.TempDir(), "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(jsonlContent), 0o644); err != nil {
		t.Fatalf("write JSONL file: %v", err)
	}

	count, err := importFromLocalJSONL(ctx, store, jsonlPath)
	if err != nil {
		t.Fatalf("importFromLocalJSONL: %v", err)
	}
	if count != 2 {
		t.Fatalf("imported issues = %d, want 2", count)
	}
	if _, err := store.GetIssue(ctx, "test-ordinary"); err != nil {
		t.Fatalf("ordinary issue missing after import: %v", err)
	}
	child, err := store.GetIssue(ctx, "test-deleted-parent.7")
	if err != nil {
		t.Fatalf("closed orphan child missing after import: %v", err)
	}
	if child.Status != types.StatusClosed {
		t.Fatalf("orphan child status = %q, want %q", child.Status, types.StatusClosed)
	}

	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "beads", "main")
	if err != nil {
		t.Fatalf("open embedded SQL connection: %v", err)
	}
	var counterRows int
	queryErr := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?",
		"test-deleted-parent").Scan(&counterRows)
	cleanupErr := cleanup()
	if queryErr != nil {
		t.Fatalf("query orphan child counter: %v", queryErr)
	}
	if cleanupErr != nil {
		t.Fatalf("close embedded SQL connection: %v", cleanupErr)
	}
	if counterRows != 0 {
		t.Fatalf("orphan child counter rows = %d, want 0", counterRows)
	}

	// Recreating the parent remains allocation-safe without an orphaned
	// counter because GetNextChildID scans existing direct child IDs.
	now := time.Now().UTC()
	if err := store.CreateIssue(ctx, &types.Issue{
		ID:        "test-deleted-parent",
		Title:     "Recreated parent",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
		CreatedAt: now,
		UpdatedAt: now,
	}, "tester"); err != nil {
		t.Fatalf("recreate parent: %v", err)
	}
	nextID, err := store.GetNextChildID(ctx, "test-deleted-parent")
	if err != nil {
		t.Fatalf("GetNextChildID after recreating parent: %v", err)
	}
	if nextID != "test-deleted-parent.8" {
		t.Fatalf("next child ID = %q, want %q", nextID, "test-deleted-parent.8")
	}
}
