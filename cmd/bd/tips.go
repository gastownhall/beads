package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

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
	sort.Slice(eligibleTips, func(i, j int) bool {
		return eligibleTips[i].Priority > eligibleTips[j].Priority
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
	_ = store.SetMetadata(context.Background(), key, value)
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
// Checks for either global or project-level installation of the beads hooks.
func isClaudeSetupComplete() bool {
	// Check for global installation
	home, err := os.UserHomeDir()
	if err == nil {
		commandFile := filepath.Join(home, ".claude", "commands", "prime_beads.md")
		hooksDir := filepath.Join(home, ".claude", "hooks")

		// Check for prime_beads command
		if _, err := os.Stat(commandFile); err == nil {
			// Check for sessionstart hook (could be a file or directory)
			hookPath := filepath.Join(hooksDir, "sessionstart")
			if _, err := os.Stat(hookPath); err == nil {
				return true // Global hooks installed
			}
			// Also check PreToolUse hook which is used by beads
			preToolUsePath := filepath.Join(hooksDir, "PreToolUse")
			if _, err := os.Stat(preToolUsePath); err == nil {
				return true // Global hooks installed
			}
		}
	}

	// Check for project-level installation
	commandFile := ".claude/commands/prime_beads.md"
	hooksDir := ".claude/hooks"

	if _, err := os.Stat(commandFile); err == nil {
		hookPath := filepath.Join(hooksDir, "sessionstart")
		if _, err := os.Stat(hookPath); err == nil {
			return true // Project hooks installed
		}
		preToolUsePath := filepath.Join(hooksDir, "PreToolUse")
		if _, err := os.Stat(preToolUsePath); err == nil {
			return true // Project hooks installed
		}
	}

	return false
}

// initDefaultTips registers the built-in tips.
// Called during initialization to populate the tip registry.
func initDefaultTips() {
	// Claude setup tip - suggest running bd setup claude when Claude is detected
	// but the integration is not configured
	InjectTip(
		"claude_setup",
		"Run 'bd setup claude' to enable automatic context recovery in Claude Code",
		100,              // Highest priority - this is important for Claude users
		24*time.Hour,     // Daily minimum gap
		0.6,              // 60% chance when eligible (~4 times per week)
		func() bool {
			return isClaudeDetected() && !isClaudeSetupComplete()
		},
	)
}

// init initializes the tip system with default tips
func init() {
	initDefaultTips()
}
