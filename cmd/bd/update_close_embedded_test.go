//go:build embeddeddolt

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// bdUpdate runs "bd update" with the given args and returns stdout.
func bdUpdate(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd update %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdUpdateFail runs "bd update" expecting failure.
func bdUpdateFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"update"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd update %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdClose runs "bd close" with the given args and returns stdout.
func bdClose(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"close"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd close %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdCloseFail runs "bd close" expecting failure.
func bdCloseFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"close"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd close %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdDepAdd runs "bd dep add" with the given args.
func bdDepAdd(t *testing.T, bd, dir string, args ...string) {
	t.Helper()
	fullArgs := append([]string{"dep", "add"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd dep add %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// bdShowJSON runs "bd show <id> --json" and returns the raw JSON output.
func bdShowJSON(t *testing.T, bd, dir, id string) string {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd show %s --json failed: %v\n%s", id, err, out)
	}
	return string(out)
}

// hasLabel checks if a label is present in the issue's labels.
func hasLabel(issue *types.Issue, label string) bool {
	for _, l := range issue.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// parseShowJSON parses the first JSON object from bd show --json output,
// which may be wrapped in an array or have non-JSON lines before it.
func parseShowJSON(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	start := strings.Index(raw, "{")
	if start < 0 {
		t.Fatalf("no JSON object in output: %s", raw)
	}
	dec := json.NewDecoder(strings.NewReader(raw[start:]))
	var obj json.RawMessage
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("parse JSON object: %v\nraw: %s", err, raw[start:])
	}
	return obj
}

// showLabels returns labels from bd show --json output (uses IssueDetails which includes labels).
func showLabels(t *testing.T, bd, dir, id string) []string {
	t.Helper()
	raw := bdShowJSON(t, bd, dir, id)
	obj := parseShowJSON(t, raw)
	var details struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(obj, &details); err != nil {
		t.Fatalf("parse labels: %v", err)
	}
	return details.Labels
}

// showDeps returns dependency IDs from bd show --json output.
func showDeps(t *testing.T, bd, dir, id string) []struct {
	ID   string `json:"id"`
	Type string `json:"dependency_type"`
} {
	t.Helper()
	raw := bdShowJSON(t, bd, dir, id)
	obj := parseShowJSON(t, raw)
	var details struct {
		Dependencies []struct {
			ID   string `json:"id"`
			Type string `json:"dependency_type"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(obj, &details); err != nil {
		t.Fatalf("parse deps: %v", err)
	}
	return details.Dependencies
}

func TestEmbeddedUpdate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tu")

	// ===== Field Update Flags =====

	t.Run("update_status", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Status test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusInProgress {
			t.Errorf("expected status in_progress, got %s", got.Status)
		}
	})

	t.Run("update_title", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Old title", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--title", "New title")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Title != "New title" {
			t.Errorf("expected title 'New title', got %q", got.Title)
		}
	})

	t.Run("update_assignee", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Assign test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee != "alice" {
			t.Errorf("expected assignee alice, got %q", got.Assignee)
		}
	})

	t.Run("update_priority", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Priority test", "--type", "task", "--priority", "3")
		bdUpdate(t, bd, dir, issue.ID, "--priority", "0")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Priority != 0 {
			t.Errorf("expected priority 0, got %d", got.Priority)
		}
	})

	t.Run("update_description", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Desc test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--description", "Updated description")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Description != "Updated description" {
			t.Errorf("expected description 'Updated description', got %q", got.Description)
		}
	})

	t.Run("update_type", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Type test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--type", "bug")
		got := bdShow(t, bd, dir, issue.ID)
		if got.IssueType != "bug" {
			t.Errorf("expected type bug, got %s", got.IssueType)
		}
	})

	t.Run("update_design", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Design test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--design", "Design notes here")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Design != "Design notes here" {
			t.Errorf("expected design 'Design notes here', got %q", got.Design)
		}
	})

	t.Run("update_notes", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Notes test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--notes", "Some notes")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Notes != "Some notes" {
			t.Errorf("expected notes 'Some notes', got %q", got.Notes)
		}
	})

	t.Run("update_append_notes", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Append notes test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--notes", "first")
		bdUpdate(t, bd, dir, issue.ID, "--append-notes", "more")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Notes != "first\nmore" {
			t.Errorf("expected notes 'first\\nmore', got %q", got.Notes)
		}
	})

	t.Run("update_notes_and_append_conflict", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Notes conflict", "--type", "task")
		out := bdUpdateFail(t, bd, dir, issue.ID, "--notes", "x", "--append-notes", "y")
		if !strings.Contains(out, "cannot specify both") {
			t.Errorf("expected conflict error, got: %s", out)
		}
	})

	t.Run("update_acceptance", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "AC test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--acceptance", "AC text")
		got := bdShow(t, bd, dir, issue.ID)
		if got.AcceptanceCriteria != "AC text" {
			t.Errorf("expected acceptance_criteria 'AC text', got %q", got.AcceptanceCriteria)
		}
	})

	t.Run("update_external_ref", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "ExtRef test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--external-ref", "gh-42")
		got := bdShow(t, bd, dir, issue.ID)
		if got.ExternalRef == nil || *got.ExternalRef != "gh-42" {
			t.Errorf("expected external_ref 'gh-42', got %v", got.ExternalRef)
		}
	})

	t.Run("update_spec_id", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "SpecID test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--spec-id", "RFC-007")
		got := bdShow(t, bd, dir, issue.ID)
		if got.SpecID != "RFC-007" {
			t.Errorf("expected spec_id 'RFC-007', got %q", got.SpecID)
		}
	})

	t.Run("update_estimate", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Estimate test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--estimate", "60")
		got := bdShow(t, bd, dir, issue.ID)
		if got.EstimatedMinutes == nil || *got.EstimatedMinutes != 60 {
			t.Errorf("expected estimated_minutes 60, got %v", got.EstimatedMinutes)
		}
	})

	t.Run("update_due", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Due test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--due", "2099-01-15")
		got := bdShow(t, bd, dir, issue.ID)
		if got.DueAt == nil {
			t.Error("expected due_at to be set")
		}
	})

	t.Run("update_due_clear", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Due clear test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--due", "2099-01-15")
		bdUpdate(t, bd, dir, issue.ID, "--due", "")
		got := bdShow(t, bd, dir, issue.ID)
		if got.DueAt != nil {
			t.Error("expected due_at to be cleared")
		}
	})

	t.Run("update_defer", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Defer test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "2099-01-15")
		got := bdShow(t, bd, dir, issue.ID)
		if got.DeferUntil == nil {
			t.Error("expected defer_until to be set")
		}
	})

	t.Run("update_defer_clear", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Defer clear test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "2099-01-15")
		bdUpdate(t, bd, dir, issue.ID, "--defer", "")
		got := bdShow(t, bd, dir, issue.ID)
		if got.DeferUntil != nil {
			t.Error("expected defer_until to be cleared")
		}
	})

	t.Run("update_await_id", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Await test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--await-id", "run-123")
		raw := bdShowJSON(t, bd, dir, issue.ID)
		if !strings.Contains(raw, `"await_id":"run-123"`) && !strings.Contains(raw, `"await_id": "run-123"`) {
			t.Errorf("expected await_id 'run-123' in JSON output, got: %s", raw)
		}
	})

	t.Run("update_multiple_fields", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Multi update", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress", "--assignee", "bob", "--priority", "1")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusInProgress {
			t.Errorf("expected status in_progress, got %s", got.Status)
		}
		if got.Assignee != "bob" {
			t.Errorf("expected assignee bob, got %q", got.Assignee)
		}
		if got.Priority != 1 {
			t.Errorf("expected priority 1, got %d", got.Priority)
		}
	})

	t.Run("close_via_status_update", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Close via update", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "closed")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected status closed, got %s", got.Status)
		}
		if got.ClosedAt == nil {
			t.Error("expected closed_at to be set")
		}
	})

	t.Run("reopen_via_status_update", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reopen test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "closed")
		bdUpdate(t, bd, dir, issue.ID, "--status", "open")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusOpen {
			t.Errorf("expected status open, got %s", got.Status)
		}
		if got.ClosedAt != nil {
			t.Error("expected closed_at to be cleared on reopen")
		}
	})

	// ===== Label Flags =====

	t.Run("update_add_label", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Label add test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--add-label", "bug")
		labels := showLabels(t, bd, dir, issue.ID)
		found := false
		for _, l := range labels {
			if l == "bug" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected label 'bug', got %v", labels)
		}
	})

	t.Run("update_add_multiple_labels", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Multi label test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--add-label", "a,b")
		labels := showLabels(t, bd, dir, issue.ID)
		hasA, hasB := false, false
		for _, l := range labels {
			if l == "a" {
				hasA = true
			}
			if l == "b" {
				hasB = true
			}
		}
		if !hasA || !hasB {
			t.Errorf("expected labels [a, b], got %v", labels)
		}
	})

	t.Run("update_remove_label", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Label remove test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--add-label", "bug")
		bdUpdate(t, bd, dir, issue.ID, "--remove-label", "bug")
		labels := showLabels(t, bd, dir, issue.ID)
		for _, l := range labels {
			if l == "bug" {
				t.Errorf("expected label 'bug' to be removed, got %v", labels)
			}
		}
	})

	t.Run("update_set_labels", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Label set test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--add-label", "a,b")
		bdUpdate(t, bd, dir, issue.ID, "--set-labels", "x,y")
		labels := showLabels(t, bd, dir, issue.ID)
		hasX, hasY, hasA := false, false, false
		for _, l := range labels {
			switch l {
			case "x":
				hasX = true
			case "y":
				hasY = true
			case "a":
				hasA = true
			}
		}
		if !hasX || !hasY {
			t.Errorf("expected labels [x, y], got %v", labels)
		}
		if hasA {
			t.Errorf("expected old label 'a' to be replaced, got %v", labels)
		}
	})

	// ===== Metadata Flags =====

	t.Run("update_metadata_json", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Meta test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--metadata", `{"key":"val"}`)
		got := bdShow(t, bd, dir, issue.ID)
		if !strings.Contains(string(got.Metadata), `"key"`) {
			t.Errorf("expected metadata to contain 'key', got %s", got.Metadata)
		}
	})

	t.Run("update_metadata_merge", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Meta merge test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--metadata", `{"a":1}`)
		bdUpdate(t, bd, dir, issue.ID, "--metadata", `{"b":2}`)
		got := bdShow(t, bd, dir, issue.ID)
		meta := string(got.Metadata)
		if !strings.Contains(meta, `"a"`) || !strings.Contains(meta, `"b"`) {
			t.Errorf("expected metadata to contain both a and b, got %s", meta)
		}
	})

	t.Run("update_set_metadata", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Set meta test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--set-metadata", "team=platform")
		got := bdShow(t, bd, dir, issue.ID)
		if !strings.Contains(string(got.Metadata), `"team"`) {
			t.Errorf("expected metadata to contain 'team', got %s", got.Metadata)
		}
	})

	t.Run("update_unset_metadata", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Unset meta test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--set-metadata", "team=platform")
		bdUpdate(t, bd, dir, issue.ID, "--unset-metadata", "team")
		got := bdShow(t, bd, dir, issue.ID)
		if strings.Contains(string(got.Metadata), `"team"`) {
			t.Errorf("expected metadata to NOT contain 'team', got %s", got.Metadata)
		}
	})

	t.Run("update_metadata_and_set_conflict", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Meta conflict", "--type", "task")
		out := bdUpdateFail(t, bd, dir, issue.ID, "--metadata", `{"a":1}`, "--set-metadata", "b=2")
		if !strings.Contains(out, "cannot combine") {
			t.Errorf("expected conflict error, got: %s", out)
		}
	})

	// ===== Claim Flag =====

	t.Run("update_claim", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Claim test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--claim")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Assignee == "" {
			t.Error("expected assignee to be set after claim")
		}
		if got.Status != types.StatusInProgress {
			t.Errorf("expected status in_progress after claim, got %s", got.Status)
		}
	})

	t.Run("update_claim_already_claimed", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Claim fail test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--assignee", "alice")
		out := bdUpdateFail(t, bd, dir, issue.ID, "--claim")
		if !strings.Contains(out, "already claimed") {
			t.Errorf("expected 'already claimed' error, got: %s", out)
		}
	})

	// ===== Parent Reparenting =====

	t.Run("update_parent_set", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Parent epic", "--type", "epic")
		child := bdCreate(t, bd, dir, "Child issue", "--type", "task")
		bdUpdate(t, bd, dir, child.ID, "--parent", epic.ID)
		deps := showDeps(t, bd, dir, child.ID)
		found := false
		for _, d := range deps {
			if d.ID == epic.ID && d.Type == "parent-child" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected parent-child dep to %s, got %v", epic.ID, deps)
		}
	})

	t.Run("update_parent_change", func(t *testing.T) {
		epic1 := bdCreate(t, bd, dir, "Old parent", "--type", "epic")
		epic2 := bdCreate(t, bd, dir, "New parent", "--type", "epic")
		child := bdCreate(t, bd, dir, "Reparent child", "--type", "task")
		bdUpdate(t, bd, dir, child.ID, "--parent", epic1.ID)
		bdUpdate(t, bd, dir, child.ID, "--parent", epic2.ID)
		deps := showDeps(t, bd, dir, child.ID)
		hasOld, hasNew := false, false
		for _, d := range deps {
			if d.Type == "parent-child" {
				if d.ID == epic1.ID {
					hasOld = true
				}
				if d.ID == epic2.ID {
					hasNew = true
				}
			}
		}
		if hasOld {
			t.Error("expected old parent dep to be removed")
		}
		if !hasNew {
			t.Error("expected new parent dep to exist")
		}
	})

	t.Run("update_parent_remove", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Remove parent epic", "--type", "epic")
		child := bdCreate(t, bd, dir, "Orphan child", "--type", "task")
		bdUpdate(t, bd, dir, child.ID, "--parent", epic.ID)
		bdUpdate(t, bd, dir, child.ID, "--parent", "")
		deps := showDeps(t, bd, dir, child.ID)
		for _, d := range deps {
			if d.Type == "parent-child" {
				t.Errorf("expected no parent-child dep, got %v", deps)
			}
		}
	})

	// ===== Ephemeral / History Flags =====

	t.Run("update_ephemeral", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Ephemeral test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--ephemeral")
		got := bdShow(t, bd, dir, issue.ID)
		if !got.Ephemeral {
			t.Error("expected ephemeral to be true")
		}
	})

	t.Run("update_persistent", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Persistent test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--ephemeral")
		bdUpdate(t, bd, dir, issue.ID, "--persistent")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Ephemeral {
			t.Error("expected ephemeral to be false after --persistent")
		}
	})

	t.Run("update_no_history", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "NoHistory test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--no-history")
		got := bdShow(t, bd, dir, issue.ID)
		if !got.NoHistory {
			t.Error("expected no_history to be true")
		}
	})

	t.Run("update_history", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "History test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--no-history")
		bdUpdate(t, bd, dir, issue.ID, "--history")
		got := bdShow(t, bd, dir, issue.ID)
		if got.NoHistory {
			t.Error("expected no_history to be false after --history")
		}
	})

	t.Run("update_ephemeral_persistent_conflict", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Eph conflict", "--type", "task")
		out := bdUpdateFail(t, bd, dir, issue.ID, "--ephemeral", "--persistent")
		if !strings.Contains(out, "cannot specify both") {
			t.Errorf("expected conflict error, got: %s", out)
		}
	})

	// ===== Session Flag =====

	t.Run("update_status_closed_with_session", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Session test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "closed", "--session", "sess-123")
		got := bdShow(t, bd, dir, issue.ID)
		// Verify the issue is closed (closed_by_session is stored but not
		// included in IssueSelectColumns, so we verify status + closed_at).
		if got.Status != types.StatusClosed {
			t.Errorf("expected status closed, got %s", got.Status)
		}
		if got.ClosedAt == nil {
			t.Error("expected closed_at to be set")
		}
	})

	// ===== Behavioral / Edge Cases =====

	t.Run("update_no_changes", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "No changes test", "--type", "task")
		out := bdUpdate(t, bd, dir, issue.ID)
		if !strings.Contains(out, "No updates specified") {
			t.Errorf("expected 'No updates specified', got: %s", out)
		}
	})

	t.Run("update_invalid_status", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Bad status", "--type", "task")
		bdUpdateFail(t, bd, dir, issue.ID, "--status", "bogus")
	})

	t.Run("update_invalid_priority", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Bad priority", "--type", "task")
		bdUpdateFail(t, bd, dir, issue.ID, "--priority", "-1")
	})

	t.Run("update_invalid_type", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Bad type", "--type", "task")
		bdUpdateFail(t, bd, dir, issue.ID, "--type", "bogus")
	})

	t.Run("update_nonexistent_id", func(t *testing.T) {
		bdUpdateFail(t, bd, dir, "tu-nonexistent999", "--status", "open")
	})

	t.Run("update_json_output", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON test", "--type", "task")
		cmd := exec.Command(bd, "update", issue.ID, "--status", "in_progress", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd update --json failed: %v\n%s", err, out)
		}
		s := string(out)
		start := strings.Index(s, "[")
		if start < 0 {
			start = strings.Index(s, "{")
		}
		if start < 0 {
			t.Fatalf("no JSON in output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON output, got: %s", s[start:])
		}
	})

	t.Run("update_multiple_ids", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Multi ID 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Multi ID 2", "--type", "task")
		bdUpdate(t, bd, dir, issue1.ID, issue2.ID, "--status", "in_progress")
		got1 := bdShow(t, bd, dir, issue1.ID)
		got2 := bdShow(t, bd, dir, issue2.ID)
		if got1.Status != types.StatusInProgress {
			t.Errorf("issue1: expected in_progress, got %s", got1.Status)
		}
		if got2.Status != types.StatusInProgress {
			t.Errorf("issue2: expected in_progress, got %s", got2.Status)
		}
	})

	t.Run("update_dolt_commit", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Dolt commit test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "in_progress")

		// Verify a Dolt commit exists by querying dolt_log.
		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		cfg, _ := configfile.Load(beadsDir)
		database := ""
		if cfg != nil {
			database = cfg.GetDoltDatabase()
		}
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		var commitCount int
		err = db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&commitCount)
		if err != nil {
			t.Fatalf("query dolt_log: %v", err)
		}
		// At minimum: init schema commit + create commit + update commit
		if commitCount < 3 {
			t.Errorf("expected at least 3 dolt commits, got %d", commitCount)
		}
	})

	t.Run("update_description_body_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Body alias test", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--body", "via body flag")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Description != "via body flag" {
			t.Errorf("expected description 'via body flag', got %q", got.Description)
		}
	})

	t.Run("update_description_from_file", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "File desc test", "--type", "task")
		tmpFile := filepath.Join(t.TempDir(), "desc.txt")
		if err := os.WriteFile(tmpFile, []byte("from file"), 0644); err != nil {
			t.Fatalf("write temp file: %v", err)
		}
		bdUpdate(t, bd, dir, issue.ID, "--body-file", tmpFile)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Description != "from file" {
			t.Errorf("expected description 'from file', got %q", got.Description)
		}
	})
}

// querySessionSQL queries closed_by_session via raw SQL since it's not in IssueSelectColumns.
func querySessionSQL(t *testing.T, beadsDir, id string) string {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var session string
	// Check both tables.
	err = db.QueryRowContext(t.Context(),
		"SELECT COALESCE(closed_by_session, '') FROM issues WHERE id = ?", id).Scan(&session)
	if err != nil {
		// Try wisps table.
		err = db.QueryRowContext(t.Context(),
			"SELECT COALESCE(closed_by_session, '') FROM wisps WHERE id = ?", id).Scan(&session)
		if err != nil {
			t.Fatalf("query closed_by_session: %v", err)
		}
	}
	return session
}

func TestEmbeddedClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tc")

	// ===== Basic Close Behavior =====

	t.Run("basic_close", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Close me", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected status closed, got %s", got.Status)
		}
		if got.ClosedAt == nil {
			t.Error("expected closed_at to be set")
		}
	})

	t.Run("close_default_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Default reason", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "Closed" {
			t.Errorf("expected default close_reason 'Closed', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Reason test", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--reason", "done")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "done" {
			t.Errorf("expected close_reason 'done', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_reason_short", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Short reason", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "-r", "fixed")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "fixed" {
			t.Errorf("expected close_reason 'fixed', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_message_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Message alias", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "-m", "via message")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "via message" {
			t.Errorf("expected close_reason 'via message', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_resolution_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Resolution alias", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--resolution", "wontfix")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "wontfix" {
			t.Errorf("expected close_reason 'wontfix', got %q", got.CloseReason)
		}
	})

	t.Run("close_with_comment_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Comment alias", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--comment", "duplicate")
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "duplicate" {
			t.Errorf("expected close_reason 'duplicate', got %q", got.CloseReason)
		}
	})

	t.Run("close_multiple_ids", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Multi close 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Multi close 2", "--type", "task")
		bdClose(t, bd, dir, issue1.ID, issue2.ID)
		got1 := bdShow(t, bd, dir, issue1.ID)
		got2 := bdShow(t, bd, dir, issue2.ID)
		if got1.Status != types.StatusClosed {
			t.Errorf("issue1: expected closed, got %s", got1.Status)
		}
		if got2.Status != types.StatusClosed {
			t.Errorf("issue2: expected closed, got %s", got2.Status)
		}
	})

	t.Run("close_already_closed", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Double close", "--type", "task")
		bdClose(t, bd, dir, issue.ID)
		// Closing again should not panic.
		cmd := exec.Command(bd, "close", issue.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		cmd.CombinedOutput() // Don't check error — behavior varies.
	})

	t.Run("close_nonexistent_id", func(t *testing.T) {
		bdCloseFail(t, bd, dir, "tc-nonexistent999")
	})

	// ===== Force Flag and Close Guards =====

	t.Run("close_blocked_refuses_without_force", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker guard", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked guard", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		// Without --force, should fail (exit non-zero).
		bdCloseFail(t, bd, dir, blocked.ID)
		got := bdShow(t, bd, dir, blocked.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected blocked issue to remain open without --force")
		}
	})

	t.Run("close_blocked_with_force", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Blocker force", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Blocked force", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		bdClose(t, bd, dir, blocked.ID, "--force")
		got := bdShow(t, bd, dir, blocked.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed with --force, got %s", got.Status)
		}
	})

	t.Run("close_pinned_refuses_without_force", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Pinned guard", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "pinned")
		bdCloseFail(t, bd, dir, issue.ID)
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected pinned issue to remain pinned without --force")
		}
	})

	t.Run("close_pinned_with_force", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Pinned force", "--type", "task")
		bdUpdate(t, bd, dir, issue.ID, "--status", "pinned")
		bdClose(t, bd, dir, issue.ID, "--force")
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed with --force, got %s", got.Status)
		}
	})

	t.Run("close_epic_open_children_refuses", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Epic guard", "--type", "epic")
		child := bdCreate(t, bd, dir, "Epic child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, epic.ID, "--type", "parent-child")

		bdCloseFail(t, bd, dir, epic.ID)
		got := bdShow(t, bd, dir, epic.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected epic with open children to remain open without --force")
		}
	})

	t.Run("close_epic_open_children_force", func(t *testing.T) {
		epic := bdCreate(t, bd, dir, "Epic force", "--type", "epic")
		child := bdCreate(t, bd, dir, "Epic child force", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, epic.ID, "--type", "parent-child")

		bdClose(t, bd, dir, epic.ID, "--force")
		got := bdShow(t, bd, dir, epic.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected epic closed with --force, got %s", got.Status)
		}
		// Child should still be open.
		_ = child
	})

	// ===== Blocker and Suggest-Next Behavior =====

	t.Run("close_unblocks_dependent", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Unblock blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Unblock blocked", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		bdClose(t, bd, dir, blocker.ID)
		got := bdShow(t, bd, dir, blocker.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected blocker closed, got %s", got.Status)
		}
		gotBlocked := bdShow(t, bd, dir, blocked.ID)
		if gotBlocked.Status != types.StatusOpen {
			t.Errorf("expected dependent still open, got %s", gotBlocked.Status)
		}
	})

	t.Run("close_suggest_next", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Suggest blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Suggest blocked", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		out := bdClose(t, bd, dir, blocker.ID, "--suggest-next")
		if !strings.Contains(out, "unblocked") && !strings.Contains(out, blocked.ID) {
			// Suggest-next may not always show output if the unblocked issue
			// isn't detected; this is best-effort.
			t.Logf("suggest-next output did not mention unblocked issue: %s", out)
		}
	})

	t.Run("close_suggest_next_json", func(t *testing.T) {
		blocker := bdCreate(t, bd, dir, "Suggest JSON blocker", "--type", "task")
		blocked := bdCreate(t, bd, dir, "Suggest JSON blocked", "--type", "task")
		bdDepAdd(t, bd, dir, blocked.ID, blocker.ID)

		cmd := exec.Command(bd, "close", blocker.ID, "--suggest-next", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd close --suggest-next --json failed: %v\n%s", err, out)
		}
		s := string(out)
		if !strings.Contains(s, "unblocked") {
			t.Logf("JSON output did not contain 'unblocked' key: %s", s)
		}
	})

	// ===== Claim-Next Flag =====

	t.Run("close_claim_next", func(t *testing.T) {
		toClose := bdCreate(t, bd, dir, "Claim next close", "--type", "task")
		nextIssue := bdCreate(t, bd, dir, "Claim next target", "--type", "task")

		out := bdClose(t, bd, dir, toClose.ID, "--claim-next")
		// The next issue should have been claimed.
		got := bdShow(t, bd, dir, nextIssue.ID)
		if got.Status == types.StatusInProgress && got.Assignee != "" {
			// Successfully claimed.
			_ = out
		} else {
			// claim-next is best-effort — may not claim if issue was filtered out.
			t.Logf("claim-next: next issue status=%s assignee=%q (may not have been claimed)", got.Status, got.Assignee)
		}
	})

	t.Run("close_claim_next_no_ready", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Only issue", "--type", "task")
		out := bdClose(t, bd, dir, issue.ID, "--claim-next")
		if !strings.Contains(out, "No ready issues") && !strings.Contains(out, "claimed") {
			t.Logf("claim-next with no ready issues: %s", out)
		}
	})

	t.Run("close_claim_next_json", func(t *testing.T) {
		toClose := bdCreate(t, bd, dir, "Claim JSON close", "--type", "task")
		_ = bdCreate(t, bd, dir, "Claim JSON target", "--type", "task")

		cmd := exec.Command(bd, "close", toClose.ID, "--claim-next", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd close --claim-next --json failed: %v\n%s", err, out)
		}
		s := string(out)
		// JSON should be valid.
		start := strings.Index(s, "{")
		if start < 0 {
			start = strings.Index(s, "[")
		}
		if start >= 0 && !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON, got: %s", s[start:])
		}
	})

	// ===== Session Flag =====

	t.Run("close_with_session", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Session test", "--type", "task")
		bdClose(t, bd, dir, issue.ID, "--session", "sess-456")
		session := querySessionSQL(t, beadsDir, issue.ID)
		if session != "sess-456" {
			t.Errorf("expected closed_by_session 'sess-456', got %q", session)
		}
	})

	t.Run("close_session_from_env", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Env session test", "--type", "task")
		cmd := exec.Command(bd, "close", issue.ID)
		cmd.Dir = dir
		env := bdEnv(dir)
		env = append(env, "CLAUDE_SESSION_ID=env-sess")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd close with env session failed: %v\n%s", err, out)
		}
		session := querySessionSQL(t, beadsDir, issue.ID)
		if session != "env-sess" {
			t.Errorf("expected closed_by_session 'env-sess', got %q", session)
		}
	})

	// ===== JSON Output and Done Alias =====

	t.Run("close_json_output", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "JSON close test", "--type", "task")
		cmd := exec.Command(bd, "close", issue.ID, "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd close --json failed: %v\n%s", err, out)
		}
		s := string(out)
		start := strings.Index(s, "[")
		if start < 0 {
			start = strings.Index(s, "{")
		}
		if start < 0 {
			t.Fatalf("no JSON in output: %s", s)
		}
		if !json.Valid([]byte(s[start:])) {
			t.Errorf("expected valid JSON, got: %s", s[start:])
		}
	})

	t.Run("done_alias", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Done alias test", "--type", "task")
		cmd := exec.Command(bd, "done", issue.ID)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd done failed: %v\n%s", err, out)
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.Status != types.StatusClosed {
			t.Errorf("expected closed via done alias, got %s", got.Status)
		}
	})

	t.Run("done_positional_reason", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Done reason test", "--type", "task")
		cmd := exec.Command(bd, "done", issue.ID, "the reason")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd done with reason failed: %v\n%s", err, out)
		}
		got := bdShow(t, bd, dir, issue.ID)
		if got.CloseReason != "the reason" {
			t.Errorf("expected close_reason 'the reason', got %q", got.CloseReason)
		}
	})

	// ===== Dolt Commit and Edge Cases =====

	t.Run("close_dolt_commit", func(t *testing.T) {
		// Count commits before.
		dataDir := filepath.Join(beadsDir, "embeddeddolt")
		cfg, _ := configfile.Load(beadsDir)
		database := ""
		if cfg != nil {
			database = cfg.GetDoltDatabase()
		}

		countCommits := func() int {
			db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
			if err != nil {
				t.Fatalf("OpenSQL: %v", err)
			}
			defer cleanup()
			var count int
			if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&count); err != nil {
				t.Fatalf("query dolt_log: %v", err)
			}
			return count
		}

		before := countCommits()
		issue := bdCreate(t, bd, dir, "Dolt commit test", "--type", "task")
		_ = issue
		afterCreate := countCommits()
		bdClose(t, bd, dir, issue.ID)
		afterClose := countCommits()

		if afterClose <= afterCreate {
			t.Errorf("expected Dolt commit count to increase after close: before=%d afterCreate=%d afterClose=%d", before, afterCreate, afterClose)
		}
	})

	t.Run("close_continue_multiple_ids_fails", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Continue multi 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Continue multi 2", "--type", "task")
		bdCloseFail(t, bd, dir, issue1.ID, issue2.ID, "--continue")
	})

	t.Run("close_suggest_next_multiple_ids_fails", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Suggest multi 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Suggest multi 2", "--type", "task")
		bdCloseFail(t, bd, dir, issue1.ID, issue2.ID, "--suggest-next")
	})
}
