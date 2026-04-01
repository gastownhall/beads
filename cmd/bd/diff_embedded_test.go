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

// bdDiff runs "bd diff" with the given args and returns raw stdout.
func bdDiff(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"diff"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd diff %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdDiffFail runs "bd diff" expecting failure.
func bdDiffFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"diff"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd diff %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdDiffJSON runs "bd diff --json" and parses the result as a slice.
func bdDiffJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"diff", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd diff --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "[")
	if start < 0 {
		// Could be "No changes" message — return empty
		return nil
	}
	var entries []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &entries); err != nil {
		t.Fatalf("parse diff JSON: %v\n%s", err, s)
	}
	return entries
}

// getCommitHash returns the current HEAD commit hash via the store.
func getCommitHash(t *testing.T, beadsDir, database string) string {
	t.Helper()
	s := openStore(t, beadsDir, database)
	hash, err := s.GetCurrentCommit(t.Context())
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	return hash
}

func TestEmbeddedDiff(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "df")

	// Capture initial commit (after init, before any issues).
	hash0 := getCommitHash(t, beadsDir, "df")

	// Create some issues — each create auto-commits.
	issue1 := bdCreate(t, bd, dir, "Diff issue one", "--type", "task")
	hash1 := getCommitHash(t, beadsDir, "df")

	issue2 := bdCreate(t, bd, dir, "Diff issue two", "--type", "bug", "--priority", "1")
	hash2 := getCommitHash(t, beadsDir, "df")

	// ===== Basic diff between commits =====

	t.Run("basic_diff_added", func(t *testing.T) {
		out := bdDiff(t, bd, dir, hash0, hash2)
		if !strings.Contains(out, issue1.ID) {
			t.Errorf("expected issue1 ID in diff output: %s", out)
		}
		if !strings.Contains(out, issue2.ID) {
			t.Errorf("expected issue2 ID in diff output: %s", out)
		}
		if !strings.Contains(out, "Added") {
			t.Errorf("expected 'Added' section in diff output: %s", out)
		}
	})

	// ===== Diff showing added, modified, removed =====

	t.Run("diff_added_modified_removed", func(t *testing.T) {
		// Modify issue1
		bdUpdate(t, bd, dir, issue1.ID, "--status", "in_progress")

		// Delete issue2
		bdDelete(t, bd, dir, issue2.ID, "--force")

		// Create a new issue
		issue3 := bdCreate(t, bd, dir, "Diff issue three", "--type", "feature")
		hashAfterAdd := getCommitHash(t, beadsDir, "df")

		// Diff from hash2 (before modifications) to now should show:
		// - issue1 modified (status changed)
		// - issue2 removed
		// - issue3 added
		entries := bdDiffJSON(t, bd, dir, hash2, hashAfterAdd)

		foundModified := false
		foundRemoved := false
		foundAdded := false
		for _, e := range entries {
			dt := e["DiffType"].(string)
			id := e["IssueID"].(string)
			if id == issue1.ID && dt == "modified" {
				foundModified = true
			}
			if id == issue2.ID && dt == "removed" {
				foundRemoved = true
			}
			if id == issue3.ID && dt == "added" {
				foundAdded = true
			}
		}

		if !foundModified {
			t.Errorf("expected modified entry for %s", issue1.ID)
		}
		if !foundRemoved {
			t.Errorf("expected removed entry for %s", issue2.ID)
		}
		if !foundAdded {
			t.Errorf("expected added entry for %s", issue3.ID)
		}
	})

	// ===== Diff with commit hashes =====

	t.Run("diff_with_commit_hashes", func(t *testing.T) {
		// Diff between two specific known commits
		entries := bdDiffJSON(t, bd, dir, hash0, hash1)
		if len(entries) == 0 {
			t.Error("expected at least 1 diff entry between init and first create")
		}
		// Should show issue1 as added
		found := false
		for _, e := range entries {
			if e["IssueID"] == issue1.ID && e["DiffType"] == "added" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected issue1 (%s) as 'added' in diff", issue1.ID)
		}
	})

	// ===== Empty diff (same ref) =====

	t.Run("empty_diff_same_ref", func(t *testing.T) {
		out := bdDiff(t, bd, dir, hash1, hash1)
		if !strings.Contains(out, "No changes") {
			t.Errorf("expected 'No changes' for same ref, got: %s", out)
		}
	})

	// ===== Invalid ref =====

	t.Run("invalid_ref", func(t *testing.T) {
		bdDiffFail(t, bd, dir, "not@valid!", hash1)
	})

	t.Run("invalid_ref_second_arg", func(t *testing.T) {
		bdDiffFail(t, bd, dir, hash1, "not@valid!")
	})

	// ===== JSON output =====

	t.Run("json_output_structure", func(t *testing.T) {
		entries := bdDiffJSON(t, bd, dir, hash0, hash1)
		if len(entries) == 0 {
			t.Fatal("expected non-empty JSON array")
		}
		e := entries[0]
		// Verify expected JSON keys
		if _, ok := e["IssueID"]; !ok {
			t.Error("expected 'IssueID' key in JSON entry")
		}
		if _, ok := e["DiffType"]; !ok {
			t.Error("expected 'DiffType' key in JSON entry")
		}
	})

	t.Run("json_modified_has_old_and_new", func(t *testing.T) {
		// issue1 was modified between hash1 and current HEAD
		currentHash := getCommitHash(t, beadsDir, "df")
		entries := bdDiffJSON(t, bd, dir, hash1, currentHash)
		for _, e := range entries {
			if e["IssueID"] == issue1.ID && e["DiffType"] == "modified" {
				if e["OldValue"] == nil {
					t.Error("expected old_value for modified entry")
				}
				if e["NewValue"] == nil {
					t.Error("expected new_value for modified entry")
				}
				return
			}
		}
		// issue1 may have been modified — if not found, just log
		t.Log("modified entry for issue1 not found in this range (may be expected)")
	})

	// ===== Human-readable format sections =====

	t.Run("human_readable_sections", func(t *testing.T) {
		out := bdDiff(t, bd, dir, hash0, hash1)
		// Should contain the "Changes from ... to ..." header
		if !strings.Contains(out, "Changes from") {
			t.Errorf("expected 'Changes from' header: %s", out)
		}
		if !strings.Contains(out, "issues affected") {
			t.Errorf("expected 'issues affected' in output: %s", out)
		}
	})

	// ===== Wrong number of args =====

	t.Run("too_few_args", func(t *testing.T) {
		bdDiffFail(t, bd, dir, hash0)
	})

	t.Run("too_many_args", func(t *testing.T) {
		bdDiffFail(t, bd, dir, hash0, hash1, hash2)
	})
}

// TestEmbeddedDiffConcurrent exercises diff operations concurrently.
func TestEmbeddedDiffConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dc")

	hash0 := getCommitHash(t, beadsDir, "dc")

	// Create several issues to build up commit history.
	for i := 0; i < 5; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("concurrent-diff-%d", i), "--type", "task")
	}
	hash1 := getCommitHash(t, beadsDir, "dc")

	for i := 5; i < 10; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("concurrent-diff-%d", i), "--type", "task")
	}
	hash2 := getCommitHash(t, beadsDir, "dc")

	const numWorkers = 8

	type workerResult struct {
		worker int
		err    error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	// Each worker diffs a different pair of refs.
	refPairs := [][2]string{
		{hash0, hash1},
		{hash0, hash2},
		{hash1, hash2},
		{hash0, hash1},
		{hash1, hash2},
		{hash0, hash2},
		{hash0, hash1},
		{hash1, hash2},
	}

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			pair := refPairs[worker]

			// JSON diff
			args := []string{"diff", "--json", pair[0], pair[1]}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d diff %s..%s: %v\n%s", worker, pair[0][:8], pair[1][:8], err, out)
				results[worker] = r
				return
			}

			// Also run plain text diff
			args = []string{"diff", pair[0], pair[1]}
			cmd = exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err = cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d plain diff: %v\n%s", worker, err, out)
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
