//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerCreate(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("scalar_and_output_boundary", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "sc")
		issue := bdProxiedCreate(t, bd, p.dir, "Scalar issue")
		if !strings.HasPrefix(issue.ID, "sc-") {
			t.Fatalf("ID should have prefix sc-, got %q", issue.ID)
		}
		if issue.Title != "Scalar issue" || issue.Status != types.StatusOpen || issue.Priority != 2 || issue.IssueType != types.TypeTask {
			t.Errorf("create result = %+v, want scalar defaults", issue)
		}

		db := openProxiedDB(t, p)
		var title, status, issueType string
		var priority int
		if err := db.QueryRowContext(context.Background(), "SELECT title, status, priority, issue_type FROM issues WHERE id = ?", issue.ID).Scan(&title, &status, &priority, &issueType); err != nil {
			t.Fatalf("query persisted scalar issue: %v", err)
		}
		if title != "Scalar issue" || status != string(types.StatusOpen) || priority != 2 || issueType != string(types.TypeTask) {
			t.Errorf("persisted scalar issue = title=%q status=%q priority=%d type=%q", title, status, priority, issueType)
		}

		out, err := bdProxiedRun(t, bd, p.dir, "create", "--silent", "Silent issue")
		if err != nil {
			t.Fatalf("bd create --silent: %v\n%s", err, out)
		}
		silentID := strings.TrimSpace(string(out))
		if !strings.HasPrefix(silentID, "sc-") || string(out) != silentID+"\n" {
			t.Errorf("silent stdout = %q, want exactly the created ID plus newline", out)
		}
		if err := db.QueryRowContext(context.Background(), "SELECT title FROM issues WHERE id = ?", silentID).Scan(&title); err != nil {
			t.Fatalf("query persisted silent issue: %v", err)
		}
		if title != "Silent issue" {
			t.Errorf("silent issue title = %q, want Silent issue", title)
		}
	})

	t.Run("aggregate_transaction_boundary", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ag")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent", "-t", "epic", "-l", "parent-label,shared")
		target := bdProxiedCreate(t, bd, p.dir, "Dependency target")
		child := bdProxiedCreate(t, bd, p.dir, "Child", "--parent", parent.ID, "-l", "own-label", "--deps", "blocks:"+target.ID)
		if !strings.HasPrefix(child.ID, parent.ID+".") {
			t.Errorf("child ID %q should start with %q.", child.ID, parent.ID)
		}

		db := openProxiedDB(t, p)
		var persistedTitle string
		if err := db.QueryRowContext(context.Background(), "SELECT title FROM issues WHERE id = ?", child.ID).Scan(&persistedTitle); err != nil {
			t.Fatalf("query persisted child: %v", err)
		}
		if persistedTitle != "Child" {
			t.Errorf("persisted child title = %q, want Child", persistedTitle)
		}
		assertProxiedDepExistsWithType(t, db, child.ID, parent.ID, "parent-child")
		assertProxiedDepExistsWithType(t, db, target.ID, child.ID, "blocks")

		labels := getProxiedLabels(t, db, child.ID)
		labelSet := make(map[string]bool, len(labels))
		for _, label := range labels {
			labelSet[label] = true
		}
		for _, want := range []string{"parent-label", "shared", "own-label"} {
			if !labelSet[want] {
				t.Errorf("child labels = %v, missing %q", labels, want)
			}
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

	// RULING R1 (TestParityCreateOnOccupiedIDRefuses): `bd create --id` on an
	// occupied ID refuses with exit 1 and a fixed message, leaving the
	// pre-existing row untouched. This is the proxied twin of the embedded
	// parity pin — before the bd-b8itp fix the proxied route sent the
	// domain-level upsert form and silently destroyed the existing issue
	// while reporting "Created issue:".
	t.Run("occupied_id_refuses", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "oc")
		seeded := bdProxiedCreate(t, bd, p.dir, "Original title",
			"--id", "oc-occ1", "-d", "original description", "-p", "0")
		if seeded.ID != "oc-occ1" {
			t.Fatalf("seed ID = %q, want oc-occ1", seeded.ID)
		}

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "create", "Replacement title",
			"--id", "oc-occ1", "-d", "replacement description", "-p", "4")
		if err == nil {
			t.Fatalf("bd create --id oc-occ1 should have refused the occupied ID\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Errorf("exit = %v, want exit code 1", err)
		}
		const wantErr = "Error: oc-occ1 already exists; use bd update, or bd import for upsert semantics\n"
		if stderr != wantErr {
			t.Errorf("stderr = %q, want %q", stderr, wantErr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty on a refused create", stdout)
		}

		db := openProxiedDB(t, p)
		var title, description string
		var priority int
		if err := db.QueryRowContext(context.Background(),
			"SELECT title, description, priority FROM issues WHERE id = ?", "oc-occ1").
			Scan(&title, &description, &priority); err != nil {
			t.Fatalf("query surviving row: %v", err)
		}
		if title != "Original title" || description != "original description" || priority != 0 {
			t.Errorf("row after refused create = title=%q description=%q priority=%d, want the seeded values", title, description, priority)
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

// TestProxiedServerCreateInfraTypeRoutesToWisps pins end-to-end that a
// configured infra type lands in the wisps tables against a real proxied
// server, matching the embedded path. Before ga-2kkue the proxied path routed
// on the --ephemeral/--no-history flags alone, so `bd create -t message` wrote
// a durable row into issues.
func TestProxiedServerCreateInfraTypeRoutesToWisps(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// countRows reports how many rows the given table holds for an ID. Issues
	// and wisps share one ID space, so both sides must be asserted: a routing
	// bug shows up as the row existing in the wrong table, not as a missing row.
	countRows := func(t *testing.T, p proxiedProject, table, id string) int {
		t.Helper()
		db := openProxiedDB(t, p)
		var count int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", table)
		if err := db.QueryRowContext(context.Background(), query, id).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		return count
	}

	t.Run("infra type routes to wisps", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "iw")
		issue := bdProxiedCreate(t, bd, p.dir, "Infra message", "-t", "message")
		if !issue.Ephemeral {
			t.Errorf("Ephemeral = false, want true for an infra type")
		}
		if got := countRows(t, p, "wisps", issue.ID); got != 1 {
			t.Errorf("wisps rows for %s = %d, want 1", issue.ID, got)
		}
		if got := countRows(t, p, "issues", issue.ID); got != 0 {
			t.Errorf("issues rows for %s = %d, want 0", issue.ID, got)
		}
	})

	t.Run("non-infra type stays durable", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "id")
		issue := bdProxiedCreate(t, bd, p.dir, "Plain task", "-t", "task")
		if got := countRows(t, p, "issues", issue.ID); got != 1 {
			t.Errorf("issues rows for %s = %d, want 1", issue.ID, got)
		}
		if got := countRows(t, p, "wisps", issue.ID); got != 0 {
			t.Errorf("wisps rows for %s = %d, want 0 — infra routing must not wisp everything", issue.ID, got)
		}
	})

	t.Run("no-history infra type keeps its retention mode", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ih")
		issue := bdProxiedCreate(t, bd, p.dir, "Infra message", "-t", "message", "--no-history")
		if issue.Ephemeral {
			t.Errorf("Ephemeral = true, want false — --no-history keeps its own retention mode")
		}
		if got := countRows(t, p, "wisps", issue.ID); got != 1 {
			t.Errorf("wisps rows for %s = %d, want 1", issue.ID, got)
		}
	})
}
