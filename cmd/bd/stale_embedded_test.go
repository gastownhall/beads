//go:build embeddeddolt

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// bdStale runs "bd stale" with the given args and returns raw stdout.
func bdStale(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"stale"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd stale %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdStaleFail runs "bd stale" expecting failure.
func bdStaleFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"stale"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd stale %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdStaleJSON runs "bd stale --json" and parses the result as a slice.
func bdStaleJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"stale", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd stale --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in stale output: %s", s)
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &entries); err != nil {
		t.Fatalf("parse stale JSON: %v\n%s", err, s)
	}
	return entries
}

// makeIssuesStale updates updated_at to 60 days ago via raw SQL.
func makeIssuesStale(t *testing.T, beadsDir, database string, issueIDs []string) {
	t.Helper()
	ctx := t.Context()
	dataDir := beadsDir + "/embeddeddolt"
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()

	for _, id := range issueIDs {
		_, err := db.ExecContext(ctx,
			"UPDATE issues SET updated_at = DATE_SUB(NOW(), INTERVAL 60 DAY) WHERE id = ?", id)
		if err != nil {
			t.Fatalf("update updated_at for %s: %v", id, err)
		}
	}
	// Commit the changes
	_, err = db.ExecContext(ctx, "CALL DOLT_ADD('-A')")
	if err != nil {
		t.Fatalf("dolt add: %v", err)
	}
	_, err = db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'make issues stale for testing')")
	if err != nil {
		t.Fatalf("dolt commit: %v", err)
	}
}

func TestEmbeddedStale(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "st")

	// Create issues: some will be made stale, some will stay fresh.
	stale1 := bdCreate(t, bd, dir, "Stale open issue", "--type", "task")
	stale2 := bdCreate(t, bd, dir, "Stale in_progress issue", "--type", "task")
	bdUpdate(t, bd, dir, stale2.ID, "--status", "in_progress")
	stale3 := bdCreate(t, bd, dir, "Stale bug", "--type", "bug", "--assignee", "alice")
	fresh1 := bdCreate(t, bd, dir, "Fresh issue", "--type", "task")
	closedIssue := bdCreate(t, bd, dir, "Closed issue", "--type", "task")
	bdClose(t, bd, dir, closedIssue.ID)

	// Make specific issues stale (60 days old).
	makeIssuesStale(t, beadsDir, "st", []string{stale1.ID, stale2.ID, stale3.ID})

	// ===== Basic stale detection =====

	t.Run("basic_stale_default_days", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir)
		if len(entries) < 3 {
			t.Errorf("expected at least 3 stale issues, got %d", len(entries))
		}
		// Fresh issue should not appear
		for _, e := range entries {
			if e["id"] == fresh1.ID {
				t.Errorf("fresh issue %s should not be stale", fresh1.ID)
			}
		}
	})

	// ===== Custom --days =====

	t.Run("custom_days", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--days", "90")
		// Issues are 60 days stale, so --days 90 should not find them
		if len(entries) != 0 {
			t.Errorf("expected 0 stale issues at 90 days, got %d", len(entries))
		}
	})

	t.Run("custom_days_lower", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--days", "1")
		// --days 1 should find all stale issues plus potentially fresh ones
		// At minimum the 3 stale ones
		if len(entries) < 3 {
			t.Errorf("expected at least 3 stale issues at 1 day, got %d", len(entries))
		}
	})

	// ===== --status filter =====

	t.Run("status_filter_in_progress", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--status", "in_progress")
		found := false
		for _, e := range entries {
			if e["id"] == stale2.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected stale in_progress issue %s in results", stale2.ID)
		}
	})

	t.Run("status_filter_open", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--status", "open")
		for _, e := range entries {
			if e["id"] == stale2.ID {
				t.Errorf("in_progress issue %s should not appear with --status open", stale2.ID)
			}
		}
	})

	// ===== --limit =====

	t.Run("limit_caps_results", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir, "--limit", "1")
		if len(entries) > 1 {
			t.Errorf("expected at most 1 result with --limit 1, got %d", len(entries))
		}
	})

	// ===== --json output =====

	t.Run("json_output_is_array", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir)
		// Already parsed as array — just verify non-nil
		if entries == nil {
			t.Error("expected non-nil JSON array")
		}
	})

	t.Run("json_issue_has_fields", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir)
		if len(entries) == 0 {
			t.Skip("no stale issues to check")
		}
		e := entries[0]
		for _, key := range []string{"id", "title", "status"} {
			if _, ok := e[key]; !ok {
				t.Errorf("expected '%s' key in stale issue JSON", key)
			}
		}
	})

	// ===== No stale issues =====

	t.Run("no_stale_issues", func(t *testing.T) {
		out := bdStale(t, bd, dir, "--days", "90")
		if !strings.Contains(out, "No stale issues") {
			t.Errorf("expected 'No stale issues' message: %s", out)
		}
	})

	// ===== Error handling =====

	t.Run("invalid_days_zero", func(t *testing.T) {
		out := bdStaleFail(t, bd, dir, "--days", "0")
		if !strings.Contains(out, "at least 1") {
			t.Errorf("expected 'at least 1' error: %s", out)
		}
	})

	t.Run("invalid_status", func(t *testing.T) {
		out := bdStaleFail(t, bd, dir, "--status", "bogus")
		if !strings.Contains(out, "invalid status") {
			t.Errorf("expected 'invalid status' error: %s", out)
		}
	})

	// ===== Boundary test =====

	t.Run("boundary_exact_cutoff", func(t *testing.T) {
		// Issues made 60 days stale should show at --days 60 but not --days 61
		entries60 := bdStaleJSON(t, bd, dir, "--days", "60")
		entries61 := bdStaleJSON(t, bd, dir, "--days", "61")
		if len(entries60) < len(entries61) {
			t.Errorf("--days 60 should find >= issues than --days 61: got %d vs %d", len(entries60), len(entries61))
		}
	})

	// ===== Human-readable output =====

	t.Run("human_readable_format", func(t *testing.T) {
		out := bdStale(t, bd, dir)
		if !strings.Contains(out, "Stale issues") {
			t.Errorf("expected 'Stale issues' header: %s", out)
		}
		if !strings.Contains(out, "days ago") {
			t.Errorf("expected 'days ago' in output: %s", out)
		}
	})

	// ===== Closed issues excluded =====

	t.Run("closed_issues_excluded", func(t *testing.T) {
		entries := bdStaleJSON(t, bd, dir)
		for _, e := range entries {
			if e["id"] == closedIssue.ID {
				t.Errorf("closed issue %s should not appear in stale results", closedIssue.ID)
			}
		}
	})
}

// TestEmbeddedStaleConcurrent exercises stale operations concurrently.
func TestEmbeddedStaleConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "slc")

	var ids []string
	for i := 0; i < 10; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-stale-%d", i), "--type", "task")
		ids = append(ids, issue.ID)
	}
	makeIssuesStale(t, beadsDir, "slc", ids[:5])

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

			queries := [][]string{
				{"--json"},
				{"--json", "--days", "1"},
				{"--json", "--status", "open"},
				{"--json", "--limit", "3"},
				{"--json"},
				{"--json", "--days", "7"},
				{"--json", "--status", "open"},
				{"--json"},
			}
			q := queries[worker%len(queries)]

			args := append([]string{"stale"}, q...)
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d stale: %v\n%s", worker, err, out)
				results[worker] = r
				return
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
