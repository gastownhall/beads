//go:build cgo && integration
// +build cgo,integration

// Package main provides the bd CLI commands.
package main

import (
	"testing"
)

// TestGitLabConflictStrategiesIntegration tests conflict strategy selection.
func TestGitLabConflictStrategiesIntegration(t *testing.T) {
	// Test prefer-newer strategy (default)
	t.Run("PreferNewer", func(t *testing.T) {
		strategy, err := getConflictStrategy(false, false, true)
		if err != nil {
			t.Fatalf("getConflictStrategy() error = %v", err)
		}
		if strategy != ConflictStrategyPreferNewer {
			t.Errorf("strategy = %q, want %q", strategy, ConflictStrategyPreferNewer)
		}
	})

	// Test prefer-local strategy
	t.Run("PreferLocal", func(t *testing.T) {
		strategy, err := getConflictStrategy(true, false, false)
		if err != nil {
			t.Fatalf("getConflictStrategy() error = %v", err)
		}
		if strategy != ConflictStrategyPreferLocal {
			t.Errorf("strategy = %q, want %q", strategy, ConflictStrategyPreferLocal)
		}
	})

	// Test prefer-gitlab strategy
	t.Run("PreferGitLab", func(t *testing.T) {
		strategy, err := getConflictStrategy(false, true, false)
		if err != nil {
			t.Fatalf("getConflictStrategy() error = %v", err)
		}
		if strategy != ConflictStrategyPreferGitLab {
			t.Errorf("strategy = %q, want %q", strategy, ConflictStrategyPreferGitLab)
		}
	})

	// Test default (no flags) returns prefer-newer
	t.Run("DefaultIsPreferNewer", func(t *testing.T) {
		strategy, err := getConflictStrategy(false, false, false)
		if err != nil {
			t.Fatalf("getConflictStrategy() error = %v", err)
		}
		if strategy != ConflictStrategyPreferNewer {
			t.Errorf("strategy = %q, want %q (default)", strategy, ConflictStrategyPreferNewer)
		}
	})

	// Test multiple flags returns error
	t.Run("MultipleFlags", func(t *testing.T) {
		_, err := getConflictStrategy(true, true, false)
		if err == nil {
			t.Error("Expected error for multiple conflicting flags")
		}
	})
}
