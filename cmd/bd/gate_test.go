package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestShouldCheckGate(t *testing.T) {
	tests := []struct {
		name       string
		awaitType  string
		typeFilter string
		want       bool
	}{
		// Empty filter matches all
		{"empty filter matches gh:run", "gh:run", "", true},
		{"empty filter matches gh:pr", "gh:pr", "", true},
		{"empty filter matches timer", "timer", "", true},
		{"empty filter matches human", "human", "", true},
		{"empty filter matches bead", "bead", "", true},

		// "all" filter matches all
		{"all filter matches gh:run", "gh:run", "all", true},
		{"all filter matches gh:pr", "gh:pr", "all", true},
		{"all filter matches timer", "timer", "all", true},
		{"all filter matches bead", "bead", "all", true},

		// "gh" filter matches all GitHub types
		{"gh filter matches gh:run", "gh:run", "gh", true},
		{"gh filter matches gh:pr", "gh:pr", "gh", true},
		{"gh filter does not match timer", "timer", "gh", false},
		{"gh filter does not match human", "human", "gh", false},
		{"gh filter does not match bead", "bead", "gh", false},

		// Exact type filters
		{"gh:run filter matches gh:run", "gh:run", "gh:run", true},
		{"gh:run filter does not match gh:pr", "gh:pr", "gh:run", false},
		{"gh:pr filter matches gh:pr", "gh:pr", "gh:pr", true},
		{"gh:pr filter does not match gh:run", "gh:run", "gh:pr", false},
		{"timer filter matches timer", "timer", "timer", true},
		{"timer filter does not match gh:run", "gh:run", "timer", false},
		{"bead filter matches bead", "bead", "bead", true},
		{"bead filter does not match timer", "timer", "bead", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{
				AwaitType: tt.awaitType,
			}
			got := shouldCheckGate(gate, tt.typeFilter)
			if got != tt.want {
				t.Errorf("shouldCheckGate(%q, %q) = %v, want %v",
					tt.awaitType, tt.typeFilter, got, tt.want)
			}
		})
	}
}

func TestCheckBeadGate_InvalidFormat(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		awaitID string
	}{
		{name: "empty", awaitID: ""},
		{name: "no colon", awaitID: "my-project-mp-abc"},
		{name: "missing rig", awaitID: ":gt-abc"},
		{name: "missing bead", awaitID: "my-project:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			satisfied, reason := checkBeadGate(ctx, tt.awaitID)
			if satisfied {
				t.Errorf("expected not satisfied for %q", tt.awaitID)
			}
			if reason == "" {
				t.Error("expected reason to be set")
			}
			if !gateTestContainsIgnoreCase(reason, "multi-rig routing removed") {
				t.Errorf("reason %q does not contain %q", reason, "multi-rig routing removed")
			}
		})
	}
}

func TestCheckBeadGate_RigNotFound(t *testing.T) {
	ctx := context.Background()

	// With multi-rig routing removed, all bead gates return the same message
	satisfied, reason := checkBeadGate(ctx, "nonexistent:some-id")
	if satisfied {
		t.Error("expected not satisfied for non-existent rig")
	}
	if reason == "" {
		t.Error("expected reason to be set")
	}
	if !gateTestContainsIgnoreCase(reason, "multi-rig routing removed") {
		t.Errorf("reason %q does not contain %q", reason, "multi-rig routing removed")
	}
}

func TestCheckBeadGate_TargetClosed(t *testing.T) {
	t.Skip("SQLite-specific: created SQLite DB directly; full integration testing requires routes.jsonl + Dolt rig infrastructure")
}

func TestIsNumericID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Numeric IDs
		{"12345", true},
		{"12345678901234567890", true},
		{"0", true},
		{"1", true},

		// Non-numeric (workflow names, etc.)
		{"", false},
		{"release.yml", false},
		{"CI", false},
		{"release", false},
		{"123abc", false},
		{"abc123", false},
		{"12.34", false},
		{"-123", false},
		{"123-456", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isNumericID(tt.input)
			if got != tt.want {
				t.Errorf("isNumericID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNeedsDiscovery(t *testing.T) {
	tests := []struct {
		name      string
		awaitType string
		awaitID   string
		want      bool
	}{
		// gh:run gates
		{"gh:run empty await_id", "gh:run", "", true},
		{"gh:run workflow name hint", "gh:run", "release.yml", true},
		{"gh:run workflow name without ext", "gh:run", "CI", true},
		{"gh:run numeric run ID", "gh:run", "12345", false},
		{"gh:run large numeric ID", "gh:run", "12345678901234567890", false},

		// Other gate types should not need discovery
		{"gh:pr gate", "gh:pr", "", false},
		{"timer gate", "timer", "", false},
		{"human gate", "human", "", false},
		{"bead gate", "bead", "rig:id", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{
				AwaitType: tt.awaitType,
				AwaitID:   tt.awaitID,
			}
			got := needsDiscovery(gate)
			if got != tt.want {
				t.Errorf("needsDiscovery(%q, %q) = %v, want %v",
					tt.awaitType, tt.awaitID, got, tt.want)
			}
		})
	}
}

func TestGetWorkflowNameHint(t *testing.T) {
	tests := []struct {
		name    string
		awaitID string
		want    string
	}{
		{"empty", "", ""},
		{"numeric ID", "12345", ""},
		{"workflow name", "release.yml", "release.yml"},
		{"workflow name yaml", "ci.yaml", "ci.yaml"},
		{"workflow name no ext", "CI", "CI"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{AwaitID: tt.awaitID}
			got := getWorkflowNameHint(gate)
			if got != tt.want {
				t.Errorf("getWorkflowNameHint(%q) = %q, want %q", tt.awaitID, got, tt.want)
			}
		})
	}
}

func TestWorkflowNameMatches(t *testing.T) {
	tests := []struct {
		name         string
		hint         string
		workflowName string
		runName      string
		want         bool
	}{
		// Exact matches
		{"exact workflow name", "Release", "Release", "release.yml", true},
		{"exact run name", "release.yml", "Release", "release.yml", true},
		{"case insensitive workflow", "release", "Release", "release.yml", true},
		{"case insensitive run", "RELEASE.YML", "Release", "release.yml", true},

		// Hint with suffix, match display name without
		{"hint yml vs display name", "release.yml", "release", "ci.yml", true},
		{"hint yaml vs display name", "release.yaml", "release", "ci.yaml", true},

		// Hint without suffix, match filename with suffix
		{"hint base vs filename yml", "release", "CI", "release.yml", true},
		{"hint base vs filename yaml", "release", "CI", "release.yaml", true},

		// No match
		{"no match different name", "release", "CI", "ci.yml", false},
		{"no match partial", "rel", "Release", "release.yml", false},
		{"empty hint", "", "Release", "release.yml", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowNameMatches(tt.hint, tt.workflowName, tt.runName)
			if got != tt.want {
				t.Errorf("workflowNameMatches(%q, %q, %q) = %v, want %v",
					tt.hint, tt.workflowName, tt.runName, got, tt.want)
			}
		})
	}
}

// gateTestContainsIgnoreCase checks if haystack contains needle (case-insensitive)
func gateTestContainsIgnoreCase(haystack, needle string) bool {
	return gateTestContains(gateTestLowerCase(haystack), gateTestLowerCase(needle))
}

func gateTestContains(s, substr string) bool {
	return len(s) >= len(substr) && gateTestFindSubstring(s, substr) >= 0
}

func gateTestLowerCase(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

func gateTestFindSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
