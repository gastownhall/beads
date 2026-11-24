package linear

import "strings"

// Linear section headings BuildLinearDescription appends, in the order it
// writes them.
const (
	linearAcceptanceHeading = "\n\n## Acceptance Criteria\n"
	linearDesignHeading     = "\n\n## Design\n"
	linearNotesHeading      = "\n\n## Notes\n"
)

// ParseLinearDescription inverts BuildLinearDescription plus the push's
// idempotency marker. It returns the description body and the acceptance
// criteria, design and notes that a push wrote as trailing sections (#8506).
//
// A section heading is recognized only in the canonical order (acceptance,
// design, notes), and the first occurrence of each counts. A description that
// itself contains one of these headings is split at it, so the round trip is
// exact only when the body does not contain a section heading.
func ParseLinearDescription(desc string) (description, acceptance, design, notes string) {
	body := stripLinearIdempotencyMarker(desc)

	type section struct {
		heading string
		start   int // index of the heading's first byte
	}
	var found []section
	headings := []string{linearAcceptanceHeading, linearDesignHeading, linearNotesHeading}
	from := 0
	for _, heading := range headings {
		idx := strings.Index(body[from:], heading)
		if idx < 0 {
			continue
		}
		start := from + idx
		found = append(found, section{heading: heading, start: start})
		from = start + len(heading)
	}
	if len(found) == 0 {
		return body, "", "", ""
	}

	description = body[:found[0].start]
	values := make(map[string]string, len(found))
	for i, sec := range found {
		end := len(body)
		if i+1 < len(found) {
			end = found[i+1].start
		}
		values[sec.heading] = body[sec.start+len(sec.heading) : end]
	}
	return description, values[linearAcceptanceHeading], values[linearDesignHeading], values[linearNotesHeading]
}

// stripLinearIdempotencyMarker removes the trailing bd-idempotency comment
// that AppendIdempotencyMarker added, along with the newline before it.
func stripLinearIdempotencyMarker(desc string) string {
	idx := strings.LastIndex(desc, idempotencyPrefix)
	if idx < 0 || !strings.HasSuffix(desc, idempotencySuffix) {
		return desc
	}
	body := desc[:idx]
	return strings.TrimSuffix(body, "\n")
}
