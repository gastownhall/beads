package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// ChangedIssueIDsInTx returns the set of issue IDs whose data differs between
// fromCommit and toCommit, derived from dolt_diff over the issues, labels,
// dependencies, and comments tables. An issue is reported under Removed
// only when the issues-table row itself was deleted; label/dep/comment row
// deletions for surviving issues fall under Upserted.
//
// It is the body of DoltStore.ChangedIssueIDs over any DBTX, so a caller
// holding only a connection (proxied-server auto-export, GH#8490) runs the
// same query.
func ChangedIssueIDsInTx(ctx context.Context, db DBTX, fromCommit, toCommit string) (storage.ChangedIssueIDs, error) {
	var out storage.ChangedIssueIDs
	if fromCommit == "" || toCommit == "" {
		return out, fmt.Errorf("ChangedIssueIDs: fromCommit and toCommit required")
	}
	// Dolt's dolt_diff() table function does not accept prepared-statement
	// bind parameters — its arguments must be SQL string literals. That
	// makes us inline the commit hashes into the query, so validate them
	// aggressively to keep the path free of injection risk.
	if !IsSafeCommitRef(fromCommit) {
		return out, fmt.Errorf("ChangedIssueIDs: fromCommit %q is not a valid commit ref", fromCommit)
	}
	if !IsSafeCommitRef(toCommit) {
		return out, fmt.Errorf("ChangedIssueIDs: toCommit %q is not a valid commit ref", toCommit)
	}

	// dolt_diff returns from_<col>/to_<col> for every column plus diff_type.
	// COALESCE(to_id, from_id) extracts the row's identifying value regardless
	// of whether the row was added, modified, or removed.
	//
	// We UNION results from the four tables that contribute to auto-export's
	// per-issue JSON record. Any change to any of them means the issue's
	// export representation has changed.
	//
	//nolint:gosec // G201: fromCommit/toCommit are constrained to [A-Za-z0-9]
	// by IsSafeCommitRef above; dolt_diff() does not accept bind parameters.
	q := fmt.Sprintf(`
		SELECT id, MAX(is_removed) AS is_removed
		FROM (
			SELECT COALESCE(to_id, from_id) AS id,
			       CASE WHEN diff_type = 'removed' THEN 1 ELSE 0 END AS is_removed
			FROM dolt_diff('%[1]s', '%[2]s', 'issues')
			UNION ALL
			SELECT COALESCE(to_issue_id, from_issue_id) AS id, 0 AS is_removed
			FROM dolt_diff('%[1]s', '%[2]s', 'labels')
			UNION ALL
			SELECT COALESCE(to_issue_id, from_issue_id) AS id, 0 AS is_removed
			FROM dolt_diff('%[1]s', '%[2]s', 'dependencies')
			UNION ALL
			SELECT COALESCE(to_issue_id, from_issue_id) AS id, 0 AS is_removed
			FROM dolt_diff('%[1]s', '%[2]s', 'comments')
		) AS u
		WHERE id IS NOT NULL AND id <> ''
		GROUP BY id
	`, fromCommit, toCommit)
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return out, fmt.Errorf("dolt_diff query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var removed int
		if err := rows.Scan(&id, &removed); err != nil {
			return out, fmt.Errorf("scan dolt_diff row: %w", err)
		}
		// MAX(is_removed) resolves an id that surfaces from more than one of
		// the four unioned sub-selects (e.g. its own row is untouched but a
		// comment on it changed) — only the 'issues' sub-select ever
		// contributes is_removed=1, so MAX just lets that value win over the
		// label/dependency/comment sub-selects' hardcoded 0. It is not
		// resolving a remove-then-readd within the range: dolt_diff(from,
		// to, table) diffs exactly those two snapshots rather than walking
		// intermediate commits, so a single call can never emit both a 0 and
		// a 1 row for the same id. An issue present at toCommit — even if it
		// was deleted and re-added somewhere between fromCommit and
		// toCommit — always reports is_removed=0 here.
		if removed == 1 {
			out.Removed = append(out.Removed, id)
		} else {
			out.Upserted = append(out.Upserted, id)
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("iterate dolt_diff rows: %w", err)
	}
	return out, nil
}

// IsSafeCommitRef reports whether s may be spliced into a dolt_diff()
// argument: 1-64 ASCII letters and digits, which covers commit hashes and
// the literal WORKING/HEAD refs and nothing that could close the quote.
func IsSafeCommitRef(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}
