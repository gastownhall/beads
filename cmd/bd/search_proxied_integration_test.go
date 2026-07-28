//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func bdProxiedSearch(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"search"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd search %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedSearchFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"search"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd search %s to fail, got:\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func bdProxiedSearchJSON(t *testing.T, bd, dir string, args ...string) []map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"search", "--json"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd search --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		return nil
	}
	var results []map[string]interface{}
	if err := json.Unmarshal([]byte(stdout[start:]), &results); err != nil {
		t.Fatalf("parse search JSON: %v\nraw: %s", err, stdout[start:])
	}
	return results
}

func searchResultIDs(results []map[string]interface{}) map[string]bool {
	ids := map[string]bool{}
	for _, r := range results {
		if id, ok := r["id"].(string); ok {
			ids[id] = true
		}
	}
	return ids
}

func TestProxiedServerSearch(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "sp")
	taskA := bdProxiedCreate(t, bd, p.dir, "Alpha task", "--type", "task", "--priority", "1", "--assignee", "alice", "--description", "Important alpha work", "--label", "urgent")
	taskB := bdProxiedCreate(t, bd, p.dir, "Beta bug", "--type", "bug", "--priority", "3", "--assignee", "bob", "--description", "Beta bug description", "--label", "backend")
	taskC := bdProxiedCreate(t, bd, p.dir, "Gamma feature", "--type", "feature", "--priority", "2", "--label", "urgent", "--label", "frontend")
	taskD := bdProxiedCreate(t, bd, p.dir, "Delta task no desc", "--type", "task")
	closedTask := bdProxiedCreate(t, bd, p.dir, "Closed epsilon", "--type", "task")
	bdProxiedClose(t, bd, p.dir, closedTask.ID)

	t.Run("positional_query", func(t *testing.T) {
		if !searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "Alpha"))[taskA.ID] {
			t.Errorf("expected to find %s in search for 'Alpha'", taskA.ID)
		}
	})

	t.Run("query_flag", func(t *testing.T) {
		if !searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "--query", "Beta"))[taskB.ID] {
			t.Errorf("expected to find %s in search for 'Beta'", taskB.ID)
		}
	})

	t.Run("no_results", func(t *testing.T) {
		if got := bdProxiedSearchJSON(t, bd, p.dir, "nonexistentxyz123"); len(got) != 0 {
			t.Errorf("expected 0 results, got %d", len(got))
		}
	})

	t.Run("missing_query_rejected", func(t *testing.T) {
		out := bdProxiedSearchFail(t, bd, p.dir)
		if !strings.Contains(out, "query is required") {
			t.Errorf("expected 'query is required' error, got:\n%s", out)
		}
	})

	t.Run("status_open_excludes_closed", func(t *testing.T) {
		if searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "task", "--status", "open"))[closedTask.ID] {
			t.Error("should not find closed task with --status open")
		}
	})

	t.Run("default_excludes_closed", func(t *testing.T) {
		if searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-"))[closedTask.ID] {
			t.Error("default search should exclude closed issues")
		}
	})

	t.Run("status_closed", func(t *testing.T) {
		if !searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "epsilon", "--status", "closed"))[closedTask.ID] {
			t.Error("expected to find closed task with --status closed")
		}
	})

	t.Run("status_all", func(t *testing.T) {
		ids := searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--status", "all"))
		if !ids[taskA.ID] || !ids[closedTask.ID] {
			t.Error("expected both open and closed issues with --status all")
		}
	})

	t.Run("assignee", func(t *testing.T) {
		ids := searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--assignee", "alice"))
		if ids[taskB.ID] {
			t.Error("bob's task should not appear with --assignee alice")
		}
		if !ids[taskA.ID] {
			t.Error("expected alice's task in results")
		}
	})

	t.Run("no_assignee", func(t *testing.T) {
		ids := searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--no-assignee"))
		if ids[taskA.ID] || ids[taskB.ID] {
			t.Error("assigned tasks should not appear with --no-assignee")
		}
	})

	t.Run("type", func(t *testing.T) {
		results := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--type", "bug")
		if len(results) == 0 {
			t.Fatal("expected bug results")
		}
		for _, r := range results {
			if r["issue_type"] != "bug" {
				t.Errorf("expected type=bug, got %v", r["issue_type"])
			}
		}
	})

	t.Run("label_and", func(t *testing.T) {
		results := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--label", "urgent", "--label", "frontend")
		if len(results) != 1 || results[0]["id"] != taskC.ID {
			t.Errorf("expected only %s with --label urgent --label frontend, got %d results", taskC.ID, len(results))
		}
	})

	t.Run("label_any", func(t *testing.T) {
		ids := searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--label-any", "urgent,frontend"))
		if !ids[taskA.ID] || !ids[taskC.ID] {
			t.Errorf("expected both urgent-labeled tasks with --label-any: %v", ids)
		}
	})

	t.Run("no_labels", func(t *testing.T) {
		ids := searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--no-labels"))
		if ids[taskA.ID] || ids[taskC.ID] {
			t.Error("labeled tasks should not appear with --no-labels")
		}
	})

	t.Run("limit", func(t *testing.T) {
		if got := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--limit", "2"); len(got) > 2 {
			t.Errorf("expected at most 2 results with --limit 2, got %d", len(got))
		}
	})

	t.Run("limit_zero_unlimited", func(t *testing.T) {
		if got := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--limit", "0", "--status", "all"); len(got) < 5 {
			t.Errorf("expected all issues with --limit 0, got %d", len(got))
		}
	})

	t.Run("sort_priority", func(t *testing.T) {
		results := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--sort", "priority")
		if len(results) < 2 {
			t.Skip("need at least 2 results to test sort")
		}
		if results[0]["priority"].(float64) > results[len(results)-1]["priority"].(float64) {
			t.Error("expected ascending priority sort")
		}
	})

	t.Run("sort_reverse", func(t *testing.T) {
		results := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--sort", "priority", "--reverse")
		if len(results) < 2 {
			t.Skip("need at least 2 results to test reverse sort")
		}
		if results[0]["priority"].(float64) < results[len(results)-1]["priority"].(float64) {
			t.Error("expected descending priority sort with --reverse")
		}
	})

	t.Run("priority_min", func(t *testing.T) {
		for _, r := range bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--priority-min", "2") {
			if int(r["priority"].(float64)) < 2 {
				t.Errorf("expected priority >= 2, got %v for %s", r["priority"], r["id"])
			}
		}
	})

	t.Run("priority_max", func(t *testing.T) {
		for _, r := range bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--priority-max", "2") {
			if int(r["priority"].(float64)) > 2 {
				t.Errorf("expected priority <= 2, got %v for %s", r["priority"], r["id"])
			}
		}
	})

	t.Run("desc_contains", func(t *testing.T) {
		if !searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--desc-contains", "alpha"))[taskA.ID] {
			t.Error("expected to find taskA with --desc-contains alpha")
		}
	})

	t.Run("empty_description", func(t *testing.T) {
		ids := searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--empty-description"))
		if !ids[taskD.ID] {
			t.Error("expected to find task without description")
		}
		if ids[taskA.ID] {
			t.Error("task with description should not appear with --empty-description")
		}
	})

	t.Run("long_text_output", func(t *testing.T) {
		out := bdProxiedSearch(t, bd, p.dir, "Alpha", "--long")
		if !strings.Contains(out, "Alpha task") {
			t.Errorf("expected long output to contain title, got:\n%s", out)
		}
	})

	t.Run("created_after", func(t *testing.T) {
		if got := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--created-after", "2020-01-01"); len(got) == 0 {
			t.Error("expected results with --created-after 2020-01-01")
		}
	})

	t.Run("created_before", func(t *testing.T) {
		if got := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--created-before", "2020-01-01"); len(got) != 0 {
			t.Errorf("expected 0 results with --created-before 2020-01-01, got %d", len(got))
		}
	})

	t.Run("metadata_field", func(t *testing.T) {
		mdIssue := bdProxiedCreate(t, bd, p.dir, "Metadata issue", "--type", "task", "--metadata", `{"team":"platform"}`)
		if !searchResultIDs(bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--metadata-field", "team=platform"))[mdIssue.ID] {
			t.Errorf("expected to find %s with --metadata-field team=platform", mdIssue.ID)
		}
	})

	t.Run("has_metadata_key", func(t *testing.T) {
		if got := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--has-metadata-key", "team"); len(got) == 0 {
			t.Error("expected results with --has-metadata-key team")
		}
	})

	t.Run("invalid_metadata_field_rejected", func(t *testing.T) {
		out := bdProxiedSearchFail(t, bd, p.dir, "sp-", "--metadata-field", "noequalsign")
		if !strings.Contains(out, "metadata-field") {
			t.Errorf("expected metadata-field error, got:\n%s", out)
		}
	})

	t.Run("combined_filters", func(t *testing.T) {
		results := bdProxiedSearchJSON(t, bd, p.dir, "sp-", "--type", "task", "--assignee", "alice", "--label", "urgent")
		if len(results) != 1 || results[0]["id"] != taskA.ID {
			t.Errorf("expected only %s with combined filters, got %d results", taskA.ID, len(results))
		}
	})

	t.Run("json_hydrates_labels_and_counts", func(t *testing.T) {
		results := bdProxiedSearchJSON(t, bd, p.dir, "Gamma")
		var row map[string]interface{}
		for _, r := range results {
			if r["id"] == taskC.ID {
				row = r
			}
		}
		if row == nil {
			t.Fatalf("taskC not found in search results")
		}
		labels := map[string]bool{}
		if raw, ok := row["labels"].([]interface{}); ok {
			for _, l := range raw {
				labels[l.(string)] = true
			}
		}
		if !labels["urgent"] || !labels["frontend"] {
			t.Errorf("expected labels urgent+frontend hydrated in JSON, got %v", row["labels"])
		}
		for _, key := range []string{"dependency_count", "dependent_count", "comment_count"} {
			if _, ok := row[key]; !ok {
				t.Errorf("expected %s field in counts JSON", key)
			}
		}
	})

	t.Run("json_comment_count", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "Countable comment target", "--type", "task")
		bdProxiedComment(t, bd, p.dir, issue.ID, "a note")

		results := bdProxiedSearchJSON(t, bd, p.dir, "Countable")
		var row map[string]interface{}
		for _, r := range results {
			if r["id"] == issue.ID {
				row = r
			}
		}
		if row == nil {
			t.Fatalf("comment target not found in search results")
		}
		if int(row["comment_count"].(float64)) != 1 {
			t.Errorf("comment_count = %v, want 1", row["comment_count"])
		}
	})

	t.Run("wisp_appears_in_search", func(t *testing.T) {
		wp := newSharedProxiedProject(t, bd, "sw")
		wisp := bdProxiedCreate(t, bd, wp.dir, "Wispy alpha search target", "--ephemeral")
		if !searchResultIDs(bdProxiedSearchJSON(t, bd, wp.dir, "Wispy"))[wisp.ID] {
			t.Errorf("expected ephemeral wisp %s to appear in search results", wisp.ID)
		}
	})

	t.Run("wisp_labels_hydrated_from_wisp_labels", func(t *testing.T) {
		wp := newSharedProxiedProject(t, bd, "swl")
		wisp := bdProxiedCreate(t, bd, wp.dir, "Wisp label search", "--ephemeral")
		bdProxiedLabel(t, bd, wp.dir, "add", wisp.ID, "wlabel")

		db := openProxiedDB(t, wp)
		var wispCount, permCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", wisp.ID).Scan(&wispCount); err != nil {
			t.Fatalf("count wisp_labels: %v", err)
		}
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM labels WHERE issue_id = ?", wisp.ID).Scan(&permCount); err != nil {
			t.Fatalf("count labels: %v", err)
		}
		if wispCount != 1 || permCount != 0 {
			t.Fatalf("wisp label routing: wisp_labels=%d labels=%d, want 1/0", wispCount, permCount)
		}

		results := bdProxiedSearchJSON(t, bd, wp.dir, "Wisp")
		var row map[string]interface{}
		for _, r := range results {
			if r["id"] == wisp.ID {
				row = r
			}
		}
		if row == nil {
			t.Fatalf("wisp not found in search results")
		}
		labels := map[string]bool{}
		if raw, ok := row["labels"].([]interface{}); ok {
			for _, l := range raw {
				labels[l.(string)] = true
			}
		}
		if !labels["wlabel"] {
			t.Errorf("expected wisp label 'wlabel' hydrated from wisp_labels in search JSON, got %v", row["labels"])
		}
	})
}
