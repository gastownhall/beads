//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func bdProxiedQueryJSON(t *testing.T, bd string, p proxiedProject, args ...string) []*types.IssueWithCounts {
	t.Helper()
	fullArgs := append([]string{"query", "--json"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd query --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		if strings.Contains(stdout, "null") || strings.TrimSpace(stdout) == "" {
			return nil
		}
		t.Fatalf("no JSON array found in query output:\n%s", stdout)
	}
	var issues []*types.IssueWithCounts
	if err := json.Unmarshal([]byte(stdout[start:]), &issues); err != nil {
		t.Fatalf("failed to parse query JSON output: %v\nraw: %s", err, stdout[start:])
	}
	return issues
}

func bdProxiedQueryCapture(t *testing.T, bd string, p proxiedProject, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"query"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd query %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout, stderr
}

func bdProxiedQueryFail(t *testing.T, bd string, p proxiedProject, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"query"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd query %s to fail, but it succeeded:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func TestProxiedServerQuery(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "qry")
	seed := seedProxiedListData(t, bd, p)

	t.Run("filter_only_and_chain", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "status=open AND priority<=1")
		for _, issue := range issues {
			if issue.Status != types.StatusOpen {
				t.Errorf("expected status open, got %s for %s", issue.Status, issue.ID)
			}
			if issue.Priority > 1 {
				t.Errorf("expected priority <= 1, got %d for %s", issue.Priority, issue.ID)
			}
		}
		if !containsID(issues, seed.openBug) {
			t.Errorf("P0 open bug should match status=open AND priority<=1, got %v", listIssueIDs(issues))
		}
	})

	t.Run("excludes_closed_by_default", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "priority>=0")
		for _, issue := range issues {
			if issue.Status == types.StatusClosed {
				t.Errorf("closed issue %s should be excluded without --all", issue.ID)
			}
		}
	})

	t.Run("type_bug", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "type=bug")
		for _, issue := range issues {
			if issue.IssueType != types.TypeBug {
				t.Errorf("expected type bug, got %s for %s", issue.IssueType, issue.ID)
			}
		}
		if !containsID(issues, seed.openBug) {
			t.Errorf("open bug should match type=bug, got %v", listIssueIDs(issues))
		}
	})

	t.Run("or_predicate_post_filters", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "type=bug OR type=feature")
		for _, issue := range issues {
			if issue.IssueType != types.TypeBug && issue.IssueType != types.TypeFeature {
				t.Errorf("expected bug or feature, got %s for %s", issue.IssueType, issue.ID)
			}
		}
		if !containsID(issues, seed.openBug) {
			t.Errorf("bug should match (bug OR feature), got %v", listIssueIDs(issues))
		}
		if !containsID(issues, seed.feature) {
			t.Errorf("feature should match (bug OR feature), got %v", listIssueIDs(issues))
		}
	})

	t.Run("offset_plus_limit_returns_slice", func(t *testing.T) {
		full := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--all", "--limit", "0")
		if len(full) < 7 {
			t.Fatalf("seeded fixture should yield >= 7 issues, got %d", len(full))
		}
		page := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--all", "--offset", "3", "--limit", "4")
		if len(page) != 4 {
			t.Fatalf("--offset 3 --limit 4 should return 4 rows, got %d", len(page))
		}
		for i := 0; i < 4; i++ {
			if page[i].ID != full[3+i].ID {
				t.Errorf("position %d (offset 3+i): paged returned %s; unlimited had %s at index %d",
					i, page[i].ID, full[3+i].ID, 3+i)
			}
		}
	})

	t.Run("page_walk_reconstructs_full_result", func(t *testing.T) {
		full := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--all", "--limit", "0")
		const pageSize = 4
		var walked []string
		seen := make(map[string]bool)
		for offset := 0; ; offset += pageSize {
			page := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--all",
				"--limit", fmt.Sprintf("%d", pageSize),
				"--offset", fmt.Sprintf("%d", offset))
			if len(page) == 0 {
				break
			}
			for _, iwc := range page {
				if seen[iwc.ID] {
					t.Errorf("page at offset %d returned duplicate %s", offset, iwc.ID)
				}
				seen[iwc.ID] = true
				walked = append(walked, iwc.ID)
			}
			if len(page) < pageSize {
				break
			}
		}
		if len(walked) != len(full) {
			t.Fatalf("page walk got %d issues, unlimited got %d", len(walked), len(full))
		}
		for i, id := range walked {
			if id != full[i].ID {
				t.Errorf("position %d: page walk had %s; unlimited had %s", i, id, full[i].ID)
			}
		}
	})

	t.Run("limit_truncates_and_hint_stays_off_stdout", func(t *testing.T) {
		full := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--all", "--limit", "0")
		if len(full) <= 2 {
			t.Fatalf("fixture should have > 2 issues, got %d", len(full))
		}
		page := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--all", "--limit", "2")
		if len(page) != 2 {
			t.Errorf("--limit 2 should cap at 2 rows, got %d", len(page))
		}
		stdout, _ := bdProxiedQueryCapture(t, bd, p, "priority>=0", "--all", "--limit", "2")
		if strings.Contains(stdout, "more results matched") {
			t.Errorf("truncation hint must not leak into stdout:\n%s", stdout)
		}
	})

	t.Run("offset_rejected_for_predicate_query", func(t *testing.T) {
		out := bdProxiedQueryFail(t, bd, p, "type=bug OR type=feature", "--offset", "1")
		if !strings.Contains(out, "--offset is not supported with OR/predicate queries") {
			t.Errorf("expected predicate+offset rejection, got: %s", out)
		}
	})

	t.Run("offset_rejected_with_sort", func(t *testing.T) {
		for _, sortField := range []string{"priority", "created", "id"} {
			out := bdProxiedQueryFail(t, bd, p, "priority>=0", "--all", "--sort", sortField, "--offset", "1")
			if !strings.Contains(out, "--offset is not supported with --sort") {
				t.Errorf("expected --sort %s + offset rejection, got: %s", sortField, out)
			}
		}
	})

	t.Run("negative_offset_rejected", func(t *testing.T) {
		out := bdProxiedQueryFail(t, bd, p, "priority>=0", "--offset=-1")
		if !strings.Contains(out, "--offset must be non-negative") {
			t.Errorf("expected negative-offset rejection, got: %s", out)
		}
	})

	t.Run("parse_only_short_circuits", func(t *testing.T) {
		stdout, _ := bdProxiedQueryCapture(t, bd, p, "status=open AND priority<=1", "--parse-only")
		if !strings.Contains(stdout, "Parsed query:") {
			t.Errorf("expected parsed-query output, got: %s", stdout)
		}
	})

	t.Run("no_match_returns_empty", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "title=this-title-matches-nothing-xyz")
		if len(issues) != 0 {
			t.Errorf("expected 0 issues for non-matching query, got %d", len(issues))
		}
	})

	t.Run("assignee_equals", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "assignee=alice")
		for _, issue := range issues {
			if issue.Assignee != "alice" {
				t.Errorf("expected assignee alice, got %q for %s", issue.Assignee, issue.ID)
			}
		}
		if !containsID(issues, seed.openBug) {
			t.Errorf("alice's open bug should match assignee=alice, got %v", listIssueIDs(issues))
		}
	})

	t.Run("priority_greater_than", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "priority>1")
		for _, issue := range issues {
			if issue.Priority <= 1 {
				t.Errorf("expected priority > 1, got %d for %s", issue.Priority, issue.ID)
			}
		}
		if !containsID(issues, seed.decision) {
			t.Errorf("P4 decision should match priority>1, got %v", listIssueIDs(issues))
		}
	})

	t.Run("priority_less_equal", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "priority<=1")
		for _, issue := range issues {
			if issue.Priority > 1 {
				t.Errorf("expected priority <= 1, got %d for %s", issue.Priority, issue.ID)
			}
		}
		if !containsID(issues, seed.openBug) {
			t.Errorf("P0 open bug should match priority<=1, got %v", listIssueIDs(issues))
		}
	})

	t.Run("not_type", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "NOT type=task")
		for _, issue := range issues {
			if issue.IssueType == types.TypeTask {
				t.Errorf("NOT type=task should exclude tasks, got task %s", issue.ID)
			}
		}
		if !containsID(issues, seed.openBug) {
			t.Errorf("bug should match NOT type=task, got %v", listIssueIDs(issues))
		}
	})

	t.Run("type_inequality", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "type!=task")
		for _, issue := range issues {
			if issue.IssueType == types.TypeTask {
				t.Errorf("type!=task should exclude tasks, got task %s", issue.ID)
			}
		}
		if !containsID(issues, seed.feature) {
			t.Errorf("feature should match type!=task, got %v", listIssueIDs(issues))
		}
	})

	t.Run("all_includes_closed", func(t *testing.T) {
		closed := bdProxiedCreate(t, bd, p.dir, "Closed query task", "--type", "task")
		bdProxiedClose(t, bd, p.dir, closed.ID)

		def := bdProxiedQueryJSON(t, bd, p, "type=task")
		if containsID(def, closed.ID) {
			t.Errorf("closed task %s should be excluded without --all", closed.ID)
		}

		all := bdProxiedQueryJSON(t, bd, p, "type=task", "--all")
		if !containsID(all, closed.ID) {
			t.Errorf("closed task %s should appear with --all, got %v", closed.ID, listIssueIDs(all))
		}
	})

	t.Run("sort_priority_ascending", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--sort", "priority")
		for i := 1; i < len(issues); i++ {
			if issues[i-1].Priority > issues[i].Priority {
				t.Errorf("--sort priority should be ascending, got P%d before P%d at index %d",
					issues[i-1].Priority, issues[i].Priority, i)
			}
		}
	})

	t.Run("sort_priority_reverse", func(t *testing.T) {
		issues := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--sort", "priority", "--reverse")
		for i := 1; i < len(issues); i++ {
			if issues[i-1].Priority < issues[i].Priority {
				t.Errorf("--sort priority --reverse should be descending, got P%d before P%d at index %d",
					issues[i-1].Priority, issues[i].Priority, i)
			}
		}
	})

	t.Run("long_text_output", func(t *testing.T) {
		stdout, _ := bdProxiedQueryCapture(t, bd, p, "status=open", "--long")
		if !strings.Contains(stdout, "Found") {
			t.Errorf("--long should contain 'Found N issues:', got:\n%s", stdout)
		}
		if !strings.Contains(stdout, seed.openBug) {
			t.Errorf("--long output should contain open bug %s, got:\n%s", seed.openBug, stdout)
		}
	})

	t.Run("no_expression_fails", func(t *testing.T) {
		_, _, err := bdProxiedRunBuffers(t, bd, p.dir, "query")
		if err == nil {
			t.Error("bd query with no expression should fail")
		}
	})

	t.Run("invalid_expression_fails", func(t *testing.T) {
		out := bdProxiedQueryFail(t, bd, p, "===invalid===")
		if !strings.Contains(out, "parsing query") {
			t.Errorf("expected parse error for invalid expression, got: %s", out)
		}
	})

	t.Run("concurrent_queries", func(t *testing.T) {
		const numWorkers = 8
		variants := [][]string{
			{"status=open"},
			{"type=task"},
			{"--sort", "priority", "priority>=1"},
			{"--limit", "3", "status=open"},
			{"--reverse", "--sort", "priority", "status=open"},
		}
		errs := make([]error, numWorkers)
		var wg sync.WaitGroup
		wg.Add(numWorkers)
		for w := 0; w < numWorkers; w++ {
			go func(worker int) {
				defer wg.Done()
				v := variants[worker%len(variants)]
				args := append([]string{"query", "--json"}, v...)
				_, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, args...)
				if err != nil {
					errs[worker] = fmt.Errorf("worker %d query %v failed: %v\n%s", worker, v, err, stderr)
				}
			}(w)
		}
		wg.Wait()
		for _, e := range errs {
			if e != nil {
				t.Error(e)
			}
		}
	})

	t.Run("ephemeral_true_returns_only_wisps", func(t *testing.T) {
		var wispIDs []string
		for i := 0; i < 3; i++ {
			w := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Wisp query %d", i), "--ephemeral")
			wispIDs = append(wispIDs, w.ID)
		}

		issues := bdProxiedQueryJSON(t, bd, p, "ephemeral=true", "--limit", "0")
		for _, wid := range wispIDs {
			if !containsID(issues, wid) {
				t.Errorf("ephemeral wisp %s should appear in ephemeral=true query, got %v",
					wid, listIssueIDs(issues))
			}
		}
		for _, issue := range issues {
			if issue.Issue == nil || !issue.Ephemeral {
				t.Errorf("ephemeral=true must return only wisps, but %s is not ephemeral", issue.ID)
			}
		}
		if containsID(issues, seed.openBug) {
			t.Errorf("permanent issue %s must not appear in ephemeral=true query", seed.openBug)
		}
	})

	t.Run("default_query_merges_issues_and_wisps", func(t *testing.T) {
		w := bdProxiedCreate(t, bd, p.dir, "Wisp merge probe", "--ephemeral")

		merged := bdProxiedQueryJSON(t, bd, p, "priority>=0", "--all", "--limit", "0")
		if !containsID(merged, w.ID) {
			t.Errorf("default query should merge in wisp %s, got %v", w.ID, listIssueIDs(merged))
		}
		if !containsID(merged, seed.openBug) {
			t.Errorf("default query should include permanent issue %s", seed.openBug)
		}
	})

	t.Run("ephemeral_and_label_filters_wisps", func(t *testing.T) {
		const label = "wq-compound"
		wisp := bdProxiedCreate(t, bd, p.dir, "Labeled wisp", "--ephemeral", "--label", label)
		perm := bdProxiedCreate(t, bd, p.dir, "Labeled permanent", "--label", label)

		issues := bdProxiedQueryJSON(t, bd, p, "ephemeral=true AND label="+label, "--limit", "0")
		if !containsID(issues, wisp.ID) {
			t.Errorf("ephemeral=true AND label=%s should return wisp %s, got %v", label, wisp.ID, listIssueIDs(issues))
		}
		if containsID(issues, perm.ID) {
			t.Errorf("ephemeral=true AND label=%s must not return permanent issue %s", label, perm.ID)
		}
		for _, issue := range issues {
			if issue.Issue == nil || !issue.Ephemeral {
				t.Errorf("compound ephemeral+label query returned non-wisp %s", issue.ID)
			}
		}
	})

	t.Run("ephemeral_and_parent_filters_wisps", func(t *testing.T) {
		parent := bdProxiedCreate(t, bd, p.dir, "Parent wisp", "--ephemeral")
		child := bdProxiedCreate(t, bd, p.dir, "Child wisp", "--ephemeral", "--parent", parent.ID)

		issues := bdProxiedQueryJSON(t, bd, p, "ephemeral=true AND parent="+parent.ID, "--limit", "0")
		if !containsID(issues, child.ID) {
			t.Errorf("ephemeral=true AND parent=%s should return child wisp %s, got %v", parent.ID, child.ID, listIssueIDs(issues))
		}
		for _, issue := range issues {
			if issue.Issue == nil || !issue.Ephemeral {
				t.Errorf("compound ephemeral+parent query returned non-wisp %s", issue.ID)
			}
		}
	})
}
