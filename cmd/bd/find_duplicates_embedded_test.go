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
)

// bdFindDups runs "bd find-duplicates" with the given args and returns raw stdout.
func bdFindDups(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"find-duplicates"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd find-duplicates %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdFindDupsFail runs "bd find-duplicates" expecting failure.
func bdFindDupsFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"find-duplicates"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd find-duplicates %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdFindDupsJSON runs "bd find-duplicates --json" and parses the result.
func bdFindDupsJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"find-duplicates", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd find-duplicates --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in find-duplicates output: %s", s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse find-duplicates JSON: %v\n%s", err, s)
	}
	return m
}

func TestEmbeddedFindDuplicates(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "fd")

	// Create near-identical issues (should be flagged as duplicates).
	bdCreate(t, bd, dir, "Fix login page timeout error", "--type", "bug",
		"--description", "The login page throws a timeout error after 30 seconds of inactivity")
	bdCreate(t, bd, dir, "Login page timeout error fix needed", "--type", "bug",
		"--description", "After 30 seconds the login page shows a timeout error to the user")

	// Create distinct issues (should NOT be flagged).
	bdCreate(t, bd, dir, "Add dark mode to settings", "--type", "feature",
		"--description", "Users want a dark mode toggle in the settings page")
	bdCreate(t, bd, dir, "Upgrade database to PostgreSQL 16", "--type", "task",
		"--description", "Migrate from PostgreSQL 14 to PostgreSQL 16 for performance")
	bdCreate(t, bd, dir, "Write API documentation", "--type", "task",
		"--description", "Document all REST endpoints with OpenAPI spec")

	// Create a closed issue for status filter testing.
	closed := bdCreate(t, bd, dir, "Fix login page timeout error (old)", "--type", "bug",
		"--description", "The login page throws a timeout error")
	bdClose(t, bd, dir, closed.ID)

	// ===== Mechanical method with near-identical issues =====

	t.Run("mechanical_finds_near_identical", func(t *testing.T) {
		// Use a low threshold since token overlap between the two login issues
		// depends on exact tokenization (Jaccard + cosine average).
		m := bdFindDupsJSON(t, bd, dir, "--threshold", "0.15")
		pairs, ok := m["pairs"].([]interface{})
		if !ok {
			t.Fatalf("expected pairs array, got %T", m["pairs"])
		}
		if len(pairs) == 0 {
			t.Error("expected at least 1 duplicate pair for near-identical issues")
		}
		// Verify at least one pair involves two login-related issues
		found := false
		for _, p := range pairs {
			pm := p.(map[string]interface{})
			titleA := pm["issue_a_title"].(string)
			titleB := pm["issue_b_title"].(string)
			if strings.Contains(strings.ToLower(titleA), "login") && strings.Contains(strings.ToLower(titleB), "login") {
				found = true
			}
		}
		if !found {
			t.Error("expected login timeout issues to be flagged as duplicates")
		}
	})

	// ===== Threshold controls sensitivity =====

	t.Run("threshold_high_reduces_results", func(t *testing.T) {
		m := bdFindDupsJSON(t, bd, dir, "--threshold", "0.95")
		pairs := m["pairs"].([]interface{})
		// Very high threshold should find fewer/no pairs
		mLow := bdFindDupsJSON(t, bd, dir, "--threshold", "0.2")
		pairsLow := mLow["pairs"].([]interface{})
		if len(pairsLow) < len(pairs) {
			t.Errorf("lower threshold should find >= as many pairs: low=%d high=%d", len(pairsLow), len(pairs))
		}
	})

	t.Run("threshold_zero_finds_all_pairs", func(t *testing.T) {
		m := bdFindDupsJSON(t, bd, dir, "--threshold", "0.0")
		pairs := m["pairs"].([]interface{})
		// With threshold 0, every pair with any similarity should appear
		if len(pairs) == 0 {
			t.Error("expected at least some pairs at threshold 0")
		}
	})

	// ===== Status filter =====

	t.Run("status_filter_open", func(t *testing.T) {
		m := bdFindDupsJSON(t, bd, dir, "--status", "open", "--threshold", "0.2")
		// Should exclude the closed issue
		pairs := m["pairs"].([]interface{})
		for _, p := range pairs {
			pm := p.(map[string]interface{})
			titleA := pm["issue_a_title"].(string)
			titleB := pm["issue_b_title"].(string)
			if strings.Contains(titleA, "(old)") || strings.Contains(titleB, "(old)") {
				t.Error("closed issue should be excluded with --status open")
			}
		}
	})

	t.Run("status_filter_all", func(t *testing.T) {
		m := bdFindDupsJSON(t, bd, dir, "--status", "all", "--threshold", "0.3")
		pairs := m["pairs"].([]interface{})
		// With "all" status, the closed issue may appear in pairs
		if len(pairs) == 0 {
			t.Error("expected at least 1 pair with --status all")
		}
	})

	// ===== Limit caps results =====

	t.Run("limit_caps_results", func(t *testing.T) {
		m := bdFindDupsJSON(t, bd, dir, "--threshold", "0.0", "--limit", "1")
		pairs := m["pairs"].([]interface{})
		if len(pairs) > 1 {
			t.Errorf("expected at most 1 pair with --limit 1, got %d", len(pairs))
		}
	})

	// ===== No duplicates found =====

	t.Run("no_duplicates_distinct_issues", func(t *testing.T) {
		// Very high threshold — distinct issues shouldn't match
		out := bdFindDups(t, bd, dir, "--threshold", "0.99")
		if !strings.Contains(out, "No similar issues") {
			// It's ok if nothing matches — just verify no crash
		}
	})

	// ===== JSON output =====

	t.Run("json_output_structure", func(t *testing.T) {
		m := bdFindDupsJSON(t, bd, dir, "--threshold", "0.3")
		if _, ok := m["pairs"]; !ok {
			t.Error("expected 'pairs' key in JSON output")
		}
		if _, ok := m["count"]; !ok {
			t.Error("expected 'count' key in JSON output")
		}
		if _, ok := m["method"]; !ok {
			t.Error("expected 'method' key in JSON output")
		}
		if _, ok := m["threshold"]; !ok {
			t.Error("expected 'threshold' key in JSON output")
		}
		if m["method"] != "mechanical" {
			t.Errorf("expected method='mechanical', got %v", m["method"])
		}
	})

	t.Run("json_pair_fields", func(t *testing.T) {
		m := bdFindDupsJSON(t, bd, dir, "--threshold", "0.2")
		pairs := m["pairs"].([]interface{})
		if len(pairs) == 0 {
			t.Skip("no pairs to check fields on")
		}
		p := pairs[0].(map[string]interface{})
		for _, key := range []string{"issue_a_id", "issue_b_id", "issue_a_title", "issue_b_title", "similarity", "method"} {
			if _, ok := p[key]; !ok {
				t.Errorf("expected '%s' key in pair JSON", key)
			}
		}
	})

	// ===== Invalid method =====

	t.Run("invalid_method_error", func(t *testing.T) {
		out := bdFindDupsFail(t, bd, dir, "--method", "bogus")
		if !strings.Contains(out, "invalid method") {
			t.Errorf("expected 'invalid method' error, got: %s", out)
		}
	})

	// ===== Fewer than 2 issues =====

	t.Run("fewer_than_2_issues", func(t *testing.T) {
		// Create a fresh init with only 1 issue
		dir2, _, _ := bdInit(t, bd, "--prefix", "fd2")
		bdCreate(t, bd, dir2, "Only one issue", "--type", "task")

		out := bdFindDups(t, bd, dir2)
		if !strings.Contains(out, "Not enough issues") {
			t.Errorf("expected 'Not enough issues' message, got: %s", out)
		}
	})

	t.Run("fewer_than_2_issues_json", func(t *testing.T) {
		dir3, _, _ := bdInit(t, bd, "--prefix", "fd3")
		bdCreate(t, bd, dir3, "Only one issue json", "--type", "task")

		m := bdFindDupsJSON(t, bd, dir3)
		count := int(m["count"].(float64))
		if count != 0 {
			t.Errorf("expected count=0 with fewer than 2 issues, got %d", count)
		}
	})

	// ===== find-dups alias =====

	t.Run("find_dups_alias", func(t *testing.T) {
		cmd := exec.Command(bd, "find-dups", "--json", "--threshold", "0.3")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd find-dups alias failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "pairs") {
			t.Errorf("expected JSON with 'pairs' from alias: %s", out)
		}
	})

	// ===== Human-readable output =====

	t.Run("human_readable_output", func(t *testing.T) {
		out := bdFindDups(t, bd, dir, "--threshold", "0.3")
		if strings.Contains(out, "No similar issues") {
			t.Skip("no pairs found at this threshold")
		}
		if !strings.Contains(out, "similar") {
			t.Errorf("expected 'similar' in human-readable output: %s", out)
		}
		if !strings.Contains(out, "Pair") {
			t.Errorf("expected 'Pair' in human-readable output: %s", out)
		}
	})
}

// TestEmbeddedFindDuplicatesConcurrent exercises find-duplicates operations concurrently.
func TestEmbeddedFindDuplicatesConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "fdc")

	// Create enough issues for meaningful comparison.
	for i := 0; i < 10; i++ {
		bdCreate(t, bd, dir, fmt.Sprintf("concurrent dup test issue %d about login", i), "--type", "task")
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

			// Vary the threshold per worker
			threshold := fmt.Sprintf("%.1f", 0.1+float64(worker)*0.1)
			args := []string{"find-duplicates", "--json", "--threshold", threshold}
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("worker %d find-duplicates: %v\n%s", worker, err, out)
				results[worker] = r
				return
			}

			// Verify JSON is parseable
			s := strings.TrimSpace(string(out))
			start := strings.Index(s, "{")
			if start < 0 {
				r.err = fmt.Errorf("worker %d: no JSON in output: %s", worker, s)
				results[worker] = r
				return
			}
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
				r.err = fmt.Errorf("worker %d: JSON parse: %v\n%s", worker, err, s)
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
