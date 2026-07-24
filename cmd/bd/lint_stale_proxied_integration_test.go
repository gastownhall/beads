//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProxiedServerLint(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "lnt")

	// Seed mirrors cmd/bd/lint_embedded_test.go.
	// Bug without template sections -> warning.
	bugBare := bdProxiedCreate(t, bd, p.dir, "Bug without template", "--type", "bug",
		"--description", "Something is broken")
	// Bug with the required sections -> no warning.
	bugGood := bdProxiedCreate(t, bd, p.dir, "Bug with template", "--type", "bug",
		"--description", "## Steps to Reproduce\n1. Do X\n2. See Y\n\n## Acceptance Criteria\nShould not crash")
	// Task without acceptance criteria -> warning.
	taskBare := bdProxiedCreate(t, bd, p.dir, "Task without AC", "--type", "task",
		"--description", "Just do it")
	// Chore -> never warns.
	bdProxiedCreate(t, bd, p.dir, "Chore is fine", "--type", "chore")
	// Feature without AC -> warning.
	bdProxiedCreate(t, bd, p.dir, "Feature no AC", "--type", "feature",
		"--description", "Add dark mode")
	// Closed bare bug for --status all coverage.
	closedBug := bdProxiedCreate(t, bd, p.dir, "Closed bug bare", "--type", "bug",
		"--description", "Old bug")
	if out, err := bdProxiedRun(t, bd, p.dir, "close", closedBug.ID); err != nil {
		t.Fatalf("close %s: %v\n%s", closedBug.ID, err, out)
	}

	type lintOutput struct {
		Total   int          `json:"total"`
		Issues  int          `json:"issues"`
		Results []LintResult `json:"results"`
	}
	lintJSON := func(t *testing.T, args ...string) lintOutput {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"lint", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("lint %v: %v\n%s", args, err, out)
		}
		var got lintOutput
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal lint: %v\n%s", err, out)
		}
		return got
	}
	hasID := func(results []LintResult, id string) bool {
		for i := range results {
			if results[i].ID == id {
				return true
			}
		}
		return false
	}

	t.Run("specific_id_with_warnings", func(t *testing.T) {
		got := lintJSON(t, bugBare.ID)
		if got.Total == 0 {
			t.Error("expected warnings for bare bug")
		}
		if len(got.Results) == 0 {
			t.Error("expected results for bare bug")
		}
		if got.Issues != 1 || len(got.Results) != 1 || got.Results[0].ID != bugBare.ID {
			t.Errorf("lint %s = %+v, want single result for that id", bugBare.ID, got)
		}
	})

	t.Run("specific_id_clean", func(t *testing.T) {
		got := lintJSON(t, bugGood.ID)
		if got.Total != 0 {
			t.Errorf("expected 0 warnings for well-formatted bug, got %d", got.Total)
		}
	})

	t.Run("multiple_ids", func(t *testing.T) {
		got := lintJSON(t, bugBare.ID, taskBare.ID)
		if got.Issues < 2 {
			t.Errorf("expected at least 2 issues with warnings, got %d", got.Issues)
		}
	})

	t.Run("by_type_bug", func(t *testing.T) {
		got := lintJSON(t, "--type", "bug")
		if !hasID(got.Results, bugBare.ID) {
			t.Errorf("expected bare bug %s in --type bug results", bugBare.ID)
		}
	})

	t.Run("by_type_chore_no_warnings", func(t *testing.T) {
		got := lintJSON(t, "--type", "chore")
		if got.Total != 0 {
			t.Errorf("expected 0 warnings for chores, got %d", got.Total)
		}
	})

	t.Run("status_all_includes_closed", func(t *testing.T) {
		got := lintJSON(t, "--status", "all")
		if !hasID(got.Results, closedBug.ID) {
			t.Errorf("expected closed bug %s in --status all results", closedBug.ID)
		}
	})

	t.Run("status_default_excludes_closed", func(t *testing.T) {
		got := lintJSON(t)
		if hasID(got.Results, closedBug.ID) {
			t.Errorf("closed bug %s should be excluded by default", closedBug.ID)
		}
	})

	t.Run("clean_issues_not_in_results", func(t *testing.T) {
		got := lintJSON(t)
		if hasID(got.Results, bugGood.ID) {
			t.Errorf("well-formatted bug %s should not have warnings", bugGood.ID)
		}
	})

	t.Run("json_missing_sections", func(t *testing.T) {
		got := lintJSON(t, bugBare.ID)
		if len(got.Results) == 0 {
			t.Fatal("expected results")
		}
		if len(got.Results[0].Missing) == 0 {
			t.Error("expected non-empty 'missing' array for bare bug")
		}
	})

	t.Run("exit_code_1_on_warnings", func(t *testing.T) {
		// Non-JSON lint returns SilentExit (exit 1) when warnings exist.
		out, err := bdProxiedRun(t, bd, p.dir, "lint")
		if err == nil {
			t.Errorf("expected non-zero exit when warnings exist, got success:\n%s", out)
		}
	})

	t.Run("exit_code_0_when_clean", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "lint", "--type", "chore")
		if err != nil {
			t.Errorf("expected exit 0 for chores, got err %v:\n%s", err, out)
		}
	})

	t.Run("nonexistent_id_graceful", func(t *testing.T) {
		// Unknown ids are logged to stderr and skipped; the command still succeeds
		// with zero warnings.
		got := lintJSON(t, p.prefix+"-nonexistent999")
		if got.Total != 0 {
			t.Errorf("expected 0 warnings for nonexistent issue, got %d", got.Total)
		}
	})

	t.Run("human_readable_warnings", func(t *testing.T) {
		out, _ := bdProxiedRun(t, bd, p.dir, "lint")
		if !strings.Contains(string(out), "Missing:") {
			t.Errorf("expected 'Missing:' in human output: %s", out)
		}
	})

	t.Run("human_readable_clean", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "lint", "--type", "chore")
		if err != nil {
			t.Fatalf("lint --type chore: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "No template warnings") {
			t.Errorf("expected 'No template warnings' for chores: %s", out)
		}
	})
}

func TestProxiedServerStale(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "stl")

	// Seed mirrors cmd/bd/stale_embedded_test.go.
	stale1 := bdProxiedCreate(t, bd, p.dir, "Stale open issue", "--type", "task")
	stale2 := bdProxiedCreate(t, bd, p.dir, "Stale in_progress issue", "--type", "task")
	bdProxiedUpdate(t, bd, p.dir, stale2.ID, "--status", "in_progress")
	stale3 := bdProxiedCreate(t, bd, p.dir, "Stale bug", "--type", "bug", "--assignee", "alice")
	fresh1 := bdProxiedCreate(t, bd, p.dir, "Fresh issue", "--type", "task")
	closedIssue := bdProxiedCreate(t, bd, p.dir, "Closed issue", "--type", "task")
	if out, err := bdProxiedRun(t, bd, p.dir, "close", closedIssue.ID); err != nil {
		t.Fatalf("close %s: %v\n%s", closedIssue.ID, err, out)
	}

	// Backdate updated_at to 60 days ago for the three "stale" issues. Done last
	// so the status change above does not overwrite the backdated timestamp.
	db := openProxiedDB(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	backdate := time.Now().UTC().AddDate(0, 0, -60)
	for _, id := range []string{stale1.ID, stale2.ID, stale3.ID} {
		if _, err := db.ExecContext(ctx,
			"UPDATE issues SET updated_at = ? WHERE id = ?", backdate, id); err != nil {
			t.Fatalf("backdate updated_at for %s: %v", id, err)
		}
	}

	staleEntries := func(t *testing.T, args ...string) []map[string]interface{} {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"stale", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("stale %v: %v\n%s", args, err, out)
		}
		var entries []map[string]interface{}
		if err := json.Unmarshal(out, &entries); err != nil {
			t.Fatalf("unmarshal stale: %v\n%s", err, out)
		}
		return entries
	}
	idSet := func(entries []map[string]interface{}) map[string]bool {
		set := make(map[string]bool, len(entries))
		for _, e := range entries {
			if id, ok := e["id"].(string); ok {
				set[id] = true
			}
		}
		return set
	}

	t.Run("basic_stale_default_days", func(t *testing.T) {
		entries := staleEntries(t)
		if len(entries) < 3 {
			t.Errorf("expected at least 3 stale issues, got %d", len(entries))
		}
		if idSet(entries)[fresh1.ID] {
			t.Errorf("fresh issue %s should not be stale", fresh1.ID)
		}
	})

	t.Run("custom_days_above_cutoff", func(t *testing.T) {
		// Issues are 60 days stale, so --days 90 finds none.
		entries := staleEntries(t, "--days", "90")
		if len(entries) != 0 {
			t.Errorf("expected 0 stale issues at 90 days, got %d", len(entries))
		}
	})

	t.Run("custom_days_lower", func(t *testing.T) {
		entries := staleEntries(t, "--days", "1")
		if len(entries) < 3 {
			t.Errorf("expected at least 3 stale issues at 1 day, got %d", len(entries))
		}
	})

	t.Run("status_filter_in_progress", func(t *testing.T) {
		entries := staleEntries(t, "--status", "in_progress")
		if !idSet(entries)[stale2.ID] {
			t.Errorf("expected stale in_progress issue %s in results", stale2.ID)
		}
	})

	t.Run("status_filter_open", func(t *testing.T) {
		entries := staleEntries(t, "--status", "open")
		if idSet(entries)[stale2.ID] {
			t.Errorf("in_progress issue %s should not appear with --status open", stale2.ID)
		}
	})

	t.Run("limit_caps_results", func(t *testing.T) {
		entries := staleEntries(t, "--limit", "1")
		if len(entries) > 1 {
			t.Errorf("expected at most 1 result with --limit 1, got %d", len(entries))
		}
	})

	t.Run("json_output_is_array", func(t *testing.T) {
		entries := staleEntries(t)
		if entries == nil {
			t.Error("expected non-nil JSON array")
		}
	})

	t.Run("json_issue_has_fields", func(t *testing.T) {
		entries := staleEntries(t)
		if len(entries) == 0 {
			t.Skip("no stale issues to check")
		}
		for _, key := range []string{"id", "title", "status"} {
			if _, ok := entries[0][key]; !ok {
				t.Errorf("expected %q key in stale issue JSON", key)
			}
		}
	})

	t.Run("no_stale_issues_message", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "stale", "--days", "90")
		if err != nil {
			t.Fatalf("stale --days 90: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "No stale issues") {
			t.Errorf("expected 'No stale issues' message: %s", out)
		}
	})

	t.Run("invalid_days_zero", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "stale", "--days", "0")
		if err == nil {
			t.Fatalf("expected error for --days 0, got success: %s", out)
		}
		if !strings.Contains(string(out), "at least 1") {
			t.Errorf("expected 'at least 1' error: %s", out)
		}
	})

	t.Run("invalid_status", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "stale", "--status", "bogus")
		if err == nil {
			t.Fatalf("expected error for --status bogus, got success: %s", out)
		}
		if !strings.Contains(string(out), "invalid status") {
			t.Errorf("expected 'invalid status' error: %s", out)
		}
	})

	t.Run("boundary_exact_cutoff", func(t *testing.T) {
		entries60 := staleEntries(t, "--days", "60")
		entries61 := staleEntries(t, "--days", "61")
		if len(entries60) < len(entries61) {
			t.Errorf("--days 60 should find >= issues than --days 61: got %d vs %d", len(entries60), len(entries61))
		}
	})

	t.Run("human_readable_format", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "stale")
		if err != nil {
			t.Fatalf("stale: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "Stale issues") {
			t.Errorf("expected 'Stale issues' header: %s", s)
		}
		if !strings.Contains(s, "days ago") {
			t.Errorf("expected 'days ago' in output: %s", s)
		}
	})

	t.Run("closed_issues_excluded", func(t *testing.T) {
		entries := staleEntries(t)
		if idSet(entries)[closedIssue.ID] {
			t.Errorf("closed issue %s should not appear in stale results", closedIssue.ID)
		}
	})
}
