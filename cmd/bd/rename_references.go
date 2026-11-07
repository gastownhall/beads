package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// The reference scan's matching rule. Rename admits ASCII letters, digits,
// underscore, dot and hyphen in the suffix of an issue ID, so a reference to
// an ID is a maximal run of exactly those characters: word boundaries do not
// fall at ID boundaries, and a \b<id>\b regex both corrupts longer IDs that
// contain the old one (renaming bd-auth rewrote the bd-auth inside
// bd-auth-v2) and rewrites the parent part of dotted children (GH#7040).

// isIDRefChar reports whether c can appear inside an issue ID.
func isIDRefChar(c byte) bool {
	return c == '_' || c == '.' || c == '-' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// replaceIDReferences returns text with every standalone mention of oldID
// replaced by newID. A mention is a maximal ID-character token equal to
// oldID, or oldID followed only by terminal periods, which are kept as
// sentence punctuation. Longer tokens that contain oldID are preserved:
// dotted children (bd-1.2 while renaming bd-1), and hyphenated, prefixed or
// underscore-adjacent spellings (bd-1-v2, pre-bd-1, bd-1_x, bd-1-). The text
// is scanned once, left to right, so a replacement is never rescanned and a
// new ID that contains the old one cannot be rewritten twice. Matching is
// case-sensitive.
func replaceIDReferences(text, oldID, newID string) string {
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); {
		if !isIDRefChar(text[i]) {
			b.WriteByte(text[i])
			i++
			continue
		}
		j := i + 1
		for j < len(text) && isIDRefChar(text[j]) {
			j++
		}
		token := text[i:j]
		if repl, ok := rewriteIDToken(token, oldID, newID); ok {
			b.WriteString(repl)
		} else {
			b.WriteString(token)
		}
		i = j
	}
	return b.String()
}

// rewriteIDToken reports whether token is a mention of oldID and returns its
// replacement: the token when it equals oldID, or newID with the terminal
// periods kept when the token is oldID plus sentence punctuation.
func rewriteIDToken(token, oldID, newID string) (string, bool) {
	if token == oldID {
		return newID, true
	}
	if len(token) > len(oldID) && strings.HasPrefix(token, oldID) {
		if rest := token[len(oldID):]; strings.Trim(rest, ".") == "" {
			return newID + rest, true
		}
	}
	return token, false
}

// idRefMention returns the first token in text that is a mention of id, as
// written, or "" when there is none. The report and the rewrite share one
// rule, so what the report calls a mention is exactly what a rewrite would
// have replaced.
func idRefMention(text, id string) string {
	for i := 0; i < len(text); {
		if !isIDRefChar(text[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(text) && isIDRefChar(text[j]) {
			j++
		}
		if _, ok := rewriteIDToken(text[i:j], id, ""); ok {
			return text[i:j]
		}
		i = j
	}
	return ""
}

// renameRefFields are the issue prose fields a rename rewrites, in the
// report's fixed order. close_reason rides along: UpdateIssue writes it
// without a status change, and ManageClosedAt leaves closed_at alone when
// status is not named.
var renameRefFields = []struct {
	name  string
	value func(*types.Issue) string
}{
	{"title", func(i *types.Issue) string { return i.Title }},
	{"description", func(i *types.Issue) string { return i.Description }},
	{"design", func(i *types.Issue) string { return i.Design }},
	{"notes", func(i *types.Issue) string { return i.Notes }},
	{"acceptance_criteria", func(i *types.Issue) string { return i.AcceptanceCriteria }},
	{"close_reason", func(i *types.Issue) string { return i.CloseReason }},
}

// renameReferenceReport is what the reference scan after a rename did and
// found. It is printed whole (printRenameReferenceReport); an empty report
// prints nothing.
type renameReferenceReport struct {
	oldID string
	newID string

	updated   []renameRefUpdated // rewrites that landed, by issue
	issuesErr error              // the issue listing failed: nothing was checked
}

// renameRefUpdated is one issue whose references were rewritten.
type renameRefUpdated struct {
	issueID string
	fields  []string // changed fields, in renameRefFields order
}

func (r *renameReferenceReport) empty() bool {
	return len(r.updated) == 0
}

// sortReports orders the report deterministically: issues in natural ID
// order.
func (r *renameReferenceReport) sortReports() {
	sort.Slice(r.updated, func(a, b int) bool {
		return utils.NaturalCompareIDs(r.updated[a].issueID, r.updated[b].issueID) < 0
	})
}

// printRenameReferenceReport writes the reference report to w, after the
// "Renamed <old> -> <new>" line. Sections print only when they have content;
// an empty report prints nothing.
func printRenameReferenceReport(w io.Writer, rep *renameReferenceReport) {
	if rep.empty() {
		return
	}
	if len(rep.updated) > 0 {
		fmt.Fprintln(w, "Updated references:")
		for _, u := range rep.updated {
			fmt.Fprintf(w, "  %s: %s\n", u.issueID, strings.Join(u.fields, ", "))
		}
	}
}
