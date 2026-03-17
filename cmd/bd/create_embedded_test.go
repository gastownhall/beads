//go:build embeddeddolt

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
// Returns the parsed issue JSON. Fatals on failure.
func bdCreate(t *testing.T, bd, dir string, args ...string) *types.Issue {
	t.Helper()
	fullArgs := append([]string{"create", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
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
func bdCreateSilent(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"create", "--silent"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
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

// bdShow runs "bd show <id> --json" and returns the parsed issue.
func bdShow(t *testing.T, bd, dir, id string) *types.Issue {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\n%s", id, err, out)
	}
	return parseIssueJSON(t, out)
}

// openStore opens an EmbeddedDoltStore for direct verification queries.
func openStore(t *testing.T, beadsDir, database string) *embeddeddolt.EmbeddedDoltStore {
	t.Helper()
	store, err := embeddeddolt.New(t.Context(), beadsDir, database, "main")
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
		"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_id = ?",
		issueID, dependsOnID).Scan(&count)
	if err != nil {
		t.Fatalf("query dependencies: %v", err)
	}
	if count == 0 {
		t.Errorf("expected dependency %s -> %s, not found", issueID, dependsOnID)
	}
}

func TestEmbeddedCreate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}

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
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd create --dry-run failed: %v\n%s", err, out)
		}

		// Dry run should not persist anything. Create a real issue and verify
		// the dry-run issue doesn't exist.
		if strings.Contains(string(out), "error") {
			t.Errorf("dry-run produced error output: %s", out)
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
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd create -f failed: %v\n%s", err, out)
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

// TestEmbeddedCreateConcurrent verifies that 20 concurrent bd create processes
// can each create 10 issues without data loss or corruption.
func TestEmbeddedCreateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}

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
				cmd := exec.Command(bd, "create", "--silent", title)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
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
			t.Errorf("worker %d failed: %v", r.worker, r.err)
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

	if failures > 0 {
		t.Fatalf("%d/%d workers failed", failures, numWorkers)
	}

	expectedTotal := numWorkers * issuesPerWorker
	if len(allIDs) != expectedTotal {
		t.Errorf("expected %d unique IDs, got %d", expectedTotal, len(allIDs))
	}

	// Verify all issues exist in the database
	store := openStore(t, beadsDir, "cc")
	stats, err := store.GetStatistics(t.Context())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if stats.TotalIssues < expectedTotal {
		t.Errorf("expected at least %d issues in DB, got %d", expectedTotal, stats.TotalIssues)
	}

	t.Logf("created %d issues across %d concurrent workers, %d in DB", len(allIDs), numWorkers, stats.TotalIssues)
}
