// Package ui provides terminal styling for beads CLI output.
package ui

import (
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/termutil"
)

// RenderMarkdown renders markdown text for terminal display.
// Performs simple word-wrapping at the terminal width (or 80 columns if
// width can't be detected). In agent mode or when colors are disabled,
// returns the text unmodified.
func RenderMarkdown(markdown string) string {
	if IsAgentMode() {
		return markdown
	}

	const maxReadableWidth = 100
	wrapWidth := 80
	if w, _, err := termutil.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		wrapWidth = w
	}
	if wrapWidth > maxReadableWidth {
		wrapWidth = maxReadableWidth
	}

	return wordWrap(markdown, wrapWidth)
}

// wordWrap wraps text to the given width, preserving existing line breaks.
func wordWrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	var out strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if len(line) <= width {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		col := 0
		for _, word := range strings.Fields(line) {
			wl := len(word)
			if col > 0 && col+1+wl > width {
				out.WriteByte('\n')
				col = 0
			}
			if col > 0 {
				out.WriteByte(' ')
				col++
			}
			out.WriteString(word)
			col += wl
		}
		out.WriteByte('\n')
	}
	// Trim trailing extra newline (Split adds one empty element for trailing \n)
	s := out.String()
	if strings.HasSuffix(text, "\n") {
		return s
	}
	return strings.TrimRight(s, "\n")
}
