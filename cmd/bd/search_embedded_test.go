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

// bdSearch runs "bd search" with the given args and returns stdout.
func bdSearch(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"search"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd search %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdSearchStreams runs "bd search" and returns stdout and stderr separately,
// for assertions about which stream a line lands on.
func bdSearchStreams(t *testing.T, bd, dir string, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"search"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd search %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

// bdSearchFail runs "bd search" and returns combined output from an expected failure.
func bdSearchFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"search"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err == nil {
		t.Fatalf("expected bd search %s to fail, got:\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), stdout.String(), stderr.String())
	}
	return stdout.String() + stderr.String()
}

// bdSearchJSON runs "bd search --json" and returns parsed results.
func bdSearchJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"search", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd search --json %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "[")
	if start < 0 {
		// No results — empty array
		return nil
	}
	var results []map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &results); err != nil {
		t.Fatalf("parse search JSON: %v\n%s", err, s)
	}
	return results
}

func TestEmbeddedSearch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sr")

	// Create test fixtures
	taskA := bdCreate(t, bd, dir, "Alpha task", "--type", "task", "--priority", "1", "--assignee", "alice", "--description", "Important alpha work", "--label", "urgent")
	taskB := bdCreate(t, bd, dir, "Beta bug", "--type", "bug", "--priority", "3", "--assignee", "bob", "--description", "Beta bug description", "--label", "backend")
	taskC := bdCreate(t, bd, dir, "Gamma feature", "--type", "feature", "--priority", "2", "--label", "urgent", "--label", "frontend")
	taskD := bdCreate(t, bd, dir, "Delta task no desc", "--type", "task")
	closedTask := bdCreate(t, bd, dir, "Closed epsilon", "--type", "task")
	bdClose(t, bd, dir, closedTask.ID)

	// ===== Basic Search =====

	t.Run("search_positional_query", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "Alpha")
		found := false
		for _, r := range results {
			if r["id"] == taskA.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected to find %s in search for 'Alpha'", taskA.ID)
		}
	})

	t.Run("search_query_flag", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "--query", "Beta")
		found := false
		for _, r := range results {
			if r["id"] == taskB.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected to find %s in search for 'Beta'", taskB.ID)
		}
	})

	t.Run("search_no_results", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "nonexistentxyz123")
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	// ===== Status Filter =====

	t.Run("search_status_open", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "task", "--status", "open")
		for _, r := range results {
			if r["id"] == closedTask.ID {
				t.Error("should not find closed task with --status open")
			}
		}
	})

	t.Run("search_status_closed", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "epsilon", "--status", "closed")
		found := false
		for _, r := range results {
			if r["id"] == closedTask.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected to find closed task with --status closed")
		}
	})

	t.Run("search_status_all", func(t *testing.T) {
		// Use prefix to match all issues
		results := bdSearchJSON(t, bd, dir, "sr-", "--status", "all")
		ids := map[string]bool{}
		for _, r := range results {
			ids[r["id"].(string)] = true
		}
		if !ids[taskA.ID] || !ids[closedTask.ID] {
			t.Error("expected both open and closed issues with --status all")
		}
	})

	t.Run("search_default_includes_closed", func(t *testing.T) {
		// bd-t5yex: "was this already filed/fixed?" is the dominant search
		// query, so the default must not silently hide closed issues.
		results := bdSearchJSON(t, bd, dir, "sr-")
		ids := map[string]bool{}
		for _, r := range results {
			ids[r["id"].(string)] = true
		}
		if !ids[taskA.ID] || !ids[closedTask.ID] {
			t.Error("expected default search to include open and closed issues")
		}
	})

	t.Run("search_status_comma_separated", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--status", "open,closed")
		ids := map[string]bool{}
		for _, r := range results {
			ids[r["id"].(string)] = true
		}
		if !ids[taskA.ID] || !ids[closedTask.ID] {
			t.Error("expected both open and closed issues with --status open,closed")
		}
	})

	t.Run("search_status_invalid", func(t *testing.T) {
		out := bdSearchFail(t, bd, dir, "sr-", "--status", "not-a-status")
		if !strings.Contains(out, "invalid status") || !strings.Contains(out, "not-a-status") {
			t.Errorf("expected useful invalid status error, got: %s", out)
		}
	})

	// ===== Assignee Filter =====

	t.Run("search_assignee", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--assignee", "alice")
		for _, r := range results {
			if r["id"] == taskB.ID {
				t.Error("bob's task should not appear with --assignee alice")
			}
		}
		found := false
		for _, r := range results {
			if r["id"] == taskA.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected alice's task in results")
		}
	})

	t.Run("search_no_assignee", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--no-assignee")
		for _, r := range results {
			if r["id"] == taskA.ID || r["id"] == taskB.ID {
				t.Errorf("assigned tasks should not appear with --no-assignee")
			}
		}
	})

	// ===== Type Filter =====

	t.Run("search_type", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--type", "bug")
		if len(results) == 0 {
			t.Fatal("expected bug results")
		}
		for _, r := range results {
			if r["issue_type"] != "bug" {
				t.Errorf("expected type=bug, got %v", r["issue_type"])
			}
		}
	})

	// ===== Label Filters =====

	t.Run("search_label_and", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--label", "urgent", "--label", "frontend")
		// Only taskC has both urgent AND frontend
		if len(results) != 1 || results[0]["id"] != taskC.ID {
			t.Errorf("expected only %s with --label urgent --label frontend, got %d results", taskC.ID, len(results))
		}
	})

	t.Run("search_label_any", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--label-any", "urgent,frontend")
		ids := map[string]bool{}
		for _, r := range results {
			ids[r["id"].(string)] = true
		}
		// taskA has urgent, taskC has urgent+frontend
		if !ids[taskA.ID] || !ids[taskC.ID] {
			t.Errorf("expected both urgent-labeled tasks with --label-any: %v", ids)
		}
	})

	t.Run("search_no_labels", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--no-labels")
		for _, r := range results {
			if r["id"] == taskA.ID || r["id"] == taskC.ID {
				t.Errorf("labeled tasks should not appear with --no-labels")
			}
		}
	})

	// ===== Limit =====

	t.Run("search_limit", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--limit", "2")
		if len(results) > 2 {
			t.Errorf("expected at most 2 results with --limit 2, got %d", len(results))
		}
	})

	t.Run("search_limit_zero", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--limit", "0", "--status", "all")
		// limit=0 means unlimited — should get all issues
		if len(results) < 5 {
			t.Errorf("expected all issues with --limit 0, got %d", len(results))
		}
	})

	// ===== Sort =====

	t.Run("search_sort_priority", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--sort", "priority")
		if len(results) < 2 {
			t.Skip("need at least 2 results to test sort")
		}
		// Priority 1 (Alpha) should come before priority 3 (Beta)
		firstPri := results[0]["priority"].(float64)
		lastPri := results[len(results)-1]["priority"].(float64)
		if firstPri > lastPri {
			t.Errorf("expected ascending priority sort, got first=%v last=%v", firstPri, lastPri)
		}
	})

	t.Run("search_sort_reverse", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--sort", "priority", "--reverse")
		if len(results) < 2 {
			t.Skip("need at least 2 results to test reverse sort")
		}
		firstPri := results[0]["priority"].(float64)
		lastPri := results[len(results)-1]["priority"].(float64)
		if firstPri < lastPri {
			t.Errorf("expected descending priority sort with --reverse, got first=%v last=%v", firstPri, lastPri)
		}
	})

	// ===== Priority Filters =====

	t.Run("search_priority_min", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--priority-min", "2")
		for _, r := range results {
			pri := int(r["priority"].(float64))
			if pri < 2 {
				t.Errorf("expected priority >= 2 with --priority-min 2, got %d for %s", pri, r["id"])
			}
		}
	})

	t.Run("search_priority_max", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--priority-max", "2")
		for _, r := range results {
			pri := int(r["priority"].(float64))
			if pri > 2 {
				t.Errorf("expected priority <= 2 with --priority-max 2, got %d for %s", pri, r["id"])
			}
		}
	})

	// ===== Description Filters =====

	t.Run("search_desc_contains", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--desc-contains", "alpha")
		found := false
		for _, r := range results {
			if r["id"] == taskA.ID {
				found = true
			}
		}
		if !found {
			t.Error("expected to find taskA with --desc-contains alpha")
		}
	})

	t.Run("search_empty_description", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--empty-description")
		found := false
		for _, r := range results {
			if r["id"] == taskD.ID {
				found = true
			}
		}
		if !found {
			t.Error("expected to find task without description")
		}
		for _, r := range results {
			if r["id"] == taskA.ID {
				t.Error("task with description should not appear with --empty-description")
			}
		}
	})

	// ===== Long Output =====

	t.Run("search_long", func(t *testing.T) {
		out := bdSearch(t, bd, dir, "sr-", "--long", "--limit", "2")
		// Long format should have more detail than default
		if !strings.Contains(out, "Alpha task") && !strings.Contains(out, "Beta bug") {
			t.Logf("long output may not contain expected titles: %s", out)
		}
	})

	// ===== Date Filters =====

	t.Run("search_created_after", func(t *testing.T) {
		// All test issues were created today, so searching for yesterday should find them
		results := bdSearchJSON(t, bd, dir, "sr-", "--created-after", "2020-01-01")
		if len(results) == 0 {
			t.Error("expected results with --created-after 2020-01-01")
		}
	})

	t.Run("search_created_before", func(t *testing.T) {
		// Searching before 2020 should find nothing
		results := bdSearchJSON(t, bd, dir, "sr-", "--created-before", "2020-01-01")
		if len(results) != 0 {
			t.Errorf("expected 0 results with --created-before 2020-01-01, got %d", len(results))
		}
	})

	// ===== Metadata Filters =====

	t.Run("search_metadata_field", func(t *testing.T) {
		// Create an issue with metadata
		mdIssue := bdCreate(t, bd, dir, "Metadata issue", "--type", "task", "--metadata", `{"team":"platform"}`)
		results := bdSearchJSON(t, bd, dir, "sr-", "--metadata-field", "team=platform")
		found := false
		for _, r := range results {
			if r["id"] == mdIssue.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected to find %s with --metadata-field team=platform", mdIssue.ID)
		}
	})

	t.Run("search_has_metadata_key", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--has-metadata-key", "team")
		if len(results) == 0 {
			t.Error("expected results with --has-metadata-key team")
		}
	})

	// ===== Combined Filters =====

	t.Run("search_combined_filters", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "sr-", "--type", "task", "--assignee", "alice", "--label", "urgent")
		if len(results) != 1 || results[0]["id"] != taskA.ID {
			t.Errorf("expected only %s with combined filters, got %d results", taskA.ID, len(results))
		}
	})

	// ===== All-Fields Search (GH#2883) =====

	// One fixture whose distinctive terms live only in a comment, and one
	// whose term lives only in the description. The reproduction from the
	// issue thread: bd comments add <id> "Root cause: env var lookup ...",
	// then bd search "env var" returns nothing.
	commentIssue := bdCreate(t, bd, dir, "All-fields comment fixture", "--type", "task")
	bdRunOK(t, bd, dir, "comments", "add", commentIssue.ID, "Root cause: quorbex flimzil lookup on every init call")
	descIssue := bdCreate(t, bd, dir, "All-fields description fixture", "--type", "task", "--description", "mentions the snorfle protocol here")
	percentIssue := bdCreate(t, bd, dir, "Progress is 100% complete", "--type", "task")
	underscoreIssue := bdCreate(t, bd, dir, "literal_under_score", "--type", "task")
	pathIssue := bdCreate(t, bd, dir, "Windows cleanup", "--type", "task", "--description", `Open C:\Temp\cache file`)

	t.Run("all_fields_default_stays_blind_to_comments", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "quorbex flimzil")
		if len(results) != 0 {
			t.Errorf("default search should not match comment text, got %d results", len(results))
		}
	})

	t.Run("all_fields_finds_comment_match_with_provenance", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "quorbex flimzil", "--all-fields")
		if len(results) != 1 {
			t.Fatalf("expected 1 all-fields result for comment text, got %d", len(results))
		}
		if results[0]["id"] != commentIssue.ID {
			t.Errorf("expected %s, got %v", commentIssue.ID, results[0]["id"])
		}
		if results[0]["matched_in"] != "comments" {
			t.Errorf("expected matched_in=comments, got %v", results[0]["matched_in"])
		}
	})

	t.Run("all_fields_finds_description_match_with_provenance", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "snorfle protocol", "--all-fields")
		if len(results) != 1 {
			t.Fatalf("expected 1 all-fields result for description text, got %d", len(results))
		}
		if results[0]["id"] != descIssue.ID {
			t.Errorf("expected %s, got %v", descIssue.ID, results[0]["id"])
		}
		if results[0]["matched_in"] != "description" {
			t.Errorf("expected matched_in=description, got %v", results[0]["matched_in"])
		}
	})

	t.Run("all_fields_treats_LIKE_metacharacters_as_literals", func(t *testing.T) {
		for _, tc := range []struct {
			query, wantID, matchedIn string
		}{
			{query: "%", wantID: percentIssue.ID, matchedIn: "title"},
			{query: "_", wantID: underscoreIssue.ID, matchedIn: "title"},
			{query: `C:\Temp`, wantID: pathIssue.ID, matchedIn: "description"},
		} {
			t.Run(tc.query, func(t *testing.T) {
				results := bdSearchJSON(t, bd, dir, tc.query, "--all-fields")
				if len(results) != 1 {
					t.Fatalf("expected exactly one literal match for %q, got %d: %v", tc.query, len(results), results)
				}
				if results[0]["id"] != tc.wantID || results[0]["matched_in"] != tc.matchedIn {
					t.Errorf("literal search %q = %v, want id=%s matched_in=%s", tc.query, results[0], tc.wantID, tc.matchedIn)
				}
			})
		}
	})

	t.Run("all_fields_title_match_keeps_working", func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "All-fields comment fixture", "--all-fields")
		found := false
		for _, r := range results {
			if r["id"] == commentIssue.ID {
				found = true
				if r["matched_in"] != "title" {
					t.Errorf("expected matched_in=title, got %v", r["matched_in"])
				}
			}
		}
		if !found {
			t.Errorf("expected to find %s by title under --all-fields", commentIssue.ID)
		}
	})

	t.Run("all_fields_json_omits_matched_in_when_off" /* shape guard */, func(t *testing.T) {
		results := bdSearchJSON(t, bd, dir, "All-fields comment fixture")
		if len(results) == 0 {
			t.Fatal("expected title match without --all-fields")
		}
		if _, present := results[0]["matched_in"]; present {
			t.Errorf("matched_in must not appear without --all-fields: %v", results[0])
		}
	})

	t.Run("empty_result_hints_at_all_fields", func(t *testing.T) {
		// The hint is advice, not a result: it goes to stderr so a caller
		// parsing text output sees no new stdout line (PR review).
		out, errOut := bdSearchStreams(t, bd, dir, "quorbex flimzil")
		if !strings.Contains(errOut, "--all-fields") {
			t.Errorf("empty default-scope result should hint at --all-fields on stderr, got stderr:\n%s", errOut)
		}
		if strings.Contains(out, "--all-fields") {
			t.Errorf("hint must not land on stdout, got stdout:\n%s", out)
		}
		if !strings.Contains(out, "No issues found") {
			t.Errorf("stdout should still carry the no-results line, got:\n%s", out)
		}
		outAll, errAll := bdSearchStreams(t, bd, dir, "zz-nonexistent-zz", "--all-fields")
		if strings.Contains(outAll+errAll, "retry with --all-fields") {
			t.Errorf("hint must not appear when --all-fields already set, got stdout:\n%s\nstderr:\n%s", outAll, errAll)
		}
	})

	_ = taskB
	_ = taskC
	_ = taskD
}

// TestEmbeddedSearchConcurrent exercises search operations concurrently.
func TestEmbeddedSearchConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sx")

	const numWorkers = 8

	// Pre-create issues for searching
	for i := 0; i < 20; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("concurrent-search-%d", i), "--type", "task", "--priority", fmt.Sprintf("%d", i%4+1))
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

			// Each worker runs different search queries
			queries := [][]string{
				{"sx-", "--json", "--limit", "5"},
				{"sx-", "--json", "--type", "task"},
				{"sx-", "--json", "--sort", "priority"},
				{"sx-", "--json", "--sort", "priority", "--reverse"},
				{"concurrent", "--json"},
			}
			q := queries[worker%len(queries)]

			cmd := exec.Command(bd, append([]string{"search"}, q...)...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("search %v: %v\n%s", q, err, out)
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
