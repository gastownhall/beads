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

// bdLint runs "bd lint" with the given args and returns raw stdout and exit code.
func bdLint(t *testing.T, bd, dir string, args ...string) (string, int) {
	t.Helper()
	fullArgs := append([]string{"lint"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(out), exitErr.ExitCode()
		}
		t.Fatalf("bd lint %s failed unexpectedly: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), 0
}

// bdLintJSON runs "bd lint --json" and parses the result.
func bdLintJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"lint", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	// lint exits 1 on warnings even with --json, so ignore exit error
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("bd lint --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in lint output: %s", s)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse lint JSON: %v\n%s", err, s)
	}
	return m
}

func TestEmbeddedLint(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ln")

	// Create issues with and without template violations.
	// Bug without "Steps to Reproduce" or "Acceptance Criteria" → lint warning.
	bugBare := bdCreate(t, bd, dir, "Bug without template", "--type", "bug",
		"--description", "Something is broken")

	// Bug with proper template sections → no warning.
	bugGood := bdCreate(t, bd, dir, "Bug with template", "--type", "bug",
		"--description", "## Steps to Reproduce\n1. Do X\n2. See Y\n\n## Acceptance Criteria\nShould not crash")

	// Task without acceptance criteria → lint warning.
	taskBare := bdCreate(t, bd, dir, "Task without AC", "--type", "task",
		"--description", "Just do it")

	// Chore → no template requirements, never warns.
	bdCreate(t, bd, dir, "Chore is fine", "--type", "chore")

	// Feature without AC → lint warning.
	bdCreate(t, bd, dir, "Feature no AC", "--type", "feature",
		"--description", "Add dark mode")

	// Closed issue for --status all testing.
	closedBug := bdCreate(t, bd, dir, "Closed bug bare", "--type", "bug",
		"--description", "Old bug")
	bdClose(t, bd, dir, closedBug.ID)

	// ===== Lint specific issue IDs =====

	t.Run("lint_specific_id_with_warnings", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir, bugBare.ID)
		total := int(m["total"].(float64))
		if total == 0 {
			t.Error("expected warnings for bare bug")
		}
		results := m["results"].([]interface{})
		if len(results) == 0 {
			t.Error("expected results for bare bug")
		}
	})

	t.Run("lint_specific_id_clean", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir, bugGood.ID)
		total := int(m["total"].(float64))
		if total != 0 {
			t.Errorf("expected 0 warnings for well-formatted bug, got %d", total)
		}
	})

	t.Run("lint_multiple_ids", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir, bugBare.ID, taskBare.ID)
		issues := int(m["issues"].(float64))
		if issues < 2 {
			t.Errorf("expected at least 2 issues with warnings, got %d", issues)
		}
	})

	// ===== Lint by --status and --type filters =====

	t.Run("lint_by_type_bug", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir, "--type", "bug")
		// Should find at least the bare bug
		results := m["results"].([]interface{})
		found := false
		for _, r := range results {
			rm := r.(map[string]interface{})
			if rm["id"] == bugBare.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected bare bug %s in lint results", bugBare.ID)
		}
	})

	t.Run("lint_by_type_chore_no_warnings", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir, "--type", "chore")
		total := int(m["total"].(float64))
		if total != 0 {
			t.Errorf("expected 0 warnings for chores, got %d", total)
		}
	})

	// ===== --status all includes closed =====

	t.Run("lint_status_all", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir, "--status", "all")
		// Should include the closed bare bug
		results := m["results"].([]interface{})
		found := false
		for _, r := range results {
			rm := r.(map[string]interface{})
			if rm["id"] == closedBug.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected closed bug %s in --status all results", closedBug.ID)
		}
	})

	t.Run("lint_status_default_excludes_closed", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir)
		results := m["results"].([]interface{})
		for _, r := range results {
			rm := r.(map[string]interface{})
			if rm["id"] == closedBug.ID {
				t.Errorf("closed bug %s should be excluded by default", closedBug.ID)
			}
		}
	})

	// ===== Issues with/without template violations =====

	t.Run("clean_issues_not_in_results", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir)
		results := m["results"].([]interface{})
		for _, r := range results {
			rm := r.(map[string]interface{})
			if rm["id"] == bugGood.ID {
				t.Errorf("well-formatted bug %s should not have warnings", bugGood.ID)
			}
		}
	})

	// ===== JSON output with missing sections =====

	t.Run("json_missing_sections", func(t *testing.T) {
		m := bdLintJSON(t, bd, dir, bugBare.ID)
		results := m["results"].([]interface{})
		if len(results) == 0 {
			t.Fatal("expected results")
		}
		rm := results[0].(map[string]interface{})
		missing, ok := rm["missing"].([]interface{})
		if !ok || len(missing) == 0 {
			t.Error("expected non-empty 'missing' array")
		}
	})

	// ===== Exit code 1 when warnings found =====

	t.Run("exit_code_1_on_warnings", func(t *testing.T) {
		_, exitCode := bdLint(t, bd, dir)
		if exitCode != 1 {
			t.Errorf("expected exit code 1 when warnings exist, got %d", exitCode)
		}
	})

	t.Run("exit_code_0_when_clean", func(t *testing.T) {
		_, exitCode := bdLint(t, bd, dir, "--type", "chore")
		if exitCode != 0 {
			t.Errorf("expected exit code 0 for chores, got %d", exitCode)
		}
	})

	// ===== Nonexistent issue ID =====

	t.Run("nonexistent_id_graceful", func(t *testing.T) {
		// Should not crash, just print error to stderr
		m := bdLintJSON(t, bd, dir, "ln-nonexistent999")
		total := int(m["total"].(float64))
		if total != 0 {
			t.Errorf("expected 0 warnings for nonexistent issue, got %d", total)
		}
	})

	// ===== Human-readable output =====

	t.Run("human_readable_warnings", func(t *testing.T) {
		out, _ := bdLint(t, bd, dir)
		if !strings.Contains(out, "Missing:") {
			t.Errorf("expected 'Missing:' in human output: %s", out)
		}
	})

	t.Run("human_readable_clean", func(t *testing.T) {
		out, _ := bdLint(t, bd, dir, "--type", "chore")
		if !strings.Contains(out, "No template warnings") {
			t.Errorf("expected 'No template warnings' for chores: %s", out)
		}
	})
}

// TestEmbeddedLintConcurrent exercises lint operations concurrently.
func TestEmbeddedLintConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "lc")

	// Create a mix of issues.
	for i := 0; i < 10; i++ {
		issueType := "task"
		if i%3 == 0 {
			issueType = "bug"
		}
		bdCreate(t, bd, dir, fmt.Sprintf("concurrent-lint-%d", i), "--type", issueType,
			"--description", "Bare issue for lint")
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

			queries := [][]string{
				{"--json"},
				{"--json", "--type", "bug"},
				{"--json", "--type", "task"},
				{"--json", "--status", "all"},
				{"--json"},
				{"--json", "--type", "chore"},
				{"--json", "--type", "bug"},
				{"--json"},
			}
			q := queries[worker%len(queries)]

			args := append([]string{"lint"}, q...)
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				// lint exits 1 on warnings, which is expected
				if _, ok := err.(*exec.ExitError); !ok {
					r.err = fmt.Errorf("worker %d lint: %v\n%s", worker, err, out)
				}
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
