package config

import (
	"bytes"
	"strings"
	"testing"
)

func TestGetSyncMode(t *testing.T) {
	tests := []struct {
		name           string
		configValue    string
		expectedMode   SyncMode
		expectsWarning bool
	}{
		{
			name:           "empty returns default",
			configValue:    "",
			expectedMode:   SyncModeGitPortable,
			expectsWarning: false,
		},
		{
			name:           "git-portable is valid",
			configValue:    "git-portable",
			expectedMode:   SyncModeGitPortable,
			expectsWarning: false,
		},
		{
			name:           "realtime is valid",
			configValue:    "realtime",
			expectedMode:   SyncModeRealtime,
			expectsWarning: false,
		},
		{
			name:           "dolt-native is valid",
			configValue:    "dolt-native",
			expectedMode:   SyncModeDoltNative,
			expectsWarning: false,
		},
		{
			name:           "belt-and-suspenders is valid",
			configValue:    "belt-and-suspenders",
			expectedMode:   SyncModeBeltAndSuspenders,
			expectsWarning: false,
		},
		{
			name:           "mixed case is normalized",
			configValue:    "Git-Portable",
			expectedMode:   SyncModeGitPortable,
			expectsWarning: false,
		},
		{
			name:           "whitespace is trimmed",
			configValue:    "  realtime  ",
			expectedMode:   SyncModeRealtime,
			expectsWarning: false,
		},
		{
			name:           "invalid value returns default with warning",
			configValue:    "invalid-mode",
			expectedMode:   SyncModeGitPortable,
			expectsWarning: true,
		},
		{
			name:           "typo returns default with warning",
			configValue:    "git-portabel",
			expectedMode:   SyncModeGitPortable,
			expectsWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset viper for test
			ResetForTesting()
			if err := Initialize(); err != nil {
				t.Fatalf("Initialize failed: %v", err)
			}

			// Set the config value
			if tt.configValue != "" {
				Set("sync.mode", tt.configValue)
			}

			// Capture warnings using ConfigWarningWriter
			var buf bytes.Buffer
			oldWriter := ConfigWarningWriter
			ConfigWarningWriter = &buf
			defer func() { ConfigWarningWriter = oldWriter }()

			result := GetSyncMode()

			stderrOutput := buf.String()

			if result != tt.expectedMode {
				t.Errorf("GetSyncMode() = %q, want %q", result, tt.expectedMode)
			}

			hasWarning := strings.Contains(stderrOutput, "Warning:")
			if tt.expectsWarning && !hasWarning {
				t.Errorf("Expected warning in output, got none. output=%q", stderrOutput)
			}
			if !tt.expectsWarning && hasWarning {
				t.Errorf("Unexpected warning in output: %q", stderrOutput)
			}
		})
	}
}

func TestGetConflictStrategy(t *testing.T) {
	tests := []struct {
		name             string
		configValue      string
		expectedStrategy ConflictStrategy
		expectsWarning   bool
	}{
		{
			name:             "empty returns default",
			configValue:      "",
			expectedStrategy: ConflictStrategyNewest,
			expectsWarning:   false,
		},
		{
			name:             "newest is valid",
			configValue:      "newest",
			expectedStrategy: ConflictStrategyNewest,
			expectsWarning:   false,
		},
		{
			name:             "ours is valid",
			configValue:      "ours",
			expectedStrategy: ConflictStrategyOurs,
			expectsWarning:   false,
		},
		{
			name:             "theirs is valid",
			configValue:      "theirs",
			expectedStrategy: ConflictStrategyTheirs,
			expectsWarning:   false,
		},
		{
			name:             "manual is valid",
			configValue:      "manual",
			expectedStrategy: ConflictStrategyManual,
			expectsWarning:   false,
		},
		{
			name:             "mixed case is normalized",
			configValue:      "NEWEST",
			expectedStrategy: ConflictStrategyNewest,
			expectsWarning:   false,
		},
		{
			name:             "whitespace is trimmed",
			configValue:      "  ours  ",
			expectedStrategy: ConflictStrategyOurs,
			expectsWarning:   false,
		},
		{
			name:             "invalid value returns default with warning",
			configValue:      "invalid-strategy",
			expectedStrategy: ConflictStrategyNewest,
			expectsWarning:   true,
		},
		{
			name:             "last-write-wins typo returns default with warning",
			configValue:      "last-write-wins",
			expectedStrategy: ConflictStrategyNewest,
			expectsWarning:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset viper for test
			ResetForTesting()
			if err := Initialize(); err != nil {
				t.Fatalf("Initialize failed: %v", err)
			}

			// Set the config value
			if tt.configValue != "" {
				Set("conflict.strategy", tt.configValue)
			}

			// Capture warnings using ConfigWarningWriter
			var buf bytes.Buffer
			oldWriter := ConfigWarningWriter
			ConfigWarningWriter = &buf
			defer func() { ConfigWarningWriter = oldWriter }()

			result := GetConflictStrategy()

			stderrOutput := buf.String()

			if result != tt.expectedStrategy {
				t.Errorf("GetConflictStrategy() = %q, want %q", result, tt.expectedStrategy)
			}

			hasWarning := strings.Contains(stderrOutput, "Warning:")
			if tt.expectsWarning && !hasWarning {
				t.Errorf("Expected warning in output, got none. output=%q", stderrOutput)
			}
			if !tt.expectsWarning && hasWarning {
				t.Errorf("Unexpected warning in output: %q", stderrOutput)
			}
		})
	}
}

func TestGetSovereignty(t *testing.T) {
	tests := []struct {
		name           string
		configValue    string
		expectedTier   Sovereignty
		expectsWarning bool
	}{
		{
			name:           "empty returns no restriction",
			configValue:    "",
			expectedTier:   SovereigntyNone,
			expectsWarning: false,
		},
		{
			name:           "T1 is valid",
			configValue:    "T1",
			expectedTier:   SovereigntyT1,
			expectsWarning: false,
		},
		{
			name:           "T2 is valid",
			configValue:    "T2",
			expectedTier:   SovereigntyT2,
			expectsWarning: false,
		},
		{
			name:           "T3 is valid",
			configValue:    "T3",
			expectedTier:   SovereigntyT3,
			expectsWarning: false,
		},
		{
			name:           "T4 is valid",
			configValue:    "T4",
			expectedTier:   SovereigntyT4,
			expectsWarning: false,
		},
		{
			name:           "lowercase is normalized",
			configValue:    "t1",
			expectedTier:   SovereigntyT1,
			expectsWarning: false,
		},
		{
			name:           "whitespace is trimmed",
			configValue:    "  T2  ",
			expectedTier:   SovereigntyT2,
			expectsWarning: false,
		},
		{
			name:           "invalid value returns T1 with warning",
			configValue:    "T5",
			expectedTier:   SovereigntyT1,
			expectsWarning: true,
		},
		{
			name:           "invalid tier 0 returns T1 with warning",
			configValue:    "T0",
			expectedTier:   SovereigntyT1,
			expectsWarning: true,
		},
		{
			name:           "word tier returns T1 with warning",
			configValue:    "public",
			expectedTier:   SovereigntyT1,
			expectsWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset viper for test
			ResetForTesting()
			if err := Initialize(); err != nil {
				t.Fatalf("Initialize failed: %v", err)
			}

			// Set the config value
			if tt.configValue != "" {
				Set("federation.sovereignty", tt.configValue)
			}

			// Capture warnings using ConfigWarningWriter
			var buf bytes.Buffer
			oldWriter := ConfigWarningWriter
			ConfigWarningWriter = &buf
			defer func() { ConfigWarningWriter = oldWriter }()

			result := GetSovereignty()

			stderrOutput := buf.String()

			if result != tt.expectedTier {
				t.Errorf("GetSovereignty() = %q, want %q", result, tt.expectedTier)
			}

			hasWarning := strings.Contains(stderrOutput, "Warning:")
			if tt.expectsWarning && !hasWarning {
				t.Errorf("Expected warning in output, got none. output=%q", stderrOutput)
			}
			if !tt.expectsWarning && hasWarning {
				t.Errorf("Unexpected warning in output: %q", stderrOutput)
			}
		})
	}
}

func TestConfigWarningsToggle(t *testing.T) {
	// Reset viper for test
	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Set an invalid value
	Set("sync.mode", "invalid-mode")

	// Capture warnings
	var buf bytes.Buffer
	oldWriter := ConfigWarningWriter
	ConfigWarningWriter = &buf

	// With warnings enabled (default)
	ConfigWarnings = true
	_ = GetSyncMode()
	if !strings.Contains(buf.String(), "Warning:") {
		t.Error("Expected warning with ConfigWarnings=true, got none")
	}

	// With warnings disabled
	buf.Reset()
	ConfigWarnings = false
	_ = GetSyncMode()
	if strings.Contains(buf.String(), "Warning:") {
		t.Error("Expected no warning with ConfigWarnings=false, got one")
	}

	// Restore defaults
	ConfigWarnings = true
	ConfigWarningWriter = oldWriter
}

func TestIsValidSyncMode(t *testing.T) {
	tests := []struct {
		mode  string
		valid bool
	}{
		{"git-portable", true},
		{"realtime", true},
		{"dolt-native", true},
		{"belt-and-suspenders", true},
		{"Git-Portable", true}, // case insensitive
		{"  realtime  ", true}, // whitespace trimmed
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := IsValidSyncMode(tt.mode); got != tt.valid {
			t.Errorf("IsValidSyncMode(%q) = %v, want %v", tt.mode, got, tt.valid)
		}
	}
}

func TestIsValidConflictStrategy(t *testing.T) {
	tests := []struct {
		strategy string
		valid    bool
	}{
		{"newest", true},
		{"ours", true},
		{"theirs", true},
		{"manual", true},
		{"NEWEST", true},   // case insensitive
		{"  ours  ", true}, // whitespace trimmed
		{"invalid", false},
		{"lww", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := IsValidConflictStrategy(tt.strategy); got != tt.valid {
			t.Errorf("IsValidConflictStrategy(%q) = %v, want %v", tt.strategy, got, tt.valid)
		}
	}
}

func TestIsValidSovereignty(t *testing.T) {
	tests := []struct {
		sovereignty string
		valid       bool
	}{
		{"T1", true},
		{"T2", true},
		{"T3", true},
		{"T4", true},
		{"t1", true},     // case insensitive
		{"  T2  ", true}, // whitespace trimmed
		{"", true},       // empty is valid (no restriction)
		{"T0", false},
		{"T5", false},
		{"public", false},
	}

	for _, tt := range tests {
		if got := IsValidSovereignty(tt.sovereignty); got != tt.valid {
			t.Errorf("IsValidSovereignty(%q) = %v, want %v", tt.sovereignty, got, tt.valid)
		}
	}
}

func TestValidSyncModes(t *testing.T) {
	modes := ValidSyncModes()
	if len(modes) != 4 {
		t.Errorf("ValidSyncModes() returned %d modes, want 4", len(modes))
	}
	expected := []string{"git-portable", "realtime", "dolt-native", "belt-and-suspenders"}
	for i, m := range modes {
		if m != expected[i] {
			t.Errorf("ValidSyncModes()[%d] = %q, want %q", i, m, expected[i])
		}
	}
}

func TestValidConflictStrategies(t *testing.T) {
	strategies := ValidConflictStrategies()
	if len(strategies) != 4 {
		t.Errorf("ValidConflictStrategies() returned %d strategies, want 4", len(strategies))
	}
	expected := []string{"newest", "ours", "theirs", "manual"}
	for i, s := range strategies {
		if s != expected[i] {
			t.Errorf("ValidConflictStrategies()[%d] = %q, want %q", i, s, expected[i])
		}
	}
}

func TestValidSovereigntyTiers(t *testing.T) {
	tiers := ValidSovereigntyTiers()
	if len(tiers) != 4 {
		t.Errorf("ValidSovereigntyTiers() returned %d tiers, want 4", len(tiers))
	}
	expected := []string{"T1", "T2", "T3", "T4"}
	for i, tier := range tiers {
		if tier != expected[i] {
			t.Errorf("ValidSovereigntyTiers()[%d] = %q, want %q", i, tier, expected[i])
		}
	}
}

func TestSyncModeString(t *testing.T) {
	if got := SyncModeGitPortable.String(); got != "git-portable" {
		t.Errorf("SyncModeGitPortable.String() = %q, want %q", got, "git-portable")
	}
}

func TestConflictStrategyString(t *testing.T) {
	if got := ConflictStrategyNewest.String(); got != "newest" {
		t.Errorf("ConflictStrategyNewest.String() = %q, want %q", got, "newest")
	}
}

func TestSovereigntyString(t *testing.T) {
	if got := SovereigntyT1.String(); got != "T1" {
		t.Errorf("SovereigntyT1.String() = %q, want %q", got, "T1")
	}
	if got := SovereigntyNone.String(); got != "" {
		t.Errorf("SovereigntyNone.String() = %q, want %q", got, "")
	}
}

func TestFieldStrategyString(t *testing.T) {
	tests := []struct {
		strategy FieldStrategy
		expected string
	}{
		{FieldStrategyNewest, "newest"},
		{FieldStrategyMax, "max"},
		{FieldStrategyUnion, "union"},
		{FieldStrategyManual, "manual"},
	}

	for _, tt := range tests {
		if got := tt.strategy.String(); got != tt.expected {
			t.Errorf("%v.String() = %q, want %q", tt.strategy, got, tt.expected)
		}
	}
}

func TestValidFieldStrategies(t *testing.T) {
	strategies := ValidFieldStrategies()
	if len(strategies) != 4 {
		t.Errorf("ValidFieldStrategies() returned %d strategies, want 4", len(strategies))
	}
	expected := []string{"newest", "max", "union", "manual"}
	for i, s := range strategies {
		if s != expected[i] {
			t.Errorf("ValidFieldStrategies()[%d] = %q, want %q", i, s, expected[i])
		}
	}
}

func TestIsValidFieldStrategy(t *testing.T) {
	tests := []struct {
		strategy string
		valid    bool
	}{
		{"newest", true},
		{"max", true},
		{"union", true},
		{"manual", true},
		{"NEWEST", true},  // case insensitive
		{"  max  ", true}, // whitespace trimmed
		{"invalid", false},
		{"lww", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := IsValidFieldStrategy(tt.strategy); got != tt.valid {
			t.Errorf("IsValidFieldStrategy(%q) = %v, want %v", tt.strategy, got, tt.valid)
		}
	}
}
