package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckDatabaseFreshness(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}
	dbPath := filepath.Join(beadsDir, "beads.db")
	s := newTestStore(t, dbPath)
	ctx := context.Background()

	t.Run("no_jsonl_file", func(t *testing.T) {
		// No issues.jsonl → should skip check (no error)
		err := checkDatabaseFreshness(ctx, s, beadsDir)
		if err != nil {
			t.Errorf("expected no error when JSONL missing, got: %v", err)
		}
	})

	t.Run("no_last_import_time", func(t *testing.T) {
		// Create JSONL file but no last_import_time metadata → skip check
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}
		defer os.Remove(jsonlPath)

		err := checkDatabaseFreshness(ctx, s, beadsDir)
		if err != nil {
			t.Errorf("expected no error when last_import_time missing, got: %v", err)
		}
	})

	t.Run("database_is_fresh", func(t *testing.T) {
		// Set last_import_time to future → database is fresh
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}
		defer os.Remove(jsonlPath)

		futureTime := time.Now().Add(1 * time.Hour).Format(time.RFC3339)
		if err := s.SetMetadata(ctx, "last_import_time", futureTime); err != nil {
			t.Fatalf("failed to set metadata: %v", err)
		}

		err := checkDatabaseFreshness(ctx, s, beadsDir)
		if err != nil {
			t.Errorf("expected no error when database is fresh, got: %v", err)
		}
	})

	t.Run("database_is_stale", func(t *testing.T) {
		// Set last_import_time to past → database is stale
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}
		defer os.Remove(jsonlPath)

		pastTime := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
		if err := s.SetMetadata(ctx, "last_import_time", pastTime); err != nil {
			t.Fatalf("failed to set metadata: %v", err)
		}

		err := checkDatabaseFreshness(ctx, s, beadsDir)
		if err == nil {
			t.Error("expected error when database is stale, got nil")
		}
	})

	t.Run("corrupted_last_import_time", func(t *testing.T) {
		// Set corrupted last_import_time → should warn but not error
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}
		defer os.Remove(jsonlPath)

		if err := s.SetMetadata(ctx, "last_import_time", "not-a-timestamp"); err != nil {
			t.Fatalf("failed to set metadata: %v", err)
		}

		err := checkDatabaseFreshness(ctx, s, beadsDir)
		if err != nil {
			t.Errorf("expected no error with corrupted metadata (warn only), got: %v", err)
		}
	})

	t.Run("rfc3339nano_format", func(t *testing.T) {
		// Set last_import_time with nanosecond precision → should parse correctly
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if err := os.WriteFile(jsonlPath, []byte(`{"id":"test-1","title":"Test"}`+"\n"), 0644); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}
		defer os.Remove(jsonlPath)

		futureTime := time.Now().Add(1 * time.Hour).Format(time.RFC3339Nano)
		if err := s.SetMetadata(ctx, "last_import_time", futureTime); err != nil {
			t.Fatalf("failed to set metadata: %v", err)
		}

		err := checkDatabaseFreshness(ctx, s, beadsDir)
		if err != nil {
			t.Errorf("expected no error with RFC3339Nano format, got: %v", err)
		}
	})
}

func TestStalenessOnlyAppliesToReadOnlyCommands(t *testing.T) {
	t.Parallel()

	// Read-only commands should trigger staleness checks
	for _, cmd := range []string{"list", "show", "ready", "stats", "search", "duplicates", "blocked", "count", "graph", "comments"} {
		if !isReadOnlyCommand(cmd) {
			t.Errorf("command %q should be read-only (staleness check applies)", cmd)
		}
	}

	// Write commands should NOT be read-only (staleness check skipped)
	for _, cmd := range []string{"create", "update", "close", "delete", "edit", "dep"} {
		if isReadOnlyCommand(cmd) {
			t.Errorf("command %q should NOT be read-only (staleness check should not apply)", cmd)
		}
	}
}
