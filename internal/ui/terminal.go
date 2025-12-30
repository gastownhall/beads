// Package ui provides terminal styling and output helpers for beads CLI.
package ui

import (
	"os"

	"golang.org/x/term"
)

// IsTerminal returns true if stdout is connected to a terminal (TTY).
func IsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// ShouldUseColor determines if ANSI color codes should be used.
// Respects standard conventions:
//   - NO_COLOR: https://no-color.org/ - disables color if set
//   - CLICOLOR=0: disables color
//   - CLICOLOR_FORCE: forces color even in non-TTY
//   - Falls back to TTY detection
func ShouldUseColor() bool {
	// NO_COLOR standard - any value disables color
	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	// CLICOLOR=0 disables color
	if os.Getenv("CLICOLOR") == "0" {
		return false
	}

	// CLICOLOR_FORCE forces color even in non-TTY
	if os.Getenv("CLICOLOR_FORCE") != "" {
		return true
	}

	// Default: use color only if stdout is a TTY
	return IsTerminal()
}

// ShouldUseEmoji determines if emoji decorations should be used.
// Disabled in non-TTY mode to keep output machine-readable.
// Can be controlled with BD_NO_EMOJI environment variable.
func ShouldUseEmoji() bool {
	// Explicit disable
	if os.Getenv("BD_NO_EMOJI") != "" {
		return false
	}

	// Default: use emoji only if stdout is a TTY
	return IsTerminal()
}
