// Package uimd provides markdown rendering for beads CLI output.
// Keep this separate from internal/ui so non-markdown ui consumers do not
// inherit the glamour/chroma dependency graph.
// This package may depend on internal/ui for terminal policy checks, but
// internal/ui must not import internal/uimd.
package uimd

import (
	"os"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/steveyegge/beads/internal/ui"
	"golang.org/x/term"
)

// RenderMarkdown renders markdown text using glamour's terminal style.
// Returns the rendered markdown or the original text if rendering fails.
// Word wraps at terminal width (or 80 columns if width can't be detected).
func RenderMarkdown(markdown string) string {
	if ui.IsAgentMode() {
		return markdown
	}

	// Cap at 100 chars for readability; wider lines are harder to scan.
	const maxReadableWidth = 100
	wrapWidth := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		wrapWidth = w
	}
	if wrapWidth > maxReadableWidth {
		wrapWidth = maxReadableWidth
	}

	// Markdown rendering and terminal escape emission are separate concerns.
	// Even when ANSI color is unavailable, Glamour's notty style still improves
	// structure for tables, lists, wrapping, and links. ANSI SGR and OSC 8 are
	// stripped below unless their specific terminal capability checks pass.
	useANSI := ui.ShouldUseColor()
	useHyperlinks := ui.ShouldUseHyperlinks()
	options := []glamour.TermRendererOption{
		glamour.WithWordWrap(wrapWidth),
		glamour.WithPreservedNewLines(),
		glamour.WithTableWrap(false),
	}
	if useANSI {
		options = append(options,
			glamour.WithEnvironmentConfig(),
			glamour.WithChromaFormatter("terminal256"),
		)
	} else {
		options = append(options, glamour.WithStandardStyle(styles.NoTTYStyle))
	}

	renderer, err := glamour.NewTermRenderer(options...)
	if err != nil {
		return markdown
	}

	rendered, err := renderer.Render(markdown)
	if err != nil {
		return markdown
	}

	if !useHyperlinks {
		rendered = stripOSC8Hyperlinks(rendered)
	}
	if !useANSI && !useHyperlinks {
		rendered = xansi.Strip(rendered)
	}

	return rendered
}

// stripOSC8Hyperlinks removes only OSC 8 hyperlink open/close sequences.
// Glamour emits OSC 8 whenever it renders links, but OSC 8 support is separate
// from ANSI SGR color support. We keep regular ANSI styling intact when color is
// supported and only remove hyperlinks when ShouldUseHyperlinks says they are
// unsafe for the current terminal.
func stripOSC8Hyperlinks(s string) string {
	const osc8 = "\x1b]8;"
	if !strings.Contains(s, osc8) {
		return s
	}

	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], osc8) {
			if end := oscSequenceEnd(s, i+len(osc8)); end > i {
				i = end
				continue
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// oscSequenceEnd returns the byte index after an OSC control sequence.
// OSC strings can end with BEL or ST (ESC \); this helper keeps the stripping
// logic local to OSC 8 handling instead of using a broad ANSI stripper that would
// also remove color/style escapes we may still want to preserve.
func oscSequenceEnd(s string, start int) int {
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\a':
			return i + 1
		case '\x1b':
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	}
	return -1
}
