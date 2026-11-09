package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// This file is the applying half of `bd dolt pull --renumber-children`
// (bd-28sb25, GH#4796); see childrenumber.go for the plan.

// childRenumberCommitPrefix starts the message of every rename commit, which
// is how a retry recognizes renames an earlier attempt already committed.
const childRenumberCommitPrefix = "bd: renumber colliding children"

// RenumberChildrenAndMerge renames the local colliding children PlanChildRenumbers
// finds between branch and ref (which the caller has just fetched), commits
// the renames as one Dolt commit, and merges the pinned remote head through
// MergeAndSettle. conn must be a single session: the rename transaction, the
// commit and the merge's session flags all run on it.
//
// It refuses to start mid-merge or over uncommitted changes, so the rename
// commit holds the renames and nothing else. When the merge fails after the
// rename commit landed, that commit stays (the renames are valid on their
// own) and the report comes back with the error, so the caller can name it.
func RenumberChildrenAndMerge(ctx context.Context, conn *sql.Conn, branch, ref, actor string) (*storage.ChildRenumberReport, error) {
	merging, err := mergeInProgress(ctx, conn)
	if err != nil {
		return nil, err
	}
	if merging {
		return nil, errors.New("renumber children: a merge is in progress; conclude or abort it first")
	}
	clean, err := WorkingSetClean(ctx, conn)
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, errors.New("renumber children: the working set has uncommitted changes; commit them first (bd dolt commit)")
	}

	report, err := PlanChildRenumbers(ctx, conn, branch, ref)
	if err != nil {
		return nil, err
	}
	if len(report.Renames) > 0 {
		if err := applyChildRenumbers(ctx, conn, report.Renames, actor); err != nil {
			return report, err
		}
		commit, err := commitChildRenumbers(ctx, conn, report.Renames)
		if err != nil {
			return report, err
		}
		report.RenameCommit = commit
		if report.Mentions, err = findChildIDMentions(ctx, conn, report.Renames); err != nil {
			return report, err
		}
	}

	// Merge the head the plan was made against, not the ref, so a fetch
	// racing this command cannot slip unplanned commits into the merge.
	if err := MergeAndSettle(ctx, conn, report.RemoteHead); err != nil {
		return report, fmt.Errorf("merge %s (%s): %w", ref, report.RemoteHead, err)
	}
	report.Merged = true
	if err := conn.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&report.Head); err != nil {
		return report, fmt.Errorf("renumber children: read head after merge: %w", err)
	}
	return report, nil
}

// applyChildRenumbers renames every planned identity in one transaction, so
// a failure part way leaves none of them.
func applyChildRenumbers(ctx context.Context, conn *sql.Conn, renames []storage.ChildRenumber, actor string) (retErr error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("renumber children: begin: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = tx.Rollback()
		}
	}()
	highWater := make(map[string]int)
	for _, r := range renames {
		issue, err := issueops.GetIssueInTx(ctx, tx, r.From)
		if err != nil {
			return fmt.Errorf("renumber children: load %s: %w", r.From, err)
		}
		// Text is passed through unchanged: mentions of a renamed id in prose
		// are reported, never rewritten.
		if err := issueops.UpdateIssueIDInTx(ctx, tx, r.From, r.To, issue, actor); err != nil {
			return fmt.Errorf("renumber children: rename %s -> %s: %w", r.From, r.To, err)
		}
		if err := moveChildCounter(ctx, tx, r.From, r.To); err != nil {
			return err
		}
		// The comments, events and snapshots followed the issue by cascade but
		// keep ids derived from the old one, which the remote's rows of that
		// identity hold too.
		if err := schema.RekeyAuxRowsForIssue(ctx, tx, r.To); err != nil {
			return fmt.Errorf("renumber children: %w", err)
		}
		if parent, n, ok := issueops.ParseHierarchicalID(r.To); ok {
			highWater[parent] = max(highWater[parent], n)
		}
	}
	// The parent counters must cover the suffixes just taken, so the next
	// `bd create --parent` cannot mint one of them again.
	for parent, n := range highWater {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?) "+
				"ON DUPLICATE KEY UPDATE last_child = GREATEST(last_child, VALUES(last_child))",
			parent, n); err != nil {
			return fmt.Errorf("renumber children: raise child counter of %s: %w", parent, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("renumber children: commit transaction: %w", err)
	}
	return nil
}

// moveChildCounter carries a renamed issue's own child counter to its new id,
// keeping the high-water mark: its children are numbered after it whether or
// not they still exist. child_counters has no foreign key since migration
// 0039, so the rename does not move the row; on a schema where it cascaded,
// the delete finds nothing and the upsert keeps the larger value.
func moveChildCounter(ctx context.Context, tx *sql.Tx, from, to string) error {
	var last int
	err := tx.QueryRowContext(ctx, "SELECT last_child FROM child_counters WHERE parent_id = ?", from).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("renumber children: read child counter of %s: %w", from, err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM child_counters WHERE parent_id = ?", from); err != nil {
		return fmt.Errorf("renumber children: move child counter of %s: %w", from, err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?) "+
			"ON DUPLICATE KEY UPDATE last_child = GREATEST(last_child, VALUES(last_child))",
		to, last); err != nil {
		return fmt.Errorf("renumber children: move child counter of %s to %s: %w", from, to, err)
	}
	return nil
}

// commitChildRenumbers commits the renamed rows as one Dolt commit and
// returns its hash. It stages only the tables the renames dirtied: config is
// never one of them, and is left out the way the pre-pull commit leaves
// non-kv config out (GH#2455).
func commitChildRenumbers(ctx context.Context, conn *sql.Conn, renames []storage.ChildRenumber) (string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT table_name FROM dolt_status")
	if err != nil {
		return "", fmt.Errorf("renumber children: read status: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return "", fmt.Errorf("renumber children: read status: %w", err)
		}
		if table == "config" || table == "wisps" || strings.HasPrefix(table, "wisp_") {
			continue
		}
		tables = append(tables, table)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("renumber children: read status: %w", err)
	}
	for _, table := range tables {
		if err := schema.DrainCall(ctx, conn, "CALL DOLT_ADD(?)", table); err != nil {
			return "", fmt.Errorf("renumber children: stage %s: %w", table, err)
		}
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-m', ?)", childRenumberCommitMessage(renames)); err != nil {
		return "", fmt.Errorf("renumber children: commit: %w", err)
	}
	var head string
	if err := conn.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&head); err != nil {
		return "", fmt.Errorf("renumber children: read commit: %w", err)
	}
	return head, nil
}

func childRenumberCommitMessage(renames []storage.ChildRenumber) string {
	var b strings.Builder
	b.WriteString(childRenumberCommitPrefix)
	b.WriteString("\n")
	for _, r := range renames {
		fmt.Fprintf(&b, "\n%s -> %s", r.From, r.To)
	}
	return b.String()
}

// mentionFields are the issue text fields searched for renamed ids, in the
// column order findChildIDMentions selects them.
var mentionFields = []string{"title", "description", "design", "acceptance_criteria", "notes"}

// findChildIDMentions lists the local issue fields and comments whose text
// names a renamed id, ordered by issue, field, comment and id. It reads the
// committed renames, before the merge brings in the remote's text.
func findChildIDMentions(ctx context.Context, conn *sql.Conn, renames []storage.ChildRenumber) ([]storage.ChildIDMention, error) {
	var mentions []storage.ChildIDMention
	issueRows, err := conn.QueryContext(ctx,
		"SELECT id, title, description, design, acceptance_criteria, notes FROM issues")
	if err != nil {
		return nil, fmt.Errorf("renumber children: read text: %w", err)
	}
	for issueRows.Next() {
		var id string
		texts := make([]sql.NullString, len(mentionFields))
		dests := []any{&id}
		for i := range texts {
			dests = append(dests, &texts[i])
		}
		if err := issueRows.Scan(dests...); err != nil {
			_ = issueRows.Close()
			return nil, fmt.Errorf("renumber children: read text: %w", err)
		}
		for i, text := range texts {
			mentions = appendChildIDMentions(mentions, storage.ChildIDMention{IssueID: id, Field: mentionFields[i]}, text.String, renames)
		}
	}
	_ = issueRows.Close()
	if err := issueRows.Err(); err != nil {
		return nil, fmt.Errorf("renumber children: read text: %w", err)
	}

	commentRows, err := conn.QueryContext(ctx, "SELECT id, issue_id, text FROM comments")
	if err != nil {
		return nil, fmt.Errorf("renumber children: read comments: %w", err)
	}
	for commentRows.Next() {
		var id, issueID string
		var text sql.NullString
		if err := commentRows.Scan(&id, &issueID, &text); err != nil {
			_ = commentRows.Close()
			return nil, fmt.Errorf("renumber children: read comments: %w", err)
		}
		mentions = appendChildIDMentions(mentions, storage.ChildIDMention{IssueID: issueID, Field: "comment", CommentID: id}, text.String, renames)
	}
	_ = commentRows.Close()
	if err := commentRows.Err(); err != nil {
		return nil, fmt.Errorf("renumber children: read comments: %w", err)
	}

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
		return a.ID < b.ID
	})
	return mentions, nil
}

func appendChildIDMentions(mentions []storage.ChildIDMention, where storage.ChildIDMention, text string, renames []storage.ChildRenumber) []storage.ChildIDMention {
	for _, r := range renames {
		if namesID(text, r.From) {
			m := where
			m.ID, m.RenamedTo = r.From, r.To
			mentions = append(mentions, m)
		}
	}
	return mentions
}

// namesID reports whether text names id as a whole id: not inside a longer
// token (bd-p.721) and not as the prefix of a child (bd-p.72.1). A trailing
// sentence period still counts.
func namesID(text, id string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], id)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(id)
		before := start == 0 || !isIDByte(text[start-1])
		after := end == len(text) || !isIDByte(text[end]) ||
			(text[end] == '.' && (end+1 == len(text) || !isDigit(text[end+1])))
		if before && after {
			return true
		}
		from = start + 1
	}
}

func isIDByte(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_' || c == '.'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
