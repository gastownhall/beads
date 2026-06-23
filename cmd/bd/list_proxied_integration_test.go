//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerList(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "lst")
	seed := seedProxiedListData(t, bd, p)

	// --- A. Basic filtering ---

	t.Run("status_open", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--status", "open")
		for _, issue := range issues {
			if issue.Status != types.StatusOpen {
				t.Errorf("expected status open, got %s for %s", issue.Status, issue.ID)
			}
		}
	})

	t.Run("all", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--all")
		if !containsID(issues, seed.openBug) {
			t.Error("--all should include open bug")
		}
		if !containsID(issues, seed.decision) {
			t.Error("--all should include decision")
		}
	})

	t.Run("assignee", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--assignee", "alice", "--all")
		for _, issue := range issues {
			if issue.Assignee != "alice" {
				t.Errorf("expected assignee alice, got %q for %s", issue.Assignee, issue.ID)
			}
		}
		if !containsID(issues, seed.openBug) {
			t.Error("alice's open bug should appear")
		}
	})

	t.Run("type_bug", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--type", "bug")
		for _, issue := range issues {
			if issue.IssueType != types.TypeBug {
				t.Errorf("expected type bug, got %s for %s", issue.IssueType, issue.ID)
			}
		}
	})

	t.Run("priority", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--priority", "0")
		for _, issue := range issues {
			if issue.Priority != 0 {
				t.Errorf("expected priority 0, got %d for %s", issue.Priority, issue.ID)
			}
		}
		if !containsID(issues, seed.openBug) {
			t.Error("P0 open bug should appear")
		}
		if !containsID(issues, seed.readyTask) {
			t.Error("P0 ready task should appear")
		}
	})

	t.Run("limit", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--limit", "2")
		if len(issues) != 2 {
			t.Errorf("expected 2 issues with --limit 2, got %d", len(issues))
		}
	})

	t.Run("id_filter", func(t *testing.T) {
		idList := seed.openBug + "," + seed.readyTask
		issues := bdProxiedListJSON(t, bd, p, "--id", idList)
		if len(issues) != 2 {
			t.Errorf("expected 2 issues with --id filter, got %d", len(issues))
		}
	})

	// --- B. Label filtering ---

	t.Run("label_and", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--label", "backend", "--label", "urgent")
		// Only openBug has both backend AND urgent.
		if len(issues) != 1 || issues[0].ID != seed.openBug {
			t.Errorf("expected only open bug with backend+urgent labels, got %v", listIssueIDs(issues))
		}
	})

	t.Run("label_any", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--label-any", "backend,frontend")
		if len(issues) < 2 {
			t.Errorf("expected multiple issues with --label-any backend,frontend, got %d", len(issues))
		}
	})

	t.Run("no_labels", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--no-labels")
		for _, issue := range issues {
			if len(issue.Labels) > 0 {
				t.Errorf("expected no labels for %s, got %v", issue.ID, issue.Labels)
			}
		}
	})

	t.Run("label_pattern", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--label-pattern", "back*")
		if !containsID(issues, seed.openBug) {
			t.Error("openBug with label 'backend' should match pattern 'back*'")
		}
	})

	t.Run("exclude_label", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--exclude-label", "urgent")
		if containsID(issues, seed.openBug) {
			t.Error("openBug with 'urgent' label should be excluded by --exclude-label urgent")
		}
		if containsID(issues, seed.overdueTask) {
			t.Error("overdueTask with 'urgent' label should be excluded by --exclude-label urgent")
		}
	})

	t.Run("skip_labels_json_suppresses_labeled_issue", func(t *testing.T) {
		stdout, stderr := bdProxiedListCapture(t, bd, p,
			"--json", "--skip-labels", "--id", seed.openBug)
		var got bdListSkipLabelsJSON
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("parse skip-labels JSON: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !got.Meta.SkipLabels {
			t.Fatal("expected meta.skip_labels=true")
		}
		if got.Meta.Count != 1 || len(got.Issues) != 1 {
			t.Fatalf("expected one issue and matching count, got count=%d issues=%d", got.Meta.Count, len(got.Issues))
		}
		if got.Issues[0].ID != seed.openBug {
			t.Fatalf("expected issue %s, got %s", seed.openBug, got.Issues[0].ID)
		}
		if got.Issues[0].Labels == nil {
			t.Fatal("expected labels field to be present as an empty array, got nil")
		}
		if len(got.Issues[0].Labels) != 0 {
			t.Fatalf("--skip-labels JSON leaked labels: %v", got.Issues[0].Labels)
		}
	})

	// --- C. Status/Ready ---

	t.Run("ready", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--ready")
		for _, issue := range issues {
			if issue.Status != types.StatusOpen {
				t.Errorf("--ready should only return open issues, got status %s for %s", issue.Status, issue.ID)
			}
		}
		if len(issues) == 0 {
			t.Error("--ready should return open issues")
		}
	})

	t.Run("ready_exclude_type", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--ready", "--exclude-type", "epic", "--limit", "0")
		if containsID(issues, seed.epic) {
			t.Errorf("--ready --exclude-type epic should exclude epic %s, got %v",
				seed.epic, listIssueIDs(issues))
		}
		if !containsID(issues, seed.readyTask) {
			t.Errorf("--ready --exclude-type epic should still include ready task %s, got %v",
				seed.readyTask, listIssueIDs(issues))
		}
	})

	t.Run("overdue", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--overdue")
		if !containsID(issues, seed.overdueTask) {
			t.Error("overdue task should appear with --overdue")
		}
		for _, issue := range issues {
			if issue.Status == types.StatusClosed {
				t.Errorf("closed issue %s should not appear in --overdue", issue.ID)
			}
		}
	})

	// --- D. Parent / hierarchy ---

	t.Run("parent_filter", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--parent", seed.epic)
		ids := listIssueIDs(issues)
		if !containsID(issues, seed.childTaskA) {
			t.Errorf("child A should appear with --parent, got %v", ids)
		}
		if !containsID(issues, seed.childTaskB) {
			t.Errorf("child B should appear with --parent, got %v", ids)
		}
	})

	t.Run("no_parent", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--no-parent")
		if containsID(issues, seed.childTaskA) {
			t.Error("child A should not appear with --no-parent")
		}
		if containsID(issues, seed.childTaskB) {
			t.Error("child B should not appear with --no-parent")
		}
	})

	t.Run("tree_parent", func(t *testing.T) {
		out := bdProxiedList(t, bd, p, "--tree", "--parent", seed.epic, "--no-pager")
		if !strings.Contains(out, seed.epic) {
			t.Errorf("tree output should contain parent ID %s:\n%s", seed.epic, out)
		}
	})

	t.Run("ready_parent_tree_excludes_blocked_descendants", func(t *testing.T) {
		// TODO: re-enable once `bd dep add` is ported to proxied mode.
		// Currently bd dep add fails with "storage is nil" under proxied
		// since the dep subcommand has no usesProxiedServer() dispatch.
		t.Skip("requires bd dep add proxied support")
	})

	// Regression for gastownhall/beads#3936: relates-to between two epics
	// must not nest them, and bidirectional relates-to must not drop them.
	t.Run("tree_relates_to_does_not_nest_or_drop_epics", func(t *testing.T) {
		t.Skip("requires bd dep add proxied support")
	})

	t.Run("ready_parent_filter_includes_grandchildren", func(t *testing.T) {
		parent := bdProxiedCreate(t, bd, p.dir, "Ready parent recursive", "--type", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Ready child recursive",
			"--type", "task", "--parent", parent.ID)
		grandchild := bdProxiedCreate(t, bd, p.dir, "Ready grandchild recursive",
			"--type", "task", "--parent", child.ID)

		issues := bdProxiedListJSON(t, bd, p, "--ready", "--parent", parent.ID, "--limit", "0")
		if !containsID(issues, child.ID) {
			t.Errorf("ready parent filter should include direct child %s, got %v",
				child.ID, listIssueIDs(issues))
		}
		if !containsID(issues, grandchild.ID) {
			t.Errorf("ready parent filter should include recursive grandchild %s, got %v",
				grandchild.ID, listIssueIDs(issues))
		}
	})

	t.Run("flat", func(t *testing.T) {
		out := bdProxiedList(t, bd, p, "--flat")
		if strings.Contains(out, "└") || strings.Contains(out, "├") {
			t.Errorf("--flat should not contain tree characters:\n%s", out)
		}
	})

	// --- E. Content search ---

	t.Run("title_search", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--title", "Open bug")
		if !containsID(issues, seed.openBug) {
			t.Error("title search should find 'Open bug'")
		}
	})

	t.Run("title_contains", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--title-contains", "task", "--all")
		if len(issues) == 0 {
			t.Error("title-contains 'task' should match some issues")
		}
	})

	t.Run("empty_description", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--empty-description")
		if !containsID(issues, seed.noDescBug) {
			t.Error("no-desc bug should appear with --empty-description")
		}
	})

	t.Run("no_assignee", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--no-assignee")
		for _, issue := range issues {
			if issue.Assignee != "" {
				t.Errorf("expected no assignee for %s, got %q", issue.ID, issue.Assignee)
			}
		}
	})

	// --- F. Date range filtering ---

	t.Run("created_after_yesterday", func(t *testing.T) {
		yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
		issues := bdProxiedListJSON(t, bd, p, "--created-after", yesterday)
		if len(issues) == 0 {
			t.Error("all issues created today should match --created-after yesterday")
		}
	})

	t.Run("created_before_yesterday", func(t *testing.T) {
		yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
		issues := bdProxiedListJSON(t, bd, p, "--created-before", yesterday)
		if len(issues) != 0 {
			t.Errorf("no issues should match --created-before yesterday, got %d", len(issues))
		}
	})

	t.Run("defer_after_now", func(t *testing.T) {
		// chore was seeded with --defer +7d; it must appear in this window.
		issues := bdProxiedListJSON(t, bd, p, "--defer-after", "+1h", "--all")
		if !containsID(issues, seed.chore) {
			t.Errorf("deferred chore %s should appear with --defer-after +1h, got %v",
				seed.chore, listIssueIDs(issues))
		}
	})

	t.Run("defer_before_far_future", func(t *testing.T) {
		// 30 days from now covers the chore's +7d defer.
		issues := bdProxiedListJSON(t, bd, p, "--defer-before", "+30d", "--all")
		if !containsID(issues, seed.chore) {
			t.Errorf("deferred chore %s should appear with --defer-before +30d, got %v",
				seed.chore, listIssueIDs(issues))
		}
	})

	t.Run("due_after_excludes_overdue", func(t *testing.T) {
		// overdueTask has due 48h in the past; --due-after now must exclude it.
		issues := bdProxiedListJSON(t, bd, p, "--due-after", "+1h", "--all")
		if containsID(issues, seed.overdueTask) {
			t.Errorf("overdueTask %s should not appear with --due-after +1h, got %v",
				seed.overdueTask, listIssueIDs(issues))
		}
	})

	t.Run("due_before_includes_overdue", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--due-before", "+1h", "--all")
		if !containsID(issues, seed.overdueTask) {
			t.Errorf("overdueTask %s should appear with --due-before +1h, got %v",
				seed.overdueTask, listIssueIDs(issues))
		}
	})

	// --- G. Priority range ---

	t.Run("priority_range", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--priority-min", "0", "--priority-max", "1")
		for _, issue := range issues {
			if issue.Priority > 1 {
				t.Errorf("expected priority 0-1, got %d for %s", issue.Priority, issue.ID)
			}
		}
	})

	t.Run("priority_min", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--priority-min", "3", "--all")
		for _, issue := range issues {
			if issue.Priority < 3 {
				t.Errorf("expected priority >= 3, got %d for %s", issue.Priority, issue.ID)
			}
		}
	})

	// --- H. Sort ---

	t.Run("sort_priority", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--sort", "priority")
		if len(issues) >= 2 && issues[0].Priority > issues[1].Priority {
			t.Errorf("--sort priority should put lower priority first, got P%d before P%d",
				issues[0].Priority, issues[1].Priority)
		}
	})

	t.Run("sort_title", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--sort", "title")
		if len(issues) >= 2 && strings.ToLower(issues[0].Title) > strings.ToLower(issues[1].Title) {
			t.Errorf("--sort title should be alphabetical, got %q before %q",
				issues[0].Title, issues[1].Title)
		}
	})

	t.Run("sort_created", func(t *testing.T) {
		// --sort created: newest first.
		issues := bdProxiedListJSON(t, bd, p, "--sort", "created", "--all")
		if len(issues) >= 2 && issues[0].CreatedAt.Before(issues[1].CreatedAt) {
			t.Errorf("--sort created should put newest first, got %v before %v",
				issues[0].CreatedAt, issues[1].CreatedAt)
		}
	})

	t.Run("sort_reverse", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--sort", "priority", "--reverse")
		if len(issues) >= 2 && issues[0].Priority < issues[1].Priority {
			t.Errorf("--sort priority --reverse should put higher priority first, got P%d before P%d",
				issues[0].Priority, issues[1].Priority)
		}
	})

	t.Run("sort_id_falls_back_to_go_side", func(t *testing.T) {
		// --sort id forces sqlLimit=0 (natural-numeric Go-side sort). Just
		// verify it produces a non-empty, ordered result without erroring;
		// the IDs themselves are gibberish-ordered so we don't assert order.
		issues := bdProxiedListJSON(t, bd, p, "--sort", "id", "--all")
		if len(issues) == 0 {
			t.Error("--sort id should still return seeded issues")
		}
	})

	// --- I. Output formats ---

	t.Run("json_output", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--all")
		if len(issues) == 0 {
			t.Fatal("expected non-empty JSON output")
		}
		for _, issue := range issues {
			if issue.ID == "" {
				t.Error("issue ID should not be empty in JSON output")
			}
			if issue.Title == "" {
				t.Error("issue title should not be empty in JSON output")
			}
		}
		for _, issue := range issues {
			if issue.ID == seed.childTaskA {
				if issue.Parent == nil || *issue.Parent != seed.epic {
					t.Errorf("child A should have parent=%s, got %v", seed.epic, issue.Parent)
				}
			}
		}
	})

	t.Run("long_format", func(t *testing.T) {
		out := bdProxiedList(t, bd, p, "--long", "--flat", "--no-pager")
		if !strings.Contains(out, "Found") {
			t.Errorf("--long format should contain 'Found N issues':\n%s", out)
		}
		if !strings.Contains(out, "Description:") || !strings.Contains(out, "This is a bug") {
			t.Errorf("--long format should include issue descriptions, got: %s", out)
		}
	})

	t.Run("pretty_format", func(t *testing.T) {
		out := bdProxiedList(t, bd, p, "--pretty", "--no-pager")
		if len(out) == 0 {
			t.Error("--pretty should produce output")
		}
	})

	t.Run("format_digraph", func(t *testing.T) {
		out := bdProxiedList(t, bd, p, "--format", "digraph", "--all", "--no-pager")
		if out == "" {
			t.Error("digraph output should not be empty")
		}
	})

	t.Run("format_dot", func(t *testing.T) {
		out := bdProxiedList(t, bd, p, "--format", "dot", "--flat", "--all", "--no-pager")
		if !strings.Contains(out, "digraph") {
			t.Errorf("dot output should contain 'digraph' header, got: %s", out)
		}
	})

	t.Run("compact_default", func(t *testing.T) {
		out := bdProxiedList(t, bd, p, "--flat", "--no-pager")
		if !strings.Contains(out, seed.openBug) {
			t.Error("default compact output should contain issue IDs")
		}
	})

	// --- J. Metadata filtering ---

	t.Run("metadata_field", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--metadata-field", "env=prod")
		if !containsID(issues, seed.metadataIssue) {
			t.Error("metadata issue with env=prod should appear")
		}
		if containsID(issues, seed.openBug) {
			t.Error("open bug should not match env=prod metadata filter")
		}
	})

	t.Run("has_metadata_key", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--has-metadata-key", "env")
		if !containsID(issues, seed.metadataIssue) {
			t.Error("metadata issue should appear with --has-metadata-key env")
		}
	})

	// --- K. Edge cases ---

	t.Run("empty_database", func(t *testing.T) {
		// Fresh proxied project with no seeded issues.
		empty := bdProxiedInit(t, bd, "lst-empty")
		issues := bdProxiedListJSON(t, bd, empty)
		if len(issues) != 0 {
			t.Errorf("expected 0 issues in empty proxied database, got %d", len(issues))
		}
	})

	t.Run("limit_zero_unlimited", func(t *testing.T) {
		issues := bdProxiedListJSON(t, bd, p, "--limit", "0")
		if len(issues) == 0 {
			t.Error("--limit 0 should return all issues")
		}
	})

	t.Run("invalid_sort_field", func(t *testing.T) {
		out := bdProxiedListFail(t, bd, p, "--sort", "nonexistent")
		if !strings.Contains(out, "invalid sort field") {
			t.Errorf("expected 'invalid sort field' error, got: %s", out)
		}
	})

	t.Run("invalid_status", func(t *testing.T) {
		out := bdProxiedListFail(t, bd, p, "--status", "nonexistent")
		if !strings.Contains(out, "invalid status") {
			t.Errorf("expected 'invalid status' error, got: %s", out)
		}
	})

	// --- L. Pagination (proxied-only surface) ---

	t.Run("limit_N_matches_first_N", func(t *testing.T) {
		full := bdProxiedListJSON(t, bd, p, "--all", "--limit", "0")
		if len(full) < 5 {
			t.Fatalf("seeded fixture should have >= 5 issues, got %d", len(full))
		}
		page := bdProxiedListJSON(t, bd, p, "--all", "--limit", "5")
		if len(page) != 5 {
			t.Fatalf("--limit 5 should return 5 rows, got %d", len(page))
		}
		for i := 0; i < 5; i++ {
			if page[i].ID != full[i].ID {
				t.Errorf("position %d: --limit 5 returned %s; unlimited returned %s",
					i, page[i].ID, full[i].ID)
			}
		}
	})

	t.Run("offset_plus_limit_returns_slice", func(t *testing.T) {
		full := bdProxiedListJSON(t, bd, p, "--all", "--limit", "0")
		if len(full) < 7 {
			t.Fatalf("seeded fixture should have >= 7 issues, got %d", len(full))
		}
		page := bdProxiedListJSON(t, bd, p, "--all", "--offset", "3", "--limit", "4")
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
		full := bdProxiedListJSON(t, bd, p, "--all", "--limit", "0")
		const pageSize = 4
		var walked []string
		seen := make(map[string]bool)
		for offset := 0; ; offset += pageSize {
			page := bdProxiedListJSON(t, bd, p, "--all",
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

	t.Run("truncation_hint_on_stderr_when_more_results", func(t *testing.T) {
		stdout, stderr := bdProxiedListCapture(t, bd, p, "--all", "--limit", "2")
		if !strings.Contains(stderr, "more results matched") {
			t.Errorf("expected truncation hint on stderr, got:\nstderr: %q\nstdout: %q", stderr, stdout)
		}
		if strings.Contains(stdout, "more results matched") {
			t.Errorf("truncation hint leaked into stdout:\n%s", stdout)
		}
	})

	t.Run("truncation_hint_suppressed_when_no_overflow", func(t *testing.T) {
		full := bdProxiedListJSON(t, bd, p, "--all", "--limit", "0")
		_, stderr := bdProxiedListCapture(t, bd, p, "--all",
			"--limit", fmt.Sprintf("%d", len(full)+10))
		if strings.Contains(stderr, "more results matched") {
			t.Errorf("no hint expected when --limit > matches; got:\n%s", stderr)
		}
	})

	// --- M. Pagination across the issues+wisps UNION ALL ---

	t.Run("ready_returns_both_perm_and_wisp", func(t *testing.T) {
		// Create three ephemeral wisps as ready candidates. The proxied
		// union path forces IncludeEphemeral=true on the wisp side, so
		// these must surface in --ready alongside permanent issues.
		var wispIDs []string
		for i := 0; i < 3; i++ {
			w := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Wisp ready %d", i), "--ephemeral")
			wispIDs = append(wispIDs, w.ID)
		}

		issues := bdProxiedListJSON(t, bd, p, "--ready", "--limit", "0")
		ids := listIssueIDs(issues)
		for _, wid := range wispIDs {
			if !containsID(issues, wid) {
				t.Errorf("ephemeral wisp %s should appear in --ready (UNION path), got %v", wid, ids)
			}
		}
		if !containsID(issues, seed.readyTask) {
			t.Errorf("permanent readyTask %s should still appear in --ready, got %v", seed.readyTask, ids)
		}
	})

	t.Run("ready_page_walk_across_tables", func(t *testing.T) {
		full := bdProxiedListJSON(t, bd, p, "--ready", "--limit", "0")
		if len(full) < 4 {
			t.Skipf("need >= 4 ready items to walk pages; have %d", len(full))
		}
		const pageSize = 2
		var walked []string
		seen := make(map[string]bool)
		for offset := 0; ; offset += pageSize {
			page := bdProxiedListJSON(t, bd, p, "--ready",
				"--limit", fmt.Sprintf("%d", pageSize),
				"--offset", fmt.Sprintf("%d", offset))
			if len(page) == 0 {
				break
			}
			for _, iwc := range page {
				if seen[iwc.ID] {
					t.Errorf("ready page at offset %d returned duplicate %s", offset, iwc.ID)
				}
				seen[iwc.ID] = true
				walked = append(walked, iwc.ID)
			}
			if len(page) < pageSize {
				break
			}
		}
		if len(walked) != len(full) {
			t.Fatalf("ready page walk got %d issues, unlimited got %d", len(walked), len(full))
		}
		for i, id := range walked {
			if id != full[i].ID {
				t.Errorf("ready position %d: page walk had %s; unlimited had %s", i, id, full[i].ID)
			}
		}
	})

	t.Run("ready_offset_lands_mid_merge", func(t *testing.T) {
		full := bdProxiedListJSON(t, bd, p, "--ready", "--limit", "0")
		if len(full) < 6 {
			t.Skipf("need >= 6 ready items; have %d", len(full))
		}
		// Pick an offset that's likely mid-merge across the two source
		// tables. The exact mix depends on created_at, but a window in
		// the middle exercises both halves of the UNION ALL.
		off := len(full) / 2
		page := bdProxiedListJSON(t, bd, p, "--ready",
			"--limit", "3", "--offset", fmt.Sprintf("%d", off))
		if len(page) == 0 {
			t.Fatalf("--offset %d --limit 3 returned empty; full has %d", off, len(full))
		}
		for i, iwc := range page {
			expected := full[off+i].ID
			if iwc.ID != expected {
				t.Errorf("ready slice position %d (global %d): got %s; expected %s",
					i, off+i, iwc.ID, expected)
			}
		}
	})

	// --- N. Reject incompatible flag combinations ---
	//
	// Note: --repo is registered only on createCmd, not listCmd; cobra
	// rejects it as unknown before runListProxiedServer's defensive
	// guard runs. No proxied test for that combination.

	t.Run("reject_format_with_watch", func(t *testing.T) {
		out := bdProxiedListFail(t, bd, p, "--watch", "--format", "dot")
		if !strings.Contains(out, "--format under --proxied-server --watch is not supported") {
			t.Errorf("expected --format+--watch rejection message, got: %s", out)
		}
	})

	t.Run("reject_offset_with_parent_pretty", func(t *testing.T) {
		out := bdProxiedListFail(t, bd, p, "--parent", seed.epic, "--pretty", "--offset", "1")
		if !strings.Contains(out, "--offset is not supported with hierarchical --parent + pretty/tree") {
			t.Errorf("expected --parent+--pretty+--offset rejection, got: %s", out)
		}
	})

	t.Run("reject_offset_with_sort_id", func(t *testing.T) {
		out := bdProxiedListFail(t, bd, p, "--sort", "id", "--offset", "1")
		if !strings.Contains(out, "--offset is not supported with --sort id") {
			t.Errorf("expected --sort id+--offset rejection, got: %s", out)
		}
	})

	// --- O. Lightweight race: 4 workers × 5 (create + list) iterations ---

	t.Run("concurrent_create_and_list", func(t *testing.T) {
		// Isolated project: race test creates 20 issues; keep them out of
		// the shared fixture so subsequent runs aren't polluted.
		race := bdProxiedInit(t, bd, "lst-race")

		const (
			workers = 4
			iters   = 5
		)
		var (
			wg     sync.WaitGroup
			errsMu sync.Mutex
			errs   []string
			allIDs sync.Map
		)

		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func(worker int) {
				defer wg.Done()
				for i := 0; i < iters; i++ {
					title := fmt.Sprintf("w%d-i%d", worker, i)
					iss := bdProxiedCreate(t, bd, race.dir, title)
					allIDs.Store(iss.ID, true)

					// List immediately to verify our own create is visible.
					list := bdProxiedListJSON(t, bd, race, "--all", "--limit", "0")
					if !containsID(list, iss.ID) {
						errsMu.Lock()
						errs = append(errs, fmt.Sprintf("worker %d iter %d: %s missing from --all", worker, i, iss.ID))
						errsMu.Unlock()
						return
					}
				}
			}(w)
		}
		wg.Wait()

		if len(errs) > 0 {
			t.Fatalf("concurrent failures:\n%s", strings.Join(errs, "\n"))
		}

		// Final assertion: --all sees every ID this test created.
		final := bdProxiedListJSON(t, bd, race, "--all", "--limit", "0")
		var missing []string
		allIDs.Range(func(k, _ any) bool {
			id := k.(string)
			if !containsID(final, id) {
				missing = append(missing, id)
			}
			return true
		})
		if len(missing) > 0 {
			t.Fatalf("post-race --all missing %d IDs: %v", len(missing), missing)
		}
		if len(final) < workers*iters {
			t.Errorf("expected at least %d issues after race, got %d", workers*iters, len(final))
		}
	})
}

// seedProxiedListData populates a proxied project with the same 12-issue
// fixture used by the embedded list tests (testSeedData in
// list_embedded_test.go). Issues are created via `bd create` subprocess
// so the seed itself exercises the proxied create path.
//
// Returns testSeedData so embedded and proxied list tests can share
// fixture-relative assertions verbatim — any drift in the seed would
// touch both call sites.
func seedProxiedListData(t *testing.T, bd string, p proxiedProject) testSeedData {
	t.Helper()
	var s testSeedData

	// 1. Open bug, P0, alice, labels: backend,urgent, with description
	issue := bdProxiedCreate(t, bd, p.dir, "Open bug", "--type", "bug", "--priority", "0",
		"--assignee", "alice", "--description", "This is a bug",
		"--label", "backend", "--label", "urgent")
	s.openBug = issue.ID

	// 2. Feature, P1, bob, labels: frontend
	issue = bdProxiedCreate(t, bd, p.dir, "Feature request", "--type", "feature", "--priority", "1",
		"--assignee", "bob", "--label", "frontend")
	s.feature = issue.ID

	// 3. Task, P2, alice, labels: backend
	issue = bdProxiedCreate(t, bd, p.dir, "Backend task", "--type", "task", "--priority", "2",
		"--assignee", "alice", "--label", "backend")
	s.task = issue.ID

	// 4. Chore, P3, no assignee, defer_until set
	issue = bdProxiedCreate(t, bd, p.dir, "Deferred chore", "--type", "chore", "--priority", "3",
		"--defer", "+7d")
	s.chore = issue.ID

	// 5. Epic, P1, labels: planning
	issue = bdProxiedCreate(t, bd, p.dir, "Epic with deps", "--type", "epic", "--priority", "1",
		"--label", "planning")
	s.epic = issue.ID

	// 6. Decision, P4, labels: pinned-ref
	issue = bdProxiedCreate(t, bd, p.dir, "Architecture decision", "--type", "decision", "--priority", "4",
		"--label", "pinned-ref")
	s.decision = issue.ID

	// 7. Child task A, P2, bob, labels: backend, parent=epic
	issue = bdProxiedCreate(t, bd, p.dir, "Child task A", "--type", "task", "--priority", "2",
		"--assignee", "bob", "--label", "backend", "--parent", s.epic)
	s.childTaskA = issue.ID

	// 8. Child task B, P3, labels: frontend, parent=epic
	issue = bdProxiedCreate(t, bd, p.dir, "Child task B", "--type", "task", "--priority", "3",
		"--label", "frontend", "--parent", s.epic)
	s.childTaskB = issue.ID

	// 9. No-desc bug, P1
	issue = bdProxiedCreate(t, bd, p.dir, "No desc bug", "--type", "bug", "--priority", "1")
	s.noDescBug = issue.ID

	// 10. Overdue task, P1, alice, due in past, labels: urgent
	pastDue := time.Now().Add(-48 * time.Hour).Format("2006-01-02")
	issue = bdProxiedCreate(t, bd, p.dir, "Overdue task", "--type", "task", "--priority", "1",
		"--assignee", "alice", "--label", "urgent", "--due", pastDue)
	s.overdueTask = issue.ID

	// 11. Metadata issue, P1, metadata: env=prod
	issue = bdProxiedCreate(t, bd, p.dir, "Metadata issue", "--type", "feature", "--priority", "1",
		"--metadata", `{"env":"prod"}`)
	s.metadataIssue = issue.ID

	// 12. Ready task, P0, labels: backend, no blockers
	issue = bdProxiedCreate(t, bd, p.dir, "Ready task", "--type", "task", "--priority", "0",
		"--label", "backend")
	s.readyTask = issue.ID

	t.Logf("Seeded %d proxied test issues", 12)
	t.Logf("  openBug=%s feature=%s task=%s chore=%s", s.openBug, s.feature, s.task, s.chore)
	t.Logf("  epic=%s decision=%s childA=%s childB=%s", s.epic, s.decision, s.childTaskA, s.childTaskB)
	t.Logf("  noDescBug=%s overdue=%s metadata=%s ready=%s",
		s.noDescBug, s.overdueTask, s.metadataIssue, s.readyTask)

	all := bdProxiedListJSON(t, bd, p, "--all", "--limit", "0")
	if len(all) < 12 {
		t.Fatalf("expected at least 12 seeded issues, got %d", len(all))
	}

	return s
}
