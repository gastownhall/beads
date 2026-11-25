package linear

import "strings"

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
//   - Every complete idempotency marker is removed, with the newline before
//     it, wherever it sits.
//   - A candidate heading is a line that equals "## Acceptance Criteria",
//     "## Design" or "## Notes" once trailing spaces, tabs and "\r" are
//     trimmed. Case is exact and leading space is not allowed, so "### Notes",
//     "## notes" and "## Notes:" are text.
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
// reproduces text up to blank lines, trailing whitespace and markers.
func ParseLinearDescription(text string) DescriptionParts {
	body, removed := stripIdempotencyMarkers(text)
	lines := strings.Split(body, "\n")

	type section struct{ rank, line int }
	var lifted []section // bottom-up
	limit, end := len(linearSectionHeadings), len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		rank := linearSectionRank(lines[i])
		if rank < 0 {
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

// stripIdempotencyMarkers removes every complete idempotency marker from text,
// together with the newline before it (or, for a marker that opens the text,
// the newline after it). A marker an earlier pull folded into the description
// can sit mid-text. It reports whether it removed any.
func stripIdempotencyMarkers(text string) (string, bool) {
	var b strings.Builder
	removed := false
	rest := text
	for {
		start := strings.Index(rest, idempotencyPrefix)
		if start < 0 {
			break
		}
		stop := strings.Index(rest[start+len(idempotencyPrefix):], idempotencySuffix)
		if stop < 0 {
			break
		}
		stop += start + len(idempotencyPrefix)
		if strings.ContainsAny(rest[start:stop], "\r\n") {
			// Not a marker AppendIdempotencyMarker wrote; keep it as text.
			b.WriteString(rest[:stop])
			rest = rest[stop:]
			continue
		}
		before := rest[:start]
		after := rest[stop+len(idempotencySuffix):]
		switch {
		case strings.HasSuffix(before, "\n"):
			before = strings.TrimSuffix(strings.TrimSuffix(before, "\n"), "\r")
		case b.Len() == 0 && before == "":
			after = strings.TrimPrefix(strings.TrimPrefix(after, "\r"), "\n")
		}
		b.WriteString(before)
		rest = after
		removed = true
	}
	if !removed {
		return text, false
	}
	b.WriteString(rest)
	return b.String(), true
}
