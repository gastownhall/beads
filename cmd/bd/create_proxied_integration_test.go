//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerCreate(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("basic", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "bc")
		issue := bdProxiedCreate(t, bd, p.dir, "Basic issue")
		if issue.ID == "" {
			t.Fatal("expected issue ID")
		}
		if issue.Title != "Basic issue" {
			t.Errorf("title: got %q, want %q", issue.Title, "Basic issue")
		}
		if issue.Status != types.StatusOpen {
			t.Errorf("status: got %q, want %q", issue.Status, types.StatusOpen)
		}
		if issue.Priority != 2 {
			t.Errorf("priority: got %d, want 2 (default)", issue.Priority)
		}
		if issue.IssueType != types.TypeTask {
			t.Errorf("type: got %q, want %q", issue.IssueType, types.TypeTask)
		}
		if !strings.HasPrefix(issue.ID, "bc-") {
			t.Errorf("ID should have prefix bc-, got %q", issue.ID)
		}
	})

	t.Run("title_flag", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "tf")
		issue := bdProxiedCreate(t, bd, p.dir, "--title", "Title via flag")
		if issue.Title != "Title via flag" {
			t.Errorf("title: got %q, want %q", issue.Title, "Title via flag")
		}
	})

	t.Run("silent", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sl")
		id := bdProxiedCreateSilent(t, bd, p.dir, "Silent issue")
		if id == "" {
			t.Fatal("expected issue ID from --silent")
		}
		if !strings.HasPrefix(id, "sl-") {
			t.Errorf("ID should have prefix sl-, got %q", id)
		}
	})

	t.Run("priority", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pr")
		for _, tc := range []struct {
			flag string
			want int
		}{
			{"0", 0}, {"1", 1}, {"P3", 3}, {"4", 4},
		} {
			t.Run("P"+tc.flag, func(t *testing.T) {
				issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Priority %s", tc.flag), "-p", tc.flag)
				if issue.Priority != tc.want {
					t.Errorf("priority: got %d, want %d", issue.Priority, tc.want)
				}
			})
		}
	})

	t.Run("issue_types", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "it")
		for _, issueType := range []string{"bug", "feature", "task", "epic", "chore", "decision"} {
			t.Run(issueType, func(t *testing.T) {
				issue := bdProxiedCreate(t, bd, p.dir, fmt.Sprintf("Type %s", issueType), "-t", issueType)
				normalized := types.IssueType(issueType).Normalize()
				if issue.IssueType != normalized {
					t.Errorf("type: got %q, want %q", issue.IssueType, normalized)
				}
			})
		}
	})

	t.Run("description", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ds")
		issue := bdProxiedCreate(t, bd, p.dir, "Desc issue", "-d", "This is the description")
		if issue.Description != "This is the description" {
			t.Errorf("description: got %q, want %q", issue.Description, "This is the description")
		}
	})

	t.Run("design_and_acceptance", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "da")
		issue := bdProxiedCreate(t, bd, p.dir, "Design issue",
			"--design", "Use MVC pattern",
			"--acceptance", "All tests pass")
		if issue.Design != "Use MVC pattern" {
			t.Errorf("design: got %q, want %q", issue.Design, "Use MVC pattern")
		}
		if issue.AcceptanceCriteria != "All tests pass" {
			t.Errorf("acceptance: got %q, want %q", issue.AcceptanceCriteria, "All tests pass")
		}
	})

	t.Run("assignee", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "as")
		issue := bdProxiedCreate(t, bd, p.dir, "Assigned issue", "-a", "alice")
		if issue.Assignee != "alice" {
			t.Errorf("assignee: got %q, want %q", issue.Assignee, "alice")
		}
	})

	t.Run("labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "lb")
		issue := bdProxiedCreate(t, bd, p.dir, "Labeled issue", "-l", "bug,critical")

		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, issue.ID)
		labelMap := make(map[string]bool)
		for _, l := range labels {
			labelMap[l] = true
		}
		if !labelMap["bug"] || !labelMap["critical"] {
			t.Errorf("expected labels bug and critical, got %v", labels)
		}
	})

	t.Run("explicit_id", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ei")
		issue := bdProxiedCreate(t, bd, p.dir, "Explicit ID", "--id", "ei-custom42")
		if issue.ID != "ei-custom42" {
			t.Errorf("ID: got %q, want %q", issue.ID, "ei-custom42")
		}
	})

	t.Run("dependencies", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dp")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent issue")
		child := bdProxiedCreate(t, bd, p.dir, "Child issue", "--deps", "blocks:"+parent.ID)

		db := openProxiedDB(t, p)
		assertProxiedDepExists(t, db, parent.ID, child.ID)
	})

	t.Run("blocked_by_alias", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "bb")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker issue")
		blocked := bdProxiedCreate(t, bd, p.dir, "Blocked issue", "--deps", "blocked-by:"+blocker.ID)

		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, blocked.ID, blocker.ID, "blocks")
	})

	t.Run("depends_on_alias", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "do")
		prereq := bdProxiedCreate(t, bd, p.dir, "Prerequisite")
		dependent := bdProxiedCreate(t, bd, p.dir, "Dependent issue", "--deps", "depends-on:"+prereq.ID)

		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, dependent.ID, prereq.ID, "blocks")
	})

	t.Run("unknown_dep_type_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ud")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker")
		out := bdProxiedCreateFail(t, bd, p.dir, "Bad dep type", "--deps", "bogus-type:"+blocker.ID)
		if !strings.Contains(out, "unknown dependency type") {
			t.Errorf("expected 'unknown dependency type' error, got:\n%s", out)
		}
		if !strings.Contains(out, "blocked-by") || !strings.Contains(out, "depends-on") {
			t.Errorf("expected accepted dependency aliases in error, got:\n%s", out)
		}
	})

	t.Run("multiple_dependencies", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "md")
		dep1 := bdProxiedCreate(t, bd, p.dir, "Dep 1")
		dep2 := bdProxiedCreate(t, bd, p.dir, "Dep 2")
		child := bdProxiedCreate(t, bd, p.dir, "Multi dep issue",
			"--deps", fmt.Sprintf("blocks:%s,related:%s", dep1.ID, dep2.ID))

		db := openProxiedDB(t, p)
		assertProxiedDepExists(t, db, dep1.ID, child.ID)
		assertProxiedDepExists(t, db, child.ID, dep2.ID)
	})

	t.Run("parent_child", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pc")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent epic", "-t", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Child task", "--parent", parent.ID)

		if !strings.HasPrefix(child.ID, parent.ID+".") {
			t.Errorf("child ID %q should start with %q.", child.ID, parent.ID)
		}
		db := openProxiedDB(t, p)
		assertProxiedDepExists(t, db, child.ID, parent.ID)
	})

	t.Run("parent_label_inheritance", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pi")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent with labels", "-t", "epic", "-l", "team-a,priority:high")
		child := bdProxiedCreate(t, bd, p.dir, "Child inherits", "--parent", parent.ID)

		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, child.ID)
		labelMap := make(map[string]bool)
		for _, l := range labels {
			labelMap[l] = true
		}
		if !labelMap["team-a"] || !labelMap["priority:high"] {
			t.Errorf("expected inherited labels team-a and priority:high, got %v", labels)
		}
	})

	t.Run("parent_no_inherit_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ni")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent", "-t", "epic", "-l", "inherited-label")
		child := bdProxiedCreate(t, bd, p.dir, "Child no inherit", "--parent", parent.ID, "--no-inherit-labels", "-l", "own-label")

		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, child.ID)
		labelMap := make(map[string]bool)
		for _, l := range labels {
			labelMap[l] = true
		}
		if !labelMap["own-label"] {
			t.Error("expected own-label on child")
		}
		if labelMap["inherited-label"] {
			t.Error("did not expect inherited-label on child with --no-inherit-labels")
		}
	})

	t.Run("due_date", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dd")
		issue := bdProxiedCreate(t, bd, p.dir, "Due issue", "--due", "+24h")
		if issue.DueAt == nil {
			t.Fatal("expected DueAt to be set")
		}
		expected := time.Now().Add(24 * time.Hour)
		diff := issue.DueAt.Sub(expected)
		if diff < -5*time.Minute || diff > 5*time.Minute {
			t.Errorf("DueAt off by too much: got %v, expected ~%v", issue.DueAt, expected)
		}
	})

	t.Run("defer_until", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "df")
		issue := bdProxiedCreate(t, bd, p.dir, "Deferred issue", "--defer", "+2h")
		if issue.DeferUntil == nil {
			t.Fatal("expected DeferUntil to be set")
		}
		expected := time.Now().Add(2 * time.Hour)
		diff := issue.DeferUntil.Sub(expected)
		if diff < -5*time.Minute || diff > 5*time.Minute {
			t.Errorf("DeferUntil off by too much: got %v, expected ~%v", issue.DeferUntil, expected)
		}
	})

}

func TestProxiedServerCreate2(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("ephemeral", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ep")
		issue := bdProxiedCreate(t, bd, p.dir, "Ephemeral issue", "--ephemeral")

		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM wisps WHERE id = ?", issue.ID).Scan(&count); err != nil {
			t.Fatalf("query wisps: %v", err)
		}
		if count != 1 {
			t.Errorf("expected ephemeral issue in wisps table, found %d rows", count)
		}
	})

	t.Run("no_history", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "nh")
		issue := bdProxiedCreate(t, bd, p.dir, "No history issue", "--no-history")
		if issue.ID == "" {
			t.Fatal("expected issue ID")
		}
	})

	t.Run("estimate", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "es")
		issue := bdProxiedCreate(t, bd, p.dir, "Estimated issue", "-e", "60")
		if issue.EstimatedMinutes == nil || *issue.EstimatedMinutes != 60 {
			t.Errorf("estimate: got %v, want 60", issue.EstimatedMinutes)
		}
	})

	t.Run("notes", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "nt")
		issue := bdProxiedCreate(t, bd, p.dir, "Notes issue", "--notes", "Some notes here")
		if issue.Notes != "Some notes here" {
			t.Errorf("notes: got %q, want %q", issue.Notes, "Some notes here")
		}
	})

	t.Run("spec_id", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sp")
		issue := bdProxiedCreate(t, bd, p.dir, "Spec issue", "--spec-id", "sp-spec1")
		if issue.SpecID != "sp-spec1" {
			t.Errorf("spec_id: got %q, want %q", issue.SpecID, "sp-spec1")
		}
	})

	t.Run("external_ref", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "er")
		issue := bdProxiedCreate(t, bd, p.dir, "External ref issue", "--external-ref", "gh-123")
		if issue.ExternalRef == nil || *issue.ExternalRef != "gh-123" {
			t.Errorf("external_ref: got %v, want %q", issue.ExternalRef, "gh-123")
		}
	})

	t.Run("linear_external_ref", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ler")
		ref := "https://linear.app/team/issue/TEAM-123/fix-login"
		issue := bdProxiedCreate(t, bd, p.dir, "Pre-linked Linear issue", "--external-ref", ref)
		if issue.ExternalRef == nil || *issue.ExternalRef != ref {
			t.Errorf("external_ref: got %v, want %q", issue.ExternalRef, ref)
		}
	})

	t.Run("metadata", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mt")
		issue := bdProxiedCreate(t, bd, p.dir, "Metadata issue", "--metadata", `{"key":"value"}`)
		if issue.Metadata == nil {
			t.Fatal("expected metadata to be set")
		}
		var m map[string]interface{}
		if err := json.Unmarshal(issue.Metadata, &m); err != nil {
			t.Fatalf("failed to parse metadata: %v", err)
		}
		if v, ok := m["key"]; !ok || v != "value" {
			t.Errorf("metadata: got %v, want key=value", m)
		}
	})

	t.Run("dry_run", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "dr")
		out, err := bdProxiedRun(t, bd, p.dir, "create", "--dry-run", "Dry run issue", "--json")
		if err != nil {
			t.Fatalf("bd create --dry-run failed: %v\n%s", err, out)
		}
		if strings.Contains(string(out), "error") {
			t.Errorf("dry-run produced error output: %s", out)
		}
		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
			t.Fatalf("query issues: %v", err)
		}
		if count != 0 {
			t.Errorf("expected dry-run to persist nothing, found %d issues", count)
		}
	})

	t.Run("skills_and_context", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sc")
		issue := bdProxiedCreate(t, bd, p.dir, "Skills issue",
			"--skills", "Go, SQL",
			"--context", "Working on embedded storage")
		if !strings.Contains(issue.Description, "Go, SQL") {
			t.Errorf("expected skills in description, got %q", issue.Description)
		}
		if !strings.Contains(issue.Description, "Working on embedded storage") {
			t.Errorf("expected context in description, got %q", issue.Description)
		}
	})

	t.Run("discovered_from_dep", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "di")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent work")
		child := bdProxiedCreate(t, bd, p.dir, "Discovered bug",
			"--deps", "discovered-from:"+parent.ID)
		if child.ID == "" {
			t.Fatal("expected child issue ID")
		}
		db := openProxiedDB(t, p)
		assertProxiedDepExists(t, db, child.ID, parent.ID)
	})

	t.Run("markdown_bulk_create", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "mk")
		mdContent := `## First issue

### Priority
1

### Type
bug

### Description
First bug description

### Labels
urgent, backend

## Second issue

### Priority
3

### Type
feature

### Description
A new feature
`
		mdFile := filepath.Join(p.dir, "issues.md")
		if err := os.WriteFile(mdFile, []byte(mdContent), 0644); err != nil {
			t.Fatal(err)
		}
		out, err := bdProxiedRun(t, bd, p.dir, "create", "-f", mdFile, "--json")
		if err != nil {
			t.Fatalf("bd create -f failed: %v\n%s", err, out)
		}
		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
			t.Fatalf("count issues: %v", err)
		}
		if count < 2 {
			t.Errorf("expected at least 2 issues from markdown, got %d", count)
		}
	})

	t.Run("both_due_and_defer", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "bd2")
		issue := bdProxiedCreate(t, bd, p.dir, "Both due and defer", "--due", "+48h", "--defer", "+24h")
		if issue.DueAt == nil {
			t.Fatal("expected DueAt to be set")
		}
		if issue.DeferUntil == nil {
			t.Fatal("expected DeferUntil to be set")
		}
	})

	t.Run("parent_label_inheritance_merge", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pm")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent with a,b", "-t", "epic", "-l", "a,b")
		child := bdProxiedCreate(t, bd, p.dir, "Child with c,a", "--parent", parent.ID, "-l", "c,a")

		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, child.ID)
		labelMap := make(map[string]bool)
		for _, l := range labels {
			labelMap[l] = true
		}
		for _, want := range []string{"a", "b", "c"} {
			if !labelMap[want] {
				t.Errorf("expected label %q, got %v", want, labels)
			}
		}
		if len(labels) != 3 {
			t.Errorf("expected 3 labels (deduped), got %d: %v", len(labels), labels)
		}
	})

	t.Run("parent_no_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pn")
		parent := bdProxiedCreate(t, bd, p.dir, "Labelless parent", "-t", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Child of labelless", "--parent", parent.ID)

		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, child.ID)
		if len(labels) != 0 {
			t.Errorf("expected 0 labels, got %d: %v", len(labels), labels)
		}
	})

	t.Run("discovered_from_inherits_source_repo", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sr")

		parent := bdProxiedCreate(t, bd, p.dir, "Parent with source repo")
		db := openProxiedDB(t, p)
		if _, err := db.ExecContext(context.Background(),
			"UPDATE issues SET source_repo = ? WHERE id = ?",
			"/path/to/repo", parent.ID); err != nil {
			t.Fatalf("set parent source_repo: %v", err)
		}

		child := bdProxiedCreate(t, bd, p.dir, "Discovered bug",
			"--deps", "discovered-from:"+parent.ID)

		var sourceRepo string
		err := db.QueryRowContext(context.Background(),
			"SELECT COALESCE(source_repo, '') FROM issues WHERE id = ?", child.ID).Scan(&sourceRepo)
		if err != nil {
			t.Fatalf("query source_repo: %v", err)
		}
		if sourceRepo != "/path/to/repo" {
			t.Errorf("source_repo: got %q, want %q", sourceRepo, "/path/to/repo")
		}
	})

	t.Run("no_title_fails", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "nt2")
		out := bdProxiedCreateFail(t, bd, p.dir)
		if !strings.Contains(out, "title") {
			t.Errorf("expected title-related error, got: %s", out)
		}
	})

	t.Run("graph_basic", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gb")
		plan := `{
  "nodes": [
    {"key": "a", "title": "Node A", "type": "task"},
    {"key": "b", "title": "Node B", "type": "task"}
  ],
  "edges": [
    {"from_key": "a", "to_key": "b", "type": "related"}
  ]
}`
		planFile := filepath.Join(p.dir, "graph.json")
		if err := os.WriteFile(planFile, []byte(plan), 0644); err != nil {
			t.Fatal(err)
		}
		out, err := bdProxiedRun(t, bd, p.dir, "create", "--graph", planFile, "--json")
		if err != nil {
			t.Fatalf("bd create --graph failed: %v\n%s", err, out)
		}
		var result GraphApplyResult
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse graph result: %v\nstdout:\n%s", err, out)
		}
		aID, bID := result.IDs["a"], result.IDs["b"]
		if aID == "" || bID == "" {
			t.Fatalf("expected both IDs in result, got %#v", result.IDs)
		}
		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, aID, bID, "related")
	})

	t.Run("graph_parent_child_top_level_ids", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gpc")
		plan := `{
  "nodes": [
    {"key": "child", "title": "Child", "type": "task", "parent_key": "parent"},
    {"key": "parent", "title": "Parent", "type": "epic"}
  ]
}`
		planFile := filepath.Join(p.dir, "graph.json")
		if err := os.WriteFile(planFile, []byte(plan), 0644); err != nil {
			t.Fatal(err)
		}
		out, err := bdProxiedRun(t, bd, p.dir, "create", "--graph", planFile, "--json")
		if err != nil {
			t.Fatalf("bd create --graph failed: %v\n%s", err, out)
		}
		var result GraphApplyResult
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse graph result: %v\nstdout:\n%s", err, out)
		}
		childID, parentID := result.IDs["child"], result.IDs["parent"]
		if strings.Contains(childID, ".") {
			t.Errorf("child ID %q should be top-level, not counter-style", childID)
		}
		if !strings.HasPrefix(childID, "gpc-") || !strings.HasPrefix(parentID, "gpc-") {
			t.Errorf("expected gpc- prefix on both IDs, got child=%q parent=%q", childID, parentID)
		}
		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, childID, parentID, "parent-child")
	})

	t.Run("graph_dry_run_db_aware", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gdr")
		db := openProxiedDB(t, p)
		_, err := db.ExecContext(context.Background(),
			`REPLACE INTO config (`+"`key`"+`, value) VALUES (?, ?)`,
			"types.custom", "gizmo")
		if err != nil {
			t.Fatalf("set types.custom: %v", err)
		}

		plan := `{
  "nodes": [
    {"key": "g1", "title": "Gizmo node", "type": "gizmo"}
  ]
}`
		planFile := filepath.Join(p.dir, "graph.json")
		if err := os.WriteFile(planFile, []byte(plan), 0644); err != nil {
			t.Fatal(err)
		}
		out, err := bdProxiedRun(t, bd, p.dir, "create", "--graph", planFile, "--dry-run", "--json")
		if err != nil {
			t.Fatalf("dry-run with DB-only custom type should succeed: %v\n%s", err, out)
		}
		var count int
		if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
			t.Fatalf("count issues: %v", err)
		}
		if count != 0 {
			t.Errorf("expected dry-run to persist nothing, found %d issues", count)
		}
	})

	t.Run("graph_initial_labels_not_duplicated", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "gil")
		plan := `{
  "nodes": [
    {"key": "root", "title": "Graph root", "type": "task", "labels": ["team-a", "shared"]}
  ]
}`
		planFile := filepath.Join(p.dir, "graph.json")
		if err := os.WriteFile(planFile, []byte(plan), 0644); err != nil {
			t.Fatal(err)
		}
		out, err := bdProxiedRun(t, bd, p.dir, "create", "--graph", planFile, "--json")
		if err != nil {
			t.Fatalf("bd create --graph failed: %v\n%s", err, out)
		}
		var result GraphApplyResult
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse graph result: %v\nstdout:\n%s", err, out)
		}
		id := result.IDs["root"]
		if id == "" {
			t.Fatalf("missing root ID: %#v", result.IDs)
		}
		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, id)
		if len(labels) != 2 {
			t.Fatalf("label count = %d, want 2 (no duplicates): %v", len(labels), labels)
		}
		var eventCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?",
			id, types.EventLabelAdded).Scan(&eventCount); err != nil {
			t.Fatalf("count label events: %v", err)
		}
		if eventCount != 2 {
			t.Fatalf("label_added event count = %d, want 2", eventCount)
		}
	})
}
