// Package agents provides embedded AGENTS.md templates for bd init and setup.
package agents

import (
	_ "embed"
	"strings"
)

//go:embed defaults/agents.md.tmpl
var defaultTemplate string

//go:embed defaults/beads-section.md
var beadsSection string

// EmbeddedDefault returns the full AGENTS.md template content.
func EmbeddedDefault() string {
	return defaultTemplate
}

// EmbeddedBeadsSection returns the beads integration section with markers.
// The returned string is trimmed to match the existing agentsBeadsSection behavior
// (no trailing newline after the end marker).
func EmbeddedBeadsSection() string {
	return strings.TrimRight(beadsSection, "\n") + "\n"
}

// EmbeddedDefaultWithOpts returns the AGENTS.md template with backend-aware
// rewrites applied. For flat-file workspaces the Dolt architecture blockquote,
// the cheat-sheet push line, and the managed section's Dolt guidance are all
// rewritten; other backends get the template verbatim. CRLF is normalized
// first so a Windows checkout still matches the LF anchors (cf. #3552).
func EmbeddedDefaultWithOpts(opts RenderOpts) string {
	if !opts.Flatfile {
		return defaultTemplate
	}
	doc := strings.ReplaceAll(defaultTemplate, "\r\n", "\n")
	doc = strings.ReplaceAll(doc, doltArchBlockquote, flatfileArchBlockquote)
	doc = strings.ReplaceAll(doc, doltCheatsheetPushLine, "")
	return flatfileizeBody(doc)
}
