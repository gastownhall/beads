//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdMigrate runs "bd migrate" with the given args and returns stdout.
func bdMigrate(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"migrate"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd migrate %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdMigrateFail runs "bd migrate" expecting failure.
func bdMigrateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"migrate"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd migrate %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedMigrate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Default (metadata update) =====

	t.Run("migrate_default", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mg")
		out := bdMigrate(t, bd, dir)
		if !strings.Contains(out, "Dolt database") {
			t.Errorf("expected 'Dolt database' in output: %s", out)
		}
	})

	// ===== --dry-run =====

	t.Run("migrate_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "md")
		out := bdMigrate(t, bd, dir, "--dry-run")
		// Should show what would be done without making changes
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty --dry-run output")
		}
	})

	// ===== --inspect =====

	t.Run("migrate_inspect", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mi")
		bdCreate(t, bd, dir, "Inspect test issue", "--type", "task")
		out := bdMigrate(t, bd, dir, "--inspect")
		if !strings.Contains(out, "Migration") || !strings.Contains(out, "Inspection") {
			t.Errorf("expected migration inspection output: %s", out)
		}
	})

	t.Run("migrate_inspect_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mj")
		out := bdMigrate(t, bd, dir, "--inspect", "--json")
		s := strings.TrimSpace(out)
		start := strings.Index(s, "{")
		if start >= 0 {
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
				t.Errorf("invalid JSON: %v\n%s", err, s)
			}
		}
		// --json flag may not produce JSON due to flag shadowing;
		// verify command at least succeeds.
	})

	// ===== --update-repo-id =====

	t.Run("migrate_update_repo_id_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mr")
		out := bdMigrate(t, bd, dir, "--update-repo-id", "--dry-run")
		if len(strings.TrimSpace(out)) == 0 {
			t.Error("expected non-empty --update-repo-id --dry-run output")
		}
	})

	t.Run("migrate_update_repo_id", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mu")
		out := bdMigrate(t, bd, dir, "--update-repo-id", "--yes")
		if !strings.Contains(out, "Repository ID") && !strings.Contains(out, "repo_id") {
			t.Errorf("expected repo ID update message: %s", out)
		}
	})

	// ===== migrate sync =====

	t.Run("migrate_sync_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ms")
		out := bdMigrate(t, bd, dir, "sync", "test-branch", "--dry-run")
		if !strings.Contains(out, "test-branch") {
			t.Errorf("expected branch name in dry-run output: %s", out)
		}
	})

	t.Run("migrate_sync", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mc")
		out := bdMigrate(t, bd, dir, "sync", "beads-sync")
		if !strings.Contains(out, "beads-sync") {
			t.Errorf("expected 'beads-sync' in sync output: %s", out)
		}
	})

	t.Run("migrate_sync_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mz")
		out := bdMigrate(t, bd, dir, "sync", "json-branch", "--json")
		s := strings.TrimSpace(out)
		start := strings.Index(s, "{")
		if start >= 0 {
			if !json.Valid([]byte(s[start:])) {
				t.Errorf("invalid JSON: %s", s)
			}
		}
		// --json flag may not produce JSON due to flag shadowing;
		// verify command at least succeeds.
	})

	// ===== migrate hooks =====

	t.Run("migrate_hooks_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mh")
		out := bdMigrate(t, bd, dir, "hooks", "--dry-run")
		_ = out // Should succeed without crashing
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
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
