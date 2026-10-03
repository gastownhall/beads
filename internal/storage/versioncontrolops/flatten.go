package versioncontrolops

import (
	"context"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

const flattenRootQuery = `SELECT l.commit_hash FROM dolt_log l
	JOIN dolt_commit_ancestors a ON l.commit_hash = a.commit_hash
	WHERE a.parent_hash IS NULL`

// Flatten squashes main's history into an empty ancestry root plus a snapshot
// of the current working state. Checkpoint pending changes before moving HEAD,
// then soft-reset and commit on the same connection. Neither checkout nor hard
// reset is safe here: checkout can leave dirty config on the old branch, and
// hard reset drops clone-local FKs on ignored tables (#6772).
//
// Callers must exclude concurrent writers, then run PruneRemoteRefs and DoltGC
// after success to reclaim history. conn must be a pinned connection, since
// Dolt branch and working-set state is session scoped.
func Flatten(ctx context.Context, conn DBConn) error {
	var branch string
	if err := conn.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil {
		return fmt.Errorf("flatten: read active branch: %w", err)
	}
	if branch != "main" {
		return fmt.Errorf("flatten requires active branch main, got %q", branch)
	}
	commitCount, initialHash, err := FlattenDryRun(ctx, conn)
	if err != nil {
		return err
	}
	if commitCount <= 1 {
		return nil // no history to shorten; leave pending changes untouched
	}
	// A timestamp is not ancestry. Also refuse a nonempty root, rather than
	// silently retaining historical data in the supposedly empty base.
	rows, err := conn.QueryContext(ctx, "SHOW TABLES AS OF ?", initialHash)
	if err != nil {
		return fmt.Errorf("flatten: inspect ancestry root: %w", err)
	}
	nonempty := rows.Next()
	var rootTable string
	if nonempty {
		if err := rows.Scan(&rootTable); err != nil {
			_ = rows.Close()
			return fmt.Errorf("flatten: read ancestry root table: %w", err)
		}
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return fmt.Errorf("flatten: inspect ancestry root: %w", rowsErr)
	}
	if closeErr != nil {
		return fmt.Errorf("flatten: close ancestry root tables: %w", closeErr)
	}
	if nonempty {
		return fmt.Errorf("flatten: ancestry root %s is not empty (table %s)", initialHash, rootTable)
	}
	pending, err := issueops.HasPendingChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("flatten: check pending changes: %w", err)
	}
	if pending {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'flatten: checkpoint pending changes')"); err != nil {
			return fmt.Errorf("flatten: checkpoint pending changes: %w", err)
		}
	}
	var checkpoint string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&checkpoint); err != nil {
		return fmt.Errorf("flatten: read checkpoint: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_RESET('--soft', ?)", initialHash); err != nil {
		return fmt.Errorf("flatten: soft reset to ancestry root: %w", err)
	}
	// --allow-empty also handles a working state in which all tracked tables
	// were deleted. Ignored rows and their schemas remain in the working set.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('--allow-empty', '-Am', 'flatten: squash all history into single commit')"); err != nil {
		// Restore history without replacing any working rows or schemas, even
		// when the caller canceled. Never use a hard reset for recovery.
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, recoveryErr := conn.ExecContext(recoveryCtx, "CALL DOLT_RESET('--soft', ?)", checkpoint); recoveryErr != nil {
			return fmt.Errorf("flatten: commit snapshot: %w (restore checkpoint %s also failed: %v; working state retained)", err, checkpoint, recoveryErr)
		}
		return fmt.Errorf("flatten: commit snapshot: %w (history restored to checkpoint %s; working state retained)", err, checkpoint)
	}
	return nil
}

// FlattenDryRun returns the reachable commit count and unique ancestry root.
func FlattenDryRun(ctx context.Context, conn DBConn) (commitCount int, initialHash string, err error) {
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commitCount); err != nil {
		return 0, "", fmt.Errorf("count commits: %w", err)
	}
	rows, err := conn.QueryContext(ctx, flattenRootQuery)
	if err != nil {
		return 0, "", fmt.Errorf("find ancestry root: %w", err)
	}
	defer rows.Close()
	roots := 0
	for rows.Next() {
		if err := rows.Scan(&initialHash); err != nil {
			return 0, "", fmt.Errorf("read ancestry root: %w", err)
		}
		roots++
	}
	if err := rows.Err(); err != nil {
		return 0, "", fmt.Errorf("read ancestry roots: %w", err)
	}
	if roots != 1 {
		return 0, "", fmt.Errorf("flatten requires one ancestry root, found %d", roots)
	}
	return commitCount, initialHash, nil
}
