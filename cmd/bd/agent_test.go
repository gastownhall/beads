package main

import (
	"os"
	"path/filepath"
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
	// Set up config with agent roles for testing
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Write config with test agent roles
	configContent := `
agent_roles:
  town_level: "mayor,deacon"
  rig_level: "witness,refinery"
  named: "crew,polecat"
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Change to tmp directory and initialize config
	t.Chdir(tmpDir)

	initConfigForTest(t)

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
		// Hyphenated rig names (GH#868)
		{
			name:         "polecat with hyphenated rig",
			agentID:      "gt-my-project-polecat-nux",
			wantRoleType: "polecat",
			wantRig:      "my-project",
		},
		{
			name:         "crew with hyphenated rig",
			agentID:      "bd-infra-dashboard-crew-alice",
			wantRoleType: "crew",
			wantRig:      "infra-dashboard",
		},
		{
			name:         "witness with hyphenated rig",
			agentID:      "gt-my-cool-rig-witness",
			wantRoleType: "witness",
			wantRig:      "my-cool-rig",
		},
		{
			name:         "refinery with hyphenated rig",
			agentID:      "bd-super-long-rig-name-refinery",
			wantRoleType: "refinery",
			wantRig:      "super-long-rig-name",
		},
		{
			name:         "polecat with multi-hyphen rig and name",
			agentID:      "gt-my-awesome-project-polecat-worker-1",
			wantRoleType: "polecat",
			wantRig:      "my-awesome-project",
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
