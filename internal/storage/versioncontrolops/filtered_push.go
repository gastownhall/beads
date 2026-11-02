package versioncontrolops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// FilteredPushStagingPrefix identifies the temporary branch a filtered push
// publishes. Each operation appends a UUID so concurrent pushes cannot collide.
const FilteredPushStagingPrefix = "__federation_push_staging_"

// FilteredPushKeepPrefix identifies the temporary branch holding the commit
// that reverts the filter commit, which the backend merges into Source so the
// local history descends from what the peer receives.
const FilteredPushKeepPrefix = "__federation_push_keep_"

// FilteredPushCommitMsg is the message of the staging commit that removes the
// excluded issue types.
const FilteredPushCommitMsg = "federation: exclude private issue types"

// FilteredPush is one federation push that publishes Source without the
// issues of ExcludeTypes ("wisp" matches ephemeral rows). Stage builds the
// filtered commit F on Staging and, on Keep, the commit R reverting it. The
// backend merges Keep into Source before pushing Staging to the peer's
// Source, so the filtered tip coming back on the next sync is an ancestor of
// Source instead of a deletion of the excluded issues, and calls Cleanup on
// every exit.
type FilteredPush struct {
	Source       string
	Staging      string
	Keep         string
	ExcludeTypes []string
	// Author is passed to the commits Stage makes; empty lets the session
	// attribute them.
	Author string
}

// NewFilteredPush allocates unique temporary branch names for a filtered push
// of source.
func NewFilteredPush(source string, excludeTypes []string, author string) (*FilteredPush, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("federation filter: generate staging branch: %w", err)
	}
	return &FilteredPush{
		Source:       source,
		Staging:      FilteredPushStagingPrefix + id.String(),
		Keep:         FilteredPushKeepPrefix + id.String(),
		ExcludeTypes: excludeTypes,
		Author:       author,
	}, nil
}

// Refspec maps the filtered branch onto the peer's copy of Source.
func (p *FilteredPush) Refspec() string {
	return p.Staging + ":" + p.Source
}

// Stage creates Staging from Source on conn, deletes the excluded issues
// there (foreign keys cascade to their labels, comments and dependencies),
// recomputes is_blocked for the filtered graph and commits it, creates Keep
// from that commit and reverts it there, then checks Source out again. It
// reports whether any row was removed; when none was, Staging is Source,
// nothing is committed and Keep is not created. Branch state is session
// scoped, so conn must be one pinned session; the caller arms Cleanup first,
// since a create whose response is lost can leave the branch behind.
//
// Any error fails the push: publishing an unfiltered or half-filtered tip
// would leak exactly what the exclusion exists to keep local.
func (p *FilteredPush) Stage(ctx context.Context, conn BlockedRecomputeConn) (bool, error) {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", p.Staging, p.Source); err != nil {
		return false, fmt.Errorf("federation filter: create staging branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, p.Staging); err != nil {
		return false, fmt.Errorf("federation filter: checkout staging: %w", err)
	}
	if err := p.materializeIgnoredTables(ctx, conn); err != nil {
		return false, err
	}
	removed, err := deleteExcludedIssues(ctx, conn, p.ExcludeTypes)
	if err != nil {
		return false, err
	}
	if removed {
		if err := CommitFilteredStaging(ctx, conn, p.Author); err != nil {
			return false, err
		}
		if err := p.revertOnKeep(ctx, conn); err != nil {
			return false, err
		}
	}
	if err := CheckoutBranch(ctx, conn, p.Source); err != nil {
		return false, fmt.Errorf("federation filter: restore branch %s: %w", p.Source, err)
	}
	return removed, nil
}

// revertOnKeep branches Keep from Staging's filter commit and reverts that
// commit on Keep: Keep's tip has Source's tree with the filter commit as its
// parent.
func (p *FilteredPush) revertOnKeep(ctx context.Context, conn DBConn) error {
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", p.Keep, p.Staging); err != nil {
		return fmt.Errorf("federation filter: create keep branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, p.Keep); err != nil {
		return fmt.Errorf("federation filter: checkout keep branch: %w", err)
	}
	query, args := "CALL DOLT_REVERT('HEAD')", []any(nil)
	if p.Author != "" {
		query, args = "CALL DOLT_REVERT('--author', ?, 'HEAD')", []any{p.Author}
	}
	if _, err := conn.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("federation filter: revert filter commit on keep branch: %w", err)
	}
	return nil
}

// filteredStagingIgnoredTables are the dolt_ignore'd tables the is_blocked
// recompute reads. A new branch's working set starts from its commit, which
// never holds them, so Stage creates them empty on Staging, where they stay
// ignored and out of the filter commit. Empty is the right content: a
// published tip carries no wisps.
var filteredStagingIgnoredTables = []string{"wisps", "wisp_dependencies"}

// materializeIgnoredTables creates each filteredStagingIgnoredTables table
// missing on conn's current branch with the definition it has in Source's
// working set; a table Source lacks too is skipped.
func (p *FilteredPush) materializeIgnoredTables(ctx context.Context, conn DBConn) error {
	var database string
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return fmt.Errorf("federation filter: read database name: %w", err)
	}
	database, _, _ = strings.Cut(database, "/")
	for _, table := range filteredStagingIgnoredTables {
		//nolint:gosec // G201: table is a constant; the revision name is backquoted with its backquotes doubled.
		query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` LIKE `%s`.`%s`",
			table, strings.ReplaceAll(database+"/"+p.Source, "`", "``"), table)
		if _, err := conn.ExecContext(ctx, query); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "table not found") {
				continue
			}
			return fmt.Errorf("federation filter: materialize ignored table %s on staging: %w", table, err)
		}
	}
	return nil
}

// deleteExcludedIssues removes the issues of excludeTypes from the committed
// issues table on conn's current branch and reports whether any row went.
func deleteExcludedIssues(ctx context.Context, conn DBConn, excludeTypes []string) (bool, error) {
	removed := false
	for _, excludeType := range excludeTypes {
		query, args := "DELETE FROM issues WHERE issue_type = ?", []any{excludeType}
		if excludeType == "wisp" {
			query, args = "DELETE FROM issues WHERE ephemeral = 1", nil
		}
		result, err := conn.ExecContext(ctx, query, args...)
		if err != nil {
			return false, fmt.Errorf("federation filter: delete excluded type %s: %w", excludeType, err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("federation filter: count deleted %s issues: %w", excludeType, err)
		}
		if n > 0 {
			removed = true
		}
	}
	return removed, nil
}

// CommitFilteredStaging recomputes is_blocked for the filtered graph on
// conn's current branch in one SQL transaction, then commits everything
// (Dolt procedures cannot run inside that transaction).
func CommitFilteredStaging(ctx context.Context, conn BlockedRecomputeConn, author string) error {
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
	query, args := "CALL DOLT_COMMIT('-Am', ?)", []any{FilteredPushCommitMsg}
	if author != "" {
		query, args = "CALL DOLT_COMMIT('-Am', ?, '--author', ?)", []any{FilteredPushCommitMsg, author}
	}
	if _, err := conn.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("federation filter: commit filtered state: %w", err)
	}
	return nil
}

// Cleanup checks Source out on db and deletes both temporary branches; a
// branch that was never created is not an error. Errors are joined so one failure
// does not hide another.
func (p *FilteredPush) Cleanup(ctx context.Context, db DBConn) error {
	var errs error
	if err := CheckoutBranch(ctx, db, p.Source); err != nil {
		errs = errors.Join(errs, fmt.Errorf("restore branch %s: %w", p.Source, err))
	}
	for _, branch := range []string{p.Staging, p.Keep} {
		if err := deleteBranchIfPresent(ctx, db, branch); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

func deleteBranchIfPresent(ctx context.Context, db DBConn, name string) error {
	_, err := db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", name)
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "branch not found") {
		return nil
	}
	return fmt.Errorf("delete branch %s: %w", name, err)
}
