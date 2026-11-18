//go:build cgo

package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedUpgradeStatus_DoesNotApplyPendingMigrations reproduces GH#6669
// (bd-u7dkkv): `bd upgrade status` is a status query, but it opens the store
// through the shared pre-run, whose smart gate auto-applies pending schema
// migrations. A status query must leave the schema cursor untouched.
func TestEmbeddedUpgradeStatus_DoesNotApplyPendingMigrations(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ups")

	latest := schema.LatestVersion()
	behind := latest - 1

	// The reported shape: a remote-backed database one migration behind,
	// with the remote at the same version (a safe first-mover), so the
	// default smart gate (BD_SMART_GATE unset; bdEnv strips BD_*) auto-applies
	// the pending migration on store open.
	remoteURL := "file://" + filepath.Join(t.TempDir(), "remote")
	withEmbeddedMigrateSQL(t, beadsDir, "ups", func(db *sql.DB) error {
		for _, q := range []string{
			fmt.Sprintf("DELETE FROM schema_migrations WHERE version = %d", latest),
			"CALL DOLT_COMMIT('-am', 'regress schema one version')",
			fmt.Sprintf("CALL DOLT_REMOTE('add', 'origin', '%s')", remoteURL),
			"CALL DOLT_PUSH('--force', 'origin', 'main')",
		} {
			if _, err := db.ExecContext(t.Context(), q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		return nil
	})

	cursor := func() int {
		t.Helper()
		var v int
		withEmbeddedMigrateSQL(t, beadsDir, "ups", func(db *sql.DB) error {
			return db.QueryRowContext(t.Context(),
				"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v)
		})
		return v
	}
	if got := cursor(); got != behind {
		t.Fatalf("precondition failed: schema cursor = %d, want %d", got, behind)
	}

	cmd := exec.Command(bd, "upgrade", "status")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)

	if got := cursor(); got != behind {
		t.Errorf("bd upgrade status advanced schema_migrations from %d to %d: a status query applied a "+
			"pending schema migration (GH#6669)", behind, got)
	}
	// A status query reports state; it must neither announce a migration nor
	// refuse to run because one is pending.
	if strings.Contains(stderr.String(), "auto-applying") {
		t.Errorf("bd upgrade status announced a smart-gate auto-migration (GH#6669)")
	}
	if err != nil {
		t.Errorf("bd upgrade status failed on a database with a pending migration: %v", err)
	}
	if t.Failed() {
		t.Logf("stdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}
