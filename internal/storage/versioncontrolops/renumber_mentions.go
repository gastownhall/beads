package versioncontrolops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
)

// The mention half of the offline child-id collision repair (GH#4796,
// bd-28sb25 AC-5): renaming an issue changes what its OLD id means, and
// prose that names the id — "blocked by bd-x.71", a note referencing the
// child — does not follow the rename. The repair reports those mentions for
// manual review and deliberately rewrites none of them: the text's intent is
// ambiguous (the operator may have meant the bead either side of the
// collision), so the words stay exactly as they were and the report says
// where to look.

// mentionIDCharset is the id character set an exact-boundary mention is
// delimited by: a candidate occurrence of a renamed id counts only when the
// characters on BOTH sides fall outside the set. The set is the bead-id
// grammar — letters, digits, dot, underscore, hyphen — so bd-x.71 matches in
// "(bd-x.71)" and "bd-x.71," but not inside bd-x.710 or bd-x.71.1: those are
// different ids (or ids of the renamed subtree), not mentions of this one.
const mentionIDCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-"

// isMentionChar reports whether c is a member of mentionIDCharset.
func isMentionChar(c byte) bool {
	return strings.IndexByte(mentionIDCharset, c) >= 0
}

// hasBoundaryMention reports whether text mentions id at least once with an
// exact boundary on both sides of the occurrence. The check is
// case-sensitive: ids are case-sensitive and a renamed id is reported only
// where it is spelled as the id, not where prose merely resembles it.
func hasBoundaryMention(text, id string) bool {
	if id == "" {
		return false
	}
	for from := 0; from+len(id) <= len(text); {
		at := strings.Index(text[from:], id)
		if at < 0 {
			return false
		}
		at += from
		if !(at > 0 && isMentionChar(text[at-1])) &&
			!(at+len(id) < len(text) && isMentionChar(text[at+len(id)])) {
			return true
		}
		from = at + 1
	}
	return false
}

// childRenumberTextFields are the issues columns the mention scan reads, in
// the fixed order collectChildRenumberMentionsInTx scans them; the names are
// what the report prints for each field.
var childRenumberTextFields = [...]struct{ column, name string }{
	{"title", "title"},
	{"description", "description"},
	{"design", "design"},
	{"acceptance_criteria", "acceptance_criteria"},
	{"notes", "notes"},
}

// collectChildRenumberMentionsInTx scans the local issues text columns and
// the comment bodies for exact-boundary mentions of the ids the repair is
// renaming, and returns one report row per (bead, field-or-comment, renamed
// id). It reads only: the repair never edits text, so the scan runs inside
// the rename transaction purely to see the same state the rename commit
// pins — after the renames, so the comment ids it reports are the final
// re-derived ones.
//
// The LIKE prefilter widens matches (an underscore in an id is a LIKE
// single-char wildcard) but never narrows them; hasBoundaryMention decides
// every row, so a prefilter false positive is dropped, never reported.
//
//nolint:gosec // G201: the column names come from the package-internal childRenumberTextFields, never user input.
func collectChildRenumberMentionsInTx(ctx context.Context, tx *sql.Tx, mappings []storage.ChildRenumberMapping) ([]storage.ChildRenumberMention, error) {
	var mentions []storage.ChildRenumberMention
	for _, m := range mappings {
		pattern := "%" + m.OldID + "%"
		rows, err := tx.QueryContext(ctx, `
			SELECT id, title, IFNULL(description, ''), IFNULL(design, ''),
			       IFNULL(acceptance_criteria, ''), IFNULL(notes, '')
			FROM issues
			WHERE title LIKE ? OR description LIKE ? OR design LIKE ?
			   OR acceptance_criteria LIKE ? OR notes LIKE ?`,
			pattern, pattern, pattern, pattern, pattern)
		if err != nil {
			return nil, fmt.Errorf("renumber: scan text for mentions of %s: %w", m.OldID, err)
		}
		for rows.Next() {
			var id string
			texts := make([]string, len(childRenumberTextFields))
			dests := make([]any, 0, len(texts)+1)
			dests = append(dests, &id)
			for i := range texts {
				dests = append(dests, &texts[i])
			}
			if err := rows.Scan(dests...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("renumber: read text for mentions of %s: %w", m.OldID, err)
			}
			for i, field := range childRenumberTextFields {
				if hasBoundaryMention(texts[i], m.OldID) {
					mentions = append(mentions, storage.ChildRenumberMention{
						IssueID:   id,
						Field:     field.name,
						MatchedID: m.OldID,
						NewID:     m.NewID,
					})
				}
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("renumber: scan text for mentions of %s: %w", m.OldID, err)
		}
		_ = rows.Close()

		comments, err := tx.QueryContext(ctx,
			"SELECT id, issue_id, text FROM comments WHERE text LIKE ?", pattern)
		if err != nil {
			return nil, fmt.Errorf("renumber: scan comments for mentions of %s: %w", m.OldID, err)
		}
		for comments.Next() {
			var commentID, issueID, text string
			if err := comments.Scan(&commentID, &issueID, &text); err != nil {
				_ = comments.Close()
				return nil, fmt.Errorf("renumber: read comment for mentions of %s: %w", m.OldID, err)
			}
			if hasBoundaryMention(text, m.OldID) {
				mentions = append(mentions, storage.ChildRenumberMention{
					IssueID:   issueID,
					Field:     "comment",
					CommentID: commentID,
					MatchedID: m.OldID,
					NewID:     m.NewID,
				})
			}
		}
		if err := comments.Err(); err != nil {
			_ = comments.Close()
			return nil, fmt.Errorf("renumber: scan comments for mentions of %s: %w", m.OldID, err)
		}
		_ = comments.Close()
	}
	// Deterministic order regardless of mapping or row order: the same
	// repair always reports the same list the same way.
	sort.Slice(mentions, func(i, j int) bool {
		a, b := mentions[i], mentions[j]
		if a.IssueID != b.IssueID {
			return a.IssueID < b.IssueID
		}
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		if a.CommentID != b.CommentID {
			return a.CommentID < b.CommentID
		}
		return a.MatchedID < b.MatchedID
	})
	return mentions, nil
}
