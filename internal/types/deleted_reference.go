package types

import "strings"

// This file is the one citation rule `bd delete` uses to mark a deleted id in
// the text of the rows that survive it. Both delete bodies (the store body in
// internal/storage/issueops and the unit-of-work body in internal/storage/domain)
// and the CLI preview call it, so the routes cannot disagree about which
// citations a deletion rewrites. It lives here because both bodies already
// import this package and issueops imports domain, so neither could own it.
//
// A CITATION is a literal occurrence of the id that is not part of a longer id.
// An id byte is [A-Za-z0-9_-]. A dot joins id bytes into one id, because a
// hierarchical child's id is its parent's id plus `.<n>` (GenerateChildID), so
// an occurrence is NOT a citation when:
//
//   - the byte before it is an id byte, or is a `.` that itself follows an id
//     byte (`xbe-1`, `x.be-1`);
//   - the byte after it is an id byte, or is a `.` that an id byte follows
//     (`be-12`, `be-1.2`, `be-1.a`, `be-1._x`, `be-1.-x`).
//
// So `be-1.` ending a sentence, `be-1...`, `(be-1)` and `..be-1` are citations,
// and `be-1.2` is not a citation of be-1. Any other byte, including every byte
// of a non-ASCII character, is a boundary. The rule is symmetric on purpose:
// a dotted run like `bd-1.bd-2` cites neither id, whichever is deleted first.
// When in doubt the rule leaves text alone; an unmarked citation is the safer
// mistake than a live id marked deleted.

// CitesID reports whether text contains at least one citation of id under the
// rule above. It agrees exactly with whether RewriteDeletedReferences would
// change text.
func CitesID(text, id string) bool {
	if id == "" {
		return false
	}
	_, _, ok := nextCitation(text, id, 0)
	return ok
}

// RewriteDeletedReferences replaces every citation of id in text with
// `[deleted:<id>]` and reports whether it replaced any. The id is literal text,
// never a pattern or a replacement template. Matching reads the original text
// only: a marker it inserts is never searched again, and a separator between
// two adjacent citations serves as the boundary of both.
func RewriteDeletedReferences(text, id string) (string, bool) {
	if id == "" {
		return text, false
	}
	start, end, ok := nextCitation(text, id, 0)
	if !ok {
		return text, false
	}
	marker := deletedMarker(id)
	var b strings.Builder
	b.Grow(len(text) + len(marker) - len(id))
	copied := 0
	for ok {
		b.WriteString(text[copied:start])
		b.WriteString(marker)
		copied = end
		start, end, ok = nextCitation(text, id, end)
	}
	b.WriteString(text[copied:])
	return b.String(), true
}

// deletedMarker is what a citation of a deleted id becomes.
func deletedMarker(id string) string {
	return "[deleted:" + id + "]"
}

// nextCitation finds the first citation of a non-empty id in text at or after
// byte offset from. Boundary bytes are always read from the whole text, so a
// search resumed past an earlier match still sees the byte before it.
func nextCitation(text, id string, from int) (start, end int, ok bool) {
	for from <= len(text)-len(id) {
		i := strings.Index(text[from:], id)
		if i < 0 {
			return 0, 0, false
		}
		start = from + i
		end = start + len(id)
		if citationBoundaryBefore(text, start) && citationBoundaryAfter(text, end) {
			return start, end, true
		}
		// A rejected occurrence may overlap the next real one, so step one
		// byte rather than past the whole id.
		from = start + 1
	}
	return 0, 0, false
}

// citationBoundaryBefore reports whether the occurrence starting at start is
// not the tail of a longer id.
func citationBoundaryBefore(text string, start int) bool {
	if start == 0 {
		return true
	}
	prev := text[start-1]
	if isIDByte(prev) {
		return false
	}
	if prev == '.' && start >= 2 && isIDByte(text[start-2]) {
		return false
	}
	return true
}

// citationBoundaryAfter reports whether the occurrence ending at end is not
// the head of a longer id.
func citationBoundaryAfter(text string, end int) bool {
	if end == len(text) {
		return true
	}
	next := text[end]
	if isIDByte(next) {
		return false
	}
	if next == '.' && end+1 < len(text) && isIDByte(text[end+1]) {
		return false
	}
	return true
}

// isIDByte reports whether b can be part of an id: an ASCII letter or digit,
// `_` or `-`.
func isIDByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '_' || b == '-':
		return true
	}
	return false
}
