package linear

import (
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// The section headings BuildLinearDescription appends, in the order it
// writes them. A heading's index is its rank.
var linearSectionHeadings = [...]string{
	"## Acceptance Criteria",
	"## Design",
	"## Notes",
}

// DescriptionParts is a Linear description split back into the bead fields
// BuildLinearDescription folded into it.
type DescriptionParts struct {
	Description        string
	AcceptanceCriteria string
	Design             string
	Notes              string
}

// ParseLinearDescription inverts BuildLinearDescription and the idempotency
// marker a create appends, so a pull restores the fields a push folded into
// the Linear description (GH#8506).
//
// Grammar:
//   - Every line that is exactly bd's idempotency marker, outside a closed
//     code fence, is removed with the newline before it, wherever it sits.
//   - A candidate heading is a line that equals "## Acceptance Criteria",
//     "## Design" or "## Notes" once trailing spaces, tabs and "\r" are
//     trimmed. Case is exact and leading space is not allowed, so "### Notes",
//     "## notes" and "## Notes:" are text. A line inside a closed ``` or ~~~
//     code fence is not a candidate; an unclosed fence hides nothing.
//   - Only the trailing in-order run a push could have written is lifted.
//     Scanning candidates from the bottom up, a heading is accepted when it
//     ranks before the heading accepted below it (Acceptance Criteria, then
//     Design, then Notes) and its body, the lines down to that heading or to
//     the end, is not blank. The first candidate that fails ends the scan, so
//     out-of-order, repeated and empty-bodied headings stay in Description
//     with all the text above them.
//   - A lifted body loses its leading blank lines (Linear stores a blank line
//     after each heading) and its trailing whitespace. Description is the text
//     above the first lifted heading with trailing whitespace trimmed.
//   - When nothing is lifted and no marker is removed, Description is text
//     byte-for-byte.
//
// Text is never dropped or reordered: BuildLinearDescription of the result
// reproduces text up to canonicalLinearText (markers, trailing whitespace
// and the blank lines around a heading).
func ParseLinearDescription(text string) DescriptionParts {
	body, removed := stripIdempotencyMarkers(text)
	lines := strings.Split(body, "\n")
	fenced := closedFenceLines(lines)

	type section struct{ rank, line int }
	var lifted []section // bottom-up
	limit, end := len(linearSectionHeadings), len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		rank := linearSectionRank(lines[i])
		if rank < 0 || fenced[i] {
			continue
		}
		if rank >= limit || linesBlank(lines[i+1:end]) {
			break
		}
		lifted = append(lifted, section{rank: rank, line: i})
		limit, end = rank, i
	}

	if len(lifted) == 0 {
		if !removed {
			return DescriptionParts{Description: text}
		}
		return DescriptionParts{Description: strings.TrimRight(body, " \t\r\n")}
	}

	var fields [len(linearSectionHeadings)]string
	end = len(lines)
	for _, s := range lifted {
		fields[s.rank] = sectionBody(lines[s.line+1 : end])
		end = s.line
	}
	return DescriptionParts{
		Description:        strings.TrimRight(strings.Join(lines[:end], "\n"), " \t\r\n"),
		AcceptanceCriteria: fields[0],
		Design:             fields[1],
		Notes:              fields[2],
	}
}

// closedFenceLines marks the lines of every closed ``` or ~~~ code fence,
// fence lines included. A fence opens on a line indented at most three spaces
// that starts with three or more backticks or tildes, and closes on such a
// line of the same character, at least as long, with nothing after it. A
// fence that never closes marks nothing.
func closedFenceLines(lines []string) []bool {
	fenced := make([]bool, len(lines))
	open, openChar, openLen := -1, byte(0), 0
	for i, line := range lines {
		char, n, rest := fenceRun(line)
		if n == 0 {
			continue
		}
		if open < 0 {
			open, openChar, openLen = i, char, n
			continue
		}
		if char == openChar && n >= openLen && strings.TrimSpace(rest) == "" {
			for j := open; j <= i; j++ {
				fenced[j] = true
			}
			open = -1
		}
	}
	return fenced
}

// fenceRun returns the fence character and run length that open line, and
// the text after the run, or n == 0 when line is not a fence line.
func fenceRun(line string) (char byte, n int, rest string) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || trimmed == "" || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, ""
	}
	char = trimmed[0]
	for n < len(trimmed) && trimmed[n] == char {
		n++
	}
	if n < 3 {
		return 0, 0, ""
	}
	return char, n, trimmed[n:]
}

// linearSectionRank returns the rank of line as a candidate section heading,
// or -1 when it is not one.
func linearSectionRank(line string) int {
	trimmed := strings.TrimRight(line, " \t\r")
	for rank, heading := range linearSectionHeadings {
		if trimmed == heading {
			return rank
		}
	}
	return -1
}

func linesBlank(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return false
		}
	}
	return true
}

// sectionBody joins a lifted section's lines without its leading blank lines
// and trailing whitespace.
func sectionBody(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return strings.TrimRight(strings.Join(lines, "\n"), " \t\r\n")
}

// stripIdempotencyMarkers removes every line that is a marker
// GenerateIdempotencyMarker makes (isIdempotencyMarkerLine) and is not inside
// a closed code fence, together with the newline before it (or, for a marker
// on the first line, the newline after it). AppendIdempotencyMarker puts the
// marker on a line of its own; one an earlier pull folded into the
// description can sit mid-text. Marker text anywhere else, quoted in a
// sentence or shown in a code block, is text. It reports whether it removed
// any.
func stripIdempotencyMarkers(text string) (string, bool) {
	if !strings.Contains(text, idempotencyPrefix) {
		return text, false
	}
	lines := strings.Split(text, "\n")
	fenced := closedFenceLines(lines)
	kept := make([]string, 0, len(lines))
	for i, line := range lines {
		if !fenced[i] && isIdempotencyMarkerLine(line) {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == len(lines) {
		return text, false
	}
	return strings.Join(kept, "\n"), true
}

// isIdempotencyMarkerLine reports whether line, less trailing spaces, tabs
// and "\r", is exactly a marker GenerateIdempotencyMarker makes: the prefix,
// idempotencyHashLen lowercase hex digits and the suffix.
func isIdempotencyMarkerLine(line string) bool {
	hash, ok := strings.CutPrefix(strings.TrimRight(line, " \t\r"), idempotencyPrefix)
	if !ok {
		return false
	}
	hash, ok = strings.CutSuffix(hash, idempotencySuffix)
	if !ok || len(hash) != idempotencyHashLen {
		return false
	}
	for i := 0; i < len(hash); i++ {
		if c := hash[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// canonicalLinearText reduces a Linear description to what a push compares.
// It undoes what bd's create and Linear's storage add to the text bd pushed
// and keeps every other change, blank lines included:
//   - bd's idempotency marker lines are removed (stripIdempotencyMarkers);
//   - trailing spaces, tabs and "\r" are trimmed from every line;
//   - outside a closed code fence, a blank line next to a heading is dropped
//     (Linear stores a blank line after each heading, and Markdown renders
//     none), and a run of blank lines counts as one;
//   - blank lines at the start and the end are dropped.
//
// Every line inside a closed fence, blank or not, counts as it is. So
// splitting or joining paragraphs, or adding a blank line inside a code
// block, is a change on either side. Trailing spaces alone, which Markdown
// reads as a hard line break, are not.
func canonicalLinearText(text string) string {
	body, _ := stripIdempotencyMarkers(text)
	lines := strings.Split(body, "\n")
	fenced := closedFenceLines(lines)
	kept := make([]string, 0, len(lines))
	prevHeading, blank := false, false
	for i, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if line == "" && !fenced[i] {
			blank = true
			continue
		}
		heading := !fenced[i] && isMarkdownHeading(line)
		if blank && len(kept) > 0 && !prevHeading && !heading {
			kept = append(kept, "")
		}
		kept = append(kept, line)
		prevHeading, blank = heading, false
	}
	return strings.Join(kept, "\n")
}

// isMarkdownHeading reports whether line is an ATX heading: up to three
// spaces, one to six '#', then a space, a tab or the end of the line.
func isMarkdownHeading(line string) bool {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	return n >= 1 && n <= 6 && (n == len(trimmed) || trimmed[n] == ' ' || trimmed[n] == '\t')
}

// visibleLinearLines returns the non-blank lines of text, markers removed and
// trailing whitespace trimmed. SettleDescriptionFields matches a field's
// text against the Linear text with it, so text Linear shows with other
// blank lines or spacing still counts as shown and is never pushed twice.
func visibleLinearLines(text string) []string {
	body, _ := stripIdempotencyMarkers(text)
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimRight(line, " \t\r"); line != "" {
			kept = append(kept, line)
		}
	}
	return kept
}

// SettleDescriptionFields implements tracker.DescriptionFieldsTracker. A pull
// parsed pulled's description, acceptance criteria, design and notes out of
// the Linear description.
//
// When the Linear text is the bead's own (DescriptionFieldsEqual), pulled
// takes existing's four fields: an unchanged bead is never reshaped, even
// one whose description ends in a heading the parser lifts. Otherwise a
// heading of a field the bead holds, left in Linear with nothing under it,
// is that section with its text deleted (liftEmptiedSections): the heading
// is dropped, the sections above it are lifted, and the field is absent.
// Then, per field:
//   - A section Linear shows wins, so a Linear edit flows into the bead.
//   - An absent section empties a blank local field, and one whose text
//     Linear still shows elsewhere (its non-blank lines adjacent and in
//     order), as after someone deleted the heading in Linear.
//   - Otherwise the local text is kept and keptLocal reported, so the push
//     restores the section.
//
// keptLocal is also reported when a heading was dropped, so the push
// removes it from Linear even when its field empties. A pull therefore
// never drops a field's text and never makes a push carry it twice. Clearing
// a field takes a local edit (bd update --clear-notes, --design "" or
// --acceptance "") and a push.
func (t *Tracker) SettleDescriptionFields(existing, pulled *types.Issue) (keptLocal bool) {
	if existing == nil || pulled == nil {
		return false
	}
	if DescriptionFieldsEqual(existing, pulled) {
		pulled.Description = existing.Description
		pulled.AcceptanceCriteria = existing.AcceptanceCriteria
		pulled.Design = existing.Design
		pulled.Notes = existing.Notes
		return false
	}
	keptLocal = liftEmptiedSections(existing, pulled)
	remote := visibleLinearLines(BuildLinearDescription(pulled))
	for _, field := range []struct {
		local  string
		pulled *string
	}{
		{existing.AcceptanceCriteria, &pulled.AcceptanceCriteria},
		{existing.Design, &pulled.Design},
		{existing.Notes, &pulled.Notes},
	} {
		if strings.TrimSpace(*field.pulled) != "" {
			continue
		}
		*field.pulled = ""
		local := visibleLinearLines(field.local)
		if len(local) == 0 || containsLineRun(remote, local) {
			continue
		}
		*field.pulled = field.local
		keptLocal = true
	}
	return keptLocal
}

// liftEmptiedSections carries on the parse of pulled's description past the
// headings Linear left with nothing under them, for the fields existing
// holds, and reports whether it dropped any. ParseLinearDescription stops at
// the first in-order heading whose body is blank, so deleting a section's
// text in Linear but keeping its heading leaves that heading, and every
// section above it, in the description.
//
// The scan starts where the parse stopped. A heading that ranks before the
// sections the parse lifted and has a blank body is dropped, and its field
// left absent, when existing holds that field. The bead never pushed such a
// heading: BuildLinearDescription writes a field's own heading, with its
// text, below any bare heading of that field in the description, so on the
// text the bead pushed the scan stops before reaching one. After a drop the
// scan lifts the sections above it as the parser does, and drops the next
// such heading too. A bare heading of a field existing leaves blank is
// text, as the parser has it.
func liftEmptiedSections(existing, pulled *types.Issue) (dropped bool) {
	local := [len(linearSectionHeadings)]string{existing.AcceptanceCriteria, existing.Design, existing.Notes}
	fields := [len(linearSectionHeadings)]*string{&pulled.AcceptanceCriteria, &pulled.Design, &pulled.Notes}
	limit := len(linearSectionHeadings)
	for rank, field := range fields {
		if strings.TrimSpace(*field) != "" {
			limit = rank
			break
		}
	}
	lines := strings.Split(pulled.Description, "\n")
	fenced := closedFenceLines(lines)
	end := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		rank := linearSectionRank(lines[i])
		if rank < 0 || fenced[i] {
			continue
		}
		if rank >= limit {
			break
		}
		if body := lines[i+1 : end]; !linesBlank(body) {
			*fields[rank] = sectionBody(body)
		} else if len(visibleLinearLines(local[rank])) > 0 {
			dropped = true
		} else {
			break
		}
		limit, end = rank, i
	}
	if end < len(lines) {
		pulled.Description = strings.TrimRight(strings.Join(lines[:end], "\n"), " \t\r\n")
	}
	return dropped
}

// containsLineRun reports whether run appears in lines as adjacent lines, in
// order.
func containsLineRun(lines, run []string) bool {
	for start := 0; start+len(run) <= len(lines); start++ {
		match := true
		for i, line := range run {
			if lines[start+i] != line {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
