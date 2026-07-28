//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxiedServerStatus(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "sts")

	// Seed the same known status distribution as TestEmbeddedStatus:
	// 3 open, 1 in_progress, 1 closed (5 total).
	bdProxiedCreate(t, bd, p.dir, "Status open 1", "--type", "task")
	bdProxiedCreate(t, bd, p.dir, "Status open 2", "--type", "bug")
	ip := bdProxiedCreate(t, bd, p.dir, "Status in_progress", "--type", "task", "--assignee", "alice")
	bdProxiedUpdate(t, bd, p.dir, ip.ID, "--status", "in_progress")
	closed := bdProxiedCreate(t, bd, p.dir, "Status closed", "--type", "task")
	if out, err := bdProxiedRun(t, bd, p.dir, "close", closed.ID); err != nil {
		t.Fatalf("close %s: %v\n%s", closed.ID, err, out)
	}
	bdProxiedCreate(t, bd, p.dir, "Status assigned bob", "--type", "task", "--assignee", "bob")

	statusJSON := func(t *testing.T, args ...string) StatusOutput {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"status", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("status %v: %v\n%s", args, err, out)
		}
		var got StatusOutput
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal status: %v\n%s", err, out)
		}
		if got.Summary == nil {
			t.Fatalf("status summary is nil: %s", out)
		}
		return got
	}

	statusText := func(t *testing.T, args ...string) string {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"status"}, args...)...)
		if err != nil {
			t.Fatalf("status %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	// ===== Default (human-readable) output =====

	t.Run("default_output", func(t *testing.T) {
		out := statusText(t)
		for _, want := range []string{"Issue Database Status", "Total Issues:", "Open:"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected %q in output: %s", want, out)
			}
		}
	})

	t.Run("human_readable_sections", func(t *testing.T) {
		out := statusText(t)
		for _, want := range []string{"Summary:", "In Progress:", "Ready to Work:"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected %q section: %s", want, out)
			}
		}
	})

	// ===== --json structure =====

	t.Run("json_output_structure", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "status", "--json")
		if err != nil {
			t.Fatalf("status --json: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		summary, ok := m["summary"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected 'summary' object: %v", m)
		}
		for _, key := range []string{"total_issues", "open_issues", "in_progress_issues", "closed_issues"} {
			if _, ok := summary[key]; !ok {
				t.Errorf("expected '%s' key in summary", key)
			}
		}
	})

	// ===== --json counts =====

	t.Run("json_counts_match", func(t *testing.T) {
		got := statusJSON(t)
		if got.Summary.TotalIssues < 5 {
			t.Errorf("expected at least 5 total issues, got %d", got.Summary.TotalIssues)
		}
		if got.Summary.OpenIssues < 3 {
			t.Errorf("expected at least 3 open issues, got %d", got.Summary.OpenIssues)
		}
		if got.Summary.InProgressIssues < 1 {
			t.Errorf("expected at least 1 in_progress issue, got %d", got.Summary.InProgressIssues)
		}
		if got.Summary.ClosedIssues < 1 {
			t.Errorf("expected at least 1 closed issue, got %d", got.Summary.ClosedIssues)
		}
	})

	t.Run("total_matches_list_all", func(t *testing.T) {
		got := statusJSON(t)
		want := len(bdProxiedListJSON(t, bd, p, "--all"))
		if got.Summary.TotalIssues != want {
			t.Errorf("status total = %d, want %d (list --all)", got.Summary.TotalIssues, want)
		}
	})

	t.Run("open_matches_list", func(t *testing.T) {
		got := statusJSON(t)
		want := len(bdProxiedListJSON(t, bd, p, "--status", "open"))
		if got.Summary.OpenIssues != want {
			t.Errorf("status open = %d, want %d (list --status open)", got.Summary.OpenIssues, want)
		}
	})

	// ===== --assigned =====

	t.Run("assigned_within_total", func(t *testing.T) {
		total := statusJSON(t).Summary.TotalIssues
		got := statusJSON(t, "--assigned")
		if got.Summary.TotalIssues < 0 || got.Summary.TotalIssues > total {
			t.Errorf("assigned total %d out of range [0, %d]", got.Summary.TotalIssues, total)
		}
	})

	// ===== --no-activity =====

	t.Run("no_activity_flag", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "status", "--json", "--no-activity")
		if err != nil {
			t.Fatalf("status --json --no-activity: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if _, ok := m["summary"]; !ok {
			t.Error("expected 'summary' even with --no-activity")
		}
		if activity, ok := m["recent_activity"]; ok && activity != nil {
			t.Error("expected no recent_activity with --no-activity")
		}
	})

	// ===== stats alias =====

	t.Run("stats_alias", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "stats", "--json")
		if err != nil {
			t.Fatalf("stats --json: %v\n%s", err, out)
		}
		var got StatusOutput
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal stats: %v\n%s", err, out)
		}
		if got.Summary == nil || got.Summary.TotalIssues != statusJSON(t).Summary.TotalIssues {
			t.Errorf("stats alias total mismatch")
		}
	})

	// ===== Empty database =====

	t.Run("empty_database", func(t *testing.T) {
		empty := newSharedProxiedProject(t, bd, "ste")
		out, err := bdProxiedRun(t, bd, empty.dir, "status", "--json")
		if err != nil {
			t.Fatalf("status --json (empty): %v\n%s", err, out)
		}
		var got StatusOutput
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		if got.Summary == nil {
			t.Fatalf("empty status summary is nil: %s", out)
		}
		if got.Summary.TotalIssues != 0 {
			t.Errorf("expected 0 total issues in empty db, got %d", got.Summary.TotalIssues)
		}
	})
}
