package main

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"
)

type tipMetadataWrite struct {
	key   string
	value string
}

// tipMetadataRecorder is a scripted recording double. It deliberately does
// not apply writes to reads; storage owns that persistence behavior.
type tipMetadataRecorder struct {
	values   map[string]string
	readErr  error
	writeErr error
	readKeys []string
	writes   []tipMetadataWrite
}

func (r *tipMetadataRecorder) GetLocalMetadata(_ context.Context, key string) (string, error) {
	r.readKeys = append(r.readKeys, key)
	if r.readErr != nil {
		return "", r.readErr
	}
	return r.values[key], nil
}

func (r *tipMetadataRecorder) SetLocalMetadata(_ context.Context, key, value string) error {
	r.writes = append(r.writes, tipMetadataWrite{key: key, value: value})
	return r.writeErr
}

func resetTipTestState(t *testing.T) {
	t.Helper()
	tipsMutex.Lock()
	originalTips := append([]Tip(nil), tips...)
	tips = nil
	tipsMutex.Unlock()

	originalRand := tipRand
	originalRandWasInitialized := originalRand != nil
	tipRand = nil
	tipRandOnce = sync.Once{}
	originalJSONOutput := jsonOutput
	jsonOutput = false
	originalQuietFlag := quietFlag
	quietFlag = false
	originalDoltAutoCommit := doltAutoCommit
	doltAutoCommit = ""
	originalDidWrite := commandDidWriteTipMetadata
	commandDidWriteTipMetadata = false
	originalTipIDsWereNil := commandTipIDsShown == nil
	originalTipIDs := make(map[string]struct{}, len(commandTipIDsShown))
	for id := range commandTipIDsShown {
		originalTipIDs[id] = struct{}{}
	}
	commandTipIDsShown = nil

	t.Cleanup(func() {
		tipsMutex.Lock()
		tips = originalTips
		tipsMutex.Unlock()
		tipRand = originalRand
		tipRandOnce = sync.Once{}
		if originalRandWasInitialized {
			tipRandOnce.Do(func() {})
		}
		jsonOutput = originalJSONOutput
		quietFlag = originalQuietFlag
		doltAutoCommit = originalDoltAutoCommit
		commandDidWriteTipMetadata = originalDidWrite
		if originalTipIDsWereNil {
			commandTipIDsShown = nil
		} else {
			commandTipIDsShown = originalTipIDs
		}
	})
}

func TestTipSelection(t *testing.T) {
	resetTipTestState(t)
	t.Setenv("BEADS_TIP_SEED", "12345")
	initTipRand()
	store := &tipMetadataRecorder{values: map[string]string{}}

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
	store.values["tip_test_tip_1_last_shown"] = time.Now().Format(time.RFC3339)
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

	for _, test := range []struct {
		name string
		tips []Tip
		want string
	}{
		{
			name: "lower priority wins after higher probability misses",
			tips: []Tip{
				{ID: "low", Condition: func() bool { return true }, Priority: 10, Probability: 1},
				{ID: "high", Condition: func() bool { return true }, Priority: 100, Probability: 0},
			},
			want: "low",
		},
		{
			name: "second equal priority tip wins after first probability misses",
			tips: []Tip{
				{ID: "first", Condition: func() bool { return true }, Priority: 50, Probability: 0},
				{ID: "second", Condition: func() bool { return true }, Priority: 50, Probability: 1},
			},
			want: "second",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tipsMutex.Lock()
			tips = test.tips
			tipsMutex.Unlock()
			got := selectNextTip(store)
			if got == nil || got.ID != test.want {
				t.Fatalf("selectNextTip() = %#v, want ID %q", got, test.want)
			}
		})
	}
}

func TestTipProbability(t *testing.T) {
	resetTipTestState(t)
	t.Setenv("BEADS_TIP_SEED", "99999")
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

	store := &tipMetadataRecorder{values: map[string]string{}}

	// Run selection multiple times
	shownCount := 0
	for i := 0; i < 100; i++ {
		tip := selectNextTip(store)
		if tip != nil {
			shownCount++
		}
	}

	if shownCount != 1 {
		t.Errorf("shown count = %d, want 1 for seed 99999", shownCount)
	}
}

func TestGetLastShown(t *testing.T) {
	resetTipTestState(t)
	shown := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		value string
		err   error
		want  time.Time
	}{
		{name: "missing"},
		{name: "valid", value: shown.Format(time.RFC3339), want: shown},
		{name: "malformed", value: "not-a-time"},
		{name: "read failure", err: errors.New("read failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &tipMetadataRecorder{values: map[string]string{"tip_test_last_shown": test.value}, readErr: test.err}
			if got := getLastShown(store, "test"); !got.Equal(test.want) {
				t.Fatalf("getLastShown() = %v, want %v", got, test.want)
			}
			if len(store.readKeys) != 1 || store.readKeys[0] != "tip_test_last_shown" {
				t.Fatalf("read keys = %v, want [tip_test_last_shown]", store.readKeys)
			}
		})
	}
}

func TestRecordTipShown(t *testing.T) {
	resetTipTestState(t)
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "unset", mode: ""},
		{name: "off", mode: "off"},
		{name: "batch", mode: "batch"},
		{name: "invalid", mode: "invalid"},
	} {
		t.Run("immediate write with auto commit "+test.name, func(t *testing.T) {
			commandDidWriteTipMetadata = false
			commandTipIDsShown = nil
			doltAutoCommit = test.mode
			store := &tipMetadataRecorder{}
			callStarted := time.Now().Truncate(time.Second)
			recordTipShown(store, "test_tip")
			callFinished := time.Now().Truncate(time.Second)
			if len(store.writes) != 1 || store.writes[0].key != "tip_test_tip_last_shown" {
				t.Fatalf("writes = %#v", store.writes)
			}
			writtenAt, err := time.Parse(time.RFC3339, store.writes[0].value)
			if err != nil {
				t.Fatalf("written timestamp %q: %v", store.writes[0].value, err)
			}
			if writtenAt.Before(callStarted) || writtenAt.After(callFinished) {
				t.Fatalf("written timestamp %v outside call window [%v, %v]", writtenAt, callStarted, callFinished)
			}
			_, tracked := commandTipIDsShown["test_tip"]
			if !commandDidWriteTipMetadata || len(commandTipIDsShown) != 1 || !tracked {
				t.Fatalf("tip write tracking = %t, %#v", commandDidWriteTipMetadata, commandTipIDsShown)
			}
		})
	}
	t.Run("write failure is not tracked", func(t *testing.T) {
		commandDidWriteTipMetadata = false
		commandTipIDsShown = nil
		store := &tipMetadataRecorder{writeErr: errors.New("write failed")}
		recordTipShown(store, "test_tip")
		if len(store.writes) != 1 || commandDidWriteTipMetadata || commandTipIDsShown != nil {
			t.Fatalf("writes = %#v, tracking = %t, %#v", store.writes, commandDidWriteTipMetadata, commandTipIDsShown)
		}
	})
	t.Run("nil and empty IDs do not write", func(t *testing.T) {
		recordTipShown(nil, "test_tip")
		store := &tipMetadataRecorder{}
		recordTipShown(store, "")
		if len(store.writes) != 0 {
			t.Fatalf("writes = %#v", store.writes)
		}
	})
	t.Run("auto commit defers write", func(t *testing.T) {
		commandDidWriteTipMetadata = false
		commandTipIDsShown = nil
		doltAutoCommit = "on"
		store := &tipMetadataRecorder{}
		recordTipShown(store, "test_tip")
		_, tracked := commandTipIDsShown["test_tip"]
		if len(store.writes) != 0 || !commandDidWriteTipMetadata || len(commandTipIDsShown) != 1 || !tracked {
			t.Fatalf("writes = %#v, tracking = %t, %#v", store.writes, commandDidWriteTipMetadata, commandTipIDsShown)
		}
	})
}

func TestMaybeShowTip_RespectsFlags(t *testing.T) {
	resetTipTestState(t)
	t.Setenv("BEADS_TIP_SEED", "54321")

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

	store := &tipMetadataRecorder{values: map[string]string{}}

	// Test 1: Should not show in JSON mode
	jsonOutput = true
	output := captureStdout(t, func() error { maybeShowTip(store); return nil })
	if output != "" || tipRand != nil || len(store.readKeys) != 0 || len(store.writes) != 0 {
		t.Fatalf("JSON mode output=%q rand=%v reads=%v writes=%v", output, tipRand, store.readKeys, store.writes)
	}
	jsonOutput = false

	// Test 2: Should not show in quiet mode
	quietFlag = true
	output = captureStdout(t, func() error { maybeShowTip(store); return nil })
	if output != "" || tipRand != nil || len(store.readKeys) != 0 || len(store.writes) != 0 {
		t.Fatalf("quiet mode output=%q rand=%v reads=%v writes=%v", output, tipRand, store.readKeys, store.writes)
	}
	quietFlag = false

	output = captureStdout(t, func() error { maybeShowTip(store); return nil })
	if output != "\n💡 Tip: Always show tip\n" || tipRand == nil || len(store.readKeys) != 1 || len(store.writes) != 1 {
		t.Fatalf("normal mode output=%q rand=%v reads=%v writes=%v", output, tipRand, store.readKeys, store.writes)
	}
}

func TestTipFrequency(t *testing.T) {
	resetTipTestState(t)
	tipRand = rand.New(rand.NewSource(1))
	store := &tipMetadataRecorder{values: map[string]string{}}

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

	// A recorder does not apply writes to reads, so script the persisted value.
	store.values["tip_frequent_tip_last_shown"] = time.Now().Format(time.RFC3339)

	// Should not show again immediately (within frequency window)
	tip = selectNextTip(store)
	if tip != nil {
		t.Errorf("Expected nil due to frequency limit, got %v", tip)
	}

	// Manually set last shown to past (simulate time passing)
	past := time.Now().Add(-10 * time.Second)
	store.values["tip_frequent_tip_last_shown"] = past.Format(time.RFC3339)

	// Should show again now
	tip = selectNextTip(store)
	if tip == nil {
		t.Error("Expected tip to be selected after frequency window passed")
	}
}

func TestInjectTip(t *testing.T) {
	resetTipTestState(t)
	store := &tipMetadataRecorder{values: map[string]string{}}

	// Set deterministic seed for testing
	t.Setenv("BEADS_TIP_SEED", "11111")
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
	resetTipTestState(t)
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
	resetTipTestState(t)
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
	resetTipTestState(t)
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
	resetTipTestState(t)
	t.Setenv("CLAUDE_CODE", "")
	t.Setenv("ANTHROPIC_CLI", "")

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
