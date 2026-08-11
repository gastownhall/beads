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

// WrapWidth reports the column width RenderMarkdown wraps body text to, or 0
// in agent mode, where the text is emitted verbatim and nothing wraps. Callers
// deciding whether a value is short enough to sit inline instead of becoming a
// rendered section use this so their idea of "one line" matches the renderer's.
func WrapWidth() int {
	if ui.IsAgentMode() {
		return 0
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
	return wrapWidth
}

// RenderMarkdown renders markdown text using glamour's terminal style.
// Returns the rendered markdown or the original text if rendering fails.
// Word wraps at terminal width (or 80 columns if width can't be detected).
func RenderMarkdown(markdown string) string {
	wrapWidth := WrapWidth()
	if wrapWidth == 0 {
		return markdown
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

	// Bead bodies are plain text, not HTML. Goldmark (glamour's parser) treats any
	// "<" that looks like the start of an HTML tag as inline/block raw HTML, and
	// glamour's renderer sanitizes those raw-HTML nodes down to nothing — silently
	// deleting the tag-shaped span (and, for an unclosed tag, everything up to the
	// next ">", across lines). Escape angle brackets before rendering so nothing
	// is ever parsed as raw HTML, then unescape the entities glamour leaves intact
	// in its plain-text output so the visible result matches the stored text.
	rendered, err := renderer.Render(escapeAngleBrackets(markdown))
	if err != nil {
		return markdown
	}
	rendered = unescapeAngleBrackets(rendered)

	if !useHyperlinks {
		rendered = stripOSC8Hyperlinks(rendered)
	}
	if !useANSI && !useHyperlinks {
		rendered = xansi.Strip(rendered)
	}

	return rendered
}

// escapeAngleBrackets replaces literal "<" and ">" with their HTML entity
// equivalents so goldmark's inline/block HTML parsing never triggers on them.
func escapeAngleBrackets(s string) string {
	return angleEscaper.Replace(s)
}

// unescapeAngleBrackets reverses escapeAngleBrackets on rendered output.
// Glamour's plain-text rendering path passes entities through unchanged, so
// after rendering the escaped markdown, "&lt;"/"&gt;" in the output are the
// original literal angle brackets, not markup that needs to stay escaped.
func unescapeAngleBrackets(s string) string {
	return angleUnescaper.Replace(s)
}

var (
	angleEscaper   = strings.NewReplacer("<", "&lt;", ">", "&gt;")
	angleUnescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">")
)

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
