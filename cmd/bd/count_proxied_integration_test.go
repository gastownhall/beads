//go:build cgo

package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestProxiedServerCount(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "cnt")

	// Seed the same fixture set as TestEmbeddedCount so counts line up with the
	// proxied list cardinality parity checks below.
	bdProxiedCreate(t, bd, p.dir, "Count bug one", "--type", "bug", "--priority", "1", "--assignee", "alice")
	bdProxiedCreate(t, bd, p.dir, "Count bug two", "--type", "bug", "--priority", "2", "--assignee", "bob", "--description", "has a description")
	bdProxiedCreate(t, bd, p.dir, "Count task one", "--type", "task", "--priority", "3", "--assignee", "alice")
	bdProxiedCreate(t, bd, p.dir, "Count feature one", "--type", "feature", "--priority", "1")
	closedIssue := bdProxiedCreate(t, bd, p.dir, "Count closed one", "--type", "task", "--priority", "2", "--assignee", "alice")
	// --force: closedIssue is assigned to alice, and the close-authority guard
	// refuses a cross-actor close (test actor != alice) without it.
	if out, err := bdProxiedRun(t, bd, p.dir, "close", closedIssue.ID, "--force"); err != nil {
		t.Fatalf("close %s: %v\n%s", closedIssue.ID, err, out)
	}
	bdProxiedCreate(t, bd, p.dir, "Count labeled", "--type", "task", "--label", "frontend", "--label", "urgent")
	bdProxiedCreate(t, bd, p.dir, "Count labeled two", "--type", "task", "--label", "backend")
	bdProxiedCreate(t, bd, p.dir, "Count notes issue", "--type", "task", "--description", "notes keyword here")

	// countInt runs "count <args>" and parses the plain integer stdout.
	countInt := func(t *testing.T, args ...string) int {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"count"}, args...)...)
		if err != nil {
			t.Fatalf("count %v: %v\n%s", args, err, out)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatalf("parse count output %q: %v", out, err)
		}
		return n
	}

	// countJSON runs "count --json <args>" and parses the object.
	countJSON := func(t *testing.T, args ...string) map[string]interface{} {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"count", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("count --json %v: %v\n%s", args, err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal count JSON %v: %v\n%s", args, err, out)
		}
		return m
	}

	countJSONInt := func(t *testing.T, args ...string) int {
		t.Helper()
		m := countJSON(t, args...)
		v, ok := m["count"].(float64)
		if !ok {
			t.Fatalf("count JSON missing numeric 'count': %v", m)
		}
		return int(v)
	}

	// listCount returns the cardinality of "list --all --limit 0 <filters>",
	// the durable-only tier that "count" (without --include-infra) counts.
	listCount := func(filters ...string) int {
		return len(bdProxiedListJSON(t, bd, p, append([]string{"--all", "--limit", "0"}, filters...)...))
	}

	// group names present in a "count --by-* --json" result.
	groupNames := func(t *testing.T, byFlag string) map[string]int {
		t.Helper()
		m := countJSON(t, byFlag)
		groups, ok := m["groups"].([]interface{})
		if !ok || len(groups) == 0 {
			t.Fatalf("expected groups array for %s: %v", byFlag, m)
		}
		names := make(map[string]int)
		for _, g := range groups {
			gm := g.(map[string]interface{})
			names[gm["group"].(string)] = int(gm["count"].(float64))
		}
		return names
	}

	// ===== Basic count =====

	t.Run("basic_count_no_filters", func(t *testing.T) {
		got := countInt(t)
		if want := listCount(); got != want {
			t.Errorf("count = %d, want %d (list --all cardinality)", got, want)
		}
		if got == 0 {
			t.Error("expected non-zero count")
		}
	})

	// ===== Status filter =====

	t.Run("filter_by_status_open", func(t *testing.T) {
		if got, want := countJSONInt(t, "--status", "open"), listCount("--status", "open"); got != want {
			t.Errorf("count --status open = %d, want %d", got, want)
		}
	})

	t.Run("filter_by_status_closed", func(t *testing.T) {
		got := countJSONInt(t, "--status", "closed")
		if want := listCount("--status", "closed"); got != want {
			t.Errorf("count --status closed = %d, want %d", got, want)
		}
		if got < 1 {
			t.Errorf("expected at least 1 closed issue, got %d", got)
		}
	})

	// ===== Priority filter =====

	t.Run("filter_by_priority", func(t *testing.T) {
		got := countJSONInt(t, "--priority", "1")
		if want := listCount("--priority-min", "1", "--priority-max", "1"); got != want {
			t.Errorf("count --priority 1 = %d, want %d", got, want)
		}
	})

	// ===== Assignee filter =====

	t.Run("filter_by_assignee", func(t *testing.T) {
		got := countJSONInt(t, "--assignee", "alice")
		if want := listCount("--assignee", "alice"); got != want {
			t.Errorf("count --assignee alice = %d, want %d", got, want)
		}
		if got < 3 {
			t.Errorf("expected at least 3 issues assigned to alice, got %d", got)
		}
	})

	// ===== Type filter =====

	t.Run("filter_by_type", func(t *testing.T) {
		got := countJSONInt(t, "--type", "bug")
		if want := listCount("--type", "bug"); got != want {
			t.Errorf("count --type bug = %d, want %d", got, want)
		}
	})

	// ===== Label filter (AND) =====

	t.Run("filter_by_label_and", func(t *testing.T) {
		got := countJSONInt(t, "--label", "frontend", "--label", "urgent")
		if want := listCount("--label", "frontend", "--label", "urgent"); got != want {
			t.Errorf("count --label frontend --label urgent = %d, want %d", got, want)
		}
	})

	// ===== Label filter (OR) =====

	t.Run("filter_by_label_any", func(t *testing.T) {
		got := countJSONInt(t, "--label-any", "frontend", "--label-any", "backend")
		if want := listCount("--label-any", "frontend", "--label-any", "backend"); got != want {
			t.Errorf("count --label-any frontend backend = %d, want %d", got, want)
		}
	})

	// ===== Title filter =====

	t.Run("filter_by_title", func(t *testing.T) {
		got := countJSONInt(t, "--title", "bug")
		if want := listCount("--title", "bug"); got != want {
			t.Errorf("count --title bug = %d, want %d", got, want)
		}
	})

	// ===== Title-contains =====

	t.Run("filter_by_title_contains", func(t *testing.T) {
		got := countJSONInt(t, "--title-contains", "feature")
		if want := listCount("--title-contains", "feature"); got != want {
			t.Errorf("count --title-contains feature = %d, want %d", got, want)
		}
	})

	// ===== Desc-contains =====

	t.Run("filter_by_desc_contains", func(t *testing.T) {
		got := countJSONInt(t, "--desc-contains", "notes keyword")
		if want := listCount("--desc-contains", "notes keyword"); got != want {
			t.Errorf("count --desc-contains 'notes keyword' = %d, want %d", got, want)
		}
	})

	// ===== Date range filters =====

	t.Run("filter_by_created_after", func(t *testing.T) {
		got := countJSONInt(t, "--created-after", "2000-01-01")
		if want := listCount(); got != want {
			t.Errorf("count --created-after 2000-01-01 = %d, want %d (all durable)", got, want)
		}
	})

	t.Run("filter_by_created_before", func(t *testing.T) {
		if got := countJSONInt(t, "--created-before", "2000-01-01"); got != 0 {
			t.Errorf("expected 0 issues created before 2000-01-01, got %d", got)
		}
	})

	t.Run("filter_by_updated_after", func(t *testing.T) {
		got := countJSONInt(t, "--updated-after", "2000-01-01")
		if want := listCount(); got != want {
			t.Errorf("count --updated-after 2000-01-01 = %d, want %d (all durable)", got, want)
		}
	})

	t.Run("filter_by_closed_after", func(t *testing.T) {
		got := countJSONInt(t, "--closed-after", "2000-01-01")
		if want := listCount("--status", "closed"); got != want {
			t.Errorf("count --closed-after 2000-01-01 = %d, want %d (closed issues)", got, want)
		}
		if got < 1 {
			t.Errorf("expected at least 1 closed issue after 2000-01-01, got %d", got)
		}
	})

	// ===== Empty description filter =====

	t.Run("filter_empty_description", func(t *testing.T) {
		got := countJSONInt(t, "--empty-description")
		if want := listCount("--empty-description"); got != want {
			t.Errorf("count --empty-description = %d, want %d", got, want)
		}
		if got < 1 {
			t.Errorf("expected at least 1 issue with empty description, got %d", got)
		}
	})

	// ===== No assignee filter =====

	t.Run("filter_no_assignee", func(t *testing.T) {
		got := countJSONInt(t, "--no-assignee")
		if want := listCount("--no-assignee"); got != want {
			t.Errorf("count --no-assignee = %d, want %d", got, want)
		}
		if got < 1 {
			t.Errorf("expected at least 1 issue with no assignee, got %d", got)
		}
	})

	// ===== No labels filter =====

	t.Run("filter_no_labels", func(t *testing.T) {
		got := countJSONInt(t, "--no-labels")
		if want := listCount("--no-labels"); got != want {
			t.Errorf("count --no-labels = %d, want %d", got, want)
		}
		if got < 1 {
			t.Errorf("expected at least 1 issue with no labels, got %d", got)
		}
	})

	// ===== Priority range filter =====

	t.Run("filter_priority_min_max", func(t *testing.T) {
		got := countJSONInt(t, "--priority-min", "1", "--priority-max", "2")
		if want := listCount("--priority-min", "1", "--priority-max", "2"); got != want {
			t.Errorf("count --priority-min 1 --priority-max 2 = %d, want %d", got, want)
		}
		if got < 3 {
			t.Errorf("expected at least 3 issues with priority 1-2, got %d", got)
		}
	})

	// ===== Group by status =====

	t.Run("group_by_status", func(t *testing.T) {
		m := countJSON(t, "--by-status")
		total := int(m["total"].(float64))
		if want := listCount(); total != want {
			t.Errorf("by-status total = %d, want %d", total, want)
		}
		names := groupNames(t, "--by-status")
		// by-status buckets are mutually exclusive, so they sum to the total.
		sum := 0
		for _, c := range names {
			sum += c
		}
		if sum != total {
			t.Errorf("by-status group sum %d != total %d", sum, total)
		}
		if _, ok := names["open"]; !ok {
			t.Error("expected 'open' group")
		}
		if _, ok := names["closed"]; !ok {
			t.Error("expected 'closed' group")
		}
	})

	// ===== Group by priority =====

	t.Run("group_by_priority", func(t *testing.T) {
		names := groupNames(t, "--by-priority")
		if _, ok := names["P1"]; !ok {
			t.Errorf("expected P1 group, got %v", names)
		}
	})

	// ===== Group by type =====

	t.Run("group_by_type", func(t *testing.T) {
		names := groupNames(t, "--by-type")
		if _, ok := names["bug"]; !ok {
			t.Error("expected 'bug' group")
		}
		if _, ok := names["task"]; !ok {
			t.Error("expected 'task' group")
		}
	})

	// ===== Group by assignee =====

	t.Run("group_by_assignee", func(t *testing.T) {
		names := groupNames(t, "--by-assignee")
		if _, ok := names["alice"]; !ok {
			t.Error("expected 'alice' group")
		}
		if _, ok := names["(unassigned)"]; !ok {
			t.Errorf("expected '(unassigned)' group, got %v", names)
		}
	})

	// ===== Group by label =====

	t.Run("group_by_label", func(t *testing.T) {
		names := groupNames(t, "--by-label")
		if _, ok := names["frontend"]; !ok {
			t.Error("expected 'frontend' label group")
		}
		if _, ok := names["backend"]; !ok {
			t.Error("expected 'backend' label group")
		}
	})

	// ===== JSON plain count =====

	t.Run("json_plain_count", func(t *testing.T) {
		m := countJSON(t)
		if _, ok := m["count"]; !ok {
			t.Errorf("expected 'count' key in JSON output: %v", m)
		}
	})

	// ===== JSON grouped count =====

	t.Run("json_grouped_count", func(t *testing.T) {
		m := countJSON(t, "--by-status")
		if _, ok := m["total"]; !ok {
			t.Error("expected 'total' key in grouped JSON output")
		}
		if _, ok := m["groups"]; !ok {
			t.Error("expected 'groups' key in grouped JSON output")
		}
	})

	// ===== Error: multiple --by-* flags =====

	t.Run("error_multiple_by_flags", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "count", "--by-status", "--by-priority")
		if err == nil {
			t.Fatalf("expected error, got success: %s", out)
		}
		if !strings.Contains(string(out), "only one") {
			t.Errorf("expected 'only one' error, got: %s", out)
		}
	})

	// ===== Combined filters =====

	t.Run("combined_filters", func(t *testing.T) {
		got := countJSONInt(t, "--status", "open", "--type", "bug", "--assignee", "alice")
		if want := listCount("--status", "open", "--type", "bug", "--assignee", "alice"); got != want {
			t.Errorf("count open+bug+alice = %d, want %d", got, want)
		}
		if got < 1 {
			t.Errorf("expected at least 1 open bug assigned to alice, got %d", got)
		}
	})

	// ===== Plain text output =====

	t.Run("plain_text_output", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "count", "--status", "open")
		if err != nil {
			t.Fatalf("count --status open: %v\n%s", err, out)
		}
		s := strings.TrimSpace(string(out))
		if len(s) == 0 {
			t.Error("expected non-empty output")
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				t.Errorf("expected plain integer, got: %q", s)
				break
			}
		}
	})

	t.Run("plain_text_grouped_output", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "count", "--by-status")
		if err != nil {
			t.Fatalf("count --by-status: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "Total:") {
			t.Errorf("expected 'Total:' in grouped text output, got: %s", s)
		}
		if !strings.Contains(s, "open:") {
			t.Errorf("expected 'open:' in grouped text output, got: %s", s)
		}
	})

	// ===== ID filter =====

	t.Run("filter_by_id", func(t *testing.T) {
		issue := bdProxiedCreate(t, bd, p.dir, "ID filter target", "--type", "task")
		if got := countJSONInt(t, "--id", issue.ID); got != 1 {
			t.Errorf("expected exactly 1 issue matching ID, got %d", got)
		}
	})
}

// TestProxiedServerCountIncludeInfra is the proxied-server parity for GH#4387:
// `bd count --include-infra <filters>` must return exactly the cardinality of
// `bd list --include-infra <filters> --all`, including the wisps tier (no_history
// + ephemeral beads). Without the flag, count keeps durable-only semantics.
func TestProxiedServerCountIncludeInfra(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "cci")

	// Durable issues tier.
	bdProxiedCreate(t, bd, p.dir, "Infra durable task one", "--type", "task")
	bdProxiedCreate(t, bd, p.dir, "Infra durable task two", "--type", "task")
	bdProxiedCreate(t, bd, p.dir, "Infra durable bug", "--type", "bug")
	closedIssue := bdProxiedCreate(t, bd, p.dir, "Infra durable task closed", "--type", "task")
	if out, err := bdProxiedRun(t, bd, p.dir, "close", closedIssue.ID, "--force"); err != nil {
		t.Fatalf("close %s: %v\n%s", closedIssue.ID, err, out)
	}
	// Wisps tier: no_history beads are durable work that list --include-infra
	// returns; ephemeral beads are GC-eligible wisps.
	bdProxiedCreate(t, bd, p.dir, "Infra nohistory task one", "--type", "task", "--no-history")
	bdProxiedCreate(t, bd, p.dir, "Infra nohistory task two", "--type", "task", "--no-history")
	bdProxiedCreate(t, bd, p.dir, "Infra ephemeral task", "--type", "task", "--ephemeral")

	countOf := func(t *testing.T, args ...string) int {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"count", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("count --json %v: %v\n%s", args, err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal count %v: %v\n%s", args, err, out)
		}
		return int(m["count"].(float64))
	}
	listCardinality := func(filters ...string) int {
		full := append([]string{"--include-infra", "--all", "--limit", "0"}, filters...)
		return len(bdProxiedListJSON(t, bd, p, full...))
	}

	t.Run("default_stays_durable_only", func(t *testing.T) {
		if got := countOf(t, "--type", "task"); got != 3 {
			t.Errorf("count --type task = %d, want 3 (durable tasks only)", got)
		}
	})

	t.Run("include_infra_counts_wisps_tier", func(t *testing.T) {
		// 3 durable tasks + 2 no_history tasks + 1 ephemeral task.
		if got := countOf(t, "--include-infra", "--type", "task"); got != 6 {
			t.Errorf("count --include-infra --type task = %d, want 6", got)
		}
	})

	t.Run("include_infra_matches_list_cardinality", func(t *testing.T) {
		for _, filters := range [][]string{
			nil,
			{"--type", "task"},
			{"--type", "bug"},
			{"--status", "open"},
			{"--status", "closed"},
		} {
			want := listCardinality(filters...)
			got := countOf(t, append([]string{"--include-infra"}, filters...)...)
			if got != want {
				t.Errorf("count --include-infra %v = %d, but list --include-infra --all %v returned %d rows", filters, got, filters, want)
			}
		}
	})

	t.Run("include_infra_grouped_by_type", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "count", "--json", "--include-infra", "--by-type")
		if err != nil {
			t.Fatalf("count --include-infra --by-type: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		total := int(m["total"].(float64))
		if want := listCardinality(); total != want {
			t.Errorf("count --include-infra --by-type total = %d, want %d", total, want)
		}
		byType := make(map[string]int)
		for _, g := range m["groups"].([]interface{}) {
			gm := g.(map[string]interface{})
			byType[gm["group"].(string)] = int(gm["count"].(float64))
		}
		if byType["task"] != 6 {
			t.Errorf("grouped task count = %d, want 6", byType["task"])
		}
		if byType["bug"] != 1 {
			t.Errorf("grouped bug count = %d, want 1", byType["bug"])
		}
	})

	t.Run("grouped_without_flag_stays_durable_only", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "count", "--json", "--by-type")
		if err != nil {
			t.Fatalf("count --by-type: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, out)
		}
		for _, g := range m["groups"].([]interface{}) {
			gm := g.(map[string]interface{})
			if gm["group"] == "task" {
				if got := int(gm["count"].(float64)); got != 3 {
					t.Errorf("count --by-type task = %d, want 3 (durable only)", got)
				}
			}
		}
	})
}
