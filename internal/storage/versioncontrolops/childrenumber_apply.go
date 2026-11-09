package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	}

	// Merge the head the plan was made against, not the ref, so a fetch
	// racing this command cannot slip unplanned commits into the merge.
	if err := MergeAndSettle(ctx, conn, report.RemoteHead); err != nil {
		return report, fmt.Errorf("merge %s (%s): %w", ref, report.RemoteHead, err)
	}
	report.Merged = true
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
