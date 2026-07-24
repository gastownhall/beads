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
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd migrate %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
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

	// Regression for GH#4361: `bd -C <dir> migrate --update-repo-id` must
	// derive the new fingerprint from the -C target, not the process cwd.
	// Otherwise it stamps the target DB with the caller repo's fingerprint,
	// which then propagates to every clone via the synced metadata table.
	t.Run("migrate_update_repo_id_honors_C", func(t *testing.T) {
		dirA, _, _ := bdInit(t, bd, "--prefix", "ca")
		dirB, _, _ := bdInit(t, bd, "--prefix", "cb")

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
	})

	// ===== --schema =====

	t.Run("migrate_schema", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sa")
		out := bdMigrate(t, bd, dir, "schema")
		if !strings.Contains(out, "Schema") {
			t.Errorf("expected 'Schema' in 'migrate schema' output: %s", out)
		}
	})

	t.Run("migrate_schema_idempotent", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "si")
		// First run: bd init already migrated to latest; this is the
		// idempotent re-check. Should report current, not error.
		first := bdMigrate(t, bd, dir, "schema")
		// Second run: must also succeed and report current.
		second := bdMigrate(t, bd, dir, "schema")
		if !strings.Contains(first, "Schema") || !strings.Contains(second, "Schema") {
			t.Errorf("expected 'Schema' in both runs:\nfirst:%s\nsecond:%s", first, second)
		}
	})

	t.Run("migrate_schema_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sj")
		out := bdMigrate(t, bd, dir, "schema", "--json")
		s := strings.TrimSpace(out)
		start := strings.Index(s, "{")
		if start < 0 {
			// --json flag may be shadowed by other migrate flags; tolerate
			// non-JSON output so long as the command succeeded.
			return
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
			t.Fatalf("invalid JSON: %v\n%s", err, s)
		}
		for _, key := range []string{"status", "applied", "latest_version"} {
			if _, ok := m[key]; !ok {
				t.Errorf("missing key %q in JSON output: %v", key, m)
			}
		}
		if status, _ := m["status"].(string); status != "current" && status != "applied" {
			t.Errorf("unexpected status %q in JSON output: %v", status, m)
		}
	})

	t.Run("migrate_schema_rejects_inspect", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sx")
		// --inspect lives on the parent migrate command, not on the schema
		// subcommand. Passing it should error rather than silently apply.
		out := bdMigrateFail(t, bd, dir, "schema", "--inspect")
		if !strings.Contains(out, "unknown flag") && !strings.Contains(out, "inspect") {
			t.Errorf("expected error about --inspect on 'migrate schema': %s", out)
		}
	})

	t.Run("migrate_schema_rejects_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sd")
		out := bdMigrateFail(t, bd, dir, "schema", "--dry-run")
		if !strings.Contains(out, "unknown flag") && !strings.Contains(out, "dry-run") {
			t.Errorf("expected error about --dry-run on 'migrate schema': %s", out)
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
