package main

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/ui"
)

// warnLabelsContainingWhitespace warns when a label being written contains a
// space, which is almost always a comma that was meant and missed (#5812).
//
// It is a WARNING and not an error, because a label containing a space is a
// legitimate thing to ask for and the shell already has the vocabulary to ask:
// `--labels 'one two'`, `--labels "one two"` and `--labels one\ two` all send
// one argument, exactly as they do for a filename containing a space. bd honors
// that boundary rather than re-splitting it — a caller who quoted or escaped
// has already said where the word ends, and overriding that would make labels
// the one place on the command line where quoting does not mean what it means
// everywhere else.
//
// What that leaves is the case the quoting cannot distinguish: someone typing
// `--labels 'theme:a theme:b'` who meant two labels gets one, silently. This is
// the whole failure mode behind #5812 — 150 corrupt rows across 111 issues,
// found months later only because theme counts failed to reconcile. A line on
// stderr is enough to catch it at the keystroke, and costs the deliberate user
// nothing but a line they can silence with --quiet.
//
// Deliberately not called for --remove-label: removing a space-containing label
// is how the damage gets repaired, and warning on the repair is noise.
func warnLabelsContainingWhitespace(labels []string) {
	if debug.IsQuiet() {
		return
	}
	var flagged []string
	for _, l := range labels {
		if strings.ContainsFunc(l, unicode.IsSpace) {
			flagged = append(flagged, l)
		}
	}
	if len(flagged) == 0 {
		return
	}
	for _, l := range flagged {
		fmt.Fprintf(os.Stderr, "%s Stored %q as ONE label — it contains a space.\n", ui.RenderWarn("⚠"), l)
	}
	fmt.Fprintf(os.Stderr, "  If you meant several labels, separate them with commas (a,b) or repeat the flag.\n")
}
