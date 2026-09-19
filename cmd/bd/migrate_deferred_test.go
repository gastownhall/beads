//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Exercise the actual migration command and open policies on an interrupted
// embedded repair. A zero-applied success here falsely certifies an old schema.
func TestSchemaMigrateReportsDeferredCursorRestoration(t *testing.T) {
	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	t.Setenv("BEADS_DIR", beadsDir)
	s, err := embeddeddolt.Open(ctx, beadsDir, "deferred_cursor", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "deferred_cursor", "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, query := range []string{
		"UPDATE ignored_schema_migrations SET applied_at = '2026-09-01 12:00:00'",
		"CREATE TABLE __temp__ignored_schema_migrations_untrack LIKE ignored_schema_migrations",
		"INSERT INTO __temp__ignored_schema_migrations_untrack SELECT * FROM ignored_schema_migrations",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"CALL DOLT_ADD('-f', 'ignored_schema_migrations')",
		"CALL DOLT_COMMIT('-m', 'fixture: tracked cursor')",
	} {
		if err := schema.DrainCall(ctx, db, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE ignored_schema_migrations"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"CALL DOLT_ADD('-f', 'ignored_schema_migrations')",
		"CALL DOLT_COMMIT('-m', 'fixture: interrupted untrack deletion')",
	} {
		if err := schema.DrainCall(ctx, db, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO dolt_ignore VALUES ('wisp_ignored_schema_migrations_restore', false)"); err != nil {
		t.Fatal(err)
	}
	// Leave a genuine main migration pending, while the scratch records all
	// previously applied ignored migrations with distinguishable timestamps.
	if _, err := db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		t.Fatal(err)
	}
	var expected int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM __temp__ignored_schema_migrations_untrack").Scan(&expected); err != nil || expected == 0 {
		t.Fatalf("scratch fixture: rows=%d, err=%v", expected, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}

	strict, err := embeddeddolt.Open(ctx, beadsDir, "deferred_cursor", "")
	if err == nil {
		_ = strict.Close()
		t.Error("strict open falsely reported successful migration")
	} else if !strings.Contains(err.Error(), "restoration deferred") || !strings.Contains(err.Error(), "migrations are pending") {
		t.Errorf("strict open did not report pending deferral: %v", err)
	}

	for _, open := range []struct {
		name string
		fn   func(context.Context, string, string, string) (*embeddeddolt.EmbeddedDoltStore, error)
	}{
		{"read command", embeddeddolt.OpenForReadOnlyCommand},
		{"working-set reconcile", embeddeddolt.OpenForWorkingSetReconcile},
	} {
		t.Run(open.name, func(t *testing.T) {
			var lenient *embeddeddolt.EmbeddedDoltStore
			warning := captureNoticeStderr(t, func() { lenient, err = open.fn(ctx, beadsDir, "deferred_cursor", "") })
			if err != nil {
				t.Fatalf("lenient open: %v", err)
			}
			defer lenient.Close()
			if strings.Count(warning, "restoration deferred") != 1 {
				t.Errorf("expected one deferral warning, got %q", warning)
			}
			oldStore, oldCtx, oldJSON := getStore(), rootCtx, jsonOutput
			setStore(lenient)
			rootCtx = ctx
			t.Cleanup(func() { setStore(oldStore); rootCtx = oldCtx; jsonOutput = oldJSON })
			for _, jsonMode := range []bool{false, true} {
				jsonOutput = jsonMode
				var out string
				var migrateErr error
				stderr := captureNoticeStderr(t, func() { out, migrateErr = captureMigrationJSON(t, handleSchemaMigrate) })
				if migrateErr == nil {
					t.Errorf("strict migrate returned success (json=%v): %s", jsonMode, out)
				}
				report := out + stderr
				if migrateErr != nil {
					report += migrateErr.Error()
				}
				if !strings.Contains(report, "restoration deferred") || !strings.Contains(report, "migrations are pending") {
					t.Errorf("migrate omitted pending deferral (json=%v): %s", jsonMode, report)
				}
				if jsonMode {
					var payload map[string]any
					if err := json.Unmarshal([]byte(out), &payload); err != nil {
						t.Fatalf("decode migrate output: %v", err)
					}
					if payload["error"] != "schema_migration_deferred" || payload["status"] != nil {
						t.Errorf("unexpected deferred output: %s", out)
					}
				}
				if strings.Contains(report, "Schema already at") {
					t.Errorf("migrate falsely certified latest schema: %s", report)
				}
			}
		})
	}
	db, cleanup, err = embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "deferred_cursor", "")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var live, preserved int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'ignored_schema_migrations'").Scan(&live); err != nil || live != 0 {
		t.Errorf("deferred open bootstrapped cursor: %d, %v", live, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM __temp__ignored_schema_migrations_untrack WHERE applied_at = '2026-09-01 12:00:00'").Scan(&preserved); err != nil || preserved != expected {
		t.Errorf("deferred open lost/replayed cursor: %d, want %d, err=%v", preserved, expected, err)
	}
	pending, err := schema.PendingVersions(ctx, db)
	if err != nil || len(pending) != 1 || pending[0] != schema.LatestVersion() {
		t.Errorf("deferred migration applied pending work: %v, %v", pending, err)
	}
}

func TestAutoMigrateReportsDeferredCursorRestoration(t *testing.T) {
	oldJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = oldJSON })
	for _, jsonMode := range []bool{false, true} {
		jsonOutput = jsonMode
		out := captureNoticeStderr(t, func() {
			noticeSharedMigrateRefusal(fmt.Errorf("failed to initialize schema: %w", schema.ErrIgnoredCursorRestoreDeferred))
		})
		if jsonMode {
			if out != "" {
				t.Errorf("JSON auto-migration notice polluted error stream: %q", out)
			}
		} else if strings.Count(out, "restoration deferred") != 1 || !strings.Contains(out, "migrations are pending") {
			t.Errorf("auto-migration swallowed deferral: %q", out)
		}
	}
}
