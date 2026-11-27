package linear

import (
	"regexp"
	"strings"
)

// DescriptionParts holds the fields ParseLinearDescription lifts out of a
// Linear description body.
type DescriptionParts struct {
	Description        string
	AcceptanceCriteria string
	Design             string
	Notes              string
}

// descriptionSectionHeadings are the section headings BuildLinearDescription
// appends, in push order; the index is the heading's rank.
var descriptionSectionHeadings = []string{
	"## Acceptance Criteria",
	"## Design",
	"## Notes",
}

// idempotencyMarkerRe matches one complete bd-idempotency marker, wherever it
// sits in the text.
var idempotencyMarkerRe = regexp.MustCompile(`<!-- bd-idempotency: [0-9a-f]+ -->`)

// ParseLinearDescription inverts the fold a push makes: it removes the
// idempotency markers and lifts the trailing run of "## Acceptance Criteria",
// "## Design" and "## Notes" sections (the form BuildLinearDescription
// produces, optionally with Linear's blank line after each heading) back into
// the structured fields.
//
// The grammar:
//
//	marker    = a complete "<!-- bd-idempotency: … -->" line, removed together
//	            with the newline before it, wherever it sits
//	heading   = a line equal to a heading in descriptionSectionHeadings after
//	            trailing spaces, tabs and \r are trimmed; "### Notes",
//	            "## notes", "## Notes:" and " ## Notes" are plain text, and a
//	            heading inside a closed ``` or ~~~ fence is not a heading
//	run       = reading candidates bottom-up: accept one whose rank is lower
//	            than every rank accepted so far and whose body down to the
//	            previously accepted heading (or the end) is non-blank; the
//	            first candidate that fails ends the run
//
// Only the final in-order run with non-blank bodies is lifted; out-of-order,
// repeated and empty-bodied headings stay in the description with everything
// above them. When nothing is lifted and no marker is removed, the
// description comes back byte-for-byte; each lifted body drops its leading
// blank lines and trailing whitespace, and so does the description once the
// parse changed it (a lifted run or a removed marker). Text is never lost or
// reordered: what is not lifted stays in the description.
func ParseLinearDescription(text string) DescriptionParts {
	stripped, markerRemoved := stripIdempotencyMarkers(text)
	lines := strings.Split(stripped, "\n")
	hidden := hiddenFenceLines(lines)

	type candidate struct {
		line int // index into lines
		rank int // index into descriptionSectionHeadings
	}
	var candidates []candidate // in document order
	for i, line := range lines {
		if hidden[i] {
			continue
		}
		if rank := headingRank(line); rank >= 0 {
			candidates = append(candidates, candidate{line: i, rank: rank})
		}
	}

	// Scan the candidates bottom-up for the trailing in-order run. bodyEnd is
	// the exclusive end of the body of the candidate under consideration: the
	// line of the previously accepted heading, or the end of the text.
	limit := len(descriptionSectionHeadings)
	bodyEnd := len(lines)
	var lifted []candidate // bottom-up
	for i := len(candidates) - 1; i >= 0; i-- {
		c := candidates[i]
		if c.rank >= limit {
			break
		}
		if !hasNonBlankLine(lines, c.line+1, bodyEnd) {
			break
		}
		lifted = append(lifted, c)
		limit = c.rank
		bodyEnd = c.line
	}

	if len(lifted) == 0 {
		if markerRemoved {
			return DescriptionParts{Description: sectionBody(lines, 0, len(lines))}
		}
		return DescriptionParts{Description: text}
	}

	// lifted holds the run bottom-up; document order is the reverse.
	var parts DescriptionParts
	parts.Description = sectionBody(lines, 0, lifted[len(lifted)-1].line)
	for k := len(lifted) - 1; k >= 0; k-- {
		start := lifted[k]
		end := len(lines)
		if k > 0 {
			end = lifted[k-1].line
		}
		body := sectionBody(lines, start.line+1, end)
		switch start.rank {
		case 0:
			parts.AcceptanceCriteria = body
		case 1:
			parts.Design = body
		case 2:
			parts.Notes = body
		}
	}
	return parts
}

// headingRank returns the section rank of a heading line, or -1 when the line
// is not one of the section headings (the match is exact-case with no leading
// space after trailing blanks are trimmed away).
func headingRank(line string) int {
	t := strings.TrimRight(line, " \t\r")
	for rank, heading := range descriptionSectionHeadings {
		if t == heading {
			return rank
		}
	}
	return -1
}

// sectionBody joins the body lines of a lifted section, dropping the leading
// blank lines and the trailing whitespace.
func sectionBody(lines []string, start, end int) string {
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	return strings.TrimRight(strings.Join(lines[start:end], "\n"), " \t\r\n")
}

func hasNonBlankLine(lines []string, start, end int) bool {
	if start < 0 {
		start = 0
	}
	if end > len(lines) {
		end = len(lines)
	}
	for i := start; i < end; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return true
		}
	}
	return false
}

// stripIdempotencyMarkers removes every complete marker together with the
// newline before it (AppendIdempotencyMarker writes "desc\n" + marker, so the
// newline belongs to the marker). It reports whether anything was removed.
func stripIdempotencyMarkers(text string) (string, bool) {
	removed := false
	for {
		loc := idempotencyMarkerRe.FindStringIndex(text)
		if loc == nil {
			break
		}
		removed = true
		start, end := loc[0], loc[1]
		if start > 0 && text[start-1] == '\n' {
			start--
			if start > 0 && text[start-1] == '\r' {
				start--
			}
		}
		text = text[:start] + text[end:]
	}
	return text, removed
}

// canonicalLinearText normalizes a Linear description body for comparison:
// markers removed, blank lines dropped, every line right-trimmed. Two bodies
// equal in this form differ only in blank lines, trailing whitespace and
// idempotency markers.
func canonicalLinearText(s string) string {
	stripped, _ := stripIdempotencyMarkers(s)
	return strings.Join(nonBlankTrimmedLines(stripped), "\n")
}

// nonBlankTrimmedLines splits s into its non-blank lines, each right-trimmed.
func nonBlankTrimmedLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimRight(line, " \t\r\n"); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// hiddenFenceLines reports which lines sit inside a closed ``` or ~~~ code
// fence: a section heading there is quoted text, not a candidate. An unclosed
// fence hides nothing.
func hiddenFenceLines(lines []string) map[int]bool {
	hidden := make(map[int]bool)
	inFence := false
	var fenceChar byte
	for i, line := range lines {
		c, closing := fenceLine(line)
		if !inFence {
			if c != 0 && hasClosingFence(lines, i+1, c) {
				inFence = true
				fenceChar = c
			}
			continue
		}
		if closing && c == fenceChar {
			inFence = false
			continue
		}
		hidden[i] = true
	}
	return hidden
}

// fenceLine reports the fence character of a line that opens or closes a ```
// or ~~~ fence and whether the line is a closing fence (fence characters
// only). An opening fence may carry an info string ("```go").
func fenceLine(line string) (char byte, closing bool) {
	t := strings.TrimSpace(line)
	if len(t) < 3 {
		return 0, false
	}
	c := t[0]
	if c != '`' && c != '~' {
		return 0, false
	}
	n := 0
	for n < len(t) && t[n] == c {
		n++
	}
	if n < 3 {
		return 0, false
	}
	return c, strings.TrimSpace(t[n:]) == ""
}

func hasClosingFence(lines []string, from int, char byte) bool {
	for i := from; i < len(lines); i++ {
		if c, closing := fenceLine(lines[i]); closing && c == char {
			return true
		}
	}
	return false
}
