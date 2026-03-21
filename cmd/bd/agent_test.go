//go:build cgo

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
