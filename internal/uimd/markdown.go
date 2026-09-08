// Package uimd provides markdown rendering for beads CLI output.
// Keep this separate from internal/ui so non-markdown ui consumers do not
// inherit the glamour/chroma dependency graph.
// This package may depend on internal/ui for terminal policy checks, but
// internal/ui must not import internal/uimd.
package uimd

import (
	"os"
	"strings"
	"unicode"

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
	rendered, err := renderer.Render(escapeAngleBrackets(neutralizeAmbiguousEmphasis(markdown)))
	if err != nil {
		return markdown
	}
	rendered = unescapeAngleBrackets(rendered)
	rendered = restoreAmbiguousEmphasis(rendered)

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

// Private-use runes stand in for emphasis delimiters that must not be parsed as
// markup. They are one cell wide, so word wrap and code-block padding are
// unaffected, and they survive code spans and fenced blocks untouched -- which a
// backslash escape does not (backslash escapes are not processed inside code, so
// "\*" would leak a literal backslash onto the screen) and an HTML entity does
// only at the cost of counting five columns instead of one.
const (
	asteriskSentinel   = ''
	underscoreSentinel = ''
)

var emphasisRestorer = strings.NewReplacer(
	string(asteriskSentinel), "*",
	string(underscoreSentinel), "_",
)

// neutralizeAmbiguousEmphasis hides "*" and "_" delimiter runs that CommonMark
// would let act as BOTH an opener and a closer.
//
// Bead bodies are plain text, not markdown authored for emphasis. A delimiter
// run is simultaneously left- and right-flanking -- able to pair with any other
// such run in the same paragraph, including one on a different line, since
// goldmark joins consecutive lines into one paragraph -- exactly when the
// characters flanking it are both punctuation or both non-punctuation. A quoted
// shell glob is precisely that shape: the "*" in '*.captured' sits between "'"
// and ".". So two globs pair up and everything between them is rendered as
// emphasis. When color is available the delimiters are consumed and the
// asterisks silently VANISH from the command; when it is not, glamour's notty
// style writes literal "**" into the middle of the text. Either way what is on
// screen is no longer the command that was stored -- and agents read bead bodies
// through bd show.
//
// Only both-flanking runs are hidden. A run that can only open or only close
// ("**bold**", "*italic*", "SELECT * FROM t", "-name *.log") is left alone, so
// ordinary authored emphasis renders as before. Flanking is judged against the
// original text so that hiding one run cannot change how the next is read.
//
// This is a deliberate trade, not a free win, and it costs two narrow shapes:
//
//   - An authored CLOSER that is itself both-flanking no longer pairs, so
//     'see *"quoted"*, next' renders literally instead of italicized. Only the
//     color path changes; glamour's notty style already printed that shape
//     literally.
//   - A backslash-escaped delimiter that is both-flanking keeps its backslash
//     on screen, because the sentinel replaces the character the escape was
//     reaching for: '\*.captured' renders as "\*.captured", not "*.captured".
//     An escape whose delimiter is not both-flanking ('a \*literal\* b') is
//     unaffected.
//
// Both are shapes CommonMark itself treats as ambiguous, and for bead bodies --
// which are stored plain text, not authored markdown -- showing the stored
// characters is the better failure. TestRenderMarkdownKnownEmphasisTrades pins
// both so a future change to the predicate cannot widen them unnoticed.
func neutralizeAmbiguousEmphasis(s string) string {
	if !strings.ContainsAny(s, "*_") {
		return s
	}

	runes := []rune(s)
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(runes); {
		delim := runes[i]
		if delim != '*' && delim != '_' {
			out.WriteRune(delim)
			i++
			continue
		}

		// Consume the whole delimiter run; CommonMark classifies runs, not
		// individual characters, so "**" must be judged as a unit.
		end := i
		for end < len(runes) && runes[end] == delim {
			end++
		}

		emit := delim
		if isBothFlanking(runes, i, end) {
			emit = asteriskSentinel
			if delim == '_' {
				emit = underscoreSentinel
			}
		}
		for range end - i {
			out.WriteRune(emit)
		}
		i = end
	}
	return out.String()
}

// restoreAmbiguousEmphasis turns the sentinels back into the literal delimiters
// the bead body actually contained.
func restoreAmbiguousEmphasis(s string) string {
	return emphasisRestorer.Replace(s)
}

// isBothFlanking reports whether the delimiter run runes[start:end] is both
// left- and right-flanking. Working through the CommonMark definitions for the
// three flanking-character classes (whitespace, punctuation, everything else),
// both conditions hold together in exactly two cases: punctuation on both sides,
// or non-punctuation non-whitespace on both sides. Text boundaries count as
// whitespace, as the spec requires.
func isBothFlanking(runes []rune, start, end int) bool {
	before, after := flankWhitespace, flankWhitespace
	if start > 0 {
		before = flankClassOf(runes[start-1])
	}
	if end < len(runes) {
		after = flankClassOf(runes[end])
	}
	return before == after && before != flankWhitespace
}

type flankClass int

const (
	flankWhitespace flankClass = iota
	flankPunctuation
	flankOther
)

// flankClassOf classifies a character for the flanking rules. CommonMark counts
// the Unicode symbol categories as punctuation alongside the punctuation
// categories proper, which matters for shell text: the "*" in "$*" is flanked by
// a currency symbol, and without that rule it would read as a class boundary and
// escape neutralization.
func flankClassOf(r rune) flankClass {
	switch {
	case unicode.IsSpace(r):
		return flankWhitespace
	case unicode.IsPunct(r) || unicode.IsSymbol(r):
		return flankPunctuation
	default:
		return flankOther
	}
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
