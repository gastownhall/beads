package main

import (
	"testing"
)

func TestValidAgentStates(t *testing.T) {
	// Test that all expected states are valid
	expectedStates := []string{
		"idle", "spawning", "running", "working",
		"stuck", "done", "stopped", "dead",
	}

	for _, state := range expectedStates {
		if !validAgentStates[state] {
			t.Errorf("expected state %q to be valid, but it's not", state)
		}
	}
}

func TestInvalidAgentStates(t *testing.T) {
	// Test that invalid states are rejected
	invalidStates := []string{
		"starting", "waiting", "active", "inactive",
		"unknown", "error", "RUNNING", "Idle",
	}

	for _, state := range invalidStates {
		if validAgentStates[state] {
			t.Errorf("expected state %q to be invalid, but it's valid", state)
		}
	}
}

func TestAgentStateCount(t *testing.T) {
	// Verify we have exactly 8 valid states
	expectedCount := 8
	actualCount := len(validAgentStates)
	if actualCount != expectedCount {
		t.Errorf("expected %d valid states, got %d", expectedCount, actualCount)
	}
}

func TestFormatTimeOrNil(t *testing.T) {
	// Test nil case
	result := formatTimeOrNil(nil)
	if result != nil {
		t.Errorf("expected nil for nil time, got %v", result)
	}
}

func TestParseAgentIDFields(t *testing.T) {
	tests := []struct {
		name         string
		agentID      string
		wantRoleType string
		wantRig      string
	}{
		// Town-level roles
		{
			name:         "town-level mayor",
			agentID:      "gt-mayor",
			wantRoleType: "mayor",
			wantRig:      "",
		},
		{
			name:         "town-level deacon",
			agentID:      "bd-deacon",
			wantRoleType: "deacon",
			wantRig:      "",
		},
		// Per-rig singleton roles
		{
			name:         "rig-level witness",
			agentID:      "gt-gastown-witness",
			wantRoleType: "witness",
			wantRig:      "gastown",
		},
		{
			name:         "rig-level refinery",
			agentID:      "bd-beads-refinery",
			wantRoleType: "refinery",
			wantRig:      "beads",
		},
		// Per-rig named roles
		{
			name:         "named polecat",
			agentID:      "gt-gastown-polecat-nux",
			wantRoleType: "polecat",
			wantRig:      "gastown",
		},
		{
			name:         "named crew",
			agentID:      "bd-beads-crew-dave",
			wantRoleType: "crew",
			wantRig:      "beads",
		},
		{
			name:         "polecat with hyphenated name",
			agentID:      "gt-gastown-polecat-nux-123",
			wantRoleType: "polecat",
			wantRig:      "gastown",
		},
		// Edge cases
		{
			name:         "no hyphen",
			agentID:      "invalid",
			wantRoleType: "",
			wantRig:      "",
		},
		{
			name:         "empty string",
			agentID:      "",
			wantRoleType: "",
			wantRig:      "",
		},
		{
			name:         "unknown role",
			agentID:      "gt-gastown-unknown",
			wantRoleType: "",
			wantRig:      "",
		},
		{
			name:         "prefix only",
			agentID:      "gt-",
			wantRoleType: "",
			wantRig:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRoleType, gotRig := parseAgentIDFields(tt.agentID)
			if gotRoleType != tt.wantRoleType {
				t.Errorf("parseAgentIDFields(%q) roleType = %q, want %q", tt.agentID, gotRoleType, tt.wantRoleType)
			}
			if gotRig != tt.wantRig {
				t.Errorf("parseAgentIDFields(%q) rig = %q, want %q", tt.agentID, gotRig, tt.wantRig)
			}
		})
	}
}
