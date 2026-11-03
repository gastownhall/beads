package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// wispExcludeType is the special exclude-types entry matching ephemeral
// issue rows (issues.ephemeral = 1) instead of an issue_type value.
const wispExcludeType = "wisp"

// filteredStagingCommitMessage labels the commit that deletes excluded
// issues from the staging branch. Callers can override it per push.
const filteredStagingCommitMessage = "federation: exclude private issue types"

// FilteredConn is the connection PushFilteredBranch stages on. The DOLT_*
// procedures refuse open transactions, so the branch/checkout/commit steps
// run directly on the connection, while the is_blocked recompute needs one
// explicit transaction; *sql.Conn provides both and is what both drivers pin.
type FilteredConn interface {
	DBConn
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// FilteredPushOptions configures one privacy-filtered publication.
type FilteredPushOptions struct {
	// SourceBranch is the branch whose committed tip is published.
	SourceBranch string
	// StagingBranch is a fresh, UUID-suffixed branch name the filtered tip
	// is built on. The caller owns its cleanup: PushFilteredBranch creates
	// and leaves it behind, on success and failure alike, because cleanup
	// needs a fresh connection that survives cancellation of a failed push.
	StagingBranch string
	// ExcludeTypes lists the issue types to withhold from the peer; the
	// special "wisp" entry matches ephemeral rows.
	ExcludeTypes []string
	// PeerRef is the peer's cached remote-tracking ref
	// ("remotes/<peer>/<branch>") used to preserve excluded-typed issues
	// the peer already holds. Empty disables that preservation.
	PeerRef string
	// CommitMessage labels the filter commit; empty uses the standard one.
	CommitMessage string
	// Push publishes the staged refspec (StagingBranch:SourceBranch) to
	// the peer. It runs after SourceBranch has been restored as the
	// connection's checked-out branch.
	Push func(ctx context.Context, refspec string) error
}

// FilteredPushResult reports what the staging pass did.
type FilteredPushResult struct {
	// Filtered is true when excluded rows were removed and a filter commit
	// was created, i.e. the pushed tip differs from the source tip.
	Filtered bool
}

// PushFilteredBranch publishes a privacy-filtered copy of opts.SourceBranch:
// it stages the branch on opts.StagingBranch, deletes the excluded issue
// types from it (preserving the ones the peer already holds), repairs and
// commits the filtered graph, restores SourceBranch, and hands the staged
// refspec to opts.Push.
//
// A DELETE that fails now fails the publication: the filter guards issue data
// the sender has chosen not to replicate, so a best-effort delete that
// silently leaves excluded issues in the pushed tip is worse than no push.
//
// Branch state is session-scoped, so every step runs on conn. Callers pin
// one connection for the whole call. On failure the session may be left on
// StagingBranch; each caller's cleanup restores SourceBranch and deletes
// StagingBranch on a fresh connection (see dolt's cleanupFilteredStaging and
// embeddeddolt's cleanupFilteredStaging).
func PushFilteredBranch(ctx context.Context, conn FilteredConn, opts FilteredPushOptions) (*FilteredPushResult, error) {
	if opts.Push == nil {
		return nil, fmt.Errorf("federation filter: no push callback")
	}
	// Create the staging branch from the source branch. Cleanup is armed by
	// the caller: a lost response can make this call fail after Dolt created
	// the branch.
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", opts.StagingBranch, opts.SourceBranch); err != nil {
		return nil, fmt.Errorf("federation filter: create staging branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, opts.StagingBranch); err != nil {
		return nil, fmt.Errorf("federation filter: checkout staging: %w", err)
	}

	peerIDs, err := peerExcludedIDs(ctx, conn, opts.PeerRef, opts.ExcludeTypes)
	if err != nil {
		return nil, err
	}
	deleted, err := deleteExcludedRows(ctx, conn, opts.ExcludeTypes, peerIDs)
	if err != nil {
		return nil, err
	}
	if deleted {
		if err := CommitFilteredRows(ctx, conn, opts.CommitMessage); err != nil {
			return nil, err
		}
	}

	// Restore the source branch before pushing: the refspec names the
	// staging ref explicitly, but the connection must not be left on a
	// branch whose life ends with the cleanup.
	if err := CheckoutBranch(ctx, conn, opts.SourceBranch); err != nil {
		return nil, fmt.Errorf("federation filter: restore branch %s: %w", opts.SourceBranch, err)
	}

	refspec := opts.StagingBranch + ":" + opts.SourceBranch
	if err := opts.Push(ctx, refspec); err != nil {
		return nil, err
	}
	return &FilteredPushResult{Filtered: deleted}, nil
}

// peerExcludedIDs returns the ids of excluded-typed issues the receiving
// peer already holds at its cached remote-tracking ref, so the filtered
// publication can leave them in the pushed tip instead of deleting data the
// peer legitimately holds. peerRef is the dolt_remote_branches name
// ("remotes/<peer>/<branch>"); a ref that is not cached means the peer has
// received nothing (nothing to preserve), while a listed ref whose tip cannot
// be read fails the publication rather than guessing what the peer holds.
func peerExcludedIDs(ctx context.Context, conn DBConn, peerRef string, excludeTypes []string) ([]string, error) {
	if peerRef == "" {
		return nil, nil
	}
	if err := issueops.ValidateRef(peerRef); err != nil {
		return nil, fmt.Errorf("federation filter: peer tracking ref: %w", err)
	}
	var hash string
	switch err := conn.QueryRowContext(ctx,
		"SELECT hash FROM dolt_remote_branches WHERE name = ?", peerRef).Scan(&hash); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("federation filter: read peer tracking ref %s: %w", peerRef, err)
	}
	if err := issueops.ValidateRef(hash); err != nil {
		return nil, fmt.Errorf("federation filter: peer tracking ref %s: %w", peerRef, err)
	}
	predicate, args := excludedIssuesPredicate(excludeTypes)
	//nolint:gosec // G201: hash comes from dolt_remote_branches and is validated above; AS OF takes it as a SQL literal
	rows, err := conn.QueryContext(ctx,
		fmt.Sprintf("SELECT id FROM issues AS OF '%s' WHERE %s", hash, predicate), args...)
	if err != nil {
		return nil, fmt.Errorf("federation filter: read excluded issues at peer ref %s: %w", peerRef, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("federation filter: scan excluded issue at peer ref %s: %w", peerRef, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// deleteExcludedRows removes the issues matching excludeTypes from conn's
// current (staging) branch and reports whether any rows were deleted, minus
// the peer-held ids. DELETE errors propagate: see PushFilteredBranch.
func deleteExcludedRows(ctx context.Context, conn DBConn, excludeTypes []string, peerIDs []string) (bool, error) {
	predicate, args := excludedIssuesPredicate(excludeTypes)
	query := "DELETE FROM issues WHERE " + predicate
	if len(peerIDs) > 0 {
		// Keep excluded-typed issues the peer already holds: the
		// publication must not destroy data on the receiving side.
		query += " AND id NOT IN (" + strings.TrimRight(strings.Repeat("?,", len(peerIDs)), ",") + ")"
		for _, id := range peerIDs {
			args = append(args, id)
		}
	}
	res, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("federation filter: delete excluded issues: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("federation filter: count deleted excluded issues: %w", err)
	}
	return n > 0, nil
}

// excludedIssuesPredicate returns the SQL predicate matching the issues rows
// excludeTypes selects — "wisp" matches ephemeral rows, every other entry its
// issue_type — plus the placeholder args in order. The predicate contains
// placeholders only, never interpolated values.
func excludedIssuesPredicate(excludeTypes []string) (string, []any) {
	var conds []string
	var args []any
	for _, excludeType := range excludeTypes {
		if excludeType == wispExcludeType {
			conds = append(conds, "ephemeral = 1")
		} else {
			conds = append(conds, "issue_type = ?")
			args = append(args, excludeType)
		}
	}
	if len(conds) == 0 {
		return "1 = 0", nil
	}
	return strings.Join(conds, " OR "), args
}

// CommitFilteredRows repairs and commits the filtered graph on conn's
// current (staging) branch — the second half of PushFilteredBranch, exported
// for the server-mode staging fixtures that exercise the commit seam on a
// pinned connection directly. The is_blocked recompute runs in one
// transaction so no reader of the staged branch observes half-recomputed
// rows; DOLT_COMMIT refuses open transactions, so the commit itself runs
// outside it.
func CommitFilteredRows(ctx context.Context, conn FilteredConn, msg string) error {
	if msg == "" {
		msg = filteredStagingCommitMessage
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("federation filter: begin staged is_blocked recompute: %w", err)
	}
	if _, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("federation filter: recompute staged is_blocked: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("federation filter: commit staged is_blocked recompute: %w", err)
	}
	if err := schema.DrainCall(ctx, conn, "CALL DOLT_COMMIT('-Am', ?)", msg); err != nil {
		return fmt.Errorf("federation filter: commit filtered state: %w", err)
	}
	return nil
}
