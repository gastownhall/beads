package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/rowid"
)

// This file is the rename-time half of the content-derived aux-row ids
// (bd-ri8bd, Protocol v0.1 C-OQ3). The insert-time half (derivedid.go) mints
// the id at insert from the row's own content; the backfill in
// internal/storage/schema converged the rows that predate it. An issue RENAME
// changes the content the id is derived from — the issue_id column is the
// first digest input — while the FK ON UPDATE CASCADE moves the rows to the
// new issue id, so the stored ids come out stale: a comment that follows its
// issue from bd-x.1 to bd-x.4 keeps rowid.New("comments", 0, digest(bd-x.1,
// ...)) while living under bd-x.4.
//
// Stale ids are not cosmetic. A replica that independently wrote the same
// comment under the OLD id — exactly what two offline clones do — holds the
// same primary key with a different issue_id, and the merge converges the two
// by newest-wins instead of keeping both: the renamed issue silently loses
// its aux rows. Re-deriving the ids inside the rename transaction restores
// the same-PK => same-content invariant before anything is committed.

// auxRenameRekeyTable names one synced aux table and the FROZEN digest column
// list the rekey backfill established (auxRekeyTables in internal/storage/
// schema). The lists must stay byte-identical to that source: deriving from
// any other column set would fork the id space the backfill converged.
type auxRenameRekeyTable struct {
	name    string
	columns string
}

// auxRenameRekeyTables are the aux tables that are BOTH synced (staged and
// committed, so the merge sees them) and keyed by content-derived ids. The
// events plane is dolt_ignore'd — its rows are clone-local, never staged and
// never merged — so its ids are re-derived on read and renaming cannot stale
// them. The wisp aux planes are likewise dolt_ignore'd per the rename commit's
// staging set (renumberDirtyTables).
var auxRenameRekeyTables = []auxRenameRekeyTable{
	{name: "comments", columns: "issue_id, author, text, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')"},
	{name: "issue_snapshots", columns: "issue_id, DATE_FORMAT(snapshot_time, '%Y-%m-%d %H:%i:%s'), compaction_level, original_size, compressed_size, original_content, archived_events"},
	{name: "compaction_snapshots", columns: "issue_id, compaction_level, snapshot_json, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s')"},
}

// RekeyAuxRowsForIssueInTx re-derives the content-derived ids of every synced
// aux row belonging to issueID, in the transaction that is renaming that
// issue: after the rename's FK cascades have moved the rows to the new issue
// id, each row's id must be rowid.New(table, ordinal, digest) recomputed from
// the NEW content, or it stays pinned to the old id the other clone may hold.
//
// The rekey is scoped to one issue and mirrors the canonical backfill
// (rekeyAuxRowTable in internal/storage/schema): rows group by digest, the
// group's targets are the ordinals 0..n-1 of that digest, rows already
// holding a target keep it, and the remaining rows — sorted by current id —
// take the unheld targets in ordinal order. Because the digest names the
// issue being renamed and every row in scope carries that issue's id, no
// other issue's row can hold or claim one of this group's targets: the
// assignment never reaches outside the renamed issue's own rows.
func RekeyAuxRowsForIssueInTx(ctx context.Context, tx *sql.Tx, issueID string) error {
	for _, t := range auxRenameRekeyTables {
		if err := rekeyAuxRowsForIssueInTx(ctx, tx, t, issueID); err != nil {
			return fmt.Errorf("rekey %s rows for %s: %w", t.name, issueID, err)
		}
	}
	return nil
}

// rekeyAuxRowsForIssueInTx applies the canonical held/free assignment to one
// table's rows for one issue. A table with no rows for the issue rekeys
// nothing; the query itself is what discovers that.
//
//nolint:gosec // G201: name/columns come from the package-internal auxRenameRekeyTables, never user input.
func rekeyAuxRowsForIssueInTx(ctx context.Context, tx *sql.Tx, t auxRenameRekeyTable, issueID string) error {
	rows, err := tx.QueryContext(ctx,
		fmt.Sprintf("SELECT id, %s FROM %s WHERE issue_id = ?", t.columns, t.name), issueID)
	if err != nil {
		return err
	}
	// Derive the field count from the driver's own column list rather than
	// counting commas in t.columns: DATE_FORMAT's format string carries an
	// internal comma a naive count would over-count (same care as the
	// backfill).
	colNames, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return err
	}
	nFields := len(colNames) - 1
	groups := make(map[string][]string)
	for rows.Next() {
		var id string
		fields := make([]sql.NullString, nFields)
		dests := make([]any, 0, nFields+1)
		dests = append(dests, &id)
		for i := range fields {
			dests = append(dests, &fields[i])
		}
		if err := rows.Scan(dests...); err != nil {
			_ = rows.Close()
			return err
		}
		digest := rowid.Digest(fields)
		groups[digest] = append(groups[digest], id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	type rekey struct{ oldID, newID string }
	var todo []rekey
	for digest, ids := range groups {
		targets := make([]string, len(ids))
		targetSet := make(map[string]bool, len(ids))
		for i := range ids {
			targets[i] = rowid.New(t.name, i, digest)
			targetSet[targets[i]] = true
		}
		held := make(map[string]bool, len(ids))
		var free []string
		for _, id := range ids {
			if targetSet[id] {
				held[id] = true
			} else {
				free = append(free, id)
			}
		}
		if len(free) == 0 {
			continue
		}
		sort.Strings(free)
		i := 0
		for _, target := range targets {
			if held[target] {
				continue
			}
			todo = append(todo, rekey{oldID: free[i], newID: target})
			i++
		}
	}
	for _, r := range todo {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("UPDATE %s SET id = ? WHERE id = ?", t.name), r.newID, r.oldID); err != nil {
			return err
		}
	}
	return nil
}
