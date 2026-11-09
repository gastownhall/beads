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
	// The rename does not migrate issue_versions rows (that table has no
	// foreign key to issues), so an issue with versioned history would keep
	// it under the old id. Refuse before anything is renamed.
	for _, r := range renames {
		var versions int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", r.From).Scan(&versions); err != nil {
			return fmt.Errorf("renumber children: check versioned history of %s: %w", r.From, err)
		}
		if versions > 0 {
			return fmt.Errorf("renumber children: %s has %d issue_versions row(s); renaming an issue with versioned history is not supported", r.From, versions)
		}
	}
	renamedTo := make(map[string]bool, len(renames))
	for _, r := range renames {
		renamedTo[r.To] = true
	}
	highWater := make(map[string]int)
	for _, r := range renames {
		issue, err := issueops.GetIssueInTx(ctx, tx, r.From)
		if err != nil {
			return fmt.Errorf("renumber children: load %s: %w", r.From, err)
		}
		// Capture the issue's own counter BEFORE the rename: a schema whose
		// child_counters still cascades on the issues FK moves the row during
		// the rename, so reading it after would read the moved row.
		counter, hasCounter, err := capturedChildCounterInTx(ctx, tx, r.From)
		if err != nil {
			return err
		}
		if err := refuseDestinationCounterInTx(ctx, tx, r.To); err != nil {
			return err
		}
		// Text is passed through unchanged: mentions of a renamed id in prose
		// are reported, never rewritten.
		if err := issueops.UpdateIssueIDInTx(ctx, tx, r.From, r.To, issue, actor); err != nil {
			return fmt.Errorf("renumber children: rename %s -> %s: %w", r.From, r.To, err)
		}
		if hasCounter {
			if err := relocateChildCounterInTx(ctx, tx, r.From, r.To, counter); err != nil {
				return err
			}
		}
		// The comments, events and snapshots followed the issue by cascade but
		// keep ids derived from the old one, which the remote's rows of that
		// identity hold too.
		if err := schema.RekeyAuxRowsForIssue(ctx, tx, r.To); err != nil {
			return fmt.Errorf("renumber children: %w", err)
		}
		// A descendant keeps its suffix under its renamed parent, whose own
		// counter (relocated above, or absent) already covers it; a counter is
		// never synthesized for a renamed node that had none.
		if parent, n, ok := issueops.ParseHierarchicalID(r.To); ok && !renamedTo[parent] {
			highWater[parent] = max(highWater[parent], n)
		}
	}
	// The allocation parents' counters must cover the suffixes just taken, so
	// the next `bd create --parent` cannot mint one of them again.
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

// capturedChildCounterInTx reads an issue's own child-counter row before its
// rename: the high-water mark its future children mint from. An issue without
// a row (its children predate the counter backfill, or it never had any)
// reports false and relocates nothing.
func capturedChildCounterInTx(ctx context.Context, tx *sql.Tx, issueID string) (int, bool, error) {
	var last int
	err := tx.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", issueID).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("renumber children: read child counter of %s: %w", issueID, err)
	}
	return last, true, nil
}

// refuseDestinationCounterInTx fails when a counter row already exists under
// a rename's destination: the plan only picks unused ids, so a row there (only
// possible on a schema without the counter's foreign key) is state the repair
// does not understand, and relocating onto it, or leaving it for a counterless
// source, would hand the renamed issue a high-water mark it never had.
func refuseDestinationCounterInTx(ctx context.Context, tx *sql.Tx, to string) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", to).Scan(&n); err != nil {
		return fmt.Errorf("renumber children: check child counter of %s: %w", to, err)
	}
	if n > 0 {
		return fmt.Errorf("renumber children: destination %s already has a child counter row", to)
	}
	return nil
}

// relocateChildCounterInTx moves a renamed issue's own counter row to its new
// id and verifies the postcondition: the row lives only under the new id with
// the captured value. The ignored counter migration restores an ON UPDATE
// CASCADE foreign key that normally moves the row with the issue, leaving the
// UPDATE nothing to match; a schema without that key leaves the row behind,
// and the UPDATE is what moves it. Either way the check is the contract, and
// a mismatch fails the transaction and with it the whole repair.
func relocateChildCounterInTx(ctx context.Context, tx *sql.Tx, from, to string, captured int) error {
	if _, err := tx.ExecContext(ctx,
		"UPDATE child_counters SET parent_id = ? WHERE parent_id = ?", to, from); err != nil {
		return fmt.Errorf("renumber children: move child counter %s -> %s: %w", from, to, err)
	}
	var got int
	err := tx.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?", to).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("renumber children: child counter of %s missing after the rename, want %d", to, captured)
	}
	if err != nil {
		return fmt.Errorf("renumber children: read child counter of %s: %w", to, err)
	}
	if got != captured {
		return fmt.Errorf("renumber children: child counter of %s is %d after the rename, want the preserved %d", to, got, captured)
	}
	var stale int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", from).Scan(&stale); err != nil {
		return fmt.Errorf("renumber children: check child counter of %s: %w", from, err)
	}
	if stale > 0 {
		return fmt.Errorf("renumber children: %d child counter row(s) remain under %s after the rename", stale, from)
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
