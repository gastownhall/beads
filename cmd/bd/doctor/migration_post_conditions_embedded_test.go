//go:build cgo

package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// GH#5269, AC-7, embedded mode: in an embedded workspace SharedStore only
// ever holds a server-mode store (nil here), so the Migration Post-Conditions
// check must open the embedded database itself — first on a healthy store
// (post-conditions hold), then on one whose recorded marks outran its DDL
// (the drift warns with the `bd migrate schema` remedy), all without
// changing schema, cursors or history.
func TestCheckMigrationPostConditions_EmbeddedDrift(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")

	// Seed a real embedded workspace; Open's strict path migrates it current
	// on both series. Each engine is fully closed before the next opens: two
	// doltembed engines on one data directory serialize forever.
	store, err := embeddeddolt.Open(ctx, beadsDir, "beads", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store.Close()

	// Healthy: the check runs against the embedded database (it must not
	// short-circuit to "N/A (no database)") and the post-conditions hold.
	got := CheckMigrationPostConditions(&SharedStore{beadsDir: beadsDir})
	if got.Message == "N/A (no database)" {
		t.Fatalf("check short-circuited to %q despite an embedded database on disk", got.Message)
	}
	if got.Status != StatusOK {
		t.Fatalf("healthy check = %q (%s), want OK", got.Status, got.Message)
	}

	// Drift: restore the default a recorded migration dropped — the
	// reporter's marks-say-current / DDL-says-older state — as a working-set
	// change, and snapshot history and cursor for the read-only pin.
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	var logCountBefore int
	var cursorBefore int
	if err := withEmbeddedPostConditionConn(ctx, dataDir, func(conn *sql.Conn) error {
		for _, q := range []string{
			"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
		} {
			if _, err := conn.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&logCountBefore); err != nil {
			return fmt.Errorf("reading dolt_log: %w", err)
		}
		if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&cursorBefore); err != nil {
			return fmt.Errorf("reading schema_migrations: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("drift fixture: %v", err)
	}

	got = CheckMigrationPostConditions(&SharedStore{beadsDir: beadsDir})
	if got.Status != StatusWarning {
		t.Fatalf("drifted check = %q (%s), want %q", got.Status, got.Message, StatusWarning)
	}
	if !strings.Contains(got.Detail, "dependencies.id") {
		t.Errorf("detail = %q, want it to name dependencies.id", got.Detail)
	}
	if !strings.Contains(got.Fix, "bd migrate schema") {
		t.Errorf("fix = %q, want it to name the `bd migrate schema` remedy", got.Fix)
	}

	// Read-only pin: the diagnosis changed nothing — the default it reported
	// is still there, history has no new commit, and the cursor did not move.
	if err := withEmbeddedPostConditionConn(ctx, dataDir, func(conn *sql.Conn) error {
		var def sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id'`).Scan(&def); err != nil {
			return fmt.Errorf("reading dependencies.id default: %w", err)
		}
		if !def.Valid {
			t.Errorf("dependencies.id default gone after the read-only check: the check must not repair")
		}
		var logCountAfter int
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&logCountAfter); err != nil {
			return fmt.Errorf("reading dolt_log: %w", err)
		}
		if logCountAfter != logCountBefore {
			t.Errorf("dolt_log grew %d -> %d: the check must not commit", logCountBefore, logCountAfter)
		}
		var cursorAfter int
		if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&cursorAfter); err != nil {
			return fmt.Errorf("reading schema_migrations: %w", err)
		}
		if cursorAfter != cursorBefore {
			t.Errorf("schema_migrations cursor moved %d -> %d: the check must not write marks", cursorBefore, cursorAfter)
		}
		return nil
	}); err != nil {
		t.Fatalf("read-only verification: %v", err)
	}
}

// withEmbeddedPostConditionConn opens the embedded database for one fixture
// or verification phase and closes the engine before returning.
func withEmbeddedPostConditionConn(ctx context.Context, dataDir string, phase func(conn *sql.Conn) error) error {
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "beads", "")
	if err != nil {
		return fmt.Errorf("OpenSQL: %w", err)
	}
	defer func() { _ = cleanup() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("conn: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return phase(conn)
}
