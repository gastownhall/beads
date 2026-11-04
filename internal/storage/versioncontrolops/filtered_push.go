package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// FilteredPushCommitMsg is the message of the staging commit F that removes
// the withheld rows from a filtered publication.
const FilteredPushCommitMsg = "federation: exclude private issue types"

// filteredPushRecomputeTables are the dolt_ignore'd tables the is_blocked
// recompute reads. A new branch's working set starts from its commit, so it
// lacks them until they are recreated (empty) on the staging branch.
var filteredPushRecomputeTables = []string{"wisps", "wisp_dependencies"}

// ErrFilteredPushSourceChanged reports that another writer advanced or dirtied
// the source branch while a filtered publication was being prepared. Nothing
// is published and the other writer's changes are kept. On a shared server
// every other writer on the branch counts, so a retry lands only while the
// branch holds still.
var ErrFilteredPushSourceChanged = errors.New("source branch changed during the filtered publication; nothing was pushed " +
	"(a filtered push needs the branch to hold still while it is prepared: sync while nothing else writes to it)")

// filteredPushAbortTimeout bounds the abort of a failed keep merge, which runs
// on a fresh context so a canceled sync still aborts its own merge.
const filteredPushAbortTimeout = 30 * time.Second

// FilteredPushRequest describes one filtered publication to a peer.
type FilteredPushRequest struct {
	// Source is the local branch being published.
	Source string
	// Staging and Keep are unique temporary branch names. The caller arms
	// cleanup of both (and of the Source checkout) before calling.
	Staging, Keep string
	// PeerTip is the commit hash of the peer's branch as last fetched, or ""
	// when the peer does not have the branch yet. Excluded issues present at
	// PeerTip are left in the publication.
	PeerTip string
	// ExcludeTypes are issue types to withhold; "wisp" matches ephemeral rows.
	ExcludeTypes []string
	// Author attributes the merge that keeps the filter commit's ancestry in
	// Source.
	Author string
	// Push publishes refspec through the driver's authenticated transport.
	Push func(ctx context.Context, refspec string) error
}

// FilteredPushResult reports what a filtered publication did.
type FilteredPushResult struct {
	// Pushed is set only after Push returned successfully.
	Pushed bool
	// Filtered reports that the published tip withholds local rows.
	Filtered bool
	// Unchanged reports that the filtered snapshot already matched PeerTip, so
	// nothing was published.
	Unchanged bool
}

// RunFilteredPush publishes Source minus the excluded issues the peer does not
// already hold. On conn it stages an immutable snapshot S of Source, deletes
// the withheld rows on the staging branch and commits that as F, reverts F on
// the keep branch and merges the revert into Source, so F is an ancestor of
// Source with Source's contents unchanged before F is pushed. A later merge of
// the peer's tip therefore cannot delete the local rows. Any error before the
// push leaves the peer untouched. Branch state is session-scoped, so conn must
// be one pinned connection; the caller removes the temporary branches.
func RunFilteredPush(ctx context.Context, conn BlockedRecomputeConn, req FilteredPushRequest) (FilteredPushResult, error) {
	res, err := runFilteredPush(ctx, conn, req)
	// The embedded engine reports a canceled statement as a plain error; keep
	// the cancellation classifiable.
	if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
		err = fmt.Errorf("%w: %w", err, ctx.Err())
	}
	return res, err
}

func runFilteredPush(ctx context.Context, conn BlockedRecomputeConn, req FilteredPushRequest) (FilteredPushResult, error) {
	var res FilteredPushResult
	for _, ref := range []string{req.Source, req.Staging, req.Keep} {
		if err := issueops.ValidateRef(ref); err != nil {
			return res, fmt.Errorf("federation filter: %w", err)
		}
	}
	if req.PeerTip != "" {
		if err := issueops.ValidateRef(req.PeerTip); err != nil {
			return res, fmt.Errorf("federation filter: peer tip: %w", err)
		}
	}

	if err := CheckoutBranch(ctx, conn, req.Source); err != nil {
		return res, fmt.Errorf("federation filter: checkout %s: %w", req.Source, err)
	}
	snapshot, err := branchHead(ctx, conn, req.Source)
	if err != nil {
		return res, fmt.Errorf("federation filter: read %s: %w", req.Source, err)
	}
	ddl, err := ignoredTableDDL(ctx, conn)
	if err != nil {
		return res, fmt.Errorf("federation filter: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", req.Staging, snapshot); err != nil {
		return res, fmt.Errorf("federation filter: create staging branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, req.Staging); err != nil {
		return res, fmt.Errorf("federation filter: checkout staging: %w", err)
	}
	for _, stmt := range ddl {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return res, fmt.Errorf("federation filter: recreate ignored table on staging: %w", err)
		}
	}

	if err := FilteredPushStage(ctx, "delete", conn, req); err != nil {
		return res, err
	}
	deleted, err := deleteWithheldIssues(ctx, conn, req.ExcludeTypes, req.PeerTip)
	if err != nil {
		return res, err
	}
	filterCommit := ""
	if deleted {
		if err := FilteredPushStage(ctx, "commit", conn, req); err != nil {
			return res, err
		}
		if filterCommit, err = CommitFilteredStaging(ctx, conn, req.Staging); err != nil {
			return res, err
		}
	}

	if req.PeerTip != "" {
		same, err := sameContent(ctx, conn, req.PeerTip, req.Staging)
		if err != nil {
			return res, err
		}
		if same {
			res.Unchanged = true
			return res, nil
		}
	}

	if filterCommit != "" {
		if err := FilteredPushStage(ctx, "keep", conn, req); err != nil {
			return res, err
		}
		if err := keepFilterAncestry(ctx, conn, req, snapshot, filterCommit); err != nil {
			return res, err
		}
	}
	if err := CheckoutBranch(ctx, conn, req.Source); err != nil {
		return res, fmt.Errorf("federation filter: restore branch %s: %w", req.Source, err)
	}
	if err := FilteredPushStage(ctx, "push", conn, req); err != nil {
		return res, err
	}
	if err := req.Push(ctx, req.Staging+":"+req.Source); err != nil {
		return res, err
	}
	res.Pushed = true
	res.Filtered = deleted
	return res, nil
}

// CleanupFilteredPush restores the source checkout on conn and force-deletes
// the temporary branches, ignoring ones that were never created.
func CleanupFilteredPush(ctx context.Context, conn DBConn, source string, branches ...string) error {
	var errs []error
	if err := FilteredPushStage(ctx, "cleanup", conn, FilteredPushRequest{Source: source}); err != nil {
		errs = append(errs, err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", source); err != nil {
		errs = append(errs, fmt.Errorf("restore branch %s: %w", source, err))
	}
	for _, branch := range branches {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH('-Df', ?)", branch); err != nil && !isBranchNotFound(err) {
			errs = append(errs, fmt.Errorf("delete branch %s: %w", branch, err))
		}
	}
	return errors.Join(errs...)
}

// filteredPushStageHook, when set, runs at each named stage of a filtered push
// (delete, commit, keep, revert, merge, push, cleanup); a non-nil error fails
// the operation there.
var filteredPushStageHook func(ctx context.Context, stage string, conn DBConn, req FilteredPushRequest) error

// SetFilteredPushStageHookForTest installs fn as the stage hook and returns a
// restore func. Tests use it to observe or fail a filtered push mid-way.
func SetFilteredPushStageHookForTest(fn func(ctx context.Context, stage string, conn DBConn, req FilteredPushRequest) error) func() {
	prev := filteredPushStageHook
	filteredPushStageHook = fn
	return func() { filteredPushStageHook = prev }
}

// FilteredPushStage runs the test stage hook, if any, for stage. A driver with
// its own cleanup calls it for the "cleanup" stage.
func FilteredPushStage(ctx context.Context, stage string, conn DBConn, req FilteredPushRequest) error {
	if filteredPushStageHook == nil {
		return nil
	}
	if err := filteredPushStageHook(ctx, stage, conn, req); err != nil {
		return fmt.Errorf("federation filter: %s: %w", stage, err)
	}
	return nil
}

// RemoteBranchTip returns the commit hash of remote's branch as last fetched,
// or "" when it has not been fetched (the peer does not have it).
func RemoteBranchTip(ctx context.Context, db DBConn, remote, branch string) (string, error) {
	var hash string
	err := db.QueryRowContext(ctx, "SELECT hash FROM dolt_remote_branches WHERE name = ?",
		"remotes/"+remote+"/"+branch).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s/%s tip: %w", remote, branch, err)
	}
	return hash, nil
}

// CommitFilteredStaging recomputes is_blocked over the filtered graph on conn's
// checked-out staging branch and commits it, returning the new HEAD of staging.
func CommitFilteredStaging(ctx context.Context, conn BlockedRecomputeConn, staging string) (string, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("federation filter: begin staged is_blocked recompute: %w", err)
	}
	if _, err := issueops.RecomputeAllIsBlockedInTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("federation filter: recompute staged is_blocked: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("federation filter: commit staged is_blocked recompute: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)", FilteredPushCommitMsg); err != nil {
		return "", fmt.Errorf("federation filter: commit filtered state: %w", err)
	}
	head, err := branchHead(ctx, conn, staging)
	if err != nil {
		return "", fmt.Errorf("federation filter: read filter commit: %w", err)
	}
	return head, nil
}

// deleteWithheldIssues deletes, on the checked-out staging branch, every issue
// matching excludeTypes that peerTip does not already hold, and reports whether
// any row was deleted. Child rows go with their issue by FK cascade.
func deleteWithheldIssues(ctx context.Context, conn DBConn, excludeTypes []string, peerTip string) (bool, error) {
	held := ""
	if peerTip != "" {
		held = fmt.Sprintf(" AND id NOT IN (SELECT id FROM issues AS OF '%s')", peerTip)
	}
	deleted := false
	for _, excludeType := range excludeTypes {
		query, args := "DELETE FROM issues WHERE issue_type = ?"+held, []any{excludeType}
		if excludeType == "wisp" {
			query, args = "DELETE FROM issues WHERE ephemeral = 1"+held, nil
		}
		result, err := conn.ExecContext(ctx, query, args...)
		if err != nil {
			return false, fmt.Errorf("federation filter: delete %s issues: %w", excludeType, err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("federation filter: count deleted %s issues: %w", excludeType, err)
		}
		if n > 0 {
			deleted = true
		}
	}
	return deleted, nil
}

// keepFilterAncestry makes filterCommit an ancestor of req.Source without
// changing its contents: revert it on the keep branch, then fast-forward
// Source to that revert. Source must still be the clean snapshot it was staged
// from; if another writer advanced or dirtied it, nothing is merged or
// published and that writer's changes are kept (never reset). A failed or
// non-fast-forward merge is aborted so this operation leaves no merge behind.
func keepFilterAncestry(ctx context.Context, conn DBConn, req FilteredPushRequest, snapshot, filterCommit string) error {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", req.Keep, filterCommit); err != nil {
		return fmt.Errorf("federation filter: create keep branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, req.Keep); err != nil {
		return fmt.Errorf("federation filter: checkout keep: %w", err)
	}
	if err := FilteredPushStage(ctx, "revert", conn, req); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_REVERT('--author', ?, ?)", req.Author, filterCommit); err != nil {
		return fmt.Errorf("federation filter: revert filter commit on keep: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, req.Source); err != nil {
		return fmt.Errorf("federation filter: restore branch %s: %w", req.Source, err)
	}
	if err := sourceUnchanged(ctx, conn, req.Source, snapshot); err != nil {
		return err
	}
	if err := FilteredPushStage(ctx, "merge", conn, req); err != nil {
		return err
	}
	conflicts, err := Merge(ctx, conn, req.Keep, req.Author)
	if err == nil && len(conflicts) > 0 {
		err = fmt.Errorf("merge branch %s: %d conflict(s)", req.Keep, len(conflicts))
	}
	if err == nil {
		// A writer that advanced Source after the check above turns the
		// fast-forward into a merge (or a conflict a session allowing commit
		// conflicts leaves in progress); refuse to publish over it.
		err = fastForwardedTo(ctx, conn, req.Source, req.Keep)
	}
	if err != nil {
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), filteredPushAbortTimeout)
		defer cancel()
		merging, mergingErr := mergeInProgress(abortCtx, conn)
		if mergingErr != nil {
			err = errors.Join(err, fmt.Errorf("check keep merge: %w", mergingErr))
		}
		if merging {
			if _, abortErr := conn.ExecContext(abortCtx, "CALL DOLT_MERGE('--abort')"); abortErr != nil {
				err = errors.Join(err, fmt.Errorf("abort keep merge: %w", abortErr))
			}
		}
		return fmt.Errorf("federation filter: keep local excluded issues: %w", err)
	}
	return nil
}

// sourceUnchanged fails with ErrFilteredPushSourceChanged unless the checked-out
// source is still at snapshot with no pending changes and no merge in progress,
// so any merge left after the keep merge is this operation's own.
func sourceUnchanged(ctx context.Context, conn DBConn, source, snapshot string) error {
	head, err := branchHead(ctx, conn, source)
	if err != nil {
		return fmt.Errorf("federation filter: read %s: %w", source, err)
	}
	if head != snapshot {
		return fmt.Errorf("federation filter: %s advanced from %s to %s: %w", source, snapshot, head, ErrFilteredPushSourceChanged)
	}
	merging, err := mergeInProgress(ctx, conn)
	if err != nil {
		return fmt.Errorf("federation filter: %w", err)
	}
	dirty, err := issueops.HasPendingChanges(ctx, conn)
	if err != nil {
		return fmt.Errorf("federation filter: %w", err)
	}
	if merging || dirty {
		return fmt.Errorf("federation filter: %s has uncommitted changes: %w", source, ErrFilteredPushSourceChanged)
	}
	return nil
}

// fastForwardedTo fails with ErrFilteredPushSourceChanged unless the keep merge
// fast-forwarded source to keep.
func fastForwardedTo(ctx context.Context, conn DBConn, source, keep string) error {
	head, err := branchHead(ctx, conn, source)
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	want, err := branchHead(ctx, conn, keep)
	if err != nil {
		return fmt.Errorf("read %s: %w", keep, err)
	}
	if head != want {
		return fmt.Errorf("%s is at %s, not the keep commit %s: %w", source, head, want, ErrFilteredPushSourceChanged)
	}
	return nil
}

// sameContent reports whether two commits hold the same versioned contents.
func sameContent(ctx context.Context, conn DBConn, from, to string) (bool, error) {
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM DOLT_DIFF_SUMMARY(?, ?)", from, to).Scan(&n); err != nil {
		return false, fmt.Errorf("federation filter: compare with peer tip: %w", err)
	}
	return n == 0, nil
}

// ignoredTableDDL returns IF NOT EXISTS creates for the ignored tables the
// recompute reads, taken from the checked-out source branch.
func ignoredTableDDL(ctx context.Context, conn DBConn) ([]string, error) {
	var stmts []string
	for _, table := range filteredPushRecomputeTables {
		var name, ddl string
		if err := conn.QueryRowContext(ctx, "SHOW CREATE TABLE `"+table+"`").Scan(&name, &ddl); err != nil {
			return nil, fmt.Errorf("read %s shape: %w", table, err)
		}
		stmts = append(stmts, strings.Replace(ddl, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1))
	}
	return stmts, nil
}

func branchHead(ctx context.Context, conn DBConn, branch string) (string, error) {
	var hash string
	if err := conn.QueryRowContext(ctx, "SELECT hash FROM dolt_branches WHERE name = ?", branch).Scan(&hash); err != nil {
		return "", err
	}
	return hash, nil
}

// isBranchNotFound matches Dolt's missing-branch error. Both drivers report it
// as a generic error, so match the message case-insensitively.
func isBranchNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "branch not found")
}
