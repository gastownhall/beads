// Package ui provides terminal styling for beads CLI output.
package ui

import (
	"os"

	"github.com/charmbracelet/glamour"
	"golang.org/x/term"
)

// RenderMarkdown renders markdown text using glamour with beads theme colors.
// Returns the rendered markdown or the original text if rendering fails.
// Word wraps at terminal width (or 80 columns if width can't be detected).
func RenderMarkdown(markdown string) string {
	// Skip glamour in agent mode to keep output clean for parsing
	if IsAgentMode() {
		return markdown
	}

	// Skip glamour if colors are disabled
	if !ShouldUseColor() {
		return markdown
	}

	// Detect terminal width for word wrap
	// Cap at 100 chars for readability - wider lines cause eye-tracking fatigue
	// Typography research suggests 50-75 chars optimal, 80-100 comfortable max
	const maxReadableWidth = 100
	wrapWidth := 80 // default if terminal size unavailable
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		wrapWidth = w
	}
	if wrapWidth > maxReadableWidth {
		wrapWidth = maxReadableWidth
	}

	// Create renderer with auto-detected style (respects terminal light/dark mode)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(wrapWidth),
	)
	if err != nil {
		// fallback to raw markdown on error
		return markdown
	}

	rendered, err := renderer.Render(markdown)
	if err != nil {
		// fallback to raw markdown on error
		return markdown
	}

	return rendered
}
