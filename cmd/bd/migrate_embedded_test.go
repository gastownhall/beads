//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// bdMigrate runs "bd migrate" with the given args and returns stdout.
func bdMigrate(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"migrate"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd migrate %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdMigrateJSON runs "bd --format=json migrate" so PersistentPreRunE observes
// the unique root flag without migrate's duplicate local --json shadowing it.
func bdMigrateJSON(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"--format=json", "migrate"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd --format=json migrate %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// extractNewRepoID pulls the fingerprint from the "  New: <hash>" line of
// `bd migrate --update-repo-id --dry-run` output.
func extractNewRepoID(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "New:") {
			fields := strings.Fields(line)
			return fields[len(fields)-1]
		}
	}
	t.Fatalf("no 'New:' fingerprint line in migrate output:\n%s", out)
	return ""
}

// withEmbeddedMigrateSQL runs a direct fixture operation and verifies that the
// embedded SQL connection closes cleanly even when the operation fails.
func withEmbeddedMigrateSQL(t *testing.T, beadsDir, database string, operation func(*sql.DB) error) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("open embedded database: %v", err)
	}
	opErr := operation(db)
	cleanupErr := cleanup()
	if opErr != nil && cleanupErr != nil {
		t.Fatalf("embedded database operation failed: %v; cleanup failed: %v", opErr, cleanupErr)
	}
	if opErr != nil {
		t.Fatalf("embedded database operation failed: %v", opErr)
	}
	if cleanupErr != nil {
		t.Fatalf("close embedded database: %v", cleanupErr)
	}
}

func setMigrateJSONConfigFalse(t *testing.T, beadsDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("json: false\n"), 0o600); err != nil {
		t.Fatalf("write explicit false JSON config: %v", err)
	}
}

func TestEmbeddedMigrate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("migrate_metadata_preview_and_inspection", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mg")
		setMigrateJSONConfigFalse(t, beadsDir)
		bdCreate(t, bd, dir, "Inspect test issue", "--type", "task")
		const staleVersion = "0.0.0"
		setVersion := func(version string) {
			t.Helper()
			withEmbeddedMigrateSQL(t, beadsDir, "mg", func(db *sql.DB) error {
				result, err := db.ExecContext(t.Context(), "UPDATE local_metadata SET value = ? WHERE `key` = ?", version, "bd_version")
				if err != nil {
					return fmt.Errorf("set fixture bd_version: %w", err)
				}
				if rows, err := result.RowsAffected(); err != nil || rows != 1 {
					return fmt.Errorf("set fixture bd_version affected %d rows, %v; want 1", rows, err)
				}
				return nil
			})
		}
		getVersion := func() string {
			t.Helper()
			var version string
			withEmbeddedMigrateSQL(t, beadsDir, "mg", func(db *sql.DB) error {
				if err := db.QueryRowContext(t.Context(), "SELECT value FROM local_metadata WHERE `key` = ?", "bd_version").Scan(&version); err != nil {
					return fmt.Errorf("read fixture bd_version: %w", err)
				}
				return nil
			})
			return version
		}
		setVersion(staleVersion)

		inspect := func() map[string]interface{} {
			t.Helper()
			out := bdMigrateJSON(t, bd, dir, "--inspect")
			var result map[string]interface{}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("bd --format=json migrate --inspect returned invalid JSON: %v\n%s", err, out)
			}
			for _, key := range []string{"registered_migrations", "current_state", "warnings", "invariants_to_check"} {
				if _, ok := result[key]; !ok {
					t.Errorf("migrate inspection JSON missing %q: %v", key, result)
				}
			}
			state, ok := result["current_state"].(map[string]interface{})
			if !ok {
				t.Fatalf("migrate inspection current_state = %#v, want object", result["current_state"])
			}
			for _, key := range []string{"schema_version", "issue_count", "config", "missing_config", "db_exists"} {
				if _, ok := state[key]; !ok {
					t.Errorf("migrate inspection current_state missing %q: %v", key, state)
				}
			}
			if got, ok := state["db_exists"].(bool); !ok || !got {
				t.Errorf("migrate inspection db_exists = %#v, want true", state["db_exists"])
			}
			if got, ok := state["issue_count"].(float64); !ok || got != 1 {
				t.Errorf("migrate inspection issue_count = %#v, want 1", state["issue_count"])
			}
			return result
		}

		before := inspect()
		out := bdMigrateJSON(t, bd, dir, "--dry-run")
		var preview map[string]interface{}
		if err := json.Unmarshal([]byte(out), &preview); err != nil {
			t.Fatalf("bd --format=json migrate --dry-run returned invalid JSON: %v\n%s", err, out)
		}
		if got, ok := preview["dry_run"].(bool); !ok || !got {
			t.Errorf("migrate metadata dry_run = %#v, want true", preview["dry_run"])
		}
		if got, ok := preview["needs_version_update"].(bool); !ok || !got {
			t.Errorf("migrate metadata needs_version_update = %#v, want true", preview["needs_version_update"])
		}
		if got, ok := preview["current_version"].(string); !ok || got != staleVersion {
			t.Errorf("migrate metadata current_version = %#v, want %q", preview["current_version"], staleVersion)
		}
		if got, ok := preview["target_version"].(string); !ok || got != Version {
			t.Errorf("migrate metadata target_version = %#v, want %q", preview["target_version"], Version)
		}
		if got := getVersion(); got != staleVersion {
			t.Errorf("migrate --dry-run changed persisted bd_version to %q, want %q", got, staleVersion)
		}
		after := inspect()
		if !reflect.DeepEqual(before["current_state"], after["current_state"]) {
			t.Errorf("migrate --dry-run changed inspection state:\nbefore: %#v\nafter:  %#v", before["current_state"], after["current_state"])
		}

		setVersion(Version)
		out = bdMigrate(t, bd, dir)
		if !strings.Contains(out, "Dolt database version") || !strings.Contains(out, "All metadata fields present") {
			t.Errorf("migrate output = %q, want current metadata summary", out)
		}
	})

	// Regression for GH#4361: `bd -C <dir> migrate --update-repo-id` must
	// derive the new fingerprint from the -C target, not the process cwd.
	// Otherwise it stamps the target DB with the caller repo's fingerprint,
	// which then propagates to every clone via the synced metadata table.
	t.Run("migrate_update_repo_id_honors_C", func(t *testing.T) {
		dirA, _, _ := bdInit(t, bd, "--prefix", "ca")
		dirB, beadsDirB, _ := bdInit(t, bd, "--prefix", "cb")
		setMigrateJSONConfigFalse(t, beadsDirB)

		runDryRun := func(cwd, target, home string) string {
			cmd := exec.Command(bd, "-C", target, "migrate", "--update-repo-id", "--dry-run")
			cmd.Dir = cwd
			cmd.Env = bdEnv(home)
			stdout, stderr, err := runCommandBuffers(t, cmd)
			if err != nil {
				t.Fatalf("bd -C %s migrate --update-repo-id --dry-run (cwd=%s) failed: %v\nstdout:\n%s\nstderr:\n%s",
					target, cwd, err, stdout.String(), stderr.String())
			}
			return extractNewRepoID(t, stdout.String())
		}

		// Each repo's fingerprint computed from its own directory.
		wantA := runDryRun(dirA, dirA, dirA)
		wantB := runDryRun(dirB, dirB, dirB)
		if wantA == wantB {
			t.Fatalf("test setup invalid: repos A and B produced the same fingerprint %q", wantA)
		}

		// Run from inside A but target B via -C. Must report B's fingerprint.
		got := runDryRun(dirA, dirB, dirB)
		if got == wantA {
			t.Fatalf("bd -C <B> reported A's fingerprint %q — computed from cwd, not -C target (GH#4361)", wantA)
		}
		if got != wantB {
			t.Errorf("bd -C <B> migrate --update-repo-id from cwd A reported %q, want B's fingerprint %q", got, wantB)
		}

		var expectedRepoID string
		withEmbeddedMigrateSQL(t, beadsDirB, "cb", func(db *sql.DB) error {
			if err := db.QueryRowContext(t.Context(), "SELECT value FROM metadata WHERE `key` = ?", "repo_id").Scan(&expectedRepoID); err != nil {
				return fmt.Errorf("read B repo_id before apply: %w", err)
			}
			if len(expectedRepoID) < 8 {
				return fmt.Errorf("B repo_id %q is shorter than the CLI fingerprint", expectedRepoID)
			}
			if expectedRepoID[:8] != wantB {
				return fmt.Errorf("B repo_id %q does not match dry-run fingerprint %q", expectedRepoID, wantB)
			}
			_, err := db.ExecContext(t.Context(), "UPDATE metadata SET value = ? WHERE `key` = ?", "stale-repo-id", "repo_id")
			if err != nil {
				return fmt.Errorf("seed B stale repo_id: %w", err)
			}
			return nil
		})

		cmd := exec.Command(bd, "-C", dirB, "--format=json", "migrate", "--update-repo-id", "--yes")
		cmd.Dir = dirA
		cmd.Env = bdEnv(dirB)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd -C %s --format=json migrate --update-repo-id --yes (cwd=%s) failed: %v\nstdout:\n%s\nstderr:\n%s", dirB, dirA, err, stdout.String(), stderr.String())
		}
		var applied map[string]interface{}
		if err := json.Unmarshal(stdout.Bytes(), &applied); err != nil {
			t.Fatalf("bd -C %s --format=json migrate --update-repo-id returned invalid JSON: %v\n%s", dirB, err, stdout.String())
		}
		if got, ok := applied["status"].(string); !ok || got != "success" {
			t.Errorf("migrate --update-repo-id status = %#v, want success", applied["status"])
		}
		if got, ok := applied["new_repo_id"].(string); !ok || got != wantB {
			t.Errorf("migrate --update-repo-id new_repo_id = %#v, want B fingerprint %q", applied["new_repo_id"], wantB)
		}

		var persistedRepoID string
		withEmbeddedMigrateSQL(t, beadsDirB, "cb", func(db *sql.DB) error {
			if err := db.QueryRowContext(t.Context(), "SELECT value FROM metadata WHERE `key` = ?", "repo_id").Scan(&persistedRepoID); err != nil {
				return fmt.Errorf("read B repo_id after apply: %w", err)
			}
			return nil
		})
		if persistedRepoID != expectedRepoID {
			t.Errorf("persisted B repo_id = %q, want %q", persistedRepoID, expectedRepoID)
		}
	})

	t.Run("migrate_schema_json_is_idempotent", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sc")
		setMigrateJSONConfigFalse(t, beadsDir)
		run := func() map[string]interface{} {
			t.Helper()
			out := bdMigrateJSON(t, bd, dir, "schema")
			var result map[string]interface{}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("bd --format=json migrate schema returned invalid JSON: %v\n%s", err, out)
			}
			for _, key := range []string{"status", "applied", "latest_version"} {
				if _, ok := result[key]; !ok {
					t.Errorf("migrate schema JSON missing %q: %v", key, result)
				}
			}
			return result
		}

		first := run()
		second := run()
		if got, ok := first["status"].(string); !ok || got != "current" {
			t.Errorf("first migrate schema status = %#v, want current", first["status"])
		}
		if got, ok := second["status"].(string); !ok || got != "current" {
			t.Errorf("second migrate schema status = %#v, want current", second["status"])
		}
		if got, ok := first["applied"].(float64); !ok || got != 0 {
			t.Errorf("first migrate schema applied = %#v, want 0", first["applied"])
		}
		if got, ok := second["applied"].(float64); !ok || got != 0 {
			t.Errorf("second migrate schema applied = %#v, want 0", second["applied"])
		}
		if !reflect.DeepEqual(first["latest_version"], second["latest_version"]) {
			t.Errorf("migrate schema latest_version changed between idempotent runs: first %#v, second %#v", first["latest_version"], second["latest_version"])
		}
	})

	t.Run("migrate_sync_persists_and_noops", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ms")
		setMigrateJSONConfigFalse(t, beadsDir)
		const branch = "beads-sync"
		decode := func(args ...string) map[string]interface{} {
			t.Helper()
			out := bdMigrateJSON(t, bd, dir, args...)
			var result map[string]interface{}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("migrate %s returned invalid JSON: %v\n%s", strings.Join(args, " "), err, out)
			}
			return result
		}

		dryRun := decode("sync", branch, "--dry-run")
		if got, ok := dryRun["dry_run"].(bool); !ok || !got {
			t.Errorf("migrate sync dry_run = %#v, want true", dryRun["dry_run"])
		}
		if got, ok := dryRun["branch"].(string); !ok || got != branch {
			t.Errorf("migrate sync dry-run branch = %#v, want %q", dryRun["branch"], branch)
		}
		if got, ok := dryRun["changed"].(bool); !ok || !got {
			t.Errorf("migrate sync dry-run changed = %#v, want true", dryRun["changed"])
		}

		applied := decode("sync", branch)
		if got, ok := applied["status"].(string); !ok || got != "success" {
			t.Errorf("migrate sync status = %#v, want success", applied["status"])
		}
		if got, ok := applied["branch"].(string); !ok || got != branch {
			t.Errorf("migrate sync branch = %#v, want %q", applied["branch"], branch)
		}

		// decode launches a fresh process, so noop proves the store value persisted across reopen.
		noop := decode("sync", branch)
		if got, ok := noop["status"].(string); !ok || got != "noop" {
			t.Errorf("same-value migrate sync status = %#v, want noop", noop["status"])
		}
		if got, ok := noop["branch"].(string); !ok || got != branch {
			t.Errorf("same-value migrate sync branch = %#v, want %q", noop["branch"], branch)
		}
	})
}

// TestEmbeddedMigrateConcurrent exercises migrate concurrently.
func TestEmbeddedMigrateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mx")

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			var args []string
			switch worker % 3 {
			case 0:
				args = []string{"migrate", "--inspect"}
			case 1:
				args = []string{"migrate", "--dry-run"}
			case 2:
				args = []string{"migrate", "sync", "test-branch", "--dry-run"}
			}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("migrate (worker %d): %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
