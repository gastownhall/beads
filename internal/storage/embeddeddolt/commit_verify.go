//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// commitAllPolicyLoader loads the Beads-owned ignore policy for commit
// verification. A variable as a test seam: the sqlmock tests of the
// verification seam drive the required policy read without re-mocking
// mainSource's cursor probes.
var commitAllPolicyLoader LoadOwnedIgnorePolicyFunc = schema.LoadOwnedIgnorePolicy

// LoadOwnedIgnorePolicyFunc is the seam type of commitAllPolicyLoader.
type LoadOwnedIgnorePolicyFunc func(ctx context.Context, db schema.DBConn) (schema.OwnedIgnorePolicy, error)

// commitAllVerification is what the post-commit working-set check found,
// collected apart from operational errors: withConn rolls the SQL
// transaction back on any callback error, so a finding (or the failure of a
// required read) is never returned from the callback. commitAll surfaces it
// only after withConn finished, when publication is confirmed.
type commitAllVerification struct {
	// committed records DOLT_COMMIT's own answer for the finding it
	// carries: a residual error reports partial publication when the
	// commit succeeded, and that nothing landed when it answered nothing
	// to commit. The verification-failure path reports the same record
	// outcome-sensitively (AC-5).
	committed bool
	// readFailed is the required status or policy read that could not run.
	// Its typing is the caller's business (commitAll turns it into the
	// verification failure error); it is never silently dropped.
	readFailed error
	// readFailedOperation names the required read that failed — the
	// status read or the owned-policy load — for the verification error's
	// Operation field.
	readFailedOperation string
	// residual are the dolt_status table_name values that stayed changed
	// after the commit and are not Beads-owned clone-local tables: the
	// tracked changes the commit skipped (GH#5433).
	residual []string
	// patterns are the enabled noncanonical dolt_ignore patterns that may
	// cover the residual tables — an optional diagnostic, empty when the
	// collecting read failed or found none.
	patterns []string
}

// verifyCommitAllInTx reads what a completed DOLT_COMMIT('-Am') left in the
// working set, in the same transaction. Dolt's '-Am' staging filters tables
// covered by an enabled dolt_ignore pattern, so a tracked-but-ignored table's
// changes stay in the working set while the commit reports success or
// "nothing to commit"; dolt_status still lists the modified tracked table
// (it hides only NEW unstaged ignored tables), which is exactly the signal
// the silent stall lacked. Residual rows are exempted only when they name
// Beads-owned clone-local tables (schema.OwnedIgnorePolicy), never by
// arbitrary ignore rows, and only the finding's own optional pattern read
// may degrade. Required read failures are collected in the verification, not
// returned: the caller decides what a failed completeness check means, after
// publication is confirmed.
func verifyCommitAllInTx(ctx context.Context, tx *sql.Tx, committed bool) *commitAllVerification {
	v := &commitAllVerification{committed: committed}
	tables, err := residualStatusTables(ctx, tx)
	if err != nil {
		v.readFailed = err
		v.readFailedOperation = "read dolt_status after the commit"
		return v
	}
	policy, err := commitAllPolicyLoader(ctx, tx)
	if err != nil {
		v.readFailed = err
		v.readFailedOperation = "load the Beads-owned dolt_ignore policy"
		return v
	}
	for _, table := range tables {
		if !policy.Exempts(table) {
			v.residual = append(v.residual, table)
		}
	}
	if len(v.residual) == 0 {
		return v
	}
	// Optional diagnostic: enabled patterns outside the owned set suggest the
	// cause of the skip; a failed read just drops them, and the residual
	// finding still surfaces.
	v.patterns, _ = enabledNoncanonicalIgnorePatterns(ctx, tx, policy)
	return v
}

// residualStatusTables reads the table_name values dolt_status still lists
// after the commit — the working-set changes the commit did not take — with
// duplicates (a staged and unstaged row for one table) collapsed. Query,
// scan and iteration errors are all failures of a required read.
func residualStatusTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT table_name, status FROM dolt_status")
	if err != nil {
		return nil, fmt.Errorf("query dolt_status after commit: %w", err)
	}
	defer rows.Close()
	var tables []string
	seen := make(map[string]bool)
	for rows.Next() {
		var table, status string
		if err := rows.Scan(&table, &status); err != nil {
			return nil, fmt.Errorf("scan dolt_status after commit: %w", err)
		}
		if seen[table] {
			continue
		}
		seen[table] = true
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read dolt_status after commit: %w", err)
	}
	return tables, nil
}

// enabledNoncanonicalIgnorePatterns lists the enabled dolt_ignore patterns
// that are not Beads' owned set, for the diagnostic half of a residual
// finding. Suggested causes, not proof, and optional: any read failure yields
// nil without weakening the finding it decorates.
func enabledNoncanonicalIgnorePatterns(ctx context.Context, tx *sql.Tx, policy schema.OwnedIgnorePolicy) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT pattern FROM dolt_ignore WHERE ignored = 1")
	if err != nil {
		return nil, fmt.Errorf("query dolt_ignore after commit: %w", err)
	}
	defer rows.Close()
	var patterns []string
	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			return nil, fmt.Errorf("scan dolt_ignore after commit: %w", err)
		}
		if policy.Owns(pattern) {
			continue
		}
		patterns = append(patterns, pattern)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read dolt_ignore after commit: %w", err)
	}
	slices.Sort(patterns)
	return patterns, nil
}
