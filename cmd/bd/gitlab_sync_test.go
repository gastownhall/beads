// Package main provides the bd CLI commands.
package main

import (
	"strings"
	"testing"
)

// TestGenerateUniqueIssueIDs verifies IDs are unique even when generated rapidly.
func TestGenerateUniqueIssueIDs(t *testing.T) {
	seen := make(map[string]bool)
	prefix := "bd"

	// Generate 100 IDs rapidly
	for i := 0; i < 100; i++ {
		id := generateIssueID(prefix)
		if seen[id] {
			t.Errorf("Duplicate ID generated: %s", id)
		}
		seen[id] = true
	}
}

// TestGenerateIssueIDHasRandomComponent verifies IDs include random bytes for restart safety.
// Without random bytes, counter reset on restart could cause ID collisions.
func TestGenerateIssueIDHasRandomComponent(t *testing.T) {
	// Save current counter
	oldCounter := issueIDCounter
	defer func() { issueIDCounter = oldCounter }()

	prefix := "test"

	// Generate ID, reset counter (simulating restart), generate another
	// Both generated at same counter value - should still be unique due to random component
	issueIDCounter = 100
	id1 := generateIssueID(prefix)

	// Simulate restart by resetting counter to same value
	issueIDCounter = 100
	id2 := generateIssueID(prefix)

	// Even with same counter, IDs should differ due to random component
	if id1 == id2 {
		t.Errorf("IDs should be unique even with counter reset: id1=%s, id2=%s", id1, id2)
	}

	// Verify format includes hex suffix (random bytes)
	// Expected format: prefix-timestamp-counter-hex
	parts := strings.Split(id1, "-")
	if len(parts) != 4 {
		t.Errorf("Expected 4 parts (prefix-timestamp-counter-random), got %d: %s", len(parts), id1)
	}
}

// TestGetConflictStrategy verifies conflict strategy selection from flags.
func TestGetConflictStrategy(t *testing.T) {
	tests := []struct {
		name         string
		preferLocal  bool
		preferGitLab bool
		preferNewer  bool
		wantStrategy ConflictStrategy
		wantError    bool
	}{
		{
			name:         "no flags - default to prefer-newer",
			wantStrategy: ConflictStrategyPreferNewer,
		},
		{
			name:         "prefer-local",
			preferLocal:  true,
			wantStrategy: ConflictStrategyPreferLocal,
		},
		{
			name:         "prefer-gitlab",
			preferGitLab: true,
			wantStrategy: ConflictStrategyPreferGitLab,
		},
		{
			name:         "prefer-newer explicit",
			preferNewer:  true,
			wantStrategy: ConflictStrategyPreferNewer,
		},
		{
			name:         "multiple flags - error",
			preferLocal:  true,
			preferGitLab: true,
			wantError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			strategy, err := getConflictStrategy(tt.preferLocal, tt.preferGitLab, tt.preferNewer)
			if tt.wantError {
				if err == nil {
					t.Error("expected error for multiple flags, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strategy != tt.wantStrategy {
				t.Errorf("strategy = %q, want %q", strategy, tt.wantStrategy)
			}
		})
	}
}

// TestParseGitLabSourceSystem verifies parsing source system string.
func TestParseGitLabSourceSystem(t *testing.T) {
	tests := []struct {
		name          string
		sourceSystem  string
		wantProjectID int
		wantIID       int
		wantOK        bool
	}{
		{
			name:          "valid gitlab source",
			sourceSystem:  "gitlab:123:42",
			wantProjectID: 123,
			wantIID:       42,
			wantOK:        true,
		},
		{
			name:          "different project",
			sourceSystem:  "gitlab:456:99",
			wantProjectID: 456,
			wantIID:       99,
			wantOK:        true,
		},
		{
			name:          "non-gitlab source",
			sourceSystem:  "linear:ABC-123",
			wantProjectID: 0,
			wantIID:       0,
			wantOK:        false,
		},
		{
			name:          "empty source",
			sourceSystem:  "",
			wantProjectID: 0,
			wantIID:       0,
			wantOK:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectID, iid, ok := parseGitLabSourceSystem(tt.sourceSystem)
			if ok != tt.wantOK {
				t.Errorf("parseGitLabSourceSystem(%q) ok = %v, want %v", tt.sourceSystem, ok, tt.wantOK)
			}
			if projectID != tt.wantProjectID {
				t.Errorf("parseGitLabSourceSystem(%q) projectID = %d, want %d", tt.sourceSystem, projectID, tt.wantProjectID)
			}
			if iid != tt.wantIID {
				t.Errorf("parseGitLabSourceSystem(%q) iid = %d, want %d", tt.sourceSystem, iid, tt.wantIID)
			}
		})
	}
}
