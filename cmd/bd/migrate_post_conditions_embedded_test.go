//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// gastownhall/beads#5269: `bd migrate schema` is the explicit repair entry.
// On a workspace whose schema marks are current but whose DDL drifted — here
// committed SET DEFAULTs back on two tracked tables — the command repairs
// them, reports the repairs on both output wires, and a repeat run is a
// no-op that creates no new repair commit.
func TestEmbeddedMigrateSchemaRepairsDroppedDefaultDrift_5269(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pd")
	setMigrateJSONConfigFalse(t, beadsDir)

	// driftIDs re-creates the reporter's state: committed SET DEFAULT drift
	// on the id columns of two tracked tables.
	drift := func() {
		t.Helper()
		withEmbeddedMigrateSQL(t, beadsDir, "pd", func(db *sql.DB) error {
			for _, q := range []string{
				"ALTER TABLE dependencies ALTER COLUMN id SET DEFAULT (UUID())",
				"ALTER TABLE comments ALTER COLUMN id SET DEFAULT (UUID())",
				"CALL DOLT_COMMIT('-Am', 'test: drifted id defaults')",
			} {
				if _, err := db.ExecContext(t.Context(), q); err != nil {
					return fmt.Errorf("%s: %w", q, err)
				}
			}
			return nil
		})
	}
	idDefault := func(table string) (def sql.NullString) {
		t.Helper()
		withEmbeddedMigrateSQL(t, beadsDir, "pd", func(db *sql.DB) error {
			return db.QueryRowContext(t.Context(), "SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS"+
				" WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'id'", table).Scan(&def)
		})
		return def
	}
	repairCommits := func() (n int) {
		t.Helper()
		withEmbeddedMigrateSQL(t, beadsDir, "pd", func(db *sql.DB) error {
			return db.QueryRowContext(t.Context(),
				"SELECT COUNT(*) FROM dolt_log WHERE message = 'schema: repair dropped-default post-conditions'").Scan(&n)
		})
		return n
	}

	drift()
	if def := idDefault("dependencies"); !def.Valid {
		t.Fatalf("precondition: dependencies.id carries no DEFAULT after seeding drift")
	}

	// JSON wire: status "repaired", the repairs named, applied 0.
	out := bdMigrateJSON(t, bd, dir, "schema")
	var first map[string]interface{}
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatalf("bd --format=json migrate schema returned invalid JSON: %v\n%s", err, out)
	}
	if got, ok := first["status"].(string); !ok || got != "repaired" {
		t.Errorf("migrate schema status = %#v, want repaired:\n%s", first["status"], out)
	}
	if got, ok := first["applied"].(float64); !ok || got != 0 {
		t.Errorf("migrate schema applied = %#v, want 0 (schema was already current)", first["applied"])
	}
	names, _ := first["repaired"].([]interface{})
	joined := make([]string, 0, len(names))
	for _, n := range names {
		if s, ok := n.(string); ok {
			joined = append(joined, s)
		}
	}
	if strings.Join(joined, ",") != "comments.id,dependencies.id" {
		t.Errorf("migrate schema repaired = %#v, want [comments.id dependencies.id]", first["repaired"])
	}

	// The repair is durable: defaults gone, exactly one repair commit.
	if def := idDefault("dependencies"); def.Valid {
		t.Errorf("dependencies.id still carries DEFAULT %q after migrate schema", def.String)
	}
	if def := idDefault("comments"); def.Valid {
		t.Errorf("comments.id still carries DEFAULT %q after migrate schema", def.String)
	}
	if n := repairCommits(); n != 1 {
		t.Errorf("repair commits = %d, want 1", n)
	}

	// Repeat run: current, no new repair commit.
	out = bdMigrateJSON(t, bd, dir, "schema")
	var second map[string]interface{}
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		t.Fatalf("repeat migrate schema returned invalid JSON: %v\n%s", err, out)
	}
	if got, ok := second["status"].(string); !ok || got != "current" {
		t.Errorf("repeat migrate schema status = %#v, want current", second["status"])
	}
	if n := repairCommits(); n != 1 {
		t.Errorf("repeat run created another repair commit (%d)", n)
	}

	// Text wire: re-introduce the drift and check the repair line.
	drift()
	text := bdMigrate(t, bd, dir, "schema")
	if !strings.Contains(text, "Repaired 2 dropped-default condition(s)") ||
		!strings.Contains(text, "comments.id, dependencies.id") {
		t.Errorf("text output does not name the repairs:\n%s", text)
	}
	if def := idDefault("dependencies"); def.Valid {
		t.Errorf("dependencies.id still carries DEFAULT %q after the text-wire repair", def.String)
	}
	if n := repairCommits(); n != 2 {
		t.Errorf("repair commits = %d after the second seeded drift, want 2", n)
	}
}

// The repair capability the CLI prefers is present on the embedded store this
// test binary drives, so the test above exercises the real repair path, not
// the plain-migrator fallback.
var _ interface {
	RepairSchemaPostConditions(ctx context.Context) (applied int, repairs []string, err error)
} = (*embeddeddolt.EmbeddedDoltStore)(nil)
