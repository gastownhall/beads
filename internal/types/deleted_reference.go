package types

import (
	"regexp"
	"strings"
)

// DeletedReferencePattern matches a citation of id that a delete rewrites, for
// both delete bodies: the literal id at ASCII word boundaries, where `-` is an
// id character and so is a `.` followed by one, because a child's id is its
// parent's plus `.<n>` (GenerateChildID). It matches `be-1` in "see (be-1)."
// and not in `xbe-1`, `be-12` or `be-1.2`. Group 2 is the id; groups 1 and 3
// consume the boundaries (RE2 has no lookahead), so replace citations with
// RewriteDeletedReferences rather than ReplaceAllString.
func DeletedReferencePattern(id string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_-])(` + regexp.QuoteMeta(id) + `)($|[^A-Za-z0-9_.-]|\.$|\.[^A-Za-z0-9_-])`)
}

// RewriteDeletedReferences replaces each citation of id in text with
// `[deleted:<id>]`; re must be DeletedReferencePattern(id). Each search resumes
// at the end of the id rather than of the match, leaving the trailing boundary
// for the next citation ("be-1 be-1", "see be-1. be-1"). On that suffix `^`
// can only match at the boundary byte, which no id begins with.
func RewriteDeletedReferences(re *regexp.Regexp, text, id string) string {
	if id == "" {
		return text
	}
	marker := "[deleted:" + id + "]"
	var out strings.Builder
	pos := 0
	for pos < len(text) {
		loc := re.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			break
		}
		idStart, idEnd := pos+loc[4], pos+loc[5]
		out.WriteString(text[pos:idStart])
		out.WriteString(marker)
		pos = idEnd
	}
	if pos == 0 {
		return text
	}
	out.WriteString(text[pos:])
	return out.String()
}
