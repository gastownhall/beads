package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
)

// Tip represents a contextual hint that can be shown to users after successful commands
type Tip struct {
	ID          string
	Condition   func() bool   // Should this tip be eligible?
	Message     string        // The tip message to display
	Frequency   time.Duration // Minimum gap between showings
	Priority    int           // Higher = shown first when eligible
	Probability float64       // 0.0 to 1.0 - chance of showing when eligible
}

var (
	// tips is the registry of all available tips
	tips []Tip

	// tipsMutex protects the tips registry for thread-safe access
	tipsMutex sync.RWMutex

	// tipRand is the random number generator for probability rolls
	// Can be seeded deterministically via BEADS_TIP_SEED for testing
	tipRand *rand.Rand

	// tipRandOnce ensures we only initialize the RNG once
	tipRandOnce sync.Once
)

// initTipRand initializes the random number generator for tip selection
// Uses BEADS_TIP_SEED env var for deterministic testing if set
func initTipRand() {
	tipRandOnce.Do(func() {
		seed := time.Now().UnixNano()
		if seedStr := os.Getenv("BEADS_TIP_SEED"); seedStr != "" {
			if parsedSeed, err := strconv.ParseInt(seedStr, 10, 64); err == nil {
				seed = parsedSeed
			}
		}
		// Use deprecated rand.NewSource for Go 1.19 compatibility
		// nolint:gosec,staticcheck // G404: deterministic seed via env var is intentional for testing
		tipRand = rand.New(rand.NewSource(seed))
	})
}

// maybeShowTip selects and displays an eligible tip based on priority and probability
// Respects --json and --quiet flags
func maybeShowTip(store storage.Storage) {
	// Skip tips in JSON output mode or quiet mode
	if jsonOutput || quietFlag {
		return
	}

	// Initialize RNG if needed
	initTipRand()

	// Select next tip
	tip := selectNextTip(store)
	if tip == nil {
		return
	}

	// Display tip to stdout (informational, not an error)
	_, _ = fmt.Fprintf(os.Stdout, "\n💡 Tip: %s\n", tip.Message)

	// Record that we showed this tip
	recordTipShown(store, tip.ID)
}

// selectNextTip finds the next tip to show based on conditions, frequency, priority, and probability
// Returns nil if no tip should be shown
func selectNextTip(store storage.Storage) *Tip {
	if store == nil {
		return nil
	}

	now := time.Now()
	var eligibleTips []Tip

	// Lock for reading the tip registry
	tipsMutex.RLock()
	defer tipsMutex.RUnlock()

	// Filter to eligible tips (condition + frequency check)
	for _, tip := range tips {
		// Check if tip's condition is met
		if !tip.Condition() {
			continue
		}

		// Check if enough time has passed since last showing
		lastShown := getLastShown(store, tip.ID)
		if !lastShown.IsZero() && now.Sub(lastShown) < tip.Frequency {
			continue
		}

		eligibleTips = append(eligibleTips, tip)
	}

	if len(eligibleTips) == 0 {
		return nil
	}

	// Sort by priority (highest first)
	slices.SortFunc(eligibleTips, func(a, b Tip) int {
		return cmp.Compare(b.Priority, a.Priority) // descending order
	})

	// Apply probability roll (in priority order)
	// Higher priority tips get first chance to show
	for i := range eligibleTips {
		if tipRand.Float64() < eligibleTips[i].Probability {
			return &eligibleTips[i]
		}
	}

	return nil // No tips won probability roll
}

// getLastShown retrieves the timestamp when a tip was last shown
// Returns zero time if never shown
func getLastShown(store storage.Storage, tipID string) time.Time {
	key := fmt.Sprintf("tip_%s_last_shown", tipID)
	value, err := store.GetMetadata(context.Background(), key)
	if err != nil || value == "" {
		return time.Time{}
	}

	// Parse RFC3339 timestamp
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}

	return t
}

// recordTipShown records the timestamp when a tip was shown
func recordTipShown(store storage.Storage, tipID string) {
	key := fmt.Sprintf("tip_%s_last_shown", tipID)
	value := time.Now().Format(time.RFC3339)
	_ = store.SetMetadata(context.Background(), key, value) // Non-critical metadata, ok to fail silently
}

// InjectTip adds a dynamic tip to the registry at runtime.
// This enables tips to be programmatically added based on detected conditions.
//
// Parameters:
//   - id: Unique identifier for the tip (used for frequency tracking)
//   - message: The tip message to display to the user
//   - priority: Higher values = shown first when eligible (e.g., 100 for critical, 30 for suggestions)
//   - frequency: Minimum time between showings (e.g., 24*time.Hour for daily)
//   - probability: Chance of showing when eligible (0.0 to 1.0)
//   - condition: Function that returns true when tip should be eligible
//
// Example usage:
//
//	// Critical security update - always show
//	InjectTip("security_update", "CRITICAL: Security update available!", 100, 0, 1.0, func() bool { return true })
//
//	// New version available - frequent but not always
//	InjectTip("upgrade_available", "New version available", 90, 7*24*time.Hour, 0.8, func() bool { return true })
//
//	// Feature suggestion - occasional
//	InjectTip("try_filters", "Try using filters", 50, 14*24*time.Hour, 0.4, func() bool { return true })
func InjectTip(id, message string, priority int, frequency time.Duration, probability float64, condition func() bool) {
	tipsMutex.Lock()
	defer tipsMutex.Unlock()

	// Check if tip with this ID already exists - update it if so
	for i, tip := range tips {
		if tip.ID == id {
			tips[i] = Tip{
				ID:          id,
				Condition:   condition,
				Message:     message,
				Frequency:   frequency,
				Priority:    priority,
				Probability: probability,
			}
			return
		}
	}

	// Add new tip
	tips = append(tips, Tip{
		ID:          id,
		Condition:   condition,
		Message:     message,
		Frequency:   frequency,
		Priority:    priority,
		Probability: probability,
	})
}

// RemoveTip removes a tip from the registry by ID.
// This is useful for removing dynamically injected tips when they are no longer relevant.
// It is safe to call with a non-existent ID (no-op).
func RemoveTip(id string) {
	tipsMutex.Lock()
	defer tipsMutex.Unlock()

	for i, tip := range tips {
		if tip.ID == id {
			tips = append(tips[:i], tips[i+1:]...)
			return
		}
	}
}

// isClaudeDetected checks if the user is running within a Claude Code environment.
// Detection methods:
// - CLAUDE_CODE environment variable (set by Claude Code)
// - ANTHROPIC_CLI environment variable
// - Presence of ~/.claude directory (Claude Code config)
func isClaudeDetected() bool {
	// Check environment variables set by Claude Code
	if os.Getenv("CLAUDE_CODE") != "" || os.Getenv("ANTHROPIC_CLI") != "" {
		return true
	}

	// Check if ~/.claude directory exists (Claude Code stores config here)
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		return true
	}

	return false
}

// isClaudeSetupComplete checks if the beads Claude integration is properly configured.
// Returns true if the beads plugin is installed (provides hooks via plugin.json),
// or if hooks were manually installed via 'bd setup claude'.
func isClaudeSetupComplete() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	// Check if beads plugin is installed - plugin now provides hooks automatically
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	// #nosec G304 - path is constructed from user home directory
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err == nil {
			if enabledPlugins, ok := settings["enabledPlugins"].(map[string]interface{}); ok {
				for key, value := range enabledPlugins {
					if strings.Contains(strings.ToLower(key), "beads") {
						if enabled, ok := value.(bool); ok && enabled {
							return true // Plugin installed - provides hooks
						}
					}
				}
			}
		}
	}

	// Check for manual hooks installation via 'bd setup claude'
	// Global hooks in settings.json
	if hasBeadsPrimeHooks(settingsPath) {
		return true
	}

	// Project-level hooks in .claude/settings.local.json
	localSettingsPath := filepath.Join(home, ".claude", "settings.local.json")
	if hasBeadsPrimeHooks(localSettingsPath) {
		return true
	}

	return false
}

// hasBeadsPrimeHooks checks if a settings file has bd prime hooks configured
func hasBeadsPrimeHooks(settingsPath string) bool {
	// #nosec G304 - path is constructed from user home directory
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}

	// Check SessionStart and PreCompact for "bd prime"
	for _, event := range []string{"SessionStart", "PreCompact"} {
		eventHooks, ok := hooks[event].([]interface{})
		if !ok {
			continue
		}

		for _, hook := range eventHooks {
			hookMap, ok := hook.(map[string]interface{})
			if !ok {
				continue
			}
			commands, ok := hookMap["hooks"].([]interface{})
			if !ok {
				continue
			}
			for _, cmd := range commands {
				cmdMap, ok := cmd.(map[string]interface{})
				if !ok {
					continue
				}
				cmdStr, _ := cmdMap["command"].(string)
				if cmdStr == "bd prime" || cmdStr == "bd prime --stealth" {
					return true
				}
			}
		}
	}

	return false
}

// initDefaultTips registers the built-in tips.
// Called during initialization to populate the tip registry.
func initDefaultTips() {
	// Claude setup tip - suggest installing the beads plugin when Claude is detected
	// but the integration is not configured
	InjectTip(
		"claude_setup",
		"Install the beads plugin for automatic workflow context, or run 'bd setup claude' for CLI-only mode",
		100,              // Highest priority - this is important for Claude users
		24*time.Hour,     // Daily minimum gap
		0.6,              // 60% chance when eligible (~4 times per week)
		func() bool {
			return isClaudeDetected() && !isClaudeSetupComplete()
		},
	)

	// Sync conflict tip - ALWAYS show when sync has failed and needs manual intervention
	// This is a proactive health check that trumps educational tips (ox-cli pattern)
	InjectTip(
		"sync_conflict",
		"Run 'bd sync' to resolve sync conflict",
		200,         // Higher than Claude setup - sync issues are urgent
		0,           // No frequency limit - always show when applicable
		1.0,         // 100% probability - always show when condition is true
		syncConflictCondition,
	)
}

// syncConflictCondition checks if there's a sync conflict that needs manual resolution.
// This is the condition function for the sync_conflict tip.
func syncConflictCondition() bool {
	// Find beads directory to check sync state
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return false
	}

	state := LoadSyncState(beadsDir)
	return state.NeedsManualSync
}

// init initializes the tip system with default tips
func init() {
	initDefaultTips()
}
