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

// bdHistory runs "bd history" with the given args and returns raw stdout.
func bdHistory(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"history"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd history %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdHistoryFail runs "bd history" expecting failure.
func bdHistoryFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"history"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd history %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdHistoryJSON runs "bd history --json" and parses the result as a slice.
func bdHistoryJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"history", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd history --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &entries); err != nil {
		t.Fatalf("parse history JSON: %v\n%s", err, s)
	}
	return entries
}

func TestEmbeddedHistory(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hi")

	// Create an issue, then modify it several times to build history.
	issue := bdCreate(t, bd, dir, "History test issue", "--type", "task", "--priority", "3")
	bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")
	bdUpdate(t, bd, dir, issue.ID, "--priority", "1")
	bdUpdate(t, bd, dir, issue.ID, "--title", "History test issue updated")

	// ===== Basic history showing state changes =====

	t.Run("basic_history", func(t *testing.T) {
		out := bdHistory(t, bd, dir, issue.ID)
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected issue ID in history output: %s", out)
		}
		if !strings.Contains(out, "History for") {
			t.Errorf("expected 'History for' header: %s", out)
		}
		// Should show commit hashes
		if !strings.Contains(out, "Author:") {
			t.Errorf("expected 'Author:' in history output: %s", out)
		}
	})

	t.Run("history_shows_multiple_entries", func(t *testing.T) {
		entries := bdHistoryJSON(t, bd, dir, issue.ID)
		// We created + updated 3 times = at least 4 commits touching this issue
		if len(entries) < 4 {
			t.Errorf("expected at least 4 history entries, got %d", len(entries))
		}
	})

	// ===== --limit restricts entries =====

	t.Run("limit_restricts_entries", func(t *testing.T) {
		entries := bdHistoryJSON(t, bd, dir, issue.ID, "--limit", "2")
		if len(entries) > 2 {
			t.Errorf("expected at most 2 entries with --limit 2, got %d", len(entries))
		}
		if len(entries) == 0 {
			t.Error("expected at least 1 entry")
		}
	})

	t.Run("limit_1", func(t *testing.T) {
		entries := bdHistoryJSON(t, bd, dir, issue.ID, "--limit", "1")
		if len(entries) != 1 {
			t.Errorf("expected exactly 1 entry with --limit 1, got %d", len(entries))
		}
	})

	// ===== --json output =====

	t.Run("json_output_structure", func(t *testing.T) {
		entries := bdHistoryJSON(t, bd, dir, issue.ID)
		if len(entries) == 0 {
			t.Fatal("expected non-empty history")
		}
		e := entries[0]
		// Check expected keys
		if _, ok := e["CommitHash"]; !ok {
			t.Error("expected 'CommitHash' key")
		}
		if _, ok := e["CommitDate"]; !ok {
			t.Error("expected 'CommitDate' key")
		}
		if _, ok := e["Committer"]; !ok {
			t.Error("expected 'Committer' key")
		}
		if _, ok := e["Issue"]; !ok {
			t.Error("expected 'Issue' key")
		}
	})

	t.Run("json_issue_snapshot_has_fields", func(t *testing.T) {
		entries := bdHistoryJSON(t, bd, dir, issue.ID)
		if len(entries) == 0 {
			t.Fatal("expected non-empty history")
		}
		// Most recent entry should have the updated title
		issueMap, ok := entries[0]["Issue"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected Issue to be a map, got %T", entries[0]["Issue"])
		}
		if issueMap["title"] != "History test issue updated" {
			t.Errorf("expected latest title 'History test issue updated', got %v", issueMap["title"])
		}
	})

	// ===== Nonexistent issue ID =====

	t.Run("nonexistent_issue_empty_history", func(t *testing.T) {
		out := bdHistory(t, bd, dir, "hi-nonexistent999")
		if !strings.Contains(out, "No history") {
			t.Errorf("expected 'No history' message for nonexistent issue, got: %s", out)
		}
	})

	// ===== Wrong number of args =====

	t.Run("no_args_fails", func(t *testing.T) {
		bdHistoryFail(t, bd, dir)
	})

	t.Run("too_many_args_fails", func(t *testing.T) {
		bdHistoryFail(t, bd, dir, issue.ID, "extra")
	})

	// ===== History for newly created issue =====

	t.Run("single_entry_for_new_issue", func(t *testing.T) {
		fresh := bdCreate(t, bd, dir, "Fresh issue no updates", "--type", "task")
		entries := bdHistoryJSON(t, bd, dir, fresh.ID)
		if len(entries) < 1 {
			t.Error("expected at least 1 history entry for a newly created issue")
		}
	})
}

// TestEmbeddedHistoryConcurrent exercises history operations concurrently.
func TestEmbeddedHistoryConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "hc")

	// Create several issues with history.
	var ids []string
	for i := 0; i < 8; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-history-%d", i), "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--priority", "1")
		ids = append(ids, issue.ID)
	}

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
			id := ids[worker]

			// JSON history
			args := []string{"history", "--json", id}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d history %s: %v\n%s", worker, id, err, out)
				results[worker] = r
				return
			}

			// Plain text history
			args = []string{"history", id, "--limit", "1"}
			cmd = exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err = cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d history --limit 1: %v\n%s", worker, err, out)
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
