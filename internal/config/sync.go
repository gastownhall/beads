package config

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Sync mode configuration values (from hq-ew1mbr.3)
// These control how Dolt syncs with remotes.

// ConfigWarnings controls whether warnings are logged for invalid config values.
// Set to false to suppress warnings (useful for tests or scripts).
var ConfigWarnings = true

// ConfigWarningWriter is the destination for config warnings.
// Defaults to os.Stderr. Can be replaced for testing or custom logging.
var ConfigWarningWriter io.Writer = os.Stderr

// logConfigWarning logs a warning message if ConfigWarnings is enabled.
func logConfigWarning(format string, args ...interface{}) {
	if ConfigWarnings && ConfigWarningWriter != nil {
		_, _ = fmt.Fprintf(ConfigWarningWriter, format, args...) // Best effort: warning output should not cause failures
	}
}

// SyncMode represents the sync mode configuration
type SyncMode string

const (
	// SyncModeDoltNative uses Dolt remote directly (the only supported mode)
	SyncModeDoltNative SyncMode = "dolt-native"
)

// validSyncModes is the set of allowed sync mode values
var validSyncModes = map[SyncMode]bool{
	SyncModeDoltNative: true,
}

// ValidSyncModes returns the list of valid sync mode values.
func ValidSyncModes() []string {
	return []string{
		string(SyncModeDoltNative),
	}
}

// IsValidSyncMode returns true if the given string is a valid sync mode.
func IsValidSyncMode(mode string) bool {
	return validSyncModes[SyncMode(strings.ToLower(strings.TrimSpace(mode)))]
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

// GetSyncMode always returns SyncModeDoltNative.
// The sync mode config key is deprecated; Dolt-native is the only supported mode.
func GetSyncMode() SyncMode {
	return SyncModeDoltNative
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

// String returns the string representation of the Sovereignty.
func (s Sovereignty) String() string {
	return string(s)
}
