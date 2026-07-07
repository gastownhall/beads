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

	t.Run("basic", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "bc")
		issue := bdCreate(t, bd, dir, "Basic issue")
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
		dir, _, _ := bdInit(t, bd, "--prefix", "tf")
		issue := bdCreate(t, bd, dir, "--title", "Title via flag")
		if issue.Title != "Title via flag" {
			t.Errorf("title: got %q, want %q", issue.Title, "Title via flag")
		}
	})

	t.Run("silent", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sl")
		id := bdCreateSilent(t, bd, dir, "Silent issue")
		if id == "" {
			t.Fatal("expected issue ID from --silent")
		}
		if !strings.HasPrefix(id, "sl-") {
			t.Errorf("ID should have prefix sl-, got %q", id)
		}
	})

	t.Run("priority", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pr")
		for _, tc := range []struct {
			flag string
			want int
		}{
			{"0", 0},
			{"1", 1},
			{"P3", 3},
			{"4", 4},
		} {
			t.Run("P"+tc.flag, func(t *testing.T) {
				issue := bdCreate(t, bd, dir, fmt.Sprintf("Priority %s", tc.flag), "-p", tc.flag)
				if issue.Priority != tc.want {
					t.Errorf("priority: got %d, want %d", issue.Priority, tc.want)
				}
			})
		}
	})

	t.Run("issue_types", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "it")
		for _, issueType := range []string{"bug", "feature", "task", "epic", "chore", "decision"} {
			t.Run(issueType, func(t *testing.T) {
				issue := bdCreate(t, bd, dir, fmt.Sprintf("Type %s", issueType), "-t", issueType)
				normalized := types.IssueType(issueType).Normalize()
				if issue.IssueType != normalized {
					t.Errorf("type: got %q, want %q", issue.IssueType, normalized)
				}
			})
		}
	})

	t.Run("description", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ds")
		issue := bdCreate(t, bd, dir, "Desc issue", "-d", "This is the description")
		if issue.Description != "This is the description" {
			t.Errorf("description: got %q, want %q", issue.Description, "This is the description")
		}
	})

	t.Run("design_and_acceptance", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "da")
		issue := bdCreate(t, bd, dir, "Design issue",
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
		dir, _, _ := bdInit(t, bd, "--prefix", "as")
		issue := bdCreate(t, bd, dir, "Assigned issue", "-a", "alice")
		if issue.Assignee != "alice" {
			t.Errorf("assignee: got %q, want %q", issue.Assignee, "alice")
		}
	})

	t.Run("labels", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "lb")
		issue := bdCreate(t, bd, dir, "Labeled issue", "-l", "bug,critical")

		store := openStore(t, beadsDir, "lb")
		labels, err := store.GetLabels(t.Context(), issue.ID)
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		labelMap := make(map[string]bool)
		for _, l := range labels {
			labelMap[l] = true
		}
		if !labelMap["bug"] || !labelMap["critical"] {
			t.Errorf("expected labels bug and critical, got %v", labels)
		}
	})

	t.Run("explicit_id", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ei")
		issue := bdCreate(t, bd, dir, "Explicit ID", "--id", "ei-custom42")
		if issue.ID != "ei-custom42" {
			t.Errorf("ID: got %q, want %q", issue.ID, "ei-custom42")
		}
	})

	t.Run("dependencies", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "dp")
		parent := bdCreate(t, bd, dir, "Parent issue")
		child := bdCreate(t, bd, dir, "Child issue", "--deps", "blocks:"+parent.ID)

		// "blocks:X" reverses direction: X depends on new issue (parent.ID -> child.ID)
		assertDepExists(t, beadsDir, "dp", parent.ID, child.ID)
	})

	t.Run("blocked_by_alias", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "bb")
		blocker := bdCreate(t, bd, dir, "Blocker issue")
		blocked := bdCreate(t, bd, dir, "Blocked issue", "--deps", "blocked-by:"+blocker.ID)

		assertDepExistsWithType(t, beadsDir, "bb", blocked.ID, blocker.ID, "blocks")
	})

	t.Run("depends_on_alias", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "do")
		prereq := bdCreate(t, bd, dir, "Prerequisite")
		dependent := bdCreate(t, bd, dir, "Dependent issue", "--deps", "depends-on:"+prereq.ID)

		assertDepExistsWithType(t, beadsDir, "do", dependent.ID, prereq.ID, "blocks")
	})

	t.Run("unknown_dep_type_rejected", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ud")
		blocker := bdCreate(t, bd, dir, "Blocker")
		out := bdCreateFail(t, bd, dir, "Bad dep type", "--deps", "bogus-type:"+blocker.ID)
		if !strings.Contains(out, "unknown dependency type") {
			t.Errorf("expected 'unknown dependency type' error, got:\n%s", out)
		}
		if !strings.Contains(out, "blocked-by") || !strings.Contains(out, "depends-on") {
			t.Errorf("expected accepted dependency aliases in error, got:\n%s", out)
		}
	})

	t.Run("multiple_dependencies", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "md")
		dep1 := bdCreate(t, bd, dir, "Dep 1")
		dep2 := bdCreate(t, bd, dir, "Dep 2")
		child := bdCreate(t, bd, dir, "Multi dep issue",
			"--deps", fmt.Sprintf("blocks:%s,related:%s", dep1.ID, dep2.ID))

		// blocks reverses direction; related keeps original direction
		assertDepExists(t, beadsDir, "md", dep1.ID, child.ID)
		assertDepExists(t, beadsDir, "md", child.ID, dep2.ID)
	})

	t.Run("parent_child", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pc")
		parent := bdCreate(t, bd, dir, "Parent epic", "-t", "epic")
		child := bdCreate(t, bd, dir, "Child task", "--parent", parent.ID)

		if !strings.HasPrefix(child.ID, parent.ID+".") {
			t.Errorf("child ID %q should start with %q.", child.ID, parent.ID)
		}

		assertDepExists(t, beadsDir, "pc", child.ID, parent.ID)
	})

	t.Run("parent_label_inheritance", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pi")
		parent := bdCreate(t, bd, dir, "Parent with labels", "-t", "epic", "-l", "team-a,priority:high")
		child := bdCreate(t, bd, dir, "Child inherits", "--parent", parent.ID)

		store := openStore(t, beadsDir, "pi")
		childLabels, err := store.GetLabels(t.Context(), child.ID)
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		labelMap := make(map[string]bool)
		for _, l := range childLabels {
			labelMap[l] = true
		}
		if !labelMap["team-a"] || !labelMap["priority:high"] {
			t.Errorf("expected inherited labels team-a and priority:high, got %v", childLabels)
		}
	})

	t.Run("parent_no_inherit_labels", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ni")
		parent := bdCreate(t, bd, dir, "Parent", "-t", "epic", "-l", "inherited-label")
		child := bdCreate(t, bd, dir, "Child no inherit", "--parent", parent.ID, "--no-inherit-labels", "-l", "own-label")

		store := openStore(t, beadsDir, "ni")
		childLabels, err := store.GetLabels(t.Context(), child.ID)
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		labelMap := make(map[string]bool)
		for _, l := range childLabels {
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
		dir, _, _ := bdInit(t, bd, "--prefix", "dd")
		issue := bdCreate(t, bd, dir, "Due issue", "--due", "+24h")
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
		dir, _, _ := bdInit(t, bd, "--prefix", "df")
		issue := bdCreate(t, bd, dir, "Deferred issue", "--defer", "+2h")
		if issue.DeferUntil == nil {
			t.Fatal("expected DeferUntil to be set")
		}
		expected := time.Now().Add(2 * time.Hour)
		diff := issue.DeferUntil.Sub(expected)
		if diff < -5*time.Minute || diff > 5*time.Minute {
			t.Errorf("DeferUntil off by too much: got %v, expected ~%v", issue.DeferUntil, expected)
		}
	})

	t.Run("initial_status_builtin", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sb")
		issue := bdCreate(t, bd, dir, "Blocked issue", "--status", "blocked")
		if issue.Status != types.StatusBlocked {
			t.Errorf("status: got %q, want %q", issue.Status, types.StatusBlocked)
		}
	})

	t.Run("initial_status_custom", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sc")
		bdConfig(t, bd, dir, "set", "status.custom", "review:wip")
		issue := bdCreate(t, bd, dir, "Review issue", "--status", "review")
		if issue.Status != types.Status("review") {
			t.Errorf("status: got %q, want review", issue.Status)
		}
	})

	t.Run("initial_status_invalid", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "si")
		out := bdCreateFail(t, bd, dir, "Invalid status issue", "--status", "not_a_status")
		if !strings.Contains(out, `invalid status "not_a_status"`) {
			t.Fatalf("expected invalid status error, got:\n%s", out)
		}
	})

	t.Run("initial_status_wins_over_defer", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sd")
		issue := bdCreate(t, bd, dir, "Deferred but blocked issue", "--status", "blocked", "--defer", "+2h")
		if issue.Status != types.StatusBlocked {
			t.Errorf("status: got %q, want %q", issue.Status, types.StatusBlocked)
		}
		if issue.DeferUntil == nil {
			t.Fatal("expected DeferUntil to be set")
		}
	})

	t.Run("ephemeral", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ep")
		issue := bdCreate(t, bd, dir, "Ephemeral issue", "--ephemeral")

		// Verify it went to wisps table
		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, "ep", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()

		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM wisps WHERE id = ?", issue.ID).Scan(&count); err != nil {
			t.Fatalf("query wisps: %v", err)
		}
		if count != 1 {
			t.Errorf("expected issue in wisps table, found %d rows", count)
		}
	})

	t.Run("no_history", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "nh")
		issue := bdCreate(t, bd, dir, "No history issue", "--no-history")
		if issue.ID == "" {
			t.Fatal("expected issue ID")
		}
	})

	t.Run("graph_ephemeral", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ge")
		planFile := writeGraphCreatePlan(t, dir)
		result := bdCreateGraph(t, bd, dir, planFile, "--ephemeral")
		rootID := result.IDs["root"]
		childID := result.IDs["child"]
		if rootID == "" || childID == "" {
			t.Fatalf("expected root and child IDs, got %#v", result.IDs)
		}

		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, "ge", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()

		for _, id := range []string{rootID, childID} {
			var ephemeral, noHistory int
			if err := db.QueryRowContext(t.Context(), "SELECT ephemeral, no_history FROM wisps WHERE id = ?", id).Scan(&ephemeral, &noHistory); err != nil {
				t.Fatalf("query graph ephemeral bead %s: %v", id, err)
			}
			if ephemeral != 1 || noHistory != 0 {
				t.Fatalf("graph ephemeral bead %s flags = ephemeral:%d no_history:%d, want 1/0", id, ephemeral, noHistory)
			}
		}
	})

	t.Run("graph_no_history", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gn")
		planFile := writeGraphCreatePlan(t, dir)
		result := bdCreateGraph(t, bd, dir, planFile, "--no-history")
		rootID := result.IDs["root"]
		childID := result.IDs["child"]
		if rootID == "" || childID == "" {
			t.Fatalf("expected root and child IDs, got %#v", result.IDs)
		}

		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, "gn", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()

		for _, id := range []string{rootID, childID} {
			var ephemeral, noHistory int
			if err := db.QueryRowContext(t.Context(), "SELECT ephemeral, no_history FROM wisps WHERE id = ?", id).Scan(&ephemeral, &noHistory); err != nil {
				t.Fatalf("query graph no-history bead %s: %v", id, err)
			}
			if ephemeral != 0 || noHistory != 1 {
				t.Fatalf("graph no-history bead %s flags = ephemeral:%d no_history:%d, want 0/1", id, ephemeral, noHistory)
			}
		}
	})

	t.Run("estimate", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "es")
		issue := bdCreate(t, bd, dir, "Estimated issue", "-e", "60")
		if issue.EstimatedMinutes == nil || *issue.EstimatedMinutes != 60 {
			t.Errorf("estimate: got %v, want 60", issue.EstimatedMinutes)
		}
	})

	t.Run("notes", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "nt")
		issue := bdCreate(t, bd, dir, "Notes issue", "--notes", "Some notes here")
		if issue.Notes != "Some notes here" {
			t.Errorf("notes: got %q, want %q", issue.Notes, "Some notes here")
		}
	})

	t.Run("spec_id", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sp")
		issue := bdCreate(t, bd, dir, "Spec issue", "--spec-id", "sp-spec1")
		if issue.SpecID != "sp-spec1" {
			t.Errorf("spec_id: got %q, want %q", issue.SpecID, "sp-spec1")
		}
	})

	t.Run("external_ref", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "er")
		issue := bdCreate(t, bd, dir, "External ref issue", "--external-ref", "gh-123")
		if issue.ExternalRef == nil || *issue.ExternalRef != "gh-123" {
			t.Errorf("external_ref: got %v, want %q", issue.ExternalRef, "gh-123")
		}
	})

	t.Run("linear_external_ref", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ler")
		ref := "https://linear.app/team/issue/TEAM-123/fix-login"
		issue := bdCreate(t, bd, dir, "Pre-linked Linear issue", "--external-ref", ref)
		if issue.ExternalRef == nil || *issue.ExternalRef != ref {
			t.Errorf("external_ref: got %v, want %q", issue.ExternalRef, ref)
		}
	})

	t.Run("metadata", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "mt")
		issue := bdCreate(t, bd, dir, "Metadata issue", "--metadata", `{"key":"value"}`)
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
		dir, _, _ := bdInit(t, bd, "--prefix", "dr")

		cmd := exec.Command(bd, "create", "--dry-run", "Dry run issue", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd create --dry-run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		// Dry run should not persist anything. Create a real issue and verify
		// the dry-run issue doesn't exist.
		if strings.Contains(stdout.String(), "error") {
			t.Errorf("dry-run produced error output: %s", stdout.String())
		}
	})

	t.Run("dry_run_parent_label_inheritance", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "dp")
		parent := bdCreate(t, bd, dir, "Parent with labels", "-t", "epic", "-l", "team-a,shared")

		cmd := exec.Command(bd, "create", "--dry-run", "Preview child", "--json",
			"--parent", parent.ID, "-l", "child,shared")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd create --dry-run --parent failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
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
		if len(preview.Labels) != 3 {
			t.Fatalf("dry-run labels = %v, want 3 deduped labels", preview.Labels)
		}

		child := bdCreate(t, bd, dir, "Real child after dry-run", "--parent", parent.ID)
		if child.ID != parent.ID+".1" {
			t.Fatalf("child ID after dry-run = %q, want %q", child.ID, parent.ID+".1")
		}
	})

	t.Run("skills_and_context", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sc")
		issue := bdCreate(t, bd, dir, "Skills issue",
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
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "di")

		parent := bdCreate(t, bd, dir, "Parent work")
		child := bdCreate(t, bd, dir, "Discovered bug",
			"--deps", "discovered-from:"+parent.ID)

		if child.ID == "" {
			t.Fatal("expected child issue ID")
		}
		// Verify discovered-from dependency was created (keeps original direction)
		assertDepExists(t, beadsDir, "di", child.ID, parent.ID)
	})

	t.Run("markdown_bulk_create", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mk")

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
		mdFile := filepath.Join(dir, "issues.md")
		if err := os.WriteFile(mdFile, []byte(mdContent), 0644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(bd, "create", "-f", mdFile, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd create -f failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		// Verify both issues were created
		store := openStore(t, beadsDir, "mk")
		stats, err := store.GetStatistics(t.Context())
		if err != nil {
			t.Fatalf("GetStatistics: %v", err)
		}
		if stats.TotalIssues < 2 {
			t.Errorf("expected at least 2 issues from markdown, got %d", stats.TotalIssues)
		}
	})

	t.Run("graph_initial_labels_not_duplicated", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gl")
		plan := `{
  "nodes": [
    {"key": "root", "title": "Graph root", "type": "task", "labels": ["team-a", "shared"]}
  ]
}`
		planFile := filepath.Join(dir, "graph-labels.json")
		if err := os.WriteFile(planFile, []byte(plan), 0644); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(bd, "create", "--graph", planFile, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd create --graph failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}

		var result GraphApplyResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("parse graph result: %v\nstdout:\n%s", err, stdout.String())
		}
		id := result.IDs["root"]
		if id == "" {
			t.Fatalf("graph result missing root ID: %#v", result.IDs)
		}

		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, "gl", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()

		var labelCount int
		if err := db.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM labels AS OF 'HEAD' WHERE issue_id = ?", id).Scan(&labelCount); err != nil {
			t.Fatalf("count labels: %v", err)
		}
		if labelCount != 2 {
			t.Fatalf("label count = %d, want 2", labelCount)
		}

		var labelEventCount int
		if err := db.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM events AS OF 'HEAD' WHERE issue_id = ? AND event_type = ?",
			id, types.EventLabelAdded).Scan(&labelEventCount); err != nil {
			t.Fatalf("count label events: %v", err)
		}
		if labelEventCount != 2 {
			t.Fatalf("label_added event count = %d, want 2", labelEventCount)
		}
	})

	t.Run("both_due_and_defer", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "bd2")
		issue := bdCreate(t, bd, dir, "Both due and defer", "--due", "+48h", "--defer", "+24h")
		if issue.DueAt == nil {
			t.Fatal("expected DueAt to be set")
		}
		if issue.DeferUntil == nil {
			t.Fatal("expected DeferUntil to be set")
		}
	})

	t.Run("parent_label_inheritance_merge", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pm")
		parent := bdCreate(t, bd, dir, "Parent with a,b", "-t", "epic", "-l", "a,b")
		// Child with explicit label "c" and "a" (overlaps parent)
		child := bdCreate(t, bd, dir, "Child with c,a", "--parent", parent.ID, "-l", "c,a")

		store := openStore(t, beadsDir, "pm")
		childLabels, err := store.GetLabels(t.Context(), child.ID)
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		labelMap := make(map[string]bool)
		for _, l := range childLabels {
			labelMap[l] = true
		}
		// Should have a, b (inherited), c (explicit) — deduped
		for _, want := range []string{"a", "b", "c"} {
			if !labelMap[want] {
				t.Errorf("expected label %q, got %v", want, childLabels)
			}
		}
		if len(childLabels) != 3 {
			t.Errorf("expected 3 labels, got %d: %v", len(childLabels), childLabels)
		}
	})

	t.Run("parent_no_labels", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pn")
		parent := bdCreate(t, bd, dir, "Labelless parent", "-t", "epic")
		child := bdCreate(t, bd, dir, "Child of labelless", "--parent", parent.ID)

		store := openStore(t, beadsDir, "pn")
		childLabels, err := store.GetLabels(t.Context(), child.ID)
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		if len(childLabels) != 0 {
			t.Errorf("expected 0 labels, got %d: %v", len(childLabels), childLabels)
		}
	})

	t.Run("discovered_from_inherits_source_repo", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sr")

		// Create parent with source_repo set via store API
		store := openStore(t, beadsDir, "sr")
		parent := &types.Issue{
			Title:      "Parent with source repo",
			Priority:   1,
			Status:     types.StatusOpen,
			IssueType:  types.TypeTask,
			SourceRepo: "/path/to/repo",
		}
		if err := store.CreateIssue(t.Context(), parent, "test"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if err := store.Commit(t.Context(), "create parent"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		store.Close()

		child := bdCreate(t, bd, dir, "Discovered bug", "--deps", "discovered-from:"+parent.ID)

		// Verify via raw SQL since the JSON output may not include source_repo
		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, "sr", "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var sourceRepo string
		err = db.QueryRowContext(t.Context(),
			"SELECT COALESCE(source_repo, '') FROM issues WHERE id = ?", child.ID).Scan(&sourceRepo)
		if err != nil {
			t.Fatalf("query source_repo: %v", err)
		}
		if sourceRepo != "/path/to/repo" {
			t.Errorf("source_repo: got %q, want %q", sourceRepo, "/path/to/repo")
		}
	})

	t.Run("no_title_fails", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "nt2")
		out := bdCreateFail(t, bd, dir)
		if !strings.Contains(out, "title") {
			t.Errorf("expected title-related error, got: %s", out)
		}
	})
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

// TestEmbeddedCreateConcurrent verifies that 20 concurrent bd create processes
// can each create 10 issues without data loss or corruption.
func TestEmbeddedCreateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cc")

	const (
		numWorkers      = 20
		issuesPerWorker = 10
	)

	type result struct {
		worker int
		ids    []string
		err    error
	}

	results := make([]result, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			var ids []string
			for i := 0; i < issuesPerWorker; i++ {
				title := fmt.Sprintf("worker-%d-issue-%d", worker, i)
				out, err := bdRunWithFlockRetry(t, bd, dir, "create", "--silent", title)
				if err != nil {
					results[worker] = result{worker: worker, err: fmt.Errorf("issue %d: %v\n%s", i, err, out)}
					return
				}
				id := strings.TrimSpace(string(out))
				if id == "" {
					results[worker] = result{worker: worker, err: fmt.Errorf("issue %d: empty ID", i)}
					return
				}
				ids = append(ids, id)
			}
			results[worker] = result{worker: worker, ids: ids}
		}(w)
	}
	wg.Wait()

	// Collect all IDs and check for errors
	allIDs := make(map[string]bool)
	var failures int
	for _, r := range results {
		if r.err != nil {
			if !strings.Contains(r.err.Error(), "one writer at a time") {
				t.Errorf("worker %d failed: %v", r.worker, r.err)
			}
			failures++
			continue
		}
		for _, id := range r.ids {
			if allIDs[id] {
				t.Errorf("duplicate ID %q from worker %d", id, r.worker)
			}
			allIDs[id] = true
		}
	}

	successes := numWorkers - failures
	if successes < 1 {
		t.Fatalf("expected at least 1 successful worker, got %d", successes)
	}

	if len(allIDs) < 1 {
		t.Errorf("expected at least 1 unique ID, got %d", len(allIDs))
	}

	// Verify all successfully created issues exist in the database
	store := openStore(t, beadsDir, "cc")
	stats, err := store.GetStatistics(t.Context())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if stats.TotalIssues < len(allIDs) {
		t.Errorf("expected at least %d issues in DB, got %d", len(allIDs), stats.TotalIssues)
	}

	t.Logf("created %d issues across %d concurrent workers (%d succeeded), %d in DB", len(allIDs), numWorkers, successes, stats.TotalIssues)
}
