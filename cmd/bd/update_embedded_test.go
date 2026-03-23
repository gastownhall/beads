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

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// ===== Shared test helpers (used by both update and close tests) =====

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

// ===== Update tests =====

func TestEmbeddedUpdate(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

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

// TestEmbeddedUpdateConcurrent exercises create, update, and list operations
// concurrently to verify EmbeddedDoltStore handles concurrent CLI invocations
// without panics, data corruption, or deadlocks.
func TestEmbeddedUpdateConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cu")

	const (
		numWorkers      = 10
		issuesPerWorker = 5
	)

	type workerResult struct {
		worker     int
		ids        []string
		listCounts []int
		err        error
	}

	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}

			for i := 0; i < issuesPerWorker; i++ {
				// Create an issue.
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
				r.ids = append(r.ids, id)

				// Update: change status to in_progress.
				uCmd := exec.Command(bd, "update", id, "--status", "in_progress")
				uCmd.Dir = dir
				uCmd.Env = bdEnv(dir)
				uOut, err := uCmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("update status %d: %v\n%s", i, err, uOut)
					results[worker] = r
					return
				}

				// Update: set priority and assignee.
				uCmd2 := exec.Command(bd, "update", id, "--priority", fmt.Sprintf("%d", worker%4), "--assignee", fmt.Sprintf("agent-%d", worker))
				uCmd2.Dir = dir
				uCmd2.Env = bdEnv(dir)
				uOut2, err := uCmd2.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("update fields %d: %v\n%s", i, err, uOut2)
					results[worker] = r
					return
				}

				// Update: add a label.
				uCmd3 := exec.Command(bd, "update", id, "--add-label", fmt.Sprintf("team-%d", worker%3))
				uCmd3.Dir = dir
				uCmd3.Env = bdEnv(dir)
				uOut3, err := uCmd3.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("update label %d: %v\n%s", i, err, uOut3)
					results[worker] = r
					return
				}

				// List to verify consistency (interleaved with writes).
				listCmd := exec.Command(bd, "list", "--json", "--limit", "0")
				listCmd.Dir = dir
				listCmd.Env = bdEnv(dir)
				listOut, err := listCmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("list after update %d: %v\n%s", i, err, listOut)
					results[worker] = r
					return
				}
				s := string(listOut)
				start := strings.Index(s, "[")
				if start < 0 {
					r.listCounts = append(r.listCounts, 0)
					continue
				}
				var issues []json.RawMessage
				if jsonErr := json.Unmarshal([]byte(s[start:]), &issues); jsonErr != nil {
					r.err = fmt.Errorf("list parse %d: %v\nraw: %s", i, jsonErr, s)
					results[worker] = r
					return
				}
				r.listCounts = append(r.listCounts, len(issues))
			}

			results[worker] = r
		}(w)
	}
	wg.Wait()

	// Check for errors and collect IDs.
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

	// Verify all issues exist and were updated correctly.
	store := openStore(t, beadsDir, "cu")
	stats, err := store.GetStatistics(t.Context())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if stats.TotalIssues < expectedTotal {
		t.Errorf("expected at least %d issues in DB, got %d", expectedTotal, stats.TotalIssues)
	}

	// Spot-check: every issue should be in_progress with an assignee.
	for id := range allIDs {
		issue, err := store.GetIssue(t.Context(), id)
		if err != nil {
			t.Errorf("GetIssue(%s): %v", id, err)
			continue
		}
		if issue.Status != types.StatusInProgress {
			t.Errorf("issue %s: expected status in_progress, got %s", id, issue.Status)
		}
		if issue.Assignee == "" {
			t.Errorf("issue %s: expected assignee to be set", id)
		}
	}

	// Verify list counts were monotonically non-decreasing per worker.
	for _, r := range results {
		if r.err != nil {
			continue
		}
		for i := 1; i < len(r.listCounts); i++ {
			if r.listCounts[i] < r.listCounts[i-1] {
				t.Errorf("worker %d: list count decreased from %d to %d at step %d",
					r.worker, r.listCounts[i-1], r.listCounts[i], i)
			}
		}
	}

	t.Logf("created and updated %d issues across %d concurrent workers, %d in DB",
		len(allIDs), numWorkers, stats.TotalIssues)
}
