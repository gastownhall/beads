//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerHistory(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "hst")

	// Seed: create an issue then mutate it several times to build commit history.
	issue := bdProxiedCreate(t, bd, p.dir, "History test issue", "--type", "task", "--priority", "3")
	bdProxiedUpdate(t, bd, p.dir, issue.ID, "--status", "in_progress")
	bdProxiedUpdate(t, bd, p.dir, issue.ID, "--priority", "1")
	bdProxiedUpdate(t, bd, p.dir, issue.ID, "--title", "History test issue updated")

	const missingID = "hst-nonexistent999"

	// historyJSON parses commit-history (or --events) JSON into a generic slice.
	historyJSON := func(t *testing.T, args ...string) []map[string]interface{} {
		t.Helper()
		out, err := bdProxiedRun(t, bd, p.dir, append([]string{"history", "--json"}, args...)...)
		if err != nil {
			t.Fatalf("history --json %v: %v\n%s", args, err, out)
		}
		s := strings.TrimSpace(string(out))
		start := strings.Index(s, "[")
		if start < 0 {
			return nil
		}
		var entries []map[string]interface{}
		if err := json.Unmarshal([]byte(s[start:]), &entries); err != nil {
			t.Fatalf("parse history JSON: %v\n%s", err, s)
		}
		return entries
	}

	// ===== Basic plain-text history =====

	t.Run("basic_history", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "history", issue.ID)
		if err != nil {
			t.Fatalf("history %s: %v\n%s", issue.ID, err, out)
		}
		s := string(out)
		if !strings.Contains(s, issue.ID) {
			t.Errorf("expected issue ID in history output: %s", s)
		}
		if !strings.Contains(s, "History for") {
			t.Errorf("expected 'History for' header: %s", s)
		}
		if !strings.Contains(s, "Author:") {
			t.Errorf("expected 'Author:' in history output: %s", s)
		}
	})

	t.Run("history_shows_multiple_entries", func(t *testing.T) {
		entries := historyJSON(t, issue.ID)
		// create + 3 updates = at least 4 commits touching this issue.
		if len(entries) < 4 {
			t.Errorf("expected at least 4 history entries, got %d", len(entries))
		}
	})

	// ===== --limit restricts entries =====

	t.Run("limit_restricts_entries", func(t *testing.T) {
		entries := historyJSON(t, issue.ID, "--limit", "2")
		if len(entries) > 2 {
			t.Errorf("expected at most 2 entries with --limit 2, got %d", len(entries))
		}
		if len(entries) == 0 {
			t.Error("expected at least 1 entry")
		}
	})

	t.Run("limit_1", func(t *testing.T) {
		entries := historyJSON(t, issue.ID, "--limit", "1")
		if len(entries) != 1 {
			t.Errorf("expected exactly 1 entry with --limit 1, got %d", len(entries))
		}
	})

	// ===== --events audit output =====

	t.Run("events_json_output", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "history", issue.ID, "--events", "--json")
		if err != nil {
			t.Fatalf("history --events --json: %v\n%s", err, out)
		}
		var events []types.Event
		if err := json.Unmarshal(out, &events); err != nil {
			t.Fatalf("unmarshal events: %v\n%s", err, out)
		}
		if len(events) == 0 {
			t.Fatal("expected non-empty history events")
		}
		var sawCreated, sawStatus bool
		for _, e := range events {
			if e.IssueID != issue.ID {
				t.Errorf("event for wrong issue: %s", e.IssueID)
			}
			if e.EventType == types.EventCreated {
				sawCreated = true
			}
			if e.EventType == types.EventStatusChanged {
				sawStatus = true
			}
		}
		if !sawCreated {
			t.Errorf("expected a %q event, got %+v", types.EventCreated, events)
		}
		if !sawStatus {
			t.Errorf("expected a %q event, got %+v", types.EventStatusChanged, events)
		}
	})

	t.Run("events_limit", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "history", issue.ID, "--events", "--limit", "1", "--json")
		if err != nil {
			t.Fatalf("history --events --limit 1 --json: %v\n%s", err, out)
		}
		var events []types.Event
		if err := json.Unmarshal(out, &events); err != nil {
			t.Fatalf("unmarshal events: %v\n%s", err, out)
		}
		if len(events) != 1 {
			t.Errorf("expected exactly 1 event with --limit 1, got %d", len(events))
		}
	})

	// ===== Commit-history --json shape =====

	t.Run("json_output_structure", func(t *testing.T) {
		entries := historyJSON(t, issue.ID)
		if len(entries) == 0 {
			t.Fatal("expected non-empty history")
		}
		e := entries[0]
		for _, key := range []string{"CommitHash", "CommitDate", "Committer", "Issue"} {
			if _, ok := e[key]; !ok {
				t.Errorf("expected %q key in history entry", key)
			}
		}
	})

	t.Run("json_issue_snapshot_has_fields", func(t *testing.T) {
		entries := historyJSON(t, issue.ID)
		if len(entries) == 0 {
			t.Fatal("expected non-empty history")
		}
		// Most recent entry should carry the updated title.
		issueMap, ok := entries[0]["Issue"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected Issue to be a map, got %T", entries[0]["Issue"])
		}
		if issueMap["title"] != "History test issue updated" {
			t.Errorf("expected latest title 'History test issue updated', got %v", issueMap["title"])
		}
	})

	t.Run("commit_history_wrong_issue_guard", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "history", issue.ID, "--json")
		if err != nil {
			t.Fatalf("history --json: %v\n%s", err, out)
		}
		var entries []struct {
			CommitHash string `json:"CommitHash"`
			Issue      *struct {
				ID string `json:"id"`
			} `json:"Issue"`
		}
		if err := json.Unmarshal(out, &entries); err != nil {
			t.Fatalf("unmarshal history: %v\n%s", err, out)
		}
		for _, e := range entries {
			if e.CommitHash == "" {
				t.Error("entry missing CommitHash")
			}
			if e.Issue != nil && e.Issue.ID != issue.ID {
				t.Errorf("history entry for wrong issue: %s", e.Issue.ID)
			}
		}
	})

	// ===== Nonexistent issue =====

	t.Run("nonexistent_issue_empty_history", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "history", missingID)
		if err != nil {
			t.Fatalf("history %s: %v\n%s", missingID, err, out)
		}
		if !strings.Contains(string(out), "No history") {
			t.Errorf("expected 'No history' message for nonexistent issue, got: %s", out)
		}
	})

	// --json must always produce parseable JSON, even when history is empty.
	t.Run("nonexistent_issue_json_returns_empty_array", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "history", "--json", missingID)
		if err != nil {
			t.Fatalf("history --json %s: %v\n%s", missingID, err, out)
		}
		var entries []map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &entries); err != nil {
			t.Fatalf("expected valid JSON for empty history, got prose:\n%s\n(parse error: %v)", out, err)
		}
		if len(entries) != 0 {
			t.Errorf("expected empty array for nonexistent issue, got %d entries", len(entries))
		}
	})

	// --limit combined with --json on empty history must still parse to empty.
	t.Run("nonexistent_issue_json_with_limit_returns_empty_array", func(t *testing.T) {
		out, err := bdProxiedRun(t, bd, p.dir, "history", "--json", "--limit", "2", missingID)
		if err != nil {
			t.Fatalf("history --json --limit 2 %s: %v\n%s", missingID, err, out)
		}
		var entries []map[string]interface{}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &entries); err != nil {
			t.Fatalf("expected valid JSON for empty history with --limit, got prose:\n%s\n(parse error: %v)", out, err)
		}
		if len(entries) != 0 {
			t.Errorf("expected empty array for nonexistent issue with --limit, got %d entries", len(entries))
		}
	})

	// ===== Argument validation =====

	t.Run("no_args_fails", func(t *testing.T) {
		if out, err := bdProxiedRun(t, bd, p.dir, "history"); err == nil {
			t.Fatalf("expected 'history' with no args to fail, got:\n%s", out)
		}
	})

	t.Run("too_many_args_fails", func(t *testing.T) {
		if out, err := bdProxiedRun(t, bd, p.dir, "history", issue.ID, "extra"); err == nil {
			t.Fatalf("expected 'history %s extra' to fail, got:\n%s", issue.ID, out)
		}
	})

	// ===== Newly created issue has history =====

	t.Run("single_entry_for_new_issue", func(t *testing.T) {
		fresh := bdProxiedCreate(t, bd, p.dir, "Fresh issue no updates", "--type", "task")
		entries := historyJSON(t, fresh.ID)
		if len(entries) < 1 {
			t.Error("expected at least 1 history entry for a newly created issue")
		}
	})
}
