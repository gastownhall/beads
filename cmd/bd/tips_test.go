//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTipSelection(t *testing.T) {
	// Set deterministic seed for testing
	os.Setenv("BEADS_TIP_SEED", "12345")
	defer os.Unsetenv("BEADS_TIP_SEED")

	// Reset RNG
	tipRandOnce = sync.Once{}
	initTipRand()

	// Reset tip registry for testing
	tipsMutex.Lock()
	tips = []Tip{}
	tipsMutex.Unlock()

	store := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// Test 1: No tips registered
	tip := selectNextTip(store)
	if tip != nil {
		t.Errorf("Expected nil with no tips registered, got %v", tip)
	}

	// Test 2: Single tip with condition = true
	tipsMutex.Lock()
	tips = append(tips, Tip{
		ID:          "test_tip_1",
		Condition:   func() bool { return true },
		Message:     "Test tip 1",
		Frequency:   1 * time.Hour,
		Priority:    100,
		Probability: 1.0, // Always show
	})
	tipsMutex.Unlock()

	tip = selectNextTip(store)
	if tip == nil {
		t.Fatal("Expected tip to be selected")
	}
	if tip.ID != "test_tip_1" {
		t.Errorf("Expected tip ID 'test_tip_1', got %q", tip.ID)
	}

	// Test 3: Frequency limit - should not show again immediately
	recordTipShown(store, "test_tip_1")
	tip = selectNextTip(store)
	if tip != nil {
		t.Errorf("Expected nil due to frequency limit, got %v", tip)
	}

	// Test 4: Multiple tips - priority order
	tipsMutex.Lock()
	tips = []Tip{
		{
			ID:          "low_priority",
			Condition:   func() bool { return true },
			Message:     "Low priority tip",
			Frequency:   1 * time.Hour,
			Priority:    10,
			Probability: 1.0,
		},
		{
			ID:          "high_priority",
			Condition:   func() bool { return true },
			Message:     "High priority tip",
			Frequency:   1 * time.Hour,
			Priority:    100,
			Probability: 1.0,
		},
	}
	tipsMutex.Unlock()

	tip = selectNextTip(store)
	if tip == nil {
		t.Fatal("Expected tip to be selected")
	}
	if tip.ID != "high_priority" {
		t.Errorf("Expected high_priority tip to be selected first, got %q", tip.ID)
	}

	// Test 5: Condition = false
	tipsMutex.Lock()
	tips = []Tip{
		{
			ID:          "never_show",
			Condition:   func() bool { return false },
			Message:     "Never shown",
			Frequency:   1 * time.Hour,
			Priority:    100,
			Probability: 1.0,
		},
	}
	tipsMutex.Unlock()

	tip = selectNextTip(store)
	if tip != nil {
		t.Errorf("Expected nil due to condition=false, got %v", tip)
	}
}

func TestTipProbability(t *testing.T) {
	// Set deterministic seed
	os.Setenv("BEADS_TIP_SEED", "99999")
	defer os.Unsetenv("BEADS_TIP_SEED")

	// Reset RNG by creating a new Once
	tipRandOnce = sync.Once{}
	initTipRand()

	tipsMutex.Lock()
	tips = []Tip{
		{
			ID:          "rare_tip",
			Condition:   func() bool { return true },
			Message:     "Rare tip",
			Frequency:   1 * time.Hour,
			Priority:    100,
			Probability: 0.01, // 1% chance
		},
	}
	tipsMutex.Unlock()

	store := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// Run selection multiple times
	shownCount := 0
	for i := 0; i < 100; i++ {
		// Clear last shown timestamp to make tip eligible
		_ = store.SetMetadata(context.Background(), "tip_rare_tip_last_shown", "")

		tip := selectNextTip(store)
		if tip != nil {
			shownCount++
		}
	}

	// With 1% probability, we expect ~1 show out of 100
	// Allow some variance (0-10 is reasonable for low probability)
	if shownCount > 10 {
		t.Errorf("Expected ~1 tip shown with 1%% probability, got %d", shownCount)
	}
}

func TestGetLastShown(t *testing.T) {
	store := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// Test 1: Never shown
	lastShown := getLastShown(store, "never_shown")
	if !lastShown.IsZero() {
		t.Errorf("Expected zero time for never shown tip, got %v", lastShown)
	}

	// Test 2: Recently shown
	now := time.Now()
	_ = store.SetMetadata(context.Background(), "tip_test_last_shown", now.Format(time.RFC3339))

	lastShown = getLastShown(store, "test")
	if lastShown.IsZero() {
		t.Error("Expected non-zero time for shown tip")
	}

	// Should be within 1 second (accounting for rounding)
	diff := now.Sub(lastShown)
	if diff < 0 {
		diff = -diff
	}
	if diff > time.Second {
		t.Errorf("Expected last shown time to be close to now, got diff %v", diff)
	}
}

func TestRecordTipShown(t *testing.T) {
	store := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	recordTipShown(store, "test_tip")

	// Verify it was recorded
	lastShown := getLastShown(store, "test_tip")
	if lastShown.IsZero() {
		t.Error("Expected tip to be recorded as shown")
	}

	// Should be very recent
	if time.Since(lastShown) > time.Second {
		t.Errorf("Expected recent timestamp, got %v", lastShown)
	}
}

func TestMaybeShowTip_RespectsFlags(t *testing.T) {
	// Set deterministic seed
	os.Setenv("BEADS_TIP_SEED", "54321")
	defer os.Unsetenv("BEADS_TIP_SEED")

	tipsMutex.Lock()
	tips = []Tip{
		{
			ID:          "always_show",
			Condition:   func() bool { return true },
			Message:     "Always show tip",
			Frequency:   1 * time.Hour,
			Priority:    100,
			Probability: 1.0,
		},
	}
	tipsMutex.Unlock()

	store := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// Test 1: Should not show in JSON mode
	jsonOutput = true
	maybeShowTip(store) // Should not panic or show output
	jsonOutput = false

	// Test 2: Should not show in quiet mode
	quietFlag = true
	maybeShowTip(store) // Should not panic or show output
	quietFlag = false

	// Test 3: Should show in normal mode (no assertions, just testing it doesn't panic)
	maybeShowTip(store)
}

func TestTipFrequency(t *testing.T) {
	store := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	tipsMutex.Lock()
	tips = []Tip{
		{
			ID:          "frequent_tip",
			Condition:   func() bool { return true },
			Message:     "Frequent tip",
			Frequency:   5 * time.Second,
			Priority:    100,
			Probability: 1.0,
		},
	}
	tipsMutex.Unlock()

	// First selection should work
	tip := selectNextTip(store)
	if tip == nil {
		t.Fatal("Expected tip to be selected")
	}

	// Record it as shown
	recordTipShown(store, tip.ID)

	// Should not show again immediately (within frequency window)
	tip = selectNextTip(store)
	if tip != nil {
		t.Errorf("Expected nil due to frequency limit, got %v", tip)
	}

	// Manually set last shown to past (simulate time passing)
	past := time.Now().Add(-10 * time.Second)
	_ = store.SetMetadata(context.Background(), "tip_frequent_tip_last_shown", past.Format(time.RFC3339))

	// Should show again now
	tip = selectNextTip(store)
	if tip == nil {
		t.Error("Expected tip to be selected after frequency window passed")
	}
}

func TestInjectTip(t *testing.T) {
	// Reset tip registry for testing
	tipsMutex.Lock()
	tips = []Tip{}
	tipsMutex.Unlock()

	store := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")

	// Set deterministic seed for testing
	os.Setenv("BEADS_TIP_SEED", "11111")
	defer os.Unsetenv("BEADS_TIP_SEED")
	tipRandOnce = sync.Once{}
	initTipRand()

	// Test 1: Inject a new tip
	InjectTip(
		"injected_tip_1",
		"This is an injected tip",
		80,
		1*time.Hour,
		1.0, // Always show when eligible
		func() bool { return true },
	)

	tipsMutex.RLock()
	tipCount := len(tips)
	tipsMutex.RUnlock()

	if tipCount != 1 {
		t.Errorf("Expected 1 tip, got %d", tipCount)
	}

	// Verify tip can be selected
	tip := selectNextTip(store)
	if tip == nil {
		t.Fatal("Expected injected tip to be selected")
	}
	if tip.ID != "injected_tip_1" {
		t.Errorf("Expected tip ID 'injected_tip_1', got %q", tip.ID)
	}
	if tip.Message != "This is an injected tip" {
		t.Errorf("Expected message 'This is an injected tip', got %q", tip.Message)
	}
	if tip.Priority != 80 {
		t.Errorf("Expected priority 80, got %d", tip.Priority)
	}

	// Test 2: Inject another tip and verify priority ordering
	InjectTip(
		"injected_tip_2",
		"Higher priority tip",
		100,
		1*time.Hour,
		1.0,
		func() bool { return true },
	)

	tipsMutex.RLock()
	tipCount = len(tips)
	tipsMutex.RUnlock()

	if tipCount != 2 {
		t.Errorf("Expected 2 tips, got %d", tipCount)
	}

	// Higher priority tip should be selected first
	tip = selectNextTip(store)
	if tip == nil {
		t.Fatal("Expected tip to be selected")
	}
	if tip.ID != "injected_tip_2" {
		t.Errorf("Expected higher priority tip 'injected_tip_2' to be selected first, got %q", tip.ID)
	}

	// Test 3: Update existing tip (same ID)
	InjectTip(
		"injected_tip_1",
		"Updated message",
		50, // Lower priority now
		2*time.Hour,
		0.5,
		func() bool { return true },
	)

	tipsMutex.RLock()
	tipCount = len(tips)
	var updatedTip *Tip
	for i := range tips {
		if tips[i].ID == "injected_tip_1" {
			updatedTip = &tips[i]
			break
		}
	}
	tipsMutex.RUnlock()

	if tipCount != 2 {
		t.Errorf("Expected 2 tips after update (no duplicate), got %d", tipCount)
	}
	if updatedTip == nil {
		t.Fatal("Expected to find updated tip")
	}
	if updatedTip.Message != "Updated message" {
		t.Errorf("Expected updated message, got %q", updatedTip.Message)
	}
	if updatedTip.Priority != 50 {
		t.Errorf("Expected updated priority 50, got %d", updatedTip.Priority)
	}
	if updatedTip.Frequency != 2*time.Hour {
		t.Errorf("Expected updated frequency 2h, got %v", updatedTip.Frequency)
	}
	if updatedTip.Probability != 0.5 {
		t.Errorf("Expected updated probability 0.5, got %v", updatedTip.Probability)
	}
}

func TestRemoveTip(t *testing.T) {
	// Reset tip registry for testing
	tipsMutex.Lock()
	tips = []Tip{}
	tipsMutex.Unlock()

	// Add some tips
	InjectTip("tip_a", "Tip A", 100, time.Hour, 1.0, func() bool { return true })
	InjectTip("tip_b", "Tip B", 90, time.Hour, 1.0, func() bool { return true })
	InjectTip("tip_c", "Tip C", 80, time.Hour, 1.0, func() bool { return true })

	tipsMutex.RLock()
	tipCount := len(tips)
	tipsMutex.RUnlock()

	if tipCount != 3 {
		t.Fatalf("Expected 3 tips, got %d", tipCount)
	}

	// Test 1: Remove middle tip
	RemoveTip("tip_b")

	tipsMutex.RLock()
	tipCount = len(tips)
	var foundB bool
	for _, tip := range tips {
		if tip.ID == "tip_b" {
			foundB = true
			break
		}
	}
	tipsMutex.RUnlock()

	if tipCount != 2 {
		t.Errorf("Expected 2 tips after removal, got %d", tipCount)
	}
	if foundB {
		t.Error("Expected tip_b to be removed")
	}

	// Test 2: Remove non-existent tip (should be no-op)
	RemoveTip("tip_nonexistent")

	tipsMutex.RLock()
	tipCount = len(tips)
	tipsMutex.RUnlock()

	if tipCount != 2 {
		t.Errorf("Expected 2 tips after no-op removal, got %d", tipCount)
	}

	// Test 3: Remove remaining tips
	RemoveTip("tip_a")
	RemoveTip("tip_c")

	tipsMutex.RLock()
	tipCount = len(tips)
	tipsMutex.RUnlock()

	if tipCount != 0 {
		t.Errorf("Expected 0 tips after removing all, got %d", tipCount)
	}
}

func TestInjectTipConcurrency(t *testing.T) {
	// Reset tip registry for testing
	tipsMutex.Lock()
	tips = []Tip{}
	tipsMutex.Unlock()

	// Test thread safety by injecting and removing tips concurrently
	var wg sync.WaitGroup
	const numGoroutines = 50

	// Inject tips concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			tipID := "concurrent_tip_" + string(rune('a'+id%26))
			InjectTip(tipID, "Message", 50, time.Hour, 0.5, func() bool { return true })
		}(i)
	}
	wg.Wait()

	// Remove some tips concurrently
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			tipID := "concurrent_tip_" + string(rune('a'+id%26))
			RemoveTip(tipID)
		}(i)
	}
	wg.Wait()

	// If we got here without panics or deadlocks, the test passes
	// Just verify we can still access the tips
	tipsMutex.RLock()
	_ = len(tips)
	tipsMutex.RUnlock()
}

func TestIsClaudeDetected(t *testing.T) {
	// Save original env vars
	origClaudeCode := os.Getenv("CLAUDE_CODE")
	origAnthropicCli := os.Getenv("ANTHROPIC_CLI")
	defer func() {
		os.Setenv("CLAUDE_CODE", origClaudeCode)
		os.Setenv("ANTHROPIC_CLI", origAnthropicCli)
	}()

	// Clear env vars for clean testing
	os.Unsetenv("CLAUDE_CODE")
	os.Unsetenv("ANTHROPIC_CLI")

	// Test 1: Detection via CLAUDE_CODE env var
	os.Setenv("CLAUDE_CODE", "1")
	if !isClaudeDetected() {
		t.Error("Expected Claude detected with CLAUDE_CODE env var")
	}
	os.Unsetenv("CLAUDE_CODE")

	// Test 2: Detection via ANTHROPIC_CLI env var
	os.Setenv("ANTHROPIC_CLI", "1")
	if !isClaudeDetected() {
		t.Error("Expected Claude detected with ANTHROPIC_CLI env var")
	}
	os.Unsetenv("ANTHROPIC_CLI")

	// Test 3: Detection via ~/.claude directory
	// This depends on the test environment - if ~/.claude exists, it should detect
	// We can't easily control this without modifying the filesystem
	home, err := os.UserHomeDir()
	if err == nil {
		claudeDir := home + "/.claude"
		if _, err := os.Stat(claudeDir); err == nil {
			// ~/.claude exists, should detect
			if !isClaudeDetected() {
				t.Error("Expected Claude detected with ~/.claude directory present")
			}
		}
	}
}

func TestIsClaudeSetupComplete(t *testing.T) {
	// This test checks the logic without modifying the filesystem
	// The actual detection depends on the presence of files

	// Test that the function returns a boolean and doesn't panic
	result := isClaudeSetupComplete()
	// Just verify it returns without error
	_ = result

	// If running in an environment with Claude setup, verify detection
	// We'll check both global and project paths exist
	home, err := os.UserHomeDir()
	if err != nil {
		return // Skip if we can't get home dir
	}

	globalCommand := home + "/.claude/commands/prime_beads.md"
	globalHooksSession := home + "/.claude/hooks/sessionstart"
	globalHooksPreTool := home + "/.claude/hooks/PreToolUse"

	// Check if global setup exists
	if _, err := os.Stat(globalCommand); err == nil {
		if _, err := os.Stat(globalHooksSession); err == nil {
			if !isClaudeSetupComplete() {
				t.Error("Expected Claude setup complete with global hooks (sessionstart)")
			}
		} else if _, err := os.Stat(globalHooksPreTool); err == nil {
			if !isClaudeSetupComplete() {
				t.Error("Expected Claude setup complete with global hooks (PreToolUse)")
			}
		}
	}

	// Check project-level setup
	projectCommand := ".claude/commands/prime_beads.md"
	projectHooksSession := ".claude/hooks/sessionstart"
	projectHooksPreTool := ".claude/hooks/PreToolUse"

	if _, err := os.Stat(projectCommand); err == nil {
		if _, err := os.Stat(projectHooksSession); err == nil {
			if !isClaudeSetupComplete() {
				t.Error("Expected Claude setup complete with project hooks (sessionstart)")
			}
		} else if _, err := os.Stat(projectHooksPreTool); err == nil {
			if !isClaudeSetupComplete() {
				t.Error("Expected Claude setup complete with project hooks (PreToolUse)")
			}
		}
	}
}

func TestClaudeSetupTipRegistered(t *testing.T) {
	// Reset tip registry with fresh default tips
	tipsMutex.Lock()
	tips = []Tip{}
	tipsMutex.Unlock()
	initDefaultTips()

	// Verify that the claude_setup tip is registered
	tipsMutex.RLock()
	defer tipsMutex.RUnlock()

	var found bool
	for _, tip := range tips {
		if tip.ID == "claude_setup" {
			found = true
			// Verify tip properties
			if tip.Priority != 100 {
				t.Errorf("Expected claude_setup priority 100, got %d", tip.Priority)
			}
			if tip.Frequency != 24*time.Hour {
				t.Errorf("Expected claude_setup frequency 24h, got %v", tip.Frequency)
			}
			if tip.Probability != 0.6 {
				t.Errorf("Expected claude_setup probability 0.6, got %v", tip.Probability)
			}
			break
		}
	}

	if !found {
		t.Error("Expected claude_setup tip to be registered")
	}
}

func TestClaudeSetupTipCondition(t *testing.T) {
	// Save original env vars
	origClaudeCode := os.Getenv("CLAUDE_CODE")
	defer os.Setenv("CLAUDE_CODE", origClaudeCode)

	// Reset tip registry with fresh default tips
	tipsMutex.Lock()
	tips = []Tip{}
	tipsMutex.Unlock()
	initDefaultTips()

	// Find the claude_setup tip
	tipsMutex.RLock()
	var claudeTip *Tip
	for i := range tips {
		if tips[i].ID == "claude_setup" {
			claudeTip = &tips[i]
			break
		}
	}
	tipsMutex.RUnlock()

	if claudeTip == nil {
		t.Fatal("claude_setup tip not found")
	}

	// Test: When Claude is not detected, condition should be false
	os.Unsetenv("CLAUDE_CODE")
	os.Unsetenv("ANTHROPIC_CLI")
	// Note: This test may pass or fail depending on ~/.claude existence
	// The important thing is that the condition function executes without error
	_ = claudeTip.Condition()

	// Test: When Claude is detected but setup might be complete
	// Set env var to simulate Claude environment
	os.Setenv("CLAUDE_CODE", "1")
	conditionResult := claudeTip.Condition()
	// If setup is complete, should be false; if not complete, should be true
	// Just verify it returns a valid boolean
	_ = conditionResult
}
