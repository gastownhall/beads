//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// TestEmbeddedMigrateSchema_RepairsRecordedDefaultDrift_5269 drives the
// explicit repair through the binary (GH#5269, AC-3): a current workspace
// whose dependencies.id carries the default migrations 0043/0050 drop is
// repaired by `bd migrate schema`, which says so in text and in the JSON
// `repaired` list; a repeat reports nothing repaired.
func TestEmbeddedMigrateSchema_RepairsRecordedDefaultDrift_5269(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rpd")

	drift := func() {
		t.Helper()
		withEmbeddedMigrateSQL(t, beadsDir, "rpd", func(db *sql.DB) error {
			conn, err := db.Conn(t.Context())
			if err != nil {
				return err
			}
			defer conn.Close()
			for _, q := range []string{
				"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
				"CALL DOLT_COMMIT('-Am', 'test: drift dependencies.id default')",
			} {
				if _, err := conn.ExecContext(t.Context(), q); err != nil {
					return err
				}
			}
			return nil
		})
	}
	migrate := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, append([]string{"migrate", "schema"}, args...)...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd migrate schema %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	type result struct {
		Status   string   `json:"status"`
		Applied  int      `json:"applied"`
		Repaired []string `json:"repaired"`
	}
	migrateJSON := func() result {
		t.Helper()
		out := migrate("--json")
		var r result
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("parse --json output %q: %v", out, err)
		}
		return r
	}

	drift()
	if out := migrate(); !strings.Contains(out, "Repaired 1 recorded migration post-condition(s): dependencies.id") {
		t.Fatalf("text output does not report the repair:\n%s", out)
	}

	drift()
	if got, want := migrateJSON(), (result{Status: "repaired", Repaired: []string{"dependencies.id"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("--json = %+v, want %+v", got, want)
	}

	if got, want := migrateJSON(), (result{Status: "current", Repaired: []string{}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("repeat --json = %+v, want %+v", got, want)
	}
}

// gastownhall/beads#5269, AC-4: when a tracked repair target holds
// uncommitted changes, `bd migrate schema` refuses before altering anything,
// names the table, and names the remedy that ends the loop (commit or reset
// the working set, then re-run).
func TestEmbeddedMigrateSchemaRefusesDirtyTrackedTarget_5269(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pt")
	setMigrateJSONConfigFalse(t, beadsDir)

	// Uncommitted drift on a tracked table: dependencies carries a default
	// AND an uncommitted schema change.
	withEmbeddedMigrateSQL(t, beadsDir, "pt", func(db *sql.DB) error {
		if _, err := db.ExecContext(t.Context(), "ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())"); err != nil {
			return fmt.Errorf("seed uncommitted drift: %w", err)
		}
		return nil
	})

	// The refusal exits non-zero; capture the text wire without failing on it.
	cmd := exec.Command(bd, "migrate", "schema")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, runErr := runCommandBuffers(t, cmd)
	if runErr == nil {
		t.Fatalf("bd migrate schema succeeded on a dirty tracked target:\n%s", stdout.String())
	}
	out := stdout.String() + stderr.String()
	for _, want := range []string{"dropped-default repair refused", "dependencies", "bd dolt commit", "migrate schema"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal output missing %q:\n%s", want, out)
		}
	}

	// Durable: the default survives, no repair commit exists.
	withEmbeddedMigrateSQL(t, beadsDir, "pt", func(db *sql.DB) error {
		var def sql.NullString
		if err := db.QueryRowContext(t.Context(), "SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS"+
			" WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'dependencies' AND COLUMN_NAME = 'id'").Scan(&def); err != nil {
			return err
		}
		if !def.Valid {
			t.Errorf("dependencies.id was altered despite the refusal")
		}
		var repairCommits int
		if err := db.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'schema: repair recorded migration post-conditions%'").Scan(&repairCommits); err != nil {
			return err
		}
		if repairCommits != 0 {
			t.Errorf("refused repair created %d commit(s)", repairCommits)
		}
		return nil
	})
}
