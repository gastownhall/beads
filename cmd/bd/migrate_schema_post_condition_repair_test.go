//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
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
