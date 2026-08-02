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

// bdHistory runs "bd history" with the given args and returns raw stdout.
func bdHistory(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"history"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd history %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
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
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd history --json %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
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

	t.Run("events_json_output", func(t *testing.T) {
		events := bdHistoryJSON(t, bd, dir, issue.ID, "--events")
		if len(events) == 0 {
			t.Fatal("expected non-empty history events")
		}
		var sawStatus bool
		for _, event := range events {
			if event["event_type"] == "status_changed" {
				sawStatus = true
			}
		}
		if !sawStatus {
			t.Fatalf("expected status_changed event in history events, got %#v", events)
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

	// ===== Short/partial issue ID resolution (GH#4868) =====

	// issue.ID is "hi-<hash>"; the short id is the part after the prefix.
	shortID := strings.TrimPrefix(issue.ID, "hi-")

	t.Run("short_id_resolves_like_full_id", func(t *testing.T) {
		out := bdHistory(t, bd, dir, shortID)
		if strings.Contains(out, "No history found") {
			t.Fatalf("bd history %s (short id) incorrectly reported no history: %s", shortID, out)
		}
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected full issue ID %s in history output for short id %s: %s", issue.ID, shortID, out)
		}

		entries := bdHistoryJSON(t, bd, dir, shortID)
		if len(entries) < 4 {
			t.Errorf("expected at least 4 history entries via short id, got %d", len(entries))
		}
	})

	t.Run("partial_id_resolves_via_events_flag_too", func(t *testing.T) {
		events := bdHistoryJSON(t, bd, dir, shortID, "--events")
		if len(events) == 0 {
			t.Fatalf("expected non-empty history events via short id %s", shortID)
		}
	})

	// ===== Ambiguous partial ID (GH#4868 review follow-up) =====

	// Two issues with explicit IDs sharing a common hash substring: a partial
	// ID that matches both must report the ambiguity rather than silently
	// falling through to the "No history found" not-found path.
	t.Run("ambiguous_partial_id_errors", func(t *testing.T) {
		bdCreate(t, bd, dir, "Ambiguous candidate one", "--type", "task", "--id", "hi-zzzaaa1")
		bdCreate(t, bd, dir, "Ambiguous candidate two", "--type", "task", "--id", "hi-zzzaaa2")

		cmd := exec.Command(bd, "history", "zzzaaa")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			t.Fatalf("expected bd history zzzaaa to fail, but succeeded:\n%s", stdout.String())
		}
		out := stderr.String()
		if !strings.Contains(strings.ToLower(out), "ambiguous") {
			t.Errorf("expected ambiguity error for shared partial id, got: %s", out)
		}
		for _, candidate := range []string{"hi-zzzaaa1", "hi-zzzaaa2"} {
			if !strings.Contains(out, candidate) {
				t.Errorf("expected ambiguity error to list candidate %q, got: %s", candidate, out)
			}
		}
		if strings.Contains(out, "No history found") {
			t.Errorf("ambiguous partial id incorrectly fell through to 'No history found': %s", out)
		}

		cmd = exec.Command(bd, "history", "--json", "zzzaaa")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout.Reset()
		stderr.Reset()
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			t.Fatalf("expected bd history --json zzzaaa to fail, but succeeded:\n%s", stdout.String())
		}
		// HandleErrorRespectJSON writes the JSON error envelope to stdout.
		s := strings.TrimSpace(stdout.String())
		var errResp map[string]interface{}
		if err := json.Unmarshal([]byte(s), &errResp); err != nil {
			t.Fatalf("parse error JSON: %v\nstdout: %s\nstderr: %s", err, s, stderr.String())
		}
		errBody, _ := errResp["error"].(string)
		if !strings.Contains(strings.ToLower(errBody), "ambiguous") {
			t.Errorf("expected error JSON to mention ambiguity, got: %s", s)
		}
		for _, candidate := range []string{"hi-zzzaaa1", "hi-zzzaaa2"} {
			if !strings.Contains(errBody, candidate) {
				t.Errorf("expected error JSON to list candidate %q, got: %s", candidate, s)
			}
		}
	})

	// ===== Nonexistent issue ID =====

	t.Run("nonexistent_issue_empty_history", func(t *testing.T) {
		out := bdHistory(t, bd, dir, "hi-nonexistent999")
		if !strings.Contains(out, "No history") {
			t.Errorf("expected 'No history' message for nonexistent issue, got: %s", out)
		}
	})

	// --json must always produce parseable JSON, even when history is empty.
	// Without this, consumers piping `bd history --json | jq` break on the
	// empty case while every other --json subcommand returns valid JSON.
	t.Run("nonexistent_issue_json_returns_empty_array", func(t *testing.T) {
		cmd := exec.Command(bd, "history", "--json", "hi-nonexistent999")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd history --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		var entries []map[string]interface{}
		if err := json.Unmarshal([]byte(s), &entries); err != nil {
			t.Fatalf("expected valid JSON for empty history, got prose:\n%s\n(parse error: %v)", s, err)
		}
		if len(entries) != 0 {
			t.Errorf("expected empty array for nonexistent issue, got %d entries", len(entries))
		}
	})

	// --limit combined with --json on empty history must still produce [].
	// Guards against future reordering that might apply limit semantics
	// before the empty-check and skip the JSON branch.
	t.Run("nonexistent_issue_json_with_limit_returns_empty_array", func(t *testing.T) {
		cmd := exec.Command(bd, "history", "--json", "--limit", "2", "hi-nonexistent999")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd history --json --limit 2 failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		s := strings.TrimSpace(stdout.String())
		var entries []map[string]interface{}
		if err := json.Unmarshal([]byte(s), &entries); err != nil {
			t.Fatalf("expected valid JSON for empty history with --limit, got prose:\n%s\n(parse error: %v)", s, err)
		}
		if len(entries) != 0 {
			t.Errorf("expected empty array for nonexistent issue with --limit, got %d entries", len(entries))
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
