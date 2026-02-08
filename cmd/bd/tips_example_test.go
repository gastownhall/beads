package main

import (
	"testing"
	"time"
)

// This file demonstrates example tip definitions for documentation purposes

func TestExampleTipDefinitions(t *testing.T) {
	// Example 1: High priority, high probability tip
	// Shows frequently when condition is met
	highPriorityTip := Tip{
		ID:          "example_high_priority",
		Condition:   func() bool { return true },
		Message:     "This is an important tip that shows often",
		Frequency:   24 * time.Hour, // Show at most once per day
		Priority:    100,            // Highest priority
		Probability: 0.8,            // 80% chance when eligible
	}

	// Example 2: Medium priority, medium probability tip
	// General feature discovery
	mediumPriorityTip := Tip{
		ID:          "example_medium_priority",
		Condition:   func() bool { return true },
		Message:     "Try using 'bd ready' to see available work",
		Frequency:   7 * 24 * time.Hour, // Show at most once per week
		Priority:    50,                 // Medium priority
		Probability: 0.5,                // 50% chance when eligible
	}

	// Example 3: Low priority, low probability tip
	// Nice-to-know information
	lowPriorityTip := Tip{
		ID:          "example_low_priority",
		Condition:   func() bool { return true },
		Message:     "You can filter issues by label with --label flag",
		Frequency:   30 * 24 * time.Hour, // Show at most once per month
		Priority:    10,                  // Low priority
		Probability: 0.2,                 // 20% chance when eligible
	}

	// Example 4: Conditional tip
	// Only shows when specific condition is true
	conditionalTip := Tip{
		ID: "example_conditional",
		Condition: func() bool {
			// Example: Only show if some condition is met
			// In real usage, this might check for specific state
			return false // Disabled for this example
		},
		Message:     "This tip only shows when condition is met",
		Frequency:   24 * time.Hour,
		Priority:    80,
		Probability: 0.6,
	}

	// Verify tips are properly structured (basic validation)
	tips := []Tip{highPriorityTip, mediumPriorityTip, lowPriorityTip, conditionalTip}

	for _, tip := range tips {
		if tip.ID == "" {
			t.Error("Tip ID should not be empty")
		}
		if tip.Message == "" {
			t.Error("Tip message should not be empty")
		}
		if tip.Condition == nil {
			t.Error("Tip condition function should not be nil")
		}
		if tip.Frequency < 0 {
			t.Error("Tip frequency should not be negative")
		}
		if tip.Probability < 0 || tip.Probability > 1 {
			t.Errorf("Tip probability should be between 0 and 1, got %f", tip.Probability)
		}
	}
}

// Example showing probability guidelines
func TestProbabilityGuidelines(t *testing.T) {
	examples := []struct {
		name        string
		probability float64
		useCase     string
	}{
		{"Critical", 1.0, "Security alerts, breaking changes"},
		{"High", 0.8, "Important updates, major features"},
		{"Medium", 0.5, "General tips, workflow improvements"},
		{"Low", 0.2, "Nice-to-know, advanced features"},
	}

	for _, ex := range examples {
		if ex.probability < 0 || ex.probability > 1 {
			t.Errorf("%s: probability %f out of range", ex.name, ex.probability)
		}
	}
}
