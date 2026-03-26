package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// bdList runs "bd list" with the given flags and returns stdout.
func bdList(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"list"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd list %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdListJSON runs "bd list --json" and parses the result as an array of IssueWithCounts.
func bdListJSON(t *testing.T, bd, dir string, args ...string) []*types.IssueWithCounts {
	t.Helper()
	fullArgs := append([]string{"list", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd list --json %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	// Find the JSON array in the output (skip any non-JSON preamble)
	s := string(out)
	start := strings.Index(s, "[")
	if start < 0 {
		// Empty list returns "[]" or possibly "null"
		if strings.Contains(s, "null") || strings.TrimSpace(s) == "" {
			return nil
		}
		t.Fatalf("no JSON array found in output:\n%s", s)
	}
	var issues []*types.IssueWithCounts
	if err := json.Unmarshal([]byte(s[start:]), &issues); err != nil {
		t.Fatalf("failed to parse JSON list output: %v\nraw: %s", err, s[start:])
	}
	return issues
}

// bdListFail runs "bd list" expecting failure.
func bdListFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"list"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd list %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// issueIDs extracts issue IDs from a list result.
func listIssueIDs(issues []*types.IssueWithCounts) []string {
	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	return ids
}

// containsID checks if an ID is in the list.
func containsID(issues []*types.IssueWithCounts, id string) bool {
	for _, issue := range issues {
		if issue.ID == id {
			return true
		}
	}
	return false
}

// testSeedData holds IDs created during test setup.
// All issues are created via `bd create` only — no `bd update` or `bd close`
// since those are not yet implemented on EmbeddedDoltStore.
type testSeedData struct {
	openBug       string // P0, alice, labels: backend,urgent, has description
	feature       string // P1, bob, labels: frontend
	task          string // P2, alice, labels: backend
	chore         string // P3, no assignee, defer_until set
	epic          string // P1, labels: planning (has blocking dep on openBug)
	decision      string // P4, labels: pinned-ref
	childTaskA    string // P2, bob, labels: backend, parent=epic
	childTaskB    string // P3, labels: frontend, parent=epic
	noDescBug     string // P1, empty description
	overdueTask   string // P1, alice, due_at in past, labels: urgent
	metadataIssue string // P1, metadata: env=prod
	readyTask     string // P0, labels: backend, no blockers
}

func TestEmbeddedList(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tl")

	// Seed test data
	seed := seedTestData(t, bd, dir)

	// --- A. Basic filtering ---

	t.Run("status_open", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--status", "open")
		for _, issue := range issues {
			if issue.Status != types.StatusOpen {
				t.Errorf("expected status open, got %s for %s", issue.Status, issue.ID)
			}
		}
	})

	t.Run("all", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--all")
		if !containsID(issues, seed.openBug) {
			t.Error("--all should include open bug")
		}
		if !containsID(issues, seed.decision) {
			t.Error("--all should include decision")
		}
	})

	t.Run("assignee", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--assignee", "alice", "--all")
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
		issues := bdListJSON(t, bd, dir, "--type", "bug")
		for _, issue := range issues {
			if issue.IssueType != types.TypeBug {
				t.Errorf("expected type bug, got %s for %s", issue.IssueType, issue.ID)
			}
		}
	})

	t.Run("priority", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--priority", "0")
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
		issues := bdListJSON(t, bd, dir, "--limit", "2")
		if len(issues) != 2 {
			t.Errorf("expected 2 issues with --limit 2, got %d", len(issues))
		}
	})

	t.Run("id_filter", func(t *testing.T) {
		idList := seed.openBug + "," + seed.readyTask
		issues := bdListJSON(t, bd, dir, "--id", idList)
		if len(issues) != 2 {
			t.Errorf("expected 2 issues with --id filter, got %d", len(issues))
		}
	})

	// --- B. Label filtering ---

	t.Run("label_and", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--label", "backend", "--label", "urgent")
		// Only openBug has both backend AND urgent
		if len(issues) != 1 || issues[0].ID != seed.openBug {
			t.Errorf("expected only open bug with backend+urgent labels, got %v", listIssueIDs(issues))
		}
	})

	t.Run("label_any", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--label-any", "backend,frontend")
		if len(issues) < 2 {
			t.Errorf("expected multiple issues with --label-any backend,frontend, got %d", len(issues))
		}
	})

	t.Run("no_labels", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--no-labels")
		for _, issue := range issues {
			if len(issue.Labels) > 0 {
				t.Errorf("expected no labels for %s, got %v", issue.ID, issue.Labels)
			}
		}
	})

	t.Run("label_pattern", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--label-pattern", "back*")
		if !containsID(issues, seed.openBug) {
			t.Error("openBug with label 'backend' should match pattern 'back*'")
		}
	})

	// --- C. Status/special filtering ---
	// Note: --ready, --pinned, --status closed/deferred/in_progress tests are
	// skipped because bd update and bd close are not yet implemented on
	// EmbeddedDoltStore. All seeded issues have status=open.

	t.Run("ready", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--ready")
		for _, issue := range issues {
			if issue.Status != types.StatusOpen {
				t.Errorf("--ready should only return open issues, got status %s for %s", issue.Status, issue.ID)
			}
		}
		// All seeded issues are open, so --ready should return most of them
		if len(issues) == 0 {
			t.Error("--ready should return open issues")
		}
	})

	t.Run("overdue", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--overdue")
		if !containsID(issues, seed.overdueTask) {
			t.Error("overdue task should appear with --overdue")
		}
		for _, issue := range issues {
			if issue.Status == types.StatusClosed {
				t.Errorf("closed issue %s should not appear in --overdue", issue.ID)
			}
		}
	})

	// --- D. Parent/hierarchy ---

	t.Run("parent_filter", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--parent", seed.epic)
		ids := listIssueIDs(issues)
		if !containsID(issues, seed.childTaskA) {
			t.Errorf("child A should appear with --parent, got %v", ids)
		}
		if !containsID(issues, seed.childTaskB) {
			t.Errorf("child B should appear with --parent, got %v", ids)
		}
	})

	t.Run("no_parent", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--no-parent")
		if containsID(issues, seed.childTaskA) {
			t.Error("child A should not appear with --no-parent")
		}
		if containsID(issues, seed.childTaskB) {
			t.Error("child B should not appear with --no-parent")
		}
	})

	t.Run("tree_parent", func(t *testing.T) {
		// --tree --parent shows hierarchical display
		out := bdList(t, bd, dir, "--tree", "--parent", seed.epic)
		if !strings.Contains(out, seed.epic) {
			t.Errorf("tree output should contain parent ID %s", seed.epic)
		}
	})

	t.Run("flat", func(t *testing.T) {
		// --flat disables tree format, uses legacy flat list
		out := bdList(t, bd, dir, "--flat")
		if strings.Contains(out, "└") || strings.Contains(out, "├") {
			t.Error("--flat should not contain tree characters")
		}
	})

	// --- E. Content search ---

	t.Run("title_search", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--title", "Open bug")
		if !containsID(issues, seed.openBug) {
			t.Error("title search should find 'Open bug'")
		}
	})

	t.Run("title_contains", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--title-contains", "task", "--all")
		if len(issues) == 0 {
			t.Error("title-contains 'task' should match some issues")
		}
	})

	t.Run("empty_description", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--empty-description")
		if !containsID(issues, seed.noDescBug) {
			t.Error("no-desc bug should appear with --empty-description")
		}
	})

	t.Run("no_assignee", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--no-assignee")
		for _, issue := range issues {
			if issue.Assignee != "" {
				t.Errorf("expected no assignee for %s, got %q", issue.ID, issue.Assignee)
			}
		}
	})

	// --- F. Date range filtering ---

	t.Run("created_after_yesterday", func(t *testing.T) {
		yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
		issues := bdListJSON(t, bd, dir, "--created-after", yesterday)
		if len(issues) == 0 {
			t.Error("all issues created today should match --created-after yesterday")
		}
	})

	t.Run("created_before_yesterday", func(t *testing.T) {
		yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
		issues := bdListJSON(t, bd, dir, "--created-before", yesterday)
		if len(issues) != 0 {
			t.Errorf("no issues should match --created-before yesterday, got %d", len(issues))
		}
	})

	// --- G. Priority range ---

	t.Run("priority_range", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--priority-min", "0", "--priority-max", "1")
		for _, issue := range issues {
			if issue.Priority > 1 {
				t.Errorf("expected priority 0-1, got %d for %s", issue.Priority, issue.ID)
			}
		}
	})

	t.Run("priority_min", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--priority-min", "3", "--all")
		for _, issue := range issues {
			if issue.Priority < 3 {
				t.Errorf("expected priority >= 3, got %d for %s", issue.Priority, issue.ID)
			}
		}
	})

	// --- H. Sort and reverse ---

	t.Run("sort_priority", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--sort", "priority")
		if len(issues) >= 2 {
			if issues[0].Priority > issues[1].Priority {
				t.Errorf("--sort priority should put lower priority first, got P%d before P%d",
					issues[0].Priority, issues[1].Priority)
			}
		}
	})

	t.Run("sort_title", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--sort", "title")
		if len(issues) >= 2 {
			if strings.ToLower(issues[0].Title) > strings.ToLower(issues[1].Title) {
				t.Errorf("--sort title should be alphabetical, got %q before %q",
					issues[0].Title, issues[1].Title)
			}
		}
	})

	t.Run("sort_reverse", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--sort", "priority", "--reverse")
		if len(issues) >= 2 {
			if issues[0].Priority < issues[1].Priority {
				t.Errorf("--sort priority --reverse should put higher priority first, got P%d before P%d",
					issues[0].Priority, issues[1].Priority)
			}
		}
	})

	// --- I. Output formats ---

	t.Run("json_output", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--all")
		if len(issues) == 0 {
			t.Fatal("expected non-empty JSON output")
		}
		// Verify fields are populated
		for _, issue := range issues {
			if issue.ID == "" {
				t.Error("issue ID should not be empty in JSON output")
			}
			if issue.Title == "" {
				t.Error("issue title should not be empty in JSON output")
			}
		}
		// Verify child has parent field
		for _, issue := range issues {
			if issue.ID == seed.childTaskA {
				if issue.Parent == nil || *issue.Parent != seed.epic {
					t.Errorf("child A should have parent=%s, got %v", seed.epic, issue.Parent)
				}
			}
		}
	})

	t.Run("long_format", func(t *testing.T) {
		out := bdList(t, bd, dir, "--long", "--flat")
		if !strings.Contains(out, "Found") {
			t.Error("--long format should contain 'Found N issues'")
		}
	})

	t.Run("pretty_format", func(t *testing.T) {
		out := bdList(t, bd, dir, "--pretty")
		// Pretty format uses status symbols
		if len(out) == 0 {
			t.Error("--pretty should produce output")
		}
	})

	t.Run("format_digraph", func(t *testing.T) {
		out := bdList(t, bd, dir, "--format", "digraph", "--all")
		// Digraph format outputs dependency edges — may be empty if no blocking deps
		// but should not panic
		if out == "" {
			t.Error("digraph output should not be empty")
		}
	})

	t.Run("format_dot", func(t *testing.T) {
		// DOT format calls GetDependencyRecords per issue — verify it doesn't panic.
		// --flat disables tree mode which would otherwise override --format.
		out := bdList(t, bd, dir, "--format", "dot", "--flat", "--all")
		if !strings.Contains(out, "digraph") {
			t.Errorf("dot output should contain 'digraph' header, got: %s", out)
		}
	})

	t.Run("compact_default", func(t *testing.T) {
		out := bdList(t, bd, dir, "--flat")
		if !strings.Contains(out, seed.openBug) {
			t.Error("default compact output should contain issue IDs")
		}
	})

	// --- J. Metadata filtering ---

	t.Run("metadata_field", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--metadata-field", "env=prod")
		if !containsID(issues, seed.metadataIssue) {
			t.Error("metadata issue with env=prod should appear")
		}
		// Should not contain issues without that metadata
		if containsID(issues, seed.openBug) {
			t.Error("open bug should not match env=prod metadata filter")
		}
	})

	t.Run("has_metadata_key", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--has-metadata-key", "env")
		if !containsID(issues, seed.metadataIssue) {
			t.Error("metadata issue should appear with --has-metadata-key env")
		}
	})

	// --- K. Edge cases ---

	t.Run("empty_database", func(t *testing.T) {
		// Create a fresh empty database
		emptyDir, _, _ := bdInit(t, bd, "--prefix", "empty")
		issues := bdListJSON(t, bd, emptyDir)
		if len(issues) != 0 {
			t.Errorf("expected 0 issues in empty database, got %d", len(issues))
		}
	})

	t.Run("limit_zero_unlimited", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--limit", "0")
		// limit 0 means unlimited — should return more than the default limit of 50
		// (we have ~13 non-default issues, just verify we get them all)
		if len(issues) == 0 {
			t.Error("--limit 0 should return all issues")
		}
	})

	t.Run("invalid_sort_field", func(t *testing.T) {
		out := bdListFail(t, bd, dir, "--sort", "nonexistent")
		if !strings.Contains(out, "invalid sort field") {
			t.Errorf("expected 'invalid sort field' error, got: %s", out)
		}
	})

	t.Run("invalid_status", func(t *testing.T) {
		out := bdListFail(t, bd, dir, "--status", "nonexistent")
		if !strings.Contains(out, "invalid status") {
			t.Errorf("expected 'invalid status' error, got: %s", out)
		}
	})
}

// seedTestData creates a rich set of test issues covering all filter dimensions.
// Uses only `bd create` and `bd dep add` — no `bd update` or `bd close` since
// those are not yet implemented on EmbeddedDoltStore.
func seedTestData(t *testing.T, bd, dir string) testSeedData {
	t.Helper()
	var s testSeedData

	// 1. Open bug, P0, alice, labels: backend,urgent, with description
	issue := bdCreate(t, bd, dir, "Open bug", "--type", "bug", "--priority", "0",
		"--assignee", "alice", "--description", "This is a bug", "--label", "backend", "--label", "urgent")
	s.openBug = issue.ID

	// 2. Feature, P1, bob, labels: frontend
	issue = bdCreate(t, bd, dir, "Feature request", "--type", "feature", "--priority", "1",
		"--assignee", "bob", "--label", "frontend")
	s.feature = issue.ID

	// 3. Task, P2, alice, labels: backend
	issue = bdCreate(t, bd, dir, "Backend task", "--type", "task", "--priority", "2",
		"--assignee", "alice", "--label", "backend")
	s.task = issue.ID

	// 4. Chore, P3, no assignee, defer_until set
	issue = bdCreate(t, bd, dir, "Deferred chore", "--type", "chore", "--priority", "3",
		"--defer", "+7d")
	s.chore = issue.ID

	// 5. Epic, P1, labels: planning
	// Note: no blocking deps via `bd dep add` — DetectCycles is not implemented
	// on EmbeddedDoltStore. Parent-child deps are created via --parent on create.
	issue = bdCreate(t, bd, dir, "Epic with deps", "--type", "epic", "--priority", "1",
		"--label", "planning")
	s.epic = issue.ID

	// 6. Decision, P4, labels: pinned-ref
	issue = bdCreate(t, bd, dir, "Architecture decision", "--type", "decision", "--priority", "4",
		"--label", "pinned-ref")
	s.decision = issue.ID

	// 7. Child task A, P2, bob, labels: backend, parent=epic
	issue = bdCreate(t, bd, dir, "Child task A", "--type", "task", "--priority", "2",
		"--assignee", "bob", "--label", "backend", "--parent", s.epic)
	s.childTaskA = issue.ID

	// 8. Child task B, P3, labels: frontend, parent=epic
	issue = bdCreate(t, bd, dir, "Child task B", "--type", "task", "--priority", "3",
		"--label", "frontend", "--parent", s.epic)
	s.childTaskB = issue.ID

	// 9. No-desc bug, P1
	issue = bdCreate(t, bd, dir, "No desc bug", "--type", "bug", "--priority", "1")
	s.noDescBug = issue.ID

	// 10. Overdue task, P1, alice, due in past, labels: urgent
	pastDue := time.Now().Add(-48 * time.Hour).Format("2006-01-02")
	issue = bdCreate(t, bd, dir, "Overdue task", "--type", "task", "--priority", "1",
		"--assignee", "alice", "--label", "urgent", "--due", pastDue)
	s.overdueTask = issue.ID

	// 12. Metadata issue, P1, metadata: env=prod
	issue = bdCreate(t, bd, dir, "Metadata issue", "--type", "feature", "--priority", "1",
		"--metadata", `{"env":"prod"}`)
	s.metadataIssue = issue.ID

	// 13. Ready task, P0, labels: backend, no blockers
	issue = bdCreate(t, bd, dir, "Ready task", "--type", "task", "--priority", "0",
		"--label", "backend")
	s.readyTask = issue.ID

	t.Logf("Seeded %d test issues", 12)
	t.Logf("  openBug=%s feature=%s task=%s chore=%s", s.openBug, s.feature, s.task, s.chore)
	t.Logf("  epic=%s decision=%s childA=%s childB=%s", s.epic, s.decision, s.childTaskA, s.childTaskB)
	t.Logf("  noDescBug=%s overdue=%s metadata=%s ready=%s",
		s.noDescBug, s.overdueTask, s.metadataIssue, s.readyTask)

	// Verify seeding worked
	all := bdListJSON(t, bd, dir, "--all", "--limit", "0")
	if len(all) < 12 {
		t.Fatalf("expected at least 12 seeded issues, got %d", len(all))
	}

	return s
}

// TestEmbeddedListConcurrent verifies that 20 concurrent workers can each
// run 10 creates and 10 lists without data loss, corruption, or errors.
func TestEmbeddedListConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cl")

	const (
		numWorkers      = 20
		issuesPerWorker = 10
	)

	type workerResult struct {
		worker     int
		createIDs  []string
		listCounts []int // number of issues returned by each list call
		err        error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			// Interleave creates and lists: create one, list once, repeat.
			for i := 0; i < issuesPerWorker; i++ {
				// Create
				title := fmt.Sprintf("w%d-issue-%d", worker, i)
				cmd := exec.Command(bd, "create", "--silent", title)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("create %d: %v\n%s", i, err, out)
					results[worker] = r
					return
				}
				id := strings.TrimSpace(string(out))
				if id == "" {
					r.err = fmt.Errorf("create %d: empty ID", i)
					results[worker] = r
					return
				}
				r.createIDs = append(r.createIDs, id)

				// List (JSON for easy parsing)
				listCmd := exec.Command(bd, "list", "--json", "--limit", "0")
				listCmd.Dir = dir
				listCmd.Env = bdEnv(dir)
				listOut, err := listCmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("list after create %d: %v\n%s", i, err, listOut)
					results[worker] = r
					return
				}
				// Parse JSON array to count issues
				s := string(listOut)
				start := strings.Index(s, "[")
				if start < 0 {
					r.listCounts = append(r.listCounts, 0)
					continue
				}
				var issues []json.RawMessage
				if jsonErr := json.Unmarshal([]byte(s[start:]), &issues); jsonErr != nil {
					r.err = fmt.Errorf("list parse after create %d: %v\nraw: %s", i, jsonErr, s)
					results[worker] = r
					return
				}
				r.listCounts = append(r.listCounts, len(issues))
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	// Collect all created IDs and check for errors.
	allIDs := make(map[string]bool)
	var failures int
	for _, r := range results {
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
			failures++
			continue
		}
		for _, id := range r.createIDs {
			if allIDs[id] {
				t.Errorf("duplicate ID %q from worker %d", id, r.worker)
			}
			allIDs[id] = true
		}
	}

	totalExpected := numWorkers * issuesPerWorker
	if failures > 0 {
		t.Fatalf("%d/%d workers failed", failures, numWorkers)
	}
	if len(allIDs) != totalExpected {
		t.Errorf("expected %d unique IDs, got %d", totalExpected, len(allIDs))
	}

	// Verify list counts were monotonically non-decreasing within each worker
	// (each worker creates then lists, so count should never decrease).
	for _, r := range results {
		if r.err != nil {
			continue
		}
		for i := 1; i < len(r.listCounts); i++ {
			if r.listCounts[i] < r.listCounts[i-1] {
				t.Errorf("worker %d: list count decreased from %d to %d between iterations %d and %d",
					r.worker, r.listCounts[i-1], r.listCounts[i], i-1, i)
			}
		}
	}

	// Final verification: one authoritative list should see all created issues.
	finalIssues := bdListJSON(t, bd, dir, "--limit", "0")
	finalIDSet := make(map[string]bool, len(finalIssues))
	for _, issue := range finalIssues {
		finalIDSet[issue.ID] = true
	}
	var missing int
	for id := range allIDs {
		if !finalIDSet[id] {
			t.Errorf("created ID %s not found in final list", id)
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d/%d created issues missing from final list (%d total in list)",
			missing, totalExpected, len(finalIssues))
	}

	t.Logf("concurrency test: %d workers × %d issues = %d total, %d in final list",
		numWorkers, issuesPerWorker, totalExpected, len(finalIssues))
}
