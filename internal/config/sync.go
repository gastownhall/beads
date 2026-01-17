package config

import (
	"fmt"
	"os"
	"strings"
)

// Sync mode configuration values (from hq-ew1mbr.3)
// These control how Dolt syncs with JSONL/remotes.

// SyncMode represents the sync mode configuration
type SyncMode string

const (
	// SyncModeGitPortable exports JSONL on push, imports on pull (default)
	SyncModeGitPortable SyncMode = "git-portable"
	// SyncModeRealtime exports JSONL on every change (legacy behavior)
	SyncModeRealtime SyncMode = "realtime"
	// SyncModeDoltNative uses Dolt remote directly (dolthub://, gs://, s3://)
	SyncModeDoltNative SyncMode = "dolt-native"
	// SyncModeBeltAndSuspenders uses Dolt remote + JSONL backup
	SyncModeBeltAndSuspenders SyncMode = "belt-and-suspenders"
)

// validSyncModes is the set of allowed sync mode values
var validSyncModes = map[SyncMode]bool{
	SyncModeGitPortable:       true,
	SyncModeRealtime:          true,
	SyncModeDoltNative:        true,
	SyncModeBeltAndSuspenders: true,
}

// ConflictStrategy represents the conflict resolution strategy
type ConflictStrategy string

const (
	// ConflictStrategyNewest uses last-write-wins (default)
	ConflictStrategyNewest ConflictStrategy = "newest"
	// ConflictStrategyOurs prefers local changes
	ConflictStrategyOurs ConflictStrategy = "ours"
	// ConflictStrategyTheirs prefers remote changes
	ConflictStrategyTheirs ConflictStrategy = "theirs"
	// ConflictStrategyManual requires manual resolution
	ConflictStrategyManual ConflictStrategy = "manual"
)

// validConflictStrategies is the set of allowed conflict strategy values
var validConflictStrategies = map[ConflictStrategy]bool{
	ConflictStrategyNewest: true,
	ConflictStrategyOurs:   true,
	ConflictStrategyTheirs: true,
	ConflictStrategyManual: true,
}

// Sovereignty represents the federation sovereignty tier
type Sovereignty string

const (
	// SovereigntyT1 is the most open tier (public repos)
	SovereigntyT1 Sovereignty = "T1"
	// SovereigntyT2 is organization-level
	SovereigntyT2 Sovereignty = "T2"
	// SovereigntyT3 is pseudonymous
	SovereigntyT3 Sovereignty = "T3"
	// SovereigntyT4 is anonymous
	SovereigntyT4 Sovereignty = "T4"
)

// validSovereigntyTiers is the set of allowed sovereignty values
var validSovereigntyTiers = map[Sovereignty]bool{
	SovereigntyT1: true,
	SovereigntyT2: true,
	SovereigntyT3: true,
	SovereigntyT4: true,
}

// GetSyncMode retrieves the sync mode configuration.
// Returns the configured mode, or SyncModeGitPortable (default) if not set or invalid.
// Logs a warning to stderr if an invalid value is configured.
//
// Config key: sync.mode
// Valid values: git-portable, realtime, dolt-native, belt-and-suspenders
func GetSyncMode() SyncMode {
	value := GetString("sync.mode")
	if value == "" {
		return SyncModeGitPortable // Default
	}

	mode := SyncMode(strings.ToLower(strings.TrimSpace(value)))
	if !validSyncModes[mode] {
		fmt.Fprintf(os.Stderr, "Warning: invalid sync.mode %q in config (valid: git-portable, realtime, dolt-native, belt-and-suspenders), using default 'git-portable'\n", value)
		return SyncModeGitPortable
	}

	return mode
}

// GetConflictStrategy retrieves the conflict resolution strategy configuration.
// Returns the configured strategy, or ConflictStrategyNewest (default) if not set or invalid.
// Logs a warning to stderr if an invalid value is configured.
//
// Config key: conflict.strategy
// Valid values: newest, ours, theirs, manual
func GetConflictStrategy() ConflictStrategy {
	value := GetString("conflict.strategy")
	if value == "" {
		return ConflictStrategyNewest // Default
	}

	strategy := ConflictStrategy(strings.ToLower(strings.TrimSpace(value)))
	if !validConflictStrategies[strategy] {
		fmt.Fprintf(os.Stderr, "Warning: invalid conflict.strategy %q in config (valid: newest, ours, theirs, manual), using default 'newest'\n", value)
		return ConflictStrategyNewest
	}

	return strategy
}

// GetSovereignty retrieves the federation sovereignty tier configuration.
// Returns the configured tier, or SovereigntyT1 (default) if not set or invalid.
// Logs a warning to stderr if an invalid value is configured.
//
// Config key: federation.sovereignty
// Valid values: T1, T2, T3, T4
func GetSovereignty() Sovereignty {
	value := GetString("federation.sovereignty")
	if value == "" {
		return SovereigntyT1 // Default
	}

	// Normalize to uppercase for comparison (T1, T2, etc.)
	tier := Sovereignty(strings.ToUpper(strings.TrimSpace(value)))
	if !validSovereigntyTiers[tier] {
		fmt.Fprintf(os.Stderr, "Warning: invalid federation.sovereignty %q in config (valid: T1, T2, T3, T4), using default 'T1'\n", value)
		return SovereigntyT1
	}

	return tier
}
