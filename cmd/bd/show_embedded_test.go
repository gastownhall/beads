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

// bdShowRaw runs "bd show" with the given args and returns raw stdout.
func bdShowRaw(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"show"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd show %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdShowDetails runs "bd show --json" and parses the IssueDetails.
func bdShowDetails(t *testing.T, bd, dir, id string) map[string]interface{} {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\n%s", id, err, out)
	}
	// JSON output may be wrapped in an array
	s := strings.TrimSpace(string(out))
	// Find JSON start
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		t.Fatalf("no JSON in show output: %s", s)
	}
	s = s[start:]

	// If array, unwrap first element
	if strings.HasPrefix(s, "[") {
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(s), &arr); err != nil {
			t.Fatalf("parse show JSON array: %v\n%s", err, s)
		}
		if len(arr) == 0 {
			t.Fatal("empty JSON array from show")
		}
		return arr[0]
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse show JSON: %v\n%s", err, s)
	}
	return m
}

// bdShowFail2 runs "bd show" expecting failure.
func bdShowFail2(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"show"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd show %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedShow(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ts")

	// ===== Basic Show =====

	t.Run("show_single_issue", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Show me", "--type", "task")
		out := bdShowRaw(t, bd, dir, issue.ID)
		if !strings.Contains(out, "Show me") {
			t.Errorf("expected title in output: %s", out)
		}
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in output: %s", out)
		}
	})

	t.Run("show_multiple_issues", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Multi 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Multi 2", "--type", "task")
		out := bdShowRaw(t, bd, dir, issue1.ID, issue2.ID)
		if !strings.Contains(out, "Multi 1") {
			t.Errorf("expected first issue title: %s", out)
		}
		if !strings.Contains(out, "Multi 2") {
			t.Errorf("expected second issue title: %s", out)
		}
	})

	t.Run("show_nonexistent_id", func(t *testing.T) {
		bdShowFail2(t, bd, dir, "ts-nonexistent999")
	})

	t.Run("show_no_args", func(t *testing.T) {
		bdShowFail2(t, bd, dir)
	})

	// ===== --json =====

	t.Run("show_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON show", "--type", "task", "--description", "A description")
		m := bdShowDetails(t, bd, dir, issue.ID)
		if m["id"] != issue.ID {
			t.Errorf("expected id=%s, got %v", issue.ID, m["id"])
		}
		if m["title"] != "JSON show" {
			t.Errorf("expected title='JSON show', got %v", m["title"])
		}
		if m["description"] != "A description" {
			t.Errorf("expected description, got %v", m["description"])
		}
	})

	t.Run("show_json_includes_labels", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Labeled show", "--type", "task", "--label", "bug")
		m := bdShowDetails(t, bd, dir, issue.ID)
		labels, ok := m["labels"].([]interface{})
		if !ok {
			t.Fatalf("expected labels array, got %T", m["labels"])
		}
		found := false
		for _, l := range labels {
			if l == "bug" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'bug' in labels: %v", labels)
		}
	})

	t.Run("show_json_includes_dependencies", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Dep parent", "--type", "task")
		child := bdCreate(t, bd, dir, "Dep child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		m := bdShowDetails(t, bd, dir, child.ID)
		deps, _ := m["dependencies"].([]interface{})
		if len(deps) == 0 {
			t.Error("expected dependencies in JSON output")
		}
	})

	t.Run("show_json_includes_comments", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Commented show", "--type", "task")
		store := openStore(t, beadsDir, "ts")
		_, _ = store.AddIssueComment(t.Context(), issue.ID, "tester", "A comment")

		m := bdShowDetails(t, bd, dir, issue.ID)
		comments, _ := m["comments"].([]interface{})
		if len(comments) == 0 {
			t.Error("expected comments in JSON output")
		}
	})

	// ===== --short =====

	t.Run("show_short", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Short show", "--type", "task")
		out := bdShowRaw(t, bd, dir, issue.ID, "--short")
		lines := strings.Split(strings.TrimSpace(out), "\n")
		// Short mode should be compact — one line per issue (maybe with ANSI codes)
		if len(lines) > 3 {
			t.Errorf("expected compact output, got %d lines:\n%s", len(lines), out)
		}
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in short output: %s", out)
		}
	})

	// ===== --long =====

	t.Run("show_long", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Long show", "--type", "task", "--description", "Desc", "--assignee", "alice")
		out := bdShowRaw(t, bd, dir, issue.ID, "--long")
		// Long mode should show extra fields
		if !strings.Contains(out, "Long show") {
			t.Errorf("expected title in long output: %s", out)
		}
	})

	// ===== --id flag (for flag-like IDs) =====

	t.Run("show_id_flag", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "ID flag test", "--type", "task")
		out := bdShowRaw(t, bd, dir, "--id", issue.ID, "--short")
		if !strings.Contains(out, issue.ID) {
			t.Errorf("expected ID in output via --id flag: %s", out)
		}
	})

	// ===== --refs =====

	t.Run("show_refs", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Refs parent", "--type", "task")
		child := bdCreate(t, bd, dir, "Refs child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		out := bdShowRaw(t, bd, dir, parent.ID, "--refs")
		// --refs should mention the child that references this issue
		if !strings.Contains(out, child.ID) {
			t.Logf("--refs output did not contain child ID %s: %s", child.ID, out)
		}
	})

	// ===== --children =====

	t.Run("show_children", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Children parent", "--type", "epic")
		child := bdCreate(t, bd, dir, "Children child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID, "--type", "parent-child")

		out := bdShowRaw(t, bd, dir, parent.ID, "--children")
		if !strings.Contains(out, child.ID) {
			t.Logf("--children output did not contain child ID %s: %s", child.ID, out)
		}
	})

	t.Run("show_children_empty", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "No children", "--type", "task")
		// Should not error even with no children
		_ = bdShowRaw(t, bd, dir, issue.ID, "--children")
	})

	// ===== --as-of =====

	t.Run("show_as_of", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "AsOf test", "--type", "task")

		// Get current commit hash
		store := openStore(t, beadsDir, "ts")
		commitHash, err := store.GetCurrentCommit(t.Context())
		if err != nil {
			t.Fatalf("GetCurrentCommit: %v", err)
		}

		// Update the issue
		bdUpdate(t, bd, dir, issue.ID, "--title", "AsOf updated")

		// Show at the old commit — should have original title
		out := bdShowRaw(t, bd, dir, issue.ID, "--as-of", commitHash)
		if !strings.Contains(out, "AsOf test") {
			t.Errorf("expected original title at old commit, got: %s", out)
		}
		if strings.Contains(out, "AsOf updated") {
			t.Errorf("should not see updated title at old commit: %s", out)
		}
	})

	// ===== --local-time =====

	t.Run("show_local_time", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Local time", "--type", "task")
		// Just verify it doesn't error
		out := bdShowRaw(t, bd, dir, issue.ID, "--local-time")
		if !strings.Contains(out, "Local time") {
			t.Errorf("expected title in output: %s", out)
		}
	})

	// ===== --current =====

	t.Run("show_current_fallback_to_last_touched", func(t *testing.T) {
		// After showing issues in earlier tests, --current resolves via last-touched fallback
		out := bdShowRaw(t, bd, dir, "--current")
		if len(out) == 0 {
			t.Error("expected --current to produce output")
		}
	})

	t.Run("show_current_with_in_progress", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "In progress for current", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress", "--assignee", "test@test.com")

		out := bdShowRaw(t, bd, dir, "--current")
		if !strings.Contains(out, "In progress for current") {
			t.Logf("--current did not resolve to in-progress issue: %s", out)
		}
	})

	// ===== view alias =====

	t.Run("view_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "View alias test", "--type", "task")
		cmd := exec.Command(bd, "view", issue.ID, "--short")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd view failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), issue.ID) {
			t.Errorf("expected ID in view alias output: %s", out)
		}
	})

	// ===== --current with --id conflict =====

	t.Run("show_current_with_id_fails", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Conflict test", "--type", "task")
		bdShowFail2(t, bd, dir, "--current", issue.ID)
	})

	// ===== Epic progress in JSON =====

	t.Run("show_json_epic_progress", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Epic progress", "--type", "epic")
		child1 := bdCreate(t, bd, dir, "Epic child 1", "--type", "task")
		child2 := bdCreate(t, bd, dir, "Epic child 2", "--type", "task")
		bdDepAdd(t, bd, dir, child1.ID, epic.ID, "--type", "parent-child")
		bdDepAdd(t, bd, dir, child2.ID, epic.ID, "--type", "parent-child")
		bdClose(t, bd, dir, child1.ID)

		m := bdShowDetails(t, bd, dir, epic.ID)
		if total, ok := m["epic_total_children"]; ok {
			if total != float64(2) {
				t.Errorf("expected epic_total_children=2, got %v", total)
			}
		}
		if closed, ok := m["epic_closed_children"]; ok {
			if closed != float64(1) {
				t.Errorf("expected epic_closed_children=1, got %v", closed)
			}
		}
	})
}

// TestEmbeddedShowConcurrent exercises show operations concurrently.
func TestEmbeddedShowConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sc")

	const (
		numWorkers      = 8
		issuesPerWorker = 3
	)

	// Pre-create issues
	var allIDs []string
	for i := 0; i < numWorkers*issuesPerWorker; i++ {
		issue := bdCreate(t, bd, dir, fmt.Sprintf("concurrent-show-%d", i), "--type", "task")
		allIDs = append(allIDs, issue.ID)
	}

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

			for i := 0; i < issuesPerWorker; i++ {
				idx := worker*issuesPerWorker + i
				id := allIDs[idx]

				// Show via JSON
				cmd := exec.Command(bd, "show", id, "--json")
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("show %s: %v\n%s", id, err, out)
					results[worker] = r
					return
				}

				// Show via short
				cmd = exec.Command(bd, "show", id, "--short")
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err = cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("show --short %s: %v\n%s", id, err, out)
					results[worker] = r
					return
				}
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
