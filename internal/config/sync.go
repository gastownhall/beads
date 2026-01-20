package config

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Sync mode configuration values (from hq-ew1mbr.3)
// These control how Dolt syncs with JSONL/remotes.

// ConfigWarnings controls whether warnings are logged for invalid config values.
// Set to false to suppress warnings (useful for tests or scripts).
var ConfigWarnings = true

// ConfigWarningWriter is the destination for config warnings.
// Defaults to os.Stderr. Can be replaced for testing or custom logging.
var ConfigWarningWriter io.Writer = os.Stderr

// logConfigWarning logs a warning message if ConfigWarnings is enabled.
func logConfigWarning(format string, args ...interface{}) {
	if ConfigWarnings && ConfigWarningWriter != nil {
		_, _ = fmt.Fprintf(ConfigWarningWriter, format, args...)
	}
}

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

// ValidSyncModes returns the list of valid sync mode values.
func ValidSyncModes() []string {
	return []string{
		string(SyncModeGitPortable),
		string(SyncModeRealtime),
		string(SyncModeDoltNative),
		string(SyncModeBeltAndSuspenders),
	}
}

// IsValidSyncMode returns true if the given string is a valid sync mode.
func IsValidSyncMode(mode string) bool {
	return validSyncModes[SyncMode(strings.ToLower(strings.TrimSpace(mode)))]
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

// ValidConflictStrategies returns the list of valid conflict strategy values.
func ValidConflictStrategies() []string {
	return []string{
		string(ConflictStrategyNewest),
		string(ConflictStrategyOurs),
		string(ConflictStrategyTheirs),
		string(ConflictStrategyManual),
	}
}

// IsValidConflictStrategy returns true if the given string is a valid conflict strategy.
func IsValidConflictStrategy(strategy string) bool {
	return validConflictStrategies[ConflictStrategy(strings.ToLower(strings.TrimSpace(strategy)))]
}

// Sovereignty represents the federation sovereignty tier
type Sovereignty string

const (
	// SovereigntyNone means no sovereignty restriction (empty value)
	SovereigntyNone Sovereignty = ""
	// SovereigntyT1 is the most open tier (public repos)
	SovereigntyT1 Sovereignty = "T1"
	// SovereigntyT2 is organization-level
	SovereigntyT2 Sovereignty = "T2"
	// SovereigntyT3 is pseudonymous
	SovereigntyT3 Sovereignty = "T3"
	// SovereigntyT4 is anonymous
	SovereigntyT4 Sovereignty = "T4"
)

// validSovereigntyTiers is the set of allowed sovereignty values (excluding empty)
var validSovereigntyTiers = map[Sovereignty]bool{
	SovereigntyT1: true,
	SovereigntyT2: true,
	SovereigntyT3: true,
	SovereigntyT4: true,
}

// ValidSovereigntyTiers returns the list of valid sovereignty tier values.
func ValidSovereigntyTiers() []string {
	return []string{
		string(SovereigntyT1),
		string(SovereigntyT2),
		string(SovereigntyT3),
		string(SovereigntyT4),
	}
}

// IsValidSovereignty returns true if the given string is a valid sovereignty tier.
// Empty string is valid (means no restriction).
func IsValidSovereignty(sovereignty string) bool {
	if sovereignty == "" {
		return true
	}
	return validSovereigntyTiers[Sovereignty(strings.ToUpper(strings.TrimSpace(sovereignty)))]
}

// GetSyncMode retrieves the sync mode configuration.
// Returns the configured mode, or SyncModeGitPortable (default) if not set or invalid.
// Logs a warning if an invalid value is configured (unless ConfigWarnings is false).
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
		logConfigWarning("Warning: invalid sync.mode %q in config (valid: %s), using default 'git-portable'\n",
			value, strings.Join(ValidSyncModes(), ", "))
		return SyncModeGitPortable
	}

	return mode
}

// GetConflictStrategy retrieves the conflict resolution strategy configuration.
// Returns the configured strategy, or ConflictStrategyNewest (default) if not set or invalid.
// Logs a warning if an invalid value is configured (unless ConfigWarnings is false).
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
		logConfigWarning("Warning: invalid conflict.strategy %q in config (valid: %s), using default 'newest'\n",
			value, strings.Join(ValidConflictStrategies(), ", "))
		return ConflictStrategyNewest
	}

	return strategy
}

// GetSovereignty retrieves the federation sovereignty tier configuration.
// Returns the configured tier, or SovereigntyNone (empty, no restriction) if not set.
// Returns SovereigntyT1 and logs a warning if an invalid non-empty value is configured.
//
// Config key: federation.sovereignty
// Valid values: T1, T2, T3, T4 (empty means no restriction)
func GetSovereignty() Sovereignty {
	value := GetString("federation.sovereignty")
	if value == "" {
		return SovereigntyNone // No restriction
	}

	// Normalize to uppercase for comparison (T1, T2, etc.)
	tier := Sovereignty(strings.ToUpper(strings.TrimSpace(value)))
	if !validSovereigntyTiers[tier] {
		logConfigWarning("Warning: invalid federation.sovereignty %q in config (valid: %s, or empty for no restriction), using 'T1'\n",
			value, strings.Join(ValidSovereigntyTiers(), ", "))
		return SovereigntyT1
	}

	return tier
}

// String returns the string representation of the SyncMode.
func (m SyncMode) String() string {
	return string(m)
}

// String returns the string representation of the ConflictStrategy.
func (s ConflictStrategy) String() string {
	return string(s)
}

// String returns the string representation of the Sovereignty.
func (s Sovereignty) String() string {
	return string(s)
}
