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

// quickReferencePushLine is the Quick Reference entry in the AGENTS.md
// template that tells agents to push beads data to the Dolt remote.
const quickReferencePushLine = "bd dolt push          # Push beads data to remote\n"

// EmbeddedDefault returns the full AGENTS.md template content.
func EmbeddedDefault() string {
	return defaultTemplate
}

// EmbeddedDefaultWithOpts returns the AGENTS.md template with the text outside
// its managed section adjusted for opts: under the rule the managed section
// follows, the Quick Reference leaves out "bd dolt push" when no Dolt remote is
// configured or the workspace is declared no-push. The managed section is
// returned as embedded; ReplaceSectionWithOpts renders it for a profile.
func EmbeddedDefaultWithOpts(opts RenderOpts) string {
	if !opts.omitsDoltPush() {
		return defaultTemplate
	}
	return strings.ReplaceAll(defaultTemplate, "\n"+quickReferencePushLine, "\n")
}

// EmbeddedBeadsSection returns the beads integration section with markers.
// The returned string is trimmed to match the existing agentsBeadsSection behavior
// (no trailing newline after the end marker).
func EmbeddedBeadsSection() string {
	return strings.TrimRight(beadsSection, "\n") + "\n"
}
