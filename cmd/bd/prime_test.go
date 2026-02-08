package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestOutputContextFunction(t *testing.T) {
	tests := []struct {
		name          string
		mcpMode       bool
		stealthMode   bool
		ephemeralMode bool
		localOnlyMode bool
		expectText    []string
		rejectText    []string
	}{
		{
			name:          "CLI Normal (non-ephemeral)",
			mcpMode:       false,
			stealthMode:   false,
			ephemeralMode: false,
			localOnlyMode: false,
			expectText:    []string{"Beads Workflow Context", "bd sync", "git push"},
			rejectText:    []string{"bd sync --flush-only", "--from-main"},
		},
		{
			name:          "CLI Normal (ephemeral)",
			mcpMode:       false,
			stealthMode:   false,
			ephemeralMode: true,
			localOnlyMode: false,
			expectText:    []string{"Beads Workflow Context", "bd sync --from-main", "ephemeral branch"},
			rejectText:    []string{"bd sync --flush-only", "git push"},
		},
		{
			name:          "CLI Stealth",
			mcpMode:       false,
			stealthMode:   true,
			ephemeralMode: false, // stealth mode overrides ephemeral detection
			localOnlyMode: false,
			expectText:    []string{"Beads Workflow Context", "bd sync --flush-only"},
			rejectText:    []string{"git push", "git pull", "git commit", "git status", "git add"},
		},
		{
			name:          "CLI Local-only (no git remote)",
			mcpMode:       false,
			stealthMode:   false,
			ephemeralMode: false,
			localOnlyMode: true,
			expectText:    []string{"Beads Workflow Context", "bd sync --flush-only", "No git remote configured"},
			rejectText:    []string{"git push", "git pull", "--from-main"},
		},
		{
			name:          "CLI Local-only overrides ephemeral",
			mcpMode:       false,
			stealthMode:   false,
			ephemeralMode: true, // ephemeral is true but local-only takes precedence
			localOnlyMode: true,
			expectText:    []string{"Beads Workflow Context", "bd sync --flush-only", "No git remote configured"},
			rejectText:    []string{"git push", "--from-main", "ephemeral branch"},
		},
		{
			name:          "CLI Stealth overrides local-only",
			mcpMode:       false,
			stealthMode:   true,
			ephemeralMode: false,
			localOnlyMode: true, // local-only is true but stealth takes precedence
			expectText:    []string{"Beads Workflow Context", "bd sync --flush-only"},
			rejectText:    []string{"git push", "git pull", "git commit", "git status", "git add", "No git remote configured"},
		},
		{
			name:          "MCP Normal (non-ephemeral)",
			mcpMode:       true,
			stealthMode:   false,
			ephemeralMode: false,
			localOnlyMode: false,
			expectText:    []string{"Beads Issue Tracker Active", "bd sync", "git push"},
			rejectText:    []string{"bd sync --flush-only", "--from-main"},
		},
		{
			name:          "MCP Normal (ephemeral)",
			mcpMode:       true,
			stealthMode:   false,
			ephemeralMode: true,
			localOnlyMode: false,
			expectText:    []string{"Beads Issue Tracker Active", "bd sync --from-main", "ephemeral branch"},
			rejectText:    []string{"bd sync --flush-only", "git push"},
		},
		{
			name:          "MCP Stealth",
			mcpMode:       true,
			stealthMode:   true,
			ephemeralMode: false, // stealth mode overrides ephemeral detection
			localOnlyMode: false,
			expectText:    []string{"Beads Issue Tracker Active", "bd sync --flush-only"},
			rejectText:    []string{"git push", "git pull", "git commit", "git status", "git add"},
		},
		{
			name:          "MCP Local-only (no git remote)",
			mcpMode:       true,
			stealthMode:   false,
			ephemeralMode: false,
			localOnlyMode: true,
			expectText:    []string{"Beads Issue Tracker Active", "bd sync --flush-only"},
			rejectText:    []string{"git push", "git pull", "--from-main"},
		},
		{
			name:          "MCP Local-only overrides ephemeral",
			mcpMode:       true,
			stealthMode:   false,
			ephemeralMode: true, // ephemeral is true but local-only takes precedence
			localOnlyMode: true,
			expectText:    []string{"Beads Issue Tracker Active", "bd sync --flush-only"},
			rejectText:    []string{"git push", "--from-main", "ephemeral branch"},
		},
		{
			name:          "MCP Stealth overrides local-only",
			mcpMode:       true,
			stealthMode:   true,
			ephemeralMode: false,
			localOnlyMode: true, // local-only is true but stealth takes precedence
			expectText:    []string{"Beads Issue Tracker Active", "bd sync --flush-only"},
			rejectText:    []string{"git push", "git pull", "git commit", "git status", "git add"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer stubIsEphemeralBranch(tt.ephemeralMode)()
			defer stubIsDaemonAutoSyncing(false)()           // Default: no auto-sync in tests
			defer stubPrimeHasGitRemote(!tt.localOnlyMode)() // localOnly = !primeHasGitRemote

			var buf bytes.Buffer
			err := outputPrimeContext(&buf, tt.mcpMode, tt.stealthMode)
			if err != nil {
				t.Fatalf("outputPrimeContext failed: %v", err)
			}

			output := buf.String()

			for _, expected := range tt.expectText {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected text not found: %s", expected)
				}
			}

			for _, rejected := range tt.rejectText {
				if strings.Contains(output, rejected) {
					t.Errorf("Unexpected text found: %s", rejected)
				}
			}
		})
	}
}

// stubIsEphemeralBranch temporarily replaces isEphemeralBranch
// with a stub returning returnValue.
//
// Returns a function to restore the original isEphemeralBranch.
// Usage:
//
//	defer stubIsEphemeralBranch(true)()
func stubIsEphemeralBranch(isEphem bool) func() {
	original := isEphemeralBranch
	isEphemeralBranch = func() bool {
		return isEphem
	}
	return func() {
		isEphemeralBranch = original
	}
}

// stubIsDaemonAutoSyncing temporarily replaces isDaemonAutoSyncing
// with a stub returning returnValue.
func stubIsDaemonAutoSyncing(isAutoSync bool) func() {
	original := isDaemonAutoSyncing
	isDaemonAutoSyncing = func() bool {
		return isAutoSync
	}
	return func() {
		isDaemonAutoSyncing = original
	}
}

// stubPrimeHasGitRemote temporarily replaces primeHasGitRemote
// with a stub returning returnValue.
//
// Returns a function to restore the original primeHasGitRemote.
// Usage:
//
//	defer stubPrimeHasGitRemote(true)()
func stubPrimeHasGitRemote(hasRemote bool) func() {
	original := primeHasGitRemote
	primeHasGitRemote = func() bool {
		return hasRemote
	}
	return func() {
		primeHasGitRemote = original
	}
}
