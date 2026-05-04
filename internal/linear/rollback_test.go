package linear

import (
	"strings"
	"testing"
)

func TestRollbackScript_NoMutations(t *testing.T) {
	script := RollbackScript(nil)
	if !strings.Contains(script, "No rollback") {
		t.Errorf("empty mutations should produce 'No rollback' message, got: %s", script)
	}
}

func TestRollbackScript_CreatedPull(t *testing.T) {
	mutations := []RollbackMutation{
		{BeadID: "bd-001", LinearID: "TEAM-1", Action: "delete_local", Direction: "pull"},
	}
	script := RollbackScript(mutations)
	if !strings.Contains(script, "bd delete bd-001") {
		t.Errorf("expected 'bd delete bd-001' in script, got:\n%s", script)
	}
}

func TestRollbackScript_CreatedPush(t *testing.T) {
	mutations := []RollbackMutation{
		{BeadID: "bd-002", LinearID: "TEAM-2", Action: "delete_remote", Direction: "push"},
	}
	script := RollbackScript(mutations)
	if !strings.Contains(script, "delete issue TEAM-2 from Linear") {
		t.Errorf("expected manual delete instruction for TEAM-2, got:\n%s", script)
	}
}

func TestRollbackScript_UpdatedPull(t *testing.T) {
	mutations := []RollbackMutation{
		{
			BeadID:    "bd-003",
			LinearID:  "TEAM-3",
			Action:    "restore_local",
			Direction: "pull",
			Fields:    map[string]string{"title": "Old Title", "status": "open"},
		},
	}
	script := RollbackScript(mutations)
	if !strings.Contains(script, "bd update bd-003") {
		t.Errorf("expected 'bd update bd-003' in script, got:\n%s", script)
	}
}

func TestRollbackCreated_Pull(t *testing.T) {
	item := SyncItem{BeadID: "bd-010", LinearID: "TEAM-10", Direction: "pull", Outcome: "created"}
	m := rollbackCreated(item)
	if m.Action != "delete_local" {
		t.Errorf("Action = %q, want %q", m.Action, "delete_local")
	}
	if m.BeadID != "bd-010" {
		t.Errorf("BeadID = %q, want %q", m.BeadID, "bd-010")
	}
}

func TestRollbackCreated_Push(t *testing.T) {
	item := SyncItem{BeadID: "bd-020", LinearID: "TEAM-20", Direction: "push", Outcome: "created"}
	m := rollbackCreated(item)
	if m.Action != "delete_remote" {
		t.Errorf("Action = %q, want %q", m.Action, "delete_remote")
	}
}

func TestRollbackUpdated_NoBeforeValues(t *testing.T) {
	item := SyncItem{BeadID: "bd-030", Direction: "pull", Outcome: "updated"}
	m := rollbackUpdated(item)
	if m != nil {
		t.Error("expected nil mutation when no before_values")
	}
}

func TestRollbackUpdated_WithBeforeValues(t *testing.T) {
	item := SyncItem{
		BeadID:       "bd-040",
		LinearID:     "TEAM-40",
		Direction:    "pull",
		Outcome:      "updated",
		BeforeValues: map[string]string{"title": "Original", "priority": "1"},
	}
	m := rollbackUpdated(item)
	if m == nil {
		t.Fatal("expected non-nil mutation")
	}
	if m.Action != "restore_local" {
		t.Errorf("Action = %q, want %q", m.Action, "restore_local")
	}
	if m.Fields["title"] != "Original" {
		t.Errorf("Fields[title] = %q, want %q", m.Fields["title"], "Original")
	}
}

func TestRollbackUpdated_Push(t *testing.T) {
	item := SyncItem{
		BeadID:       "bd-050",
		LinearID:     "TEAM-50",
		Direction:    "push",
		Outcome:      "updated",
		BeforeValues: map[string]string{"status": "open"},
	}
	m := rollbackUpdated(item)
	if m == nil {
		t.Fatal("expected non-nil mutation")
	}
	if m.Action != "restore_remote" {
		t.Errorf("Action = %q, want %q", m.Action, "restore_remote")
	}
}
