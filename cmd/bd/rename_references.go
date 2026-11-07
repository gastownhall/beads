package main

import (
	"context"
	"fmt"
	"io"
	"slices"
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

// addShortForms records, for each issue field, a possible short-form
// reference the rewrite deliberately left. texts holds the field text as it
// now stands: rewritten where the update landed, observed where it did not.
func (r *renameReferenceReport) addShortForms(issueID string, texts map[string]string, short string) {
	if short == "" {
		return
	}
	for _, field := range renameRefFields {
		if spelling := idRefMention(texts[field.name], short); spelling != "" {
			r.left = append(r.left, renameRefLeft{
				issueID: issueID, field: field.name,
				spelling: spelling, reason: reasonShortForm,
			})
		}
	}
}

// commentRefStore reads comments for the reference report. It is narrow on
// purpose: the field rewrite (issueRefStore, cmd/bd/rename.go) and this half
// are fakeable without each other.
type commentRefStore interface {
	GetCommentsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Comment, error)
}

// commentField names a comment's text as a report location, beside the issue
// prose fields.
const commentField = "comment"

// reasonComment is why a comment's reference was deliberately left unrewritten.
const reasonComment = "comment text is not rewritten"

// reasonShortForm is why a possible short-form reference was left unrewritten.
const reasonShortForm = "possible short-form reference"

// shortFormSuffix returns the parent-relative short form of oldID under the
// known prefixes (the configured issue prefix plus allowed prefixes), or ""
// when there is none worth reporting. Hierarchical children are written
// shortly by dropping the prefix (abc-12x.39 becomes 12x.39), so only a
// dotted remainder whose first segment is not all digits qualifies: a bare
// remainder (a3f) names the bead itself, a numeric root (1.2) is an ordinal,
// and neither is a child spelling a rename could sensibly flag.
func shortFormSuffix(oldID string, knownPrefixes []string) string {
	prefix := utils.ExtractIssuePrefixKnown(oldID, knownPrefixes)
	if prefix == "" || !strings.HasPrefix(oldID, prefix+"-") {
		return ""
	}
	suffix := oldID[len(prefix)+1:]
	root, _, dotted := strings.Cut(suffix, ".")
	if !dotted || root == "" || allDigits(root) {
		return ""
	}
	return suffix
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// renameReferenceReport is what the reference scan after a rename did and
// found. It is printed whole (printRenameReferenceReport); an empty report
// prints nothing.
type renameReferenceReport struct {
	oldID string
	newID string

	updated     []renameRefUpdated // rewrites that landed, by issue
	failed      []renameRefFailed  // updates that failed; unconfirmed, never "unchanged"
	left        []renameRefLeft    // references deliberately left in place, deduplicated
	issuesErr   error              // the issue listing failed: nothing was checked
	commentsErr error              // comments could not be read: fields stand, comments unchecked
}

// renameRefLeft is one reference location the scan did not rewrite on purpose.
type renameRefLeft struct {
	issueID   string
	field     string // issue prose field, or commentField for a comment
	commentID string // comment ID when field is commentField
	spelling  string // the matched spelling, reported as written
	reason    string // why it was left
}

// renameRefFailed is one issue whose reference update failed after the
// identity rename had committed. The write may still have landed (a lost
// commit response), so its fields are unconfirmed, not unchanged.
type renameRefFailed struct {
	issueID string
	fields  []string // fields the update named
	err     error
}

// renameRefUpdated is one issue whose references were rewritten.
type renameRefUpdated struct {
	issueID string
	fields  []string // changed fields, in renameRefFields order
}

func (r *renameReferenceReport) empty() bool {
	return len(r.updated) == 0 && len(r.failed) == 0 && len(r.left) == 0 &&
		r.issuesErr == nil && r.commentsErr == nil
}

// sortReports orders the report deterministically: issues in natural ID
// order, fields in renameRefFields order with comments after fields, and
// comments by ID. Left locations are deduplicated to one entry per location
// and reason, however often the reference occurs there.
func (r *renameReferenceReport) sortReports() {
	sort.Slice(r.updated, func(a, b int) bool {
		return utils.NaturalCompareIDs(r.updated[a].issueID, r.updated[b].issueID) < 0
	})
	sort.Slice(r.failed, func(a, b int) bool {
		return utils.NaturalCompareIDs(r.failed[a].issueID, r.failed[b].issueID) < 0
	})

	fieldRank := func(field string) int {
		if field == commentField {
			return len(renameRefFields)
		}
		for i, f := range renameRefFields {
			if f.name == field {
				return i
			}
		}
		return len(renameRefFields) + 1
	}
	sort.SliceStable(r.left, func(a, b int) bool {
		x, y := r.left[a], r.left[b]
		if c := utils.NaturalCompareIDs(x.issueID, y.issueID); c != 0 {
			return c < 0
		}
		if rx, ry := fieldRank(x.field), fieldRank(y.field); rx != ry {
			return rx < ry
		}
		return utils.NaturalCompareIDs(x.commentID, y.commentID) < 0
	})
	r.left = slices.CompactFunc(r.left, func(x, y renameRefLeft) bool {
		return x.issueID == y.issueID && x.field == y.field &&
			x.commentID == y.commentID && x.reason == y.reason
	})
}

// printRenameReferenceReport writes the reference report to w, after the
// "Renamed <old> -> <new>" line. Sections print only when they have content;
// an empty report prints nothing. Comment bodies are never printed.
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
	if len(rep.left) > 0 {
		fmt.Fprintln(w, "Warning: references left in place:")
		for _, l := range rep.left {
			location := l.field
			if l.field == commentField {
				location = "comment " + l.commentID
			}
			fmt.Fprintf(w, "  %s %s: %s (%s)\n", l.issueID, location, l.spelling, l.reason)
		}
	}
	if len(rep.failed) > 0 {
		fmt.Fprintln(w, "Warning: could not rewrite some references (the rename itself succeeded):")
		for _, f := range rep.failed {
			fmt.Fprintf(w, "  %s (%s): %v\n", f.issueID, strings.Join(f.fields, ", "), f.err)
		}
	}
	if rep.issuesErr != nil {
		fmt.Fprintf(w, "Warning: references could not be checked: %v\n", rep.issuesErr)
	}
	if rep.commentsErr != nil {
		fmt.Fprintf(w, "Warning: comments could not be checked: %v\n", rep.commentsErr)
	}
}
