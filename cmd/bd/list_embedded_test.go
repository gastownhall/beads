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

	"github.com/steveyegge/beads/internal/types"
)

// bdList runs "bd list" with the given flags and returns stdout.
func bdList(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"list"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd list %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdListJSON runs "bd list --json" and parses the result as an array of IssueWithCounts.
func bdListJSON(t *testing.T, bd, dir string, args ...string) []*types.IssueWithCounts {
	t.Helper()
	fullArgs := append([]string{"list", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd list --json %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	// Parse stdout only; hints/warnings (e.g. truncation) go to stderr (GH#3212).
	s := stdout.String()
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

type bdListSkipLabelsJSON struct {
	SchemaVersion int `json:"schema_version"`
	Issues        []struct {
		ID     string   `json:"id"`
		Labels []string `json:"labels"`
	} `json:"issues"`
	Meta struct {
		SkipLabels bool `json:"skip_labels"`
		Count      int  `json:"count"`
	} `json:"meta"`
}

func bdListSkipLabelsJSONOutput(t *testing.T, bd, dir string, args ...string) bdListSkipLabelsJSON {
	t.Helper()
	fullArgs := append([]string{"list", "--json", "--skip-labels"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd list --json --skip-labels %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	var out bdListSkipLabelsJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("failed to parse skip-labels JSON output: %v\nraw: %s", err, stdout.String())
	}
	return out
}

// bdListCapture runs "bd list" and returns (stdout, stderr) separately.
func bdListCapture(t *testing.T, bd, dir string, args ...string) (string, string) {
	t.Helper()
	fullArgs := append([]string{"list"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd list %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

// bdListJSONWithEnv runs "bd list --json" with extra environment variables.
func bdListJSONWithEnv(t *testing.T, bd, dir string, extraEnv []string, args ...string) []*types.IssueWithCounts {
	t.Helper()
	fullArgs := append([]string{"list", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), extraEnv...)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd list --json %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	s := stdout.String()
	start := strings.Index(s, "[")
	if start < 0 {
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

	t.Run("limit_truncation_hint", func(t *testing.T) {
		// GH#4094: hint is suppressed when stderr is not a terminal (piped).
		// bdListCapture always runs in piped mode, so no hint expected even when truncated.
		stdout, stderr := bdListCapture(t, bd, dir, "--limit", "2")
		if strings.Contains(stderr, "more results matched") {
			t.Errorf("truncation hint must not appear on piped stderr (GH#4094):\nstderr: %q\nstdout: %q", stderr, stdout)
		}
		// Hint must never appear in stdout.
		if strings.Contains(stdout, "more results matched") {
			t.Errorf("truncation hint leaked into stdout:\n%s", stdout)
		}

		// Not truncated: --limit 0 (unlimited) must not emit the hint.
		_, stderrAll := bdListCapture(t, bd, dir, "--limit", "0")
		if strings.Contains(stderrAll, "more results matched") {
			t.Errorf("unexpected truncation hint with --limit 0:\n%s", stderrAll)
		}

		// Not truncated: exact count match (we seed 12 issues, closed ones excluded by default).
		// Use a generous --limit that exceeds any default view.
		_, stderrHigh := bdListCapture(t, bd, dir, "--limit", "1000")
		if strings.Contains(stderrHigh, "more results matched") {
			t.Errorf("false-positive truncation hint when under limit:\n%s", stderrHigh)
		}
	})

	t.Run("list_limit_config_env", func(t *testing.T) {
		// BD_LIST_LIMIT env var sets the default limit when --limit not passed.
		// With BD_LIST_LIMIT=1, default list should return at most 1 issue.
		limitedIssues := bdListJSONWithEnv(t, bd, dir, []string{"BD_LIST_LIMIT=1"})
		if len(limitedIssues) > 1 {
			t.Errorf("BD_LIST_LIMIT=1 should limit to at most 1, got %d", len(limitedIssues))
		}

		// --limit flag still takes precedence over env var.
		allIssues := bdListJSONWithEnv(t, bd, dir, []string{"BD_LIST_LIMIT=1"}, "--limit", "0")
		if len(allIssues) <= 1 {
			t.Errorf("--limit 0 should override BD_LIST_LIMIT=1, got %d", len(allIssues))
		}

		// --all is also an explicit list request and should override the configured default.
		allFlagIssues := bdListJSONWithEnv(t, bd, dir, []string{"BD_LIST_LIMIT=1"}, "--all")
		if len(allFlagIssues) <= 1 {
			t.Errorf("--all should override BD_LIST_LIMIT=1, got %d", len(allFlagIssues))
		}
	})

	t.Run("list_limit_config_file", func(t *testing.T) {
		// Write project config.yaml with list.limit: 1.
		configPath := filepath.Join(dir, ".beads", "config.yaml")
		if err := os.WriteFile(configPath, []byte("list:\n  limit: 1\n"), 0o644); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
		defer func() { _ = os.Remove(configPath) }()

		// Config file should limit to at most 1 issue.
		limitedIssues := bdListJSON(t, bd, dir)
		if len(limitedIssues) > 1 {
			t.Errorf("list.limit=1 in config should limit to at most 1, got %d", len(limitedIssues))
		}

		// --limit flag still takes precedence over config file.
		allIssues := bdListJSON(t, bd, dir, "--limit", "0")
		if len(allIssues) <= 1 {
			t.Errorf("--limit 0 should override config list.limit=1, got %d", len(allIssues))
		}

		// --all is also an explicit list request and should override the configured default.
		allFlagIssues := bdListJSON(t, bd, dir, "--all")
		if len(allFlagIssues) <= 1 {
			t.Errorf("--all should override config list.limit=1, got %d", len(allFlagIssues))
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
		// be-ucslk4 regression guard: prior version returned the full set
		// because LabelPattern was set on IssueFilter but never consumed by
		// the SQL builder. Issues without a 'back*' label must be excluded.
		if containsID(issues, seed.feature) {
			t.Error("feature (no 'back*' label) should NOT match pattern 'back*'")
		}
		if containsID(issues, seed.chore) {
			t.Error("chore (no 'back*' label) should NOT match pattern 'back*'")
		}
	})

	t.Run("label_regex", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--label-regex", "back(end|log)")
		if !containsID(issues, seed.openBug) {
			t.Error("openBug with label 'backend' should match regex 'back(end|log)'")
		}
		if !containsID(issues, seed.readyTask) {
			t.Error("readyTask with label 'backend' should match regex 'back(end|log)'")
		}
		// be-ucslk4 regression guard: prior version returned the full set
		// because LabelRegex was set on IssueFilter but never consumed by
		// the SQL builder. Issues without a 'back(end|log)' label must be excluded.
		if containsID(issues, seed.feature) {
			t.Error("feature (label 'frontend') should NOT match regex 'back(end|log)'")
		}
		if containsID(issues, seed.epic) {
			t.Error("epic (label 'planning') should NOT match regex 'back(end|log)'")
		}

		// Alternation across unrelated labels: verifies REGEXP semantics
		// (not just glob-style prefix matching) — 'urgent' and 'planning'
		// share no substring, so only a true regex OR catches both.
		altIssues := bdListJSON(t, bd, dir, "--label-regex", "urgent|planning")
		if !containsID(altIssues, seed.openBug) {
			t.Error("openBug with label 'urgent' should match regex 'urgent|planning'")
		}
		if !containsID(altIssues, seed.epic) {
			t.Error("epic with label 'planning' should match regex 'urgent|planning'")
		}
		if containsID(altIssues, seed.task) {
			t.Error("task (label 'backend' only) should NOT match regex 'urgent|planning'")
		}
	})

	t.Run("exclude_label", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--exclude-label", "urgent")
		// openBug has labels: backend,urgent — should be excluded
		if containsID(issues, seed.openBug) {
			t.Error("openBug with 'urgent' label should be excluded by --exclude-label urgent")
		}
		// overdueTask also has label: urgent — should be excluded
		if containsID(issues, seed.overdueTask) {
			t.Error("overdueTask with 'urgent' label should be excluded by --exclude-label urgent")
		}
	})

	t.Run("exclude_label_with_include", func(t *testing.T) {
		// Include backend but exclude urgent — should get issues with backend but not urgent
		issues := bdListJSON(t, bd, dir, "--label", "backend", "--exclude-label", "urgent")
		// openBug has both backend and urgent — should be excluded
		if containsID(issues, seed.openBug) {
			t.Error("openBug with backend+urgent should be excluded when --exclude-label urgent")
		}
	})

	t.Run("skip_labels_json_suppresses_labeled_issue", func(t *testing.T) {
		out := bdListSkipLabelsJSONOutput(t, bd, dir, "--id", seed.openBug)
		if !out.Meta.SkipLabels {
			t.Fatal("expected meta.skip_labels=true")
		}
		if out.Meta.Count != 1 || len(out.Issues) != 1 {
			t.Fatalf("expected one issue and matching count, got count=%d issues=%d", out.Meta.Count, len(out.Issues))
		}
		if out.Issues[0].ID != seed.openBug {
			t.Fatalf("expected issue %s, got %s", seed.openBug, out.Issues[0].ID)
		}
		if out.Issues[0].Labels == nil {
			t.Fatal("expected labels field to be present as an empty array, got nil")
		}
		if len(out.Issues[0].Labels) != 0 {
			t.Fatalf("--skip-labels JSON leaked labels: %v", out.Issues[0].Labels)
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

	t.Run("ready_exclude_type", func(t *testing.T) {
		issues := bdListJSON(t, bd, dir, "--ready", "--exclude-type", "epic", "--limit", "0")
		if containsID(issues, seed.epic) {
			t.Errorf("--ready --exclude-type epic should exclude epic %s, got %v", seed.epic, listIssueIDs(issues))
		}
		if !containsID(issues, seed.readyTask) {
			t.Errorf("--ready --exclude-type epic should still include ready task %s, got %v", seed.readyTask, listIssueIDs(issues))
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

	t.Run("ready_parent_tree_excludes_blocked_descendants", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Ready parent tree", "--type", "epic")
		readyChild := bdCreate(t, bd, dir, "Ready child in tree", "--type", "task", "--parent", parent.ID)
		blockedChild := bdCreate(t, bd, dir, "Blocked child in tree", "--type", "task", "--parent", parent.ID)
		blocker := bdCreate(t, bd, dir, "Tree child blocker", "--type", "task")
		bdDepAdd(t, bd, dir, blockedChild.ID, blocker.ID)

		out := bdList(t, bd, dir, "--ready", "--parent", parent.ID, "--no-pager")
		if !strings.Contains(out, readyChild.ID) {
			t.Errorf("ready child %s should appear in ready parent tree:\n%s", readyChild.ID, out)
		}
		if strings.Contains(out, blockedChild.ID) {
			t.Errorf("blocked child %s should not appear in ready parent tree:\n%s", blockedChild.ID, out)
		}
	})

	// Regression for gastownhall/beads#3936: relates-to between two epics
	// must not nest them in `bd list` tree mode, and a bidirectional
	// relates-to must not silently drop both epics from the output.
	t.Run("tree_relates_to_does_not_nest_or_drop_epics", func(t *testing.T) {
		epicA := bdCreate(t, bd, dir, "Relates Epic A", "--type", "epic", "--priority", "2")
		epicB := bdCreate(t, bd, dir, "Relates Epic B", "--type", "epic", "--priority", "2")

		bdDep(t, bd, dir, "add", epicA.ID, epicB.ID, "--type", "relates-to")
		out := bdList(t, bd, dir, "--no-pager", "--type", "epic")
		if !strings.Contains(out, epicA.ID) || !strings.Contains(out, epicB.ID) {
			t.Fatalf("one-direction relates-to should keep both epics visible:\n%s", out)
		}
		if strings.Contains(out, "└── "+epicA.ID) || strings.Contains(out, "└── "+epicB.ID) ||
			strings.Contains(out, "├── "+epicA.ID) || strings.Contains(out, "├── "+epicB.ID) {
			t.Fatalf("relates-to must not nest epics under each other:\n%s", out)
		}

		bdDep(t, bd, dir, "add", epicB.ID, epicA.ID, "--type", "relates-to")
		out = bdList(t, bd, dir, "--no-pager", "--type", "epic")
		if !strings.Contains(out, epicA.ID) || !strings.Contains(out, epicB.ID) {
			t.Fatalf("bidirectional relates-to must not drop epics from tree output:\n%s", out)
		}
	})

	t.Run("ready_parent_filter_includes_grandchildren", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Ready parent recursive", "--type", "epic")
		child := bdCreate(t, bd, dir, "Ready child recursive", "--type", "task", "--parent", parent.ID)
		grandchild := bdCreate(t, bd, dir, "Ready grandchild recursive", "--type", "task", "--parent", child.ID)

		issues := bdListJSON(t, bd, dir, "--ready", "--parent", parent.ID, "--limit", "0")
		if !containsID(issues, child.ID) {
			t.Errorf("ready parent filter should include direct child %s, got %v", child.ID, listIssueIDs(issues))
		}
		if !containsID(issues, grandchild.ID) {
			t.Errorf("ready parent filter should include recursive grandchild %s, got %v", grandchild.ID, listIssueIDs(issues))
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
		if !strings.Contains(out, "Description:") || !strings.Contains(out, "This is a bug") {
			t.Errorf("--long format should include issue descriptions, got: %s", out)
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

	t.Run("reject_offset_in_direct_mode", func(t *testing.T) {
		// --offset is only honored under --proxied-server; the direct
		// (embedded) path must fatal before touching the store.
		out := bdListFail(t, bd, dir, "--offset", "1")
		if !strings.Contains(out, "--offset is only supported under --proxied-server") {
			t.Errorf("expected --offset direct-mode rejection, got: %s", out)
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
				out, err := bdRunWithFlockRetry(t, bd, dir, "create", "--silent", title)
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
				listStdout, listStderr, err := runCommandBuffers(t, listCmd)
				if err != nil {
					r.err = fmt.Errorf("list after create %d: %v\nstdout:\n%s\nstderr:\n%s", i, err, listStdout.String(), listStderr.String())
					results[worker] = r
					return
				}
				// Parse JSON array to count issues
				s := listStdout.String()
				start := strings.Index(s, "[")
				if start < 0 {
					r.listCounts = append(r.listCounts, 0)
					continue
				}
				var issues []json.RawMessage
				if jsonErr := json.Unmarshal([]byte(s[start:]), &issues); jsonErr != nil {
					r.err = fmt.Errorf("list parse after create %d: %v\nstdout:\n%s\nstderr:\n%s", i, jsonErr, s, listStderr.String())
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
	var successes int
	for _, r := range results {
		if r.err != nil {
			if !strings.Contains(r.err.Error(), "one writer at a time") {
				t.Errorf("worker %d failed: %v", r.worker, r.err)
			}
			continue
		}
		successes++
		for _, id := range r.createIDs {
			if allIDs[id] {
				t.Errorf("duplicate ID %q from worker %d", id, r.worker)
			}
			allIDs[id] = true
		}
	}

	if successes == 0 {
		t.Fatal("all workers failed — expected at least 1 success")
	}
	expectedIDs := successes * issuesPerWorker
	if len(allIDs) != expectedIDs {
		t.Errorf("expected %d unique IDs from %d successful workers, got %d", expectedIDs, successes, len(allIDs))
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
			missing, len(allIDs), len(finalIssues))
	}

	t.Logf("concurrency test: %d/%d workers succeeded, %d IDs created, %d in final list",
		successes, numWorkers, len(allIDs), len(finalIssues))
}
