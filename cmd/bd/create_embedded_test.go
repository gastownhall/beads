//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// bdCreate runs "bd create" in the given dir with --json and extra args.
// Returns the parsed issue JSON. Retries on flock contention, fatals on other failures.
func bdCreate(t *testing.T, bd, dir string, args ...string) *types.Issue {
	t.Helper()
	fullArgs := append([]string{"create", "--json"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd create %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return parseIssueJSON(t, out)
}

// parseIssueJSON extracts a JSON issue object from command output that may
// contain non-JSON lines (tips, warnings) mixed with multi-line pretty-printed JSON.
func parseIssueJSON(t *testing.T, out []byte) *types.Issue {
	t.Helper()
	s := string(out)

	// Find the first '{' and extract from there to the end.
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object found in output:\n%s", s)
	}

	var issue types.Issue
	if err := json.Unmarshal([]byte(s[start:]), &issue); err != nil {
		// Try to find the matching closing brace for multi-line JSON
		// by attempting progressively larger substrings.
		// Fall back to decoder which handles trailing content.
		dec := json.NewDecoder(strings.NewReader(s[start:]))
		if decErr := dec.Decode(&issue); decErr != nil {
			t.Fatalf("failed to parse JSON output: %v\nraw: %s", decErr, s[start:])
		}
	}
	return &issue
}

// bdCreateSilent runs "bd create" with --silent and returns the issue ID.
// Retries on flock contention.
func bdCreateSilent(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"create", "--silent"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd create --silent %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// bdCreateFail runs "bd create" expecting failure. Returns combined output.
func bdCreateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"create"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("bd create should have failed")
	}
	return string(out)
}

type graphCreateResult struct {
	IDs map[string]string `json:"ids"`
}

func writeGraphCreatePlan(t *testing.T, dir string) string {
	t.Helper()
	plan := `{
		"nodes": [
			{"key": "root", "title": "Graph root", "type": "task"},
			{"key": "child", "title": "Graph child", "type": "task", "parent_key": "root"}
		]
	}`
	planFile := filepath.Join(dir, "graph-plan.json")
	if err := os.WriteFile(planFile, []byte(plan), 0o600); err != nil {
		t.Fatalf("write graph plan: %v", err)
	}
	return planFile
}

func bdCreateGraph(t *testing.T, bd, dir, planFile string, args ...string) graphCreateResult {
	t.Helper()
	fullArgs := append([]string{"create", "--json", "--graph", planFile}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd create --graph %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	var result graphCreateResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse graph create result: %v\n%s", err, out)
	}
	return result
}

// bdShow runs "bd show <id> --json" and returns the parsed issue.
func bdShow(t *testing.T, bd, dir, id string) *types.Issue {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\nstdout:\n%s\nstderr:\n%s", id, err, stdout.String(), stderr.String())
	}
	return parseIssueJSON(t, stdout.Bytes())
}

// openStore opens an EmbeddedDoltStore for direct verification queries.
func openStore(t *testing.T, beadsDir, database string) *embeddeddolt.EmbeddedDoltStore {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// assertDepExists verifies a dependency row exists via raw SQL.
func assertDepExists(t *testing.T, beadsDir, database, issueID, dependsOnID string) {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var count int
	err = db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		issueID, dependsOnID).Scan(&count)
	if err != nil {
		t.Fatalf("query dependencies: %v", err)
	}
	if count == 0 {
		t.Errorf("expected dependency %s -> %s, not found", issueID, dependsOnID)
	}
}

func assertDepExistsWithType(t *testing.T, beadsDir, database, issueID, dependsOnID, expectedType string) {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()

	var depType string
	err = db.QueryRowContext(t.Context(),
		"SELECT type FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		issueID, dependsOnID).Scan(&depType)
	if err != nil {
		t.Fatalf("query dependencies for %s -> %s: %v", issueID, dependsOnID, err)
	}
	if depType != expectedType {
		t.Errorf("dependency %s -> %s: got type %q, want %q", issueID, dependsOnID, depType, expectedType)
	}
}

func TestEmbeddedCreate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("scalar_create_journey", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sc")

		basic := bdCreate(t, bd, dir, "Basic JSON issue")
		if basic.ID == "" || basic.Title != "Basic JSON issue" || basic.Status != types.StatusOpen || basic.Priority != 2 || basic.IssueType != types.TypeTask {
			t.Fatalf("default JSON create = %+v", basic)
		}
		if issue := bdCreate(t, bd, dir, "--title", "Title via flag"); issue.Title != "Title via flag" {
			t.Fatalf("title flag = %q", issue.Title)
		}
		if id := bdCreateSilent(t, bd, dir, "Silent issue"); id == "" || strings.Contains(id, "\n") {
			t.Fatalf("silent output = %q, want exactly one ID", id)
		}

		full := bdCreate(t, bd, dir, "Full fields",
			"--id", "sc-full", "-p", "1", "-t", "bug", "-d", "description", "--design", "design",
			"--acceptance", "acceptance", "-a", "worker", "-l", "portfolio-scalar-a,portfolio-scalar-b",
			"--due", "+24h", "--defer", "+2h", "-e", "60", "--notes", "notes", "--spec-id", "sc-spec",
			"--external-ref", "gh-123", "--metadata", `{"key":"value"}`, "--skills", "Go,SQL", "--context", "embedded create")
		if full.ID != "sc-full" || full.Priority != 1 || full.IssueType != types.TypeBug || full.Design != "design" || full.AcceptanceCriteria != "acceptance" || full.Assignee != "worker" || full.Notes != "notes" || full.SpecID != "sc-spec" || full.ExternalRef == nil || *full.ExternalRef != "gh-123" || full.EstimatedMinutes == nil || *full.EstimatedMinutes != 60 || full.DueAt == nil || full.DeferUntil == nil {
			t.Fatalf("full-field create lost values: %+v", full)
		}
		for _, want := range []string{"description", "## Required Skills\nGo,SQL", "## Context\nembedded create"} {
			if !strings.Contains(full.Description, want) {
				t.Fatalf("full-field description = %q, missing %q", full.Description, want)
			}
		}
		if delta := full.DueAt.Sub(time.Now().Add(24 * time.Hour)); delta < -5*time.Minute || delta > 5*time.Minute {
			t.Fatalf("full-field due date = %v, want approximately +24h", full.DueAt)
		}
		var metadata map[string]any
		if err := json.Unmarshal(full.Metadata, &metadata); err != nil || metadata["key"] != "value" {
			t.Fatalf("full-field metadata = %q, err = %v", full.Metadata, err)
		}

		if issue := bdCreate(t, bd, dir, "Built-in status", "--status", "blocked"); issue.Status != types.StatusBlocked {
			t.Fatalf("built-in status = %q, want %q", issue.Status, types.StatusBlocked)
		}
		bdConfig(t, bd, dir, "set", "status.custom", "review:wip")
		if issue := bdCreate(t, bd, dir, "Custom status", "--status", "review"); issue.Status != types.Status("review") {
			t.Fatalf("custom status = %q, want review", issue.Status)
		}
		if out := bdCreateFail(t, bd, dir, "Invalid status", "--status", "not_a_status"); !strings.Contains(out, `invalid status "not_a_status"`) {
			t.Fatalf("invalid status output = %s", out)
		}
		if issue := bdCreate(t, bd, dir, "Status wins", "--status", "blocked", "--defer", "+2h"); issue.Status != types.StatusBlocked || issue.DeferUntil == nil {
			t.Fatalf("status-over-defer = %+v", issue)
		}
		if out := bdCreateFail(t, bd, dir); !strings.Contains(out, "title") {
			t.Fatalf("missing title output = %s", out)
		}
	})

	t.Run("relationships_and_parent_journey", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rp")
		blocker := bdCreate(t, bd, dir, "Blocker")
		dependent := bdCreate(t, bd, dir, "Dependent", "--deps", "blocks:"+blocker.ID)
		assertDepExists(t, beadsDir, "rp", blocker.ID, dependent.ID)

		parent := bdCreate(t, bd, dir, "Parent", "-t", "epic", "-l", "portfolio-parent,portfolio-shared")
		child := bdCreate(t, bd, dir, "Child", "--parent", parent.ID, "-l", "portfolio-child,portfolio-shared")
		if child.ID != parent.ID+".1" {
			t.Fatalf("child ID = %q, want %q", child.ID, parent.ID+".1")
		}
		assertDepExists(t, beadsDir, "rp", child.ID, parent.ID)

		noInherit := bdCreate(t, bd, dir, "No inherited labels", "--parent", parent.ID, "--no-inherit-labels", "-l", "portfolio-own")
		grandchild := bdCreate(t, bd, dir, "Grandchild", "--parent", child.ID)
		if grandchild.ID != child.ID+".1" {
			t.Fatalf("hierarchical child counter = %q, want %q", grandchild.ID, child.ID+".1")
		}

		store := openStore(t, beadsDir, "rp")
		labels, err := store.GetLabels(t.Context(), child.ID)
		if err != nil {
			t.Fatalf("get child labels: %v", err)
		}
		wantLabels := map[string]bool{"portfolio-parent": true, "portfolio-shared": true, "portfolio-child": true}
		if len(labels) != len(wantLabels) {
			t.Fatalf("merged labels = %v, want three deduplicated labels", labels)
		}
		for _, label := range labels {
			delete(wantLabels, label)
		}
		if len(wantLabels) != 0 {
			t.Fatalf("missing merged labels: %v", wantLabels)
		}

		labels, err = store.GetLabels(t.Context(), noInherit.ID)
		if err != nil {
			t.Fatalf("get no-inherit labels: %v", err)
		}
		if len(labels) != 1 || labels[0] != "portfolio-own" {
			t.Fatalf("no-inherit labels = %v, want [portfolio-own]", labels)
		}

		sourceParent := &types.Issue{
			Title:      "Source repository parent",
			Priority:   1,
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			SourceRepo: "/path/to/repo",
		}
		if err := store.CreateIssue(t.Context(), sourceParent, "test"); err != nil {
			t.Fatalf("create source repository parent: %v", err)
		}
		if err := store.Commit(t.Context(), "create source repository parent"); err != nil {
			t.Fatalf("commit source repository parent: %v", err)
		}
		store.Close()

		discovered := bdCreate(t, bd, dir, "Discovered work", "--deps", "discovered-from:"+sourceParent.ID)
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "rp", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var sourceRepo string
		if err := db.QueryRowContext(t.Context(), "SELECT COALESCE(source_repo, '') FROM issues WHERE id = ?", discovered.ID).Scan(&sourceRepo); err != nil {
			t.Fatalf("query discovered source_repo: %v", err)
		}
		if sourceRepo != sourceParent.SourceRepo {
			t.Fatalf("discovered source_repo = %q, want %q", sourceRepo, sourceParent.SourceRepo)
		}
	})

	t.Run("storage_plane_journey", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sp")
		ephemeral := bdCreate(t, bd, dir, "Ephemeral", "--ephemeral")
		noHistory := bdCreate(t, bd, dir, "No history", "--no-history")
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "sp", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		for _, check := range []struct {
			id                   string
			ephemeral, noHistory int
		}{{ephemeral.ID, 1, 0}, {noHistory.ID, 0, 1}} {
			var gotEphemeral, gotNoHistory int
			if err := db.QueryRowContext(t.Context(), "SELECT ephemeral, no_history FROM wisps WHERE id = ?", check.id).Scan(&gotEphemeral, &gotNoHistory); err != nil {
				t.Fatalf("query wisp %s: %v", check.id, err)
			}
			if gotEphemeral != check.ephemeral || gotNoHistory != check.noHistory {
				t.Fatalf("wisp %s flags = %d/%d, want %d/%d", check.id, gotEphemeral, gotNoHistory, check.ephemeral, check.noHistory)
			}
		}
	})

	t.Run("graph_create_journey", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gr")
		plan := `{
			"nodes": [
				{"key":"main","id":"gr-a1b2c3","title":"Full-field node","type":"task","status":"in_progress","description":"desc","design":"the design","acceptance_criteria":"the criteria","notes":"the notes","spec_id":"gr-spec1","external_ref":"gh-42","assignee":"worker","owner":"owner@example.com","priority":1,"estimated_minutes":45,"due_at":"2030-01-02T15:04:05Z","labels":["portfolio-graph-a","portfolio-graph-b"],"metadata":{"str":"v","num":3},"mol_type":"swarm","storage_class":"unversioned","pinned":true},
				{"key":"deferred","title":"Deferred node","defer_until":"2030-01-01T00:00:00Z"},
				{"key":"done","title":"Closed node","status":"closed"},
				{"key":"evt","title":"Event node","type":"event","event_kind":"agent.started","actor":"agent://a","target":"bead://b","payload":"{\"k\":1}"},
				{"key":"wisp","title":"Wisp node","ephemeral":true},
				{"key":"gate","title":"Fanout gate"},
				{"key":"spawner","title":"Spawner step"}
			],
			"edges": [{"from_key":"gate","to_key":"spawner","type":"waits-for","gate":"any-children","spawner_key":"spawner"}]
		}`
		planFile := filepath.Join(dir, "compound-plan.json")
		if err := os.WriteFile(planFile, []byte(plan), 0o600); err != nil {
			t.Fatalf("write compound graph plan: %v", err)
		}
		result := bdCreateGraph(t, bd, dir, planFile)
		if result.IDs["main"] != "gr-a1b2c3" || result.IDs["deferred"] == "" || result.IDs["done"] == "" || result.IDs["evt"] == "" || result.IDs["wisp"] == "" || result.IDs["gate"] == "" || result.IDs["spawner"] == "" {
			t.Fatalf("compound graph IDs = %#v", result.IDs)
		}
		issue := bdShow(t, bd, dir, result.IDs["main"])
		if issue.Status != types.StatusInProgress || issue.Description != "desc" || issue.Design != "the design" || issue.AcceptanceCriteria != "the criteria" || issue.Notes != "the notes" || issue.SpecID != "gr-spec1" || issue.ExternalRef == nil || *issue.ExternalRef != "gh-42" || issue.Assignee != "worker" || issue.Owner != "owner@example.com" || issue.Priority != 1 || issue.EstimatedMinutes == nil || *issue.EstimatedMinutes != 45 || issue.MolType != types.MolType("swarm") || issue.StorageClass != types.StorageClassUnversioned || !issue.Pinned {
			t.Fatalf("compound graph full-field issue = %+v", issue)
		}
		if issue.DueAt == nil || issue.DueAt.UTC().Format(time.RFC3339) != "2030-01-02T15:04:05Z" {
			t.Fatalf("compound graph due_at = %v, want 2030-01-02T15:04:05Z", issue.DueAt)
		}
		var metadata map[string]any
		if err := json.Unmarshal(issue.Metadata, &metadata); err != nil || metadata["str"] != "v" || metadata["num"] != float64(3) {
			t.Fatalf("compound graph metadata = %q, err = %v", issue.Metadata, err)
		}
		deferred := bdShow(t, bd, dir, result.IDs["deferred"])
		if deferred.Status != types.StatusDeferred || deferred.DeferUntil == nil || deferred.DeferUntil.UTC().Format(time.RFC3339) != "2030-01-01T00:00:00Z" {
			t.Fatalf("compound graph deferred node = %+v", deferred)
		}
		done := bdShow(t, bd, dir, result.IDs["done"])
		if done.Status != types.StatusClosed || done.ClosedAt == nil {
			t.Fatalf("compound graph closed node = %+v", done)
		}
		event := bdShow(t, bd, dir, result.IDs["evt"])
		if event.EventKind != "agent.started" || event.Actor != "agent://a" || event.Target != "bead://b" || event.Payload != `{"k":1}` {
			t.Fatalf("compound graph event fields: kind=%q actor=%q target=%q payload=%q", event.EventKind, event.Actor, event.Target, event.Payload)
		}
		planWide := make([]struct {
			ids                          []string
			wantEphemeral, wantNoHistory int
		}, 0, 2)
		for _, check := range []struct {
			args                         []string
			wantEphemeral, wantNoHistory int
		}{{[]string{"--ephemeral"}, 1, 0}, {[]string{"--no-history"}, 0, 1}} {
			planFile := writeGraphCreatePlan(t, dir)
			journey := bdCreateGraph(t, bd, dir, planFile, check.args...)
			planWide = append(planWide, struct {
				ids                          []string
				wantEphemeral, wantNoHistory int
			}{[]string{journey.IDs["root"], journey.IDs["child"]}, check.wantEphemeral, check.wantNoHistory})
		}

		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "gr", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM issues WHERE id = ?", result.IDs["main"]).Scan(&count); err != nil || count != 1 {
			t.Fatalf("durable graph node count = %d, err = %v", count, err)
		}
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM wisps WHERE id = ?", result.IDs["wisp"]).Scan(&count); err != nil || count != 1 {
			t.Fatalf("wisp graph node count = %d, err = %v", count, err)
		}
		var depMetadata string
		if err := db.QueryRowContext(t.Context(), "SELECT COALESCE(metadata, '') FROM dependencies WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ? AND type = 'waits-for'", result.IDs["gate"], result.IDs["spawner"]).Scan(&depMetadata); err != nil {
			t.Fatalf("query waits-for dependency: %v", err)
		}
		var waitsFor types.WaitsForMeta
		if err := json.Unmarshal([]byte(depMetadata), &waitsFor); err != nil || waitsFor.Gate != types.WaitsForAnyChildren || waitsFor.SpawnerID != result.IDs["spawner"] {
			t.Fatalf("waits-for metadata = %+v, raw = %q, err = %v", waitsFor, depMetadata, err)
		}
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?", result.IDs["main"]).Scan(&count); err != nil || count != 2 {
			t.Fatalf("graph label count = %d, err = %v", count, err)
		}
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM events WHERE issue_id = ? AND event_type = ?", result.IDs["main"], types.EventLabelAdded).Scan(&count); err != nil || count != 2 {
			t.Fatalf("graph label_added event count = %d, err = %v", count, err)
		}

		for _, check := range planWide {
			for _, id := range check.ids {
				var gotEphemeral, gotNoHistory int
				if err := db.QueryRowContext(t.Context(), "SELECT ephemeral, no_history FROM wisps WHERE id = ?", id).Scan(&gotEphemeral, &gotNoHistory); err != nil {
					t.Fatalf("query plan-wide graph wisp %s: %v", id, err)
				}
				if gotEphemeral != check.wantEphemeral || gotNoHistory != check.wantNoHistory {
					t.Fatalf("plan-wide graph wisp %s flags = %d/%d, want %d/%d", id, gotEphemeral, gotNoHistory, check.wantEphemeral, check.wantNoHistory)
				}
			}
		}
	})

	t.Run("dry_run_journey", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dr")
		parent := bdCreate(t, bd, dir, "Parent", "-t", "epic", "-l", "portfolio-preview-parent,portfolio-preview-shared")
		type dryRunSnapshot struct {
			head                                        string
			issues, wisps, labels, dependencies, events int
		}
		readSnapshot := func() dryRunSnapshot {
			db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), "dr", "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer func() {
				if err := cleanup(); err != nil {
					t.Errorf("cleanup dry-run snapshot: %v", err)
				}
			}()
			var got dryRunSnapshot
			if err := db.QueryRowContext(t.Context(), "SELECT HASHOF('HEAD')").Scan(&got.head); err != nil {
				t.Fatalf("read dry-run HEAD: %v", err)
			}
			for _, query := range []struct {
				table string
				count *int
			}{
				{"issues", &got.issues},
				{"wisps", &got.wisps},
				{"labels", &got.labels},
				{"dependencies", &got.dependencies},
				{"events", &got.events},
			} {
				if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+query.table).Scan(query.count); err != nil {
					t.Fatalf("count dry-run %s: %v", query.table, err)
				}
			}
			return got
		}
		before := readSnapshot()
		cmd := exec.Command(bd, "create", "--dry-run", "Preview child", "--json", "--parent", parent.ID, "-l", "portfolio-preview-child,portfolio-preview-shared")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("dry-run create: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		preview := parseIssueJSON(t, stdout.Bytes())
		wantLabels := map[string]bool{"portfolio-preview-parent": true, "portfolio-preview-shared": true, "portfolio-preview-child": true}
		for _, label := range preview.Labels {
			delete(wantLabels, label)
		}
		if len(preview.Labels) != 3 || len(wantLabels) != 0 {
			t.Fatalf("dry-run labels = %v, want three deduplicated labels", preview.Labels)
		}
		after := readSnapshot()
		if after != before {
			t.Fatalf("dry-run mutated embedded state: before=%+v after=%+v", before, after)
		}
		child := bdCreate(t, bd, dir, "Real child", "--parent", parent.ID)
		if child.ID != parent.ID+".1" {
			t.Fatalf("dry-run consumed child counter: got %q, want %q", child.ID, parent.ID+".1")
		}
	})

	t.Run("markdown_bulk_journey", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mk")
		markdown := `## First issue

### Priority
1

### Type
bug

### Description
First bug description

### Labels
portfolio-markdown-a, portfolio-markdown-b

## Second issue

### Priority
3

### Type
feature

### Description
A new feature
`
		path := filepath.Join(dir, "issues.md")
		if err := os.WriteFile(path, []byte(markdown), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bd, "create", "-f", path, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("markdown bulk create: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		store := openStore(t, beadsDir, "mk")
		stats, err := store.GetStatistics(t.Context())
		if err != nil || stats.TotalIssues < 2 {
			t.Fatalf("markdown bulk statistics = %+v, err = %v", stats, err)
		}
	})
}

// embeddedStoreSnapshot is the "did anything write to this database?"
// tripwire the preview regression tests compare across a command run.
type embeddedStoreSnapshot struct {
	schemaVersion int
	head          string
	issueCount    int
}

func readEmbeddedStoreSnapshot(t *testing.T, beadsDir, database string) embeddedStoreSnapshot {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(
		t.Context(),
		filepath.Join(beadsDir, "embeddeddolt"),
		database,
		"main",
	)
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup OpenSQL: %v", err)
		}
	}()

	var got embeddedStoreSnapshot
	if err := db.QueryRowContext(t.Context(),
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&got.schemaVersion); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT HASHOF('HEAD')").Scan(&got.head); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM issues").Scan(&got.issueCount); err != nil {
		t.Fatalf("read issue count: %v", err)
	}
	return got
}

// regressEmbeddedSchemaCursor rolls the recorded migration cursor back one
// version WITHOUT touching the physical schema. The latest migration is
// idempotent, so a writable open reapplies it, restores the cursor, and
// commits a new HEAD — which is exactly what makes the cursor a usable
// tripwire. Keeping the physical schema intact lets the preview's own reads
// still work against the older recorded version.
func regressEmbeddedSchemaCursor(t *testing.T, beadsDir, database string) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(
		t.Context(),
		filepath.Join(beadsDir, "embeddeddolt"),
		database,
		"main",
	)
	if err != nil {
		t.Fatalf("OpenSQL for regression fixture: %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion()); err != nil {
		_ = cleanup()
		t.Fatalf("regress schema cursor: %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		"CALL DOLT_COMMIT('-am', 'test: regress schema before dry-run')"); err != nil {
		_ = cleanup()
		t.Fatalf("commit regressed schema cursor: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup regression fixture: %v", err)
	}
}

func TestEmbeddedCreateDryRunDoesNotMigrate(t *testing.T) {
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dnm")
	bdCreate(t, bd, dir, "Existing issue")

	readSnapshot := func() embeddedStoreSnapshot {
		return readEmbeddedStoreSnapshot(t, beadsDir, "dnm")
	}

	regressEmbeddedSchemaCursor(t, beadsDir, "dnm")

	// Force the version-bump path that previously opened a second writable
	// store and migrated before create.RunE reached --dry-run handling.
	if err := os.WriteFile(filepath.Join(beadsDir, localVersionFile), []byte("0.9.0\n"), 0o600); err != nil {
		t.Fatalf("write old local version: %v", err)
	}

	before := readSnapshot()
	if before.schemaVersion != schema.LatestVersion()-1 {
		t.Fatalf("fixture schema version = %d, want %d", before.schemaVersion, schema.LatestVersion()-1)
	}

	cmd := exec.Command(bd, "create", "--dry-run", "Preview only", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd create --dry-run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	after := readSnapshot()
	if after.schemaVersion != before.schemaVersion {
		t.Errorf("schema version changed during dry-run: before=%d after=%d", before.schemaVersion, after.schemaVersion)
	}
	if after.head != before.head {
		t.Errorf("Dolt HEAD changed during dry-run: before=%s after=%s", before.head, after.head)
	}
	if after.issueCount != before.issueCount {
		t.Errorf("issue count changed during dry-run: before=%d after=%d", before.issueCount, after.issueCount)
	}
}

// TestEmbeddedCreateDryRunCrossRepoDoesNotMigrateTarget covers the second
// store a dry-run can reach: `create --dry-run --parent X --repo <other>`
// resolves the parent against the OTHER repo, and openDryRunTargetStore used
// to open it with the writable factory. The command's own store being opened
// read-only says nothing about that one — the mutation lands in a repository
// the user only named as a lookup target.
func TestEmbeddedCreateDryRunCrossRepoDoesNotMigrateTarget(t *testing.T) {
	bd := buildEmbeddedBD(t)
	targetDir, targetBeadsDir, _ := bdInit(t, bd, "--prefix", "xtgt")
	parent := bdCreate(t, bd, targetDir, "Parent in the target repo")
	if parent.ID == "" {
		t.Fatal("parent issue has no ID")
	}

	callerDir, callerBeadsDir, _ := bdInit(t, bd, "--prefix", "xsrc")

	// The tripwire goes in the TARGET repo: only a writable open of that repo
	// restores its cursor and commits.
	regressEmbeddedSchemaCursor(t, targetBeadsDir, "xtgt")

	// Force the version-bump path in the caller repo too, so this exercises
	// the same post-upgrade window as the single-repo test.
	if err := os.WriteFile(filepath.Join(callerBeadsDir, localVersionFile), []byte("0.9.0\n"), 0o600); err != nil {
		t.Fatalf("write old local version: %v", err)
	}

	before := readEmbeddedStoreSnapshot(t, targetBeadsDir, "xtgt")
	if before.schemaVersion != schema.LatestVersion()-1 {
		t.Fatalf("fixture schema version = %d, want %d", before.schemaVersion, schema.LatestVersion()-1)
	}

	cmd := exec.Command(bd, "create", "--dry-run",
		"--parent", parent.ID, "--repo", targetDir, "Preview only", "--json")
	cmd.Dir = callerDir
	cmd.Env = bdEnv(callerDir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd create --dry-run --parent --repo failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	after := readEmbeddedStoreSnapshot(t, targetBeadsDir, "xtgt")
	if after.schemaVersion != before.schemaVersion {
		t.Errorf("target repo schema version changed during cross-repo dry-run: before=%d after=%d", before.schemaVersion, after.schemaVersion)
	}
	if after.head != before.head {
		t.Errorf("target repo Dolt HEAD changed during cross-repo dry-run: before=%s after=%s", before.head, after.head)
	}
	if after.issueCount != before.issueCount {
		t.Errorf("target repo issue count changed during cross-repo dry-run: before=%d after=%d", before.issueCount, after.issueCount)
	}
}

// TestEmbeddedPreviewDoesNotConsumeVersionMarker is the two-invocation
// regression for the one-shot upgrade signal: a preview run first after an
// upgrade correctly skips the version-bump reconciliation, so it must also
// leave .beads/.local_version alone. Burning the marker there would mean the
// next ordinary command sees a matching version and never reconciles —
// whichever command happened to run first would silently decide whether the
// upgrade was finished.
func TestEmbeddedPreviewDoesNotConsumeVersionMarker(t *testing.T) {
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pvm")
	bdCreate(t, bd, dir, "Existing issue")

	localVersionPath := filepath.Join(beadsDir, localVersionFile)
	if err := os.WriteFile(localVersionPath, []byte("0.9.0\n"), 0o600); err != nil {
		t.Fatalf("write old local version: %v", err)
	}

	// Invocation 1: preview.
	preview := exec.Command(bd, "create", "--dry-run", "Preview only", "--json")
	preview.Dir = dir
	preview.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, preview)
	if err != nil {
		t.Fatalf("bd create --dry-run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	raw, err := os.ReadFile(localVersionPath)
	if err != nil {
		t.Fatalf("read local version after preview: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "0.9.0" {
		t.Fatalf("preview consumed the version marker: .local_version = %q, want %q", got, "0.9.0")
	}

	// Invocation 2: an ordinary command, which must still see the upgrade.
	status := exec.Command(bd, "upgrade", "status", "--json")
	status.Dir = dir
	status.Env = bdEnv(dir)
	stdout, stderr, err = runCommandBuffers(t, status)
	if err != nil {
		t.Fatalf("bd upgrade status failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	var upgradeStatus struct {
		Upgraded        bool   `json:"upgraded"`
		PreviousVersion string `json:"previous_version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &upgradeStatus); err != nil {
		t.Fatalf("parse upgrade status: %v\nstdout:\n%s", err, stdout.String())
	}
	if !upgradeStatus.Upgraded || upgradeStatus.PreviousVersion != "0.9.0" {
		t.Errorf("ordinary command after a preview no longer sees the upgrade: upgraded=%v previous=%q; stdout:\n%s",
			upgradeStatus.Upgraded, upgradeStatus.PreviousVersion, stdout.String())
	}

	raw, err = os.ReadFile(localVersionPath)
	if err != nil {
		t.Fatalf("read local version after ordinary command: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got == "0.9.0" {
		t.Errorf("ordinary command left .local_version at %q; the marker should have been updated", got)
	}
}

func TestEmbeddedChangeDirOverridesInheritedBeadsDir(t *testing.T) {
	bd := buildEmbeddedBD(t)
	callerDir, callerBeadsDir, _ := bdInit(t, bd, "--prefix", "caller")
	targetDir, targetBeadsDir, _ := bdInit(t, bd, "--prefix", "target")

	cmd := exec.Command(bd, "-C", targetDir, "create", "Explicit target", "--json")
	cmd.Dir = callerDir
	cmd.Env = append(bdEnv(callerDir), "BEADS_DIR="+callerBeadsDir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd -C target create failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	countIssues := func(beadsDir, database string) int {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(
			t.Context(),
			filepath.Join(beadsDir, "embeddeddolt"),
			database,
			"main",
		)
		if err != nil {
			t.Fatalf("OpenSQL %s: %v", database, err)
		}
		defer func() {
			if err := cleanup(); err != nil {
				t.Errorf("cleanup OpenSQL %s: %v", database, err)
			}
		}()
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM issues").Scan(&count); err != nil {
			t.Fatalf("count issues in %s: %v", database, err)
		}
		return count
	}

	if got := countIssues(callerBeadsDir, "caller"); got != 0 {
		t.Fatalf("inherited BEADS_DIR received %d issues, want 0", got)
	}
	if got := countIssues(targetBeadsDir, "target"); got != 1 {
		t.Fatalf("-C target received %d issues, want 1", got)
	}
}

// TestEmbeddedCreateCommitPending verifies that CommitPending works on EmbeddedDoltStore:
// no-op when clean, commits when there are pending changes.
func TestEmbeddedCreateCommitPending(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("no_pending_changes", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "cp1")
		store := openStore(t, beadsDir, "cp1")
		committed, err := store.CommitPending(t.Context(), "test")
		if err != nil {
			t.Fatalf("CommitPending: %v", err)
		}
		if committed {
			t.Error("expected no commit on clean store")
		}
	})

	t.Run("with_pending_changes", func(t *testing.T) {
		_, beadsDir, _ := bdInit(t, bd, "--prefix", "cp2")
		store := openStore(t, beadsDir, "cp2")
		ctx := t.Context()

		// Create an issue (writes to working set, no dolt commit in embedded mode)
		issue := &types.Issue{
			Title:     "Pending issue",
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		committed, err := store.CommitPending(ctx, "test")
		if err != nil {
			t.Fatalf("CommitPending: %v", err)
		}
		if !committed {
			t.Error("expected commit with pending changes")
		}

		// Second call should be no-op
		committed2, err := store.CommitPending(ctx, "test")
		if err != nil {
			t.Fatalf("CommitPending (second): %v", err)
		}
		if committed2 {
			t.Error("expected no commit after already committed")
		}
	})
}

func TestEmbeddedCreateFormCommitsLabelOnlyCreate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}

	bd := buildEmbeddedBD(t)
	_, beadsDir, _ := bdInit(t, bd, "--prefix", "cfl")
	store := openStore(t, beadsDir, "cfl")

	issue, err := CreateIssueFromFormValues(t.Context(), store, &createFormValues{
		Title:     "Form labels commit",
		Priority:  2,
		IssueType: "task",
		Labels:    []string{"form", "initial"},
	}, "tester")
	if err != nil {
		t.Fatalf("CreateIssueFromFormValues: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}

	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, "cfl", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()

	var labelCount int
	if err := db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?",
		issue.ID,
	).Scan(&labelCount); err != nil {
		t.Fatalf("count committed labels: %v", err)
	}
	if labelCount != 2 {
		t.Fatalf("committed label count = %d, want 2", labelCount)
	}
}

// TestEmbeddedCreateCrossRepo verifies that bd create --repo routes to a different
// repo's embedded dolt store, creates the issue there, and commits it.
func TestEmbeddedCreateCrossRepo(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Set up primary repo
	dir, _, _ := bdInit(t, bd, "--prefix", "cr")

	// Set up target repo in a subdirectory
	targetDir := filepath.Join(dir, "target-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "tgt")

	// Create issue routed to target repo
	issue := bdCreate(t, bd, dir, "Cross-repo issue", "--repo", targetDir)
	if issue.ID == "" {
		t.Fatal("expected issue ID")
	}

	// Verify issue exists in the TARGET store, not the source
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	tgtStore := openStore(t, targetBeadsDir, "tgt")
	got, err := tgtStore.GetIssue(t.Context(), issue.ID)
	if err != nil {
		t.Fatalf("GetIssue in target: %v", err)
	}
	if got.Title != "Cross-repo issue" {
		t.Errorf("title in target: got %q, want %q", got.Title, "Cross-repo issue")
	}
}

// TestEmbeddedCreateCrossRepoWithParent verifies that --parent works correctly
// when combined with --repo routing (regression test for GH#2736). The old --rig
// flag had a separate code path (createInRig) that silently dropped --parent.
// After the multi-rig refactor (d7629204), --repo uses the same code path as
// local create, so --parent is resolved against the target store.
func TestEmbeddedCreateCrossRepoWithParent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Set up primary repo (where we run from)
	dir, _, _ := bdInit(t, bd, "--prefix", "cr")

	// Set up target repo
	targetDir := filepath.Join(dir, "target-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "tgt")

	// Create parent issue in target repo
	parent := bdCreate(t, bd, dir, "Parent epic", "-t", "epic", "--repo", targetDir)
	if parent.ID == "" {
		t.Fatal("expected parent issue ID")
	}

	// Create child issue with --parent in the same target repo
	child := bdCreate(t, bd, dir, "Child task", "--parent", parent.ID, "--repo", targetDir)
	if child.ID == "" {
		t.Fatal("expected child issue ID")
	}

	// Child ID should be a dotted child of the parent
	if !strings.HasPrefix(child.ID, parent.ID+".") {
		t.Errorf("child ID %q should start with %q.", child.ID, parent.ID+".")
	}

	// Verify parent-child dependency exists in the target store
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	assertDepExists(t, targetBeadsDir, "tgt", child.ID, parent.ID)
}

func TestEmbeddedCreateDryRunRepoDoesNotInitializeTarget(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	dir, _, _ := bdInit(t, bd, "--prefix", "dr")
	targetDir := filepath.Join(dir, "uninit-dry-run-target")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)

	cmd := exec.Command(bd, "create", "--dry-run", "Preview only", "--json", "--repo", targetDir)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd create --dry-run --repo failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if _, err := os.Stat(filepath.Join(targetDir, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("dry-run target .beads stat err = %v, want not exist", err)
	}
}

func TestEmbeddedCreateCrossRepoDryRunWithParent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	dir, _, _ := bdInit(t, bd, "--prefix", "drp")
	targetDir := filepath.Join(dir, "target-repo")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)
	runBDInit(t, bd, targetDir, "--prefix", "tgt")

	parent := bdCreate(t, bd, dir, "Parent epic", "-t", "epic", "-l", "team-a,shared", "--repo", targetDir)
	cmd := exec.Command(bd, "create", "--dry-run", "Preview child", "--json",
		"--parent", parent.ID, "-l", "child,shared", "--repo", targetDir)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd create --dry-run --repo --parent failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	preview := parseIssueJSON(t, stdout.Bytes())
	labelMap := make(map[string]bool)
	for _, label := range preview.Labels {
		labelMap[label] = true
	}
	for _, want := range []string{"team-a", "shared", "child"} {
		if !labelMap[want] {
			t.Fatalf("dry-run labels = %v, want %q", preview.Labels, want)
		}
	}

	child := bdCreate(t, bd, dir, "Real child after dry-run", "--parent", parent.ID, "--repo", targetDir)
	if child.ID != parent.ID+".1" {
		t.Fatalf("child ID after dry-run = %q, want %q", child.ID, parent.ID+".1")
	}
}

// TestEmbeddedCreateCrossRepoUninit verifies that bd create --repo works when
// the target directory has NOT been initialized with bd init. This is a
// regression test for be-sy8 / GH#2988: newDoltStoreFromConfig used to pass
// an empty database name to the embedded Dolt engine, causing "no database
// selected" during schema init.
func TestEmbeddedCreateCrossRepoUninit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Set up primary repo (source — initialized)
	dir, _, _ := bdInit(t, bd, "--prefix", "src")

	// Set up target repo WITHOUT bd init — just a bare git repo
	targetDir := filepath.Join(dir, "uninit-target")
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, targetDir)

	// This should succeed: ensureBeadsDirForPath creates .beads,
	// and newDoltStoreFromConfig defaults to database "beads".
	issue := bdCreate(t, bd, dir, "Issue in uninit target", "--repo", targetDir)
	if issue.ID == "" {
		t.Fatal("expected issue ID")
	}

	// Verify issue exists in the target store
	targetBeadsDir := filepath.Join(targetDir, ".beads")
	tgtStore, err := newDoltStoreFromConfig(t.Context(), targetBeadsDir)
	if err != nil {
		t.Fatalf("failed to open target store: %v", err)
	}
	defer tgtStore.Close()

	got, err := tgtStore.GetIssue(t.Context(), issue.ID)
	if err != nil {
		t.Fatalf("GetIssue in target: %v", err)
	}
	if got.Title != "Issue in uninit target" {
		t.Errorf("title: got %q, want %q", got.Title, "Issue in uninit target")
	}
}

// TestEmbeddedCreateRepoRelativeUninitRefused is a regression test for
// bd-8d3f: a bare/relative --repo value with no existing workspace resolves
// silently against the current working directory (routing.ExpandPath has no
// concept of an external rig/alias registry), so it must be refused instead
// of auto-creating a disconnected database that the caller never intended
// and nothing else will ever route to. Contrast with
// TestEmbeddedCreateCrossRepoUninit, which uses an absolute --repo path and
// must still succeed.
func TestEmbeddedCreateRepoRelativeUninitRefused(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "src")

	relTarget := "some-other-rig"
	out, err := bdRunWithFlockRetry(t, bd, dir, "create", "--json", "Should not land anywhere", "--repo", relTarget)
	if err == nil {
		t.Fatalf("expected bd create --repo %q to fail for an uninitialized relative target, got success: %s", relTarget, out)
	}
	if !strings.Contains(string(out), "absolute") {
		t.Errorf("expected error to explain the absolute/~-prefixed path requirement, got: %s", out)
	}

	if _, statErr := os.Stat(filepath.Join(dir, relTarget, ".beads")); !os.IsNotExist(statErr) {
		t.Errorf("expected no .beads directory to be fabricated at %s, stat err = %v", filepath.Join(dir, relTarget), statErr)
	}
}

// TestEmbeddedCreateRepoRelativeExistingWorkspaceUnaffected verifies the
// bd-8d3f gate only blocks *fabricating* a new workspace: a relative --repo
// value that already has an initialized workspace at the resolved path must
// keep working exactly as before, since ensureBeadsDirForPath's existing
// os.Stat(metadata.json) check returns early before the new gate runs.
func TestEmbeddedCreateRepoRelativeExistingWorkspaceUnaffected(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "src")

	relTarget := "already-initialized-sibling"
	absTarget := filepath.Join(dir, relTarget)
	if err := os.MkdirAll(absTarget, 0750); err != nil {
		t.Fatal(err)
	}
	initGitRepoAt(t, absTarget)

	// Initialize the target workspace first via an absolute --repo path
	// (the already-covered, unambiguous case).
	first := bdCreate(t, bd, dir, "Seed issue", "--repo", absTarget)
	if first.ID == "" {
		t.Fatal("expected issue ID for absolute --repo seed create")
	}

	// A relative --repo pointing at that SAME, now-initialized workspace
	// must succeed unaffected by the bd-8d3f gate: the gate only blocks
	// fabricating a workspace that doesn't exist yet.
	second := bdCreate(t, bd, dir, "Should land in the pre-existing workspace", "--repo", relTarget)
	if second.ID == "" {
		t.Fatal("expected issue ID for relative --repo pointing at an existing workspace")
	}
}

// TestEmbeddedCreateWithGitRemote verifies bd create works end-to-end when a
// git remote exists (which enables auto-backup in PersistentPostRun). This
// catches panics from unimplemented methods called after the create succeeds.
func TestEmbeddedCreateWithGitRemote(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gr")

	// Add a fake git remote so isBackupAutoEnabled returns true
	cmd := exec.Command("git", "remote", "add", "origin", "https://example.com/fake.git")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add failed: %v\n%s", err, out)
	}

	// bd create should succeed without panicking in PersistentPostRun
	issue := bdCreate(t, bd, dir, "Issue with git remote")
	if issue.ID == "" {
		t.Fatal("expected issue ID")
	}
}

// TestEmbeddedCreateConcurrent verifies one contended create from each of six
// CLI processes, then proves every requested issue was durably created.
func TestEmbeddedCreateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cc")

	// Six first attempts plus at most one serial retry per lock loser keeps the
	// create-process budget at twelve.
	const numWorkers = 6

	type result struct {
		title string
		out   string
		err   error
	}

	results := make([]result, numWorkers)
	ready := make(chan struct{}, numWorkers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			title := fmt.Sprintf("concurrent-create-%d", worker)
			ready <- struct{}{}
			<-start

			cmd := exec.Command(bd, "create", "--silent", title)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			results[worker] = result{title: title, out: string(out), err: err}
		}(w)
	}
	for range numWorkers {
		<-ready
	}
	close(start)
	wg.Wait()

	expected := make(map[string]string, numWorkers)
	lockLosers := make([]result, 0, numWorkers)
	immediateSuccesses := 0
	recordSuccess := func(title, out string) {
		t.Helper()
		id := strings.TrimSpace(out)
		if id == "" {
			t.Fatalf("create %q succeeded without an issue ID", title)
		}
		if previousTitle, exists := expected[id]; exists {
			t.Fatalf("create %q returned duplicate ID %q already returned for %q", title, id, previousTitle)
		}
		expected[id] = title
	}

	for _, r := range results {
		if r.err == nil {
			recordSuccess(r.title, r.out)
			immediateSuccesses++
			continue
		}
		if strings.Contains(r.out, "panic") {
			t.Fatalf("first create %q panicked:\n%s", r.title, r.out)
		}
		if isEmbeddedLockOutput(r.out) {
			lockLosers = append(lockLosers, r)
			continue
		}
		t.Fatalf("first create %q failed unexpectedly: %v\n%s", r.title, r.err, r.out)
	}
	if immediateSuccesses == 0 {
		t.Fatal("expected at least one immediate create success")
	}

	// Retry only the lock losers, once each, after concurrent contention ends.
	for _, r := range lockLosers {
		cmd := exec.Command(bd, "create", "--silent", r.title)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("retry create %q failed: %v\n%s", r.title, err, out)
		}
		recordSuccess(r.title, string(out))
	}

	if len(expected) != numWorkers {
		t.Fatalf("got %d unique issue IDs, want %d", len(expected), numWorkers)
	}

	// Read the durable store directly: a CLI list would duplicate a read path
	// rather than prove an additional risk here.
	store := openStore(t, beadsDir, "cc")
	issues, err := store.SearchIssues(t.Context(), "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues durable readback: %v", err)
	}
	got := make(map[string]string, len(issues))
	for _, issue := range issues {
		if previousTitle, exists := got[issue.ID]; exists {
			t.Fatalf("durable readback contains duplicate ID %q for %q and %q", issue.ID, previousTitle, issue.Title)
		}
		got[issue.ID] = issue.Title
	}
	if len(got) != len(expected) {
		t.Fatalf("durable issue count = %d, want exactly %d; got ID-title pairs %#v", len(got), len(expected), got)
	}
	for id, wantTitle := range expected {
		if gotTitle, exists := got[id]; !exists || gotTitle != wantTitle {
			t.Errorf("durable issue %q title = %q, want %q", id, gotTitle, wantTitle)
		}
	}
}
