package config

import (
	"bytes"
	"os"
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

			// Capture stderr
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			result := GetSyncMode()

			// Restore stderr and get output
			w.Close()
			os.Stderr = oldStderr
			var buf bytes.Buffer
			buf.ReadFrom(r)
			stderrOutput := buf.String()

			if result != tt.expectedMode {
				t.Errorf("GetSyncMode() = %q, want %q", result, tt.expectedMode)
			}

			hasWarning := strings.Contains(stderrOutput, "Warning:")
			if tt.expectsWarning && !hasWarning {
				t.Errorf("Expected warning in stderr, got none. stderr=%q", stderrOutput)
			}
			if !tt.expectsWarning && hasWarning {
				t.Errorf("Unexpected warning in stderr: %q", stderrOutput)
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

			// Capture stderr
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			result := GetConflictStrategy()

			// Restore stderr and get output
			w.Close()
			os.Stderr = oldStderr
			var buf bytes.Buffer
			buf.ReadFrom(r)
			stderrOutput := buf.String()

			if result != tt.expectedStrategy {
				t.Errorf("GetConflictStrategy() = %q, want %q", result, tt.expectedStrategy)
			}

			hasWarning := strings.Contains(stderrOutput, "Warning:")
			if tt.expectsWarning && !hasWarning {
				t.Errorf("Expected warning in stderr, got none. stderr=%q", stderrOutput)
			}
			if !tt.expectsWarning && hasWarning {
				t.Errorf("Unexpected warning in stderr: %q", stderrOutput)
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
			name:           "empty returns default",
			configValue:    "",
			expectedTier:   SovereigntyT1,
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
			name:           "invalid value returns default with warning",
			configValue:    "T5",
			expectedTier:   SovereigntyT1,
			expectsWarning: true,
		},
		{
			name:           "invalid tier 0 returns default with warning",
			configValue:    "T0",
			expectedTier:   SovereigntyT1,
			expectsWarning: true,
		},
		{
			name:           "word tier returns default with warning",
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

			// Capture stderr
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w

			result := GetSovereignty()

			// Restore stderr and get output
			w.Close()
			os.Stderr = oldStderr
			var buf bytes.Buffer
			buf.ReadFrom(r)
			stderrOutput := buf.String()

			if result != tt.expectedTier {
				t.Errorf("GetSovereignty() = %q, want %q", result, tt.expectedTier)
			}

			hasWarning := strings.Contains(stderrOutput, "Warning:")
			if tt.expectsWarning && !hasWarning {
				t.Errorf("Expected warning in stderr, got none. stderr=%q", stderrOutput)
			}
			if !tt.expectsWarning && hasWarning {
				t.Errorf("Unexpected warning in stderr: %q", stderrOutput)
			}
		})
	}
}
