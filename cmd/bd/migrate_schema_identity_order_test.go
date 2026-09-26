//go:build cgo

package main

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestMigrateSchema_IdentityMismatchRefusesBeforeApplying reproduces be-0gfcs:
// bd migrate schema's workspace-identity guard (validateWorkspaceIdentity)
// runs AFTER the shared pre-run has already opened the store and let the
// smart gate auto-apply a pending schema migration as a side effect. The
// refusal itself is correct -- exit 1, the mismatch message -- but it
// arrives after a real, permanent Dolt commit has already advanced
// schema_migrations. The fix must make the identity check run before (or
// otherwise gate) that auto-apply, so a refused migrate leaves the schema
// cursor untouched.
func TestMigrateSchema_IdentityMismatchRefusesBeforeApplying(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "idm")

	latest := schema.LatestVersion()
	behind := latest - 1

	// Put the workspace one migration behind -- so opening the store has a
	// pending migration for the smart gate to auto-apply -- and give the
	// database a _project_id that no longer matches this workspace's
	// config.yaml. Both conditions together are what the real incident
	// needs: a decoy/mismatched database that is also behind on schema.
	withEmbeddedMigrateSQL(t, beadsDir, "idm", func(db *sql.DB) error {
		if _, err := db.ExecContext(t.Context(),
			"DELETE FROM schema_migrations WHERE version = ?", latest); err != nil {
			return err
		}
		_, err := db.ExecContext(t.Context(),
			"UPDATE metadata SET value = ? WHERE `key` = ?", "corrupted-project-id-be-0gfcs", "_project_id")
		return err
	})

	cursor := func() int {
		t.Helper()
		var v int
		withEmbeddedMigrateSQL(t, beadsDir, "idm", func(db *sql.DB) error {
			return db.QueryRowContext(t.Context(),
				"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&v)
		})
		return v
	}
	if got := cursor(); got != behind {
		t.Fatalf("precondition failed: schema cursor = %d, want %d", got, behind)
	}

	cmd := exec.Command(bd, "migrate", "schema")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)

	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running bd migrate schema: %v", err)
		}
		code = exitErr.ExitCode()
	}

	if code != 1 {
		t.Errorf("bd migrate schema exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "workspace identity mismatch detected") {
		t.Errorf("bd migrate schema stderr missing identity mismatch message:\n%s", stderr.String())
	}

	if got := cursor(); got != behind {
		t.Fatalf("bd migrate schema refused the workspace but still advanced schema_migrations from %d to %d -- "+
			"the smart-gate auto-migration on store-open ran before the identity check (be-0gfcs)", behind, got)
	}
}
