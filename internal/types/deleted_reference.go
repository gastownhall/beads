package types

import "strings"

// This file owns the deleted-reference citation rule: which occurrences of a
// deleted issue's id in a s's text count as citations of it, and how
// they become `[deleted:<id>]` markers. Both delete bodies — the store role
// (internal/storage/issueops.RewriteDeletedReferencesInTx) and the
// unit-of-work body (internal/storage/domain's rewriteTextReferences) — call
// the two functions below rather than spelling the rule again, because a
// route that spelled it differently would rewrite a different set of
// citations for the same deletion. `bd delete`'s preview (cmd/bd/delete.go)
// asks CitesID the same question, so it names the same ss the
// deletion rewrites.
//
// AN ID BYTE is [A-Za-z0-9_-]: the characters an id is made of, including the
// hyphen every bd id is full of. A '.' is not an id byte, but a hierarchical
// child's id is its parent's id plus ".N" (GenerateChildID), so the boundary
// has to know about dots anyway. The rule is symmetric:
//
//   - an occurrence is rejected when the byte before it is an id byte, or is
//     a '.' that itself follows an id byte (the id continues to the left);
//   - an occurrence is rejected when the byte after it is an id byte, or is
//     a '.' followed by an id byte (the id continues to the right).
//
// So `be-1.2`, `be-1._x`, `be-1.-x` and `x.be-1` are not citations of be-1 —
// the text names a longer id, which deleting be-1 must not touch — while
// `be-1.`, `(be-1)`, `be-1...` and `..be-1` are. Symmetry is what makes a
// multi-id deletion order-independent: a rule that only tightened the
// trailing boundary would rewrite `bd-1.bd-2` one way under one request
// order and another way under the reverse. When in doubt the rule leaves
// text unchanged: an un-rewritten citation is the safer mistake than a
// mangled one.

// CitesID reports whether text contains a citation of id: a literal
// occurrence of id standing at the boundaries RewriteDeletedReferences
// rewrites.
func CitesID(text, id string) bool {
	_, _, ok := nextCitation(text, id, 0)
	return ok
}

// RewriteDeletedReferences replaces every citation of id in text with
// `[deleted:<id>]` and reports whether it changed anything.
//
// The id is matched literally — never as a pattern or a replacement template
// — and only the ORIGINAL input is searched, so a marker this pass inserts is
// never itself matched and a delete cannot re-wrap what it just wrote. The
// search resumes at the END of a rewritten id, never past the boundary byte,
// so adjacent citations that share their one separator byte ("be-1 be-1")
// both rewrite; a consuming matcher would drop the second.
func RewriteDeletedReferences(text, id string) (string, bool) {
	if id == "" || text == "" {
		return text, false
	}
	var out strings.Builder
	marker := "[deleted:" + id + "]"
	cursor := 0
	changed := false
	for {
		start, end, ok := nextCitation(text, id, cursor)
		if !ok {
			break
		}
		out.WriteString(text[cursor:start])
		out.WriteString(marker)
		cursor = end
		changed = true
	}
	if !changed {
		return text, false
	}
	out.WriteString(text[cursor:])
	return out.String(), true
}

// nextCitation finds the next citation of id in text at or after from. A
// rejected occurrence resumes the search one byte later, so id-like runs that
// overlap are examined, never skipped.
func nextCitation(text, id string, from int) (start, end int, ok bool) {
	if id == "" {
		return 0, 0, false
	}
	for start = from; start+len(id) <= len(text); {
		offset := strings.Index(text[start:], id)
		if offset < 0 {
			return 0, 0, false
		}
		start += offset
		end = start + len(id)
		if leadingBoundary(text, start) && trailingBoundary(text, end) {
			return start, end, true
		}
		start++
	}
	return 0, 0, false
}

// leadingBoundary reads the byte BEFORE the occurrence text[start:...] from
// the whole input, so a start position handed over by an earlier match is
// still fenced by the byte that preceded it.
func leadingBoundary(text string, start int) bool {
	if start == 0 {
		return true
	}
	prev := text[start-1]
	if isIDByte(prev) {
		return false
	}
	// A '.' that follows an id byte continues that id: in "x.be-1" the
	// occurrence of be-1 is inside a longer dotted token, not a citation.
	return prev != '.' || start < 2 || !isIDByte(text[start-2])
}

// trailingBoundary reads the byte AFTER the occurrence text[:end] from the
// whole input.
func trailingBoundary(text string, end int) bool {
	if end == len(text) {
		return true
	}
	next := text[end]
	if isIDByte(next) {
		return false
	}
	// The trailing arm of the same rule: in "be-1.2" the occurrence of be-1
	// is the parent of a live child, not a citation; in "see be-1." the
	// sentence's full stop still ends the citation.
	return next != '.' || end+1 >= len(text) || !isIDByte(text[end+1])
}

func isIDByte(b byte) bool {
	return b == '_' || b == '-' ||
		(b >= '0' && b <= '9') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z')
}
