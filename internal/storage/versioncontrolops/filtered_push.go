package versioncontrolops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
// issues of ExcludeTypes ("wisp" matches ephemeral rows) that no peer already
// holds. Stage builds the filtered commit F on Staging and, on Keep, the
// commit R reverting it. The backend merges Keep into Source before pushing
// Staging to the peer's Source, so the filtered tip coming back on the next
// sync is an ancestor of Source instead of a deletion of the excluded issues,
// and calls Cleanup on every exit.
type FilteredPush struct {
	Source       string
	Staging      string
	Keep         string
	ExcludeTypes []string
	// Peer is the remote the push goes to. Excluded issues that a peer's
	// copy of Source holds stay in F: Peer's copy, fresh from the caller's
	// fetch, and the copy last fetched from every other remote but
	// federationSyncRemote. Deleting one would delete the peer's own issue,
	// or one it already has, when it merges F, directly or through another
	// peer that merged F first. Every excluded issue is withheld when no
	// peer copy has been fetched.
	Peer string
	// Author is passed to the commits Stage makes; empty lets the session
	// attribute them.
	Author string
	// Removed is the number of excluded issues Stage deleted from Staging.
	Removed int64
	// BeforeStep, when set, runs before each step named by the
	// FilteredPushStep* constants; an error fails that step. It is a test
	// seam for failure and cancellation injection.
	BeforeStep func(step string) error
}

// The steps of a filtered push, in order, as passed to BeforeStep.
const (
	FilteredPushStepBranch    = "branch"
	FilteredPushStepDelete    = "delete"
	FilteredPushStepRecompute = "recompute"
	FilteredPushStepCommit    = "commit"
	FilteredPushStepKeep      = "keep"
	FilteredPushStepRevert    = "revert"
	FilteredPushStepMerge     = "merge"
	FilteredPushStepPush      = "push"
	FilteredPushStepCleanup   = "cleanup"
)

// FilteredPushSteps lists every step BeforeStep sees, in order.
var FilteredPushSteps = []string{
	FilteredPushStepBranch, FilteredPushStepDelete, FilteredPushStepRecompute,
	FilteredPushStepCommit, FilteredPushStepKeep, FilteredPushStepRevert,
	FilteredPushStepMerge, FilteredPushStepPush, FilteredPushStepCleanup,
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

// Step runs BeforeStep for step, if set, and wraps its error with the step.
func (p *FilteredPush) Step(step string) error {
	if p.BeforeStep == nil {
		return nil
	}
	if err := p.BeforeStep(step); err != nil {
		return fmt.Errorf("federation filter: %s: %w", step, err)
	}
	return nil
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
	if err := p.Step(FilteredPushStepBranch); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", p.Staging, p.Source); err != nil {
		return false, fmt.Errorf("federation filter: create staging branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, p.Staging); err != nil {
		return false, fmt.Errorf("federation filter: checkout staging: %w", err)
	}
	if err := p.materializeIgnoredTables(ctx, conn); err != nil {
		return false, err
	}
	if err := p.Step(FilteredPushStepDelete); err != nil {
		return false, err
	}
	notHeld, err := p.notHeldByPeers(ctx, conn)
	if err != nil {
		return false, err
	}
	n, err := deleteExcludedIssues(ctx, conn, p.ExcludeTypes, notHeld)
	if err != nil {
		return false, err
	}
	p.Removed = n
	removed := n > 0
	if removed {
		if err := p.commitFiltered(ctx, conn); err != nil {
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
	if err := p.Step(FilteredPushStepKeep); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "CALL DOLT_BRANCH(?, ?)", p.Keep, p.Staging); err != nil {
		return fmt.Errorf("federation filter: create keep branch: %w", err)
	}
	if err := CheckoutBranch(ctx, conn, p.Keep); err != nil {
		return fmt.Errorf("federation filter: checkout keep branch: %w", err)
	}
	if err := p.Step(FilteredPushStepRevert); err != nil {
		return err
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

// doltCommitHashPattern is the shape of a Dolt commit hash (32 base32
// characters), the only text notHeldByPeers interpolates into its AS OF.
var doltCommitHashPattern = regexp.MustCompile(`^[0-9a-v]{32}$`)

// federationSyncRemote is the remote bd dolt push and pull use. bd
// federation sync without --peer skips it, and unless it is Peer its copy
// does not count as a peer's: bd dolt push sends it every issue, excluded
// types included, so its holding one says nothing about any peer.
const federationSyncRemote = "origin"

// notHeldByPeers returns the condition limiting the delete to issues that no
// peer copy of Source holds (see Peer), or "" (no limit) when no copy has
// been fetched. Each copy is resolved to its commit hash, so a remote name
// never reaches the SQL text.
func (p *FilteredPush) notHeldByPeers(ctx context.Context, conn DBConn) (string, error) {
	refs, err := p.peerRefs(ctx, conn)
	if err != nil {
		return "", err
	}
	var notHeld strings.Builder
	for _, ref := range refs {
		var hash string
		err := conn.QueryRowContext(ctx,
			"SELECT hash FROM dolt_remote_branches WHERE name = ?", ref).Scan(&hash)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("federation filter: resolve peer branch %s: %w", ref, err)
		}
		if !doltCommitHashPattern.MatchString(hash) {
			return "", fmt.Errorf("federation filter: peer branch %s resolved to unexpected hash %q", ref, hash)
		}
		fmt.Fprintf(&notHeld, " AND id NOT IN (SELECT id FROM issues AS OF '%s')", hash)
	}
	return notHeld.String(), nil
}

// peerRefs names the remote-tracking refs of Source that notHeldByPeers
// reads: Peer's first, then every other remote's but federationSyncRemote's,
// in name order.
func (p *FilteredPush) peerRefs(ctx context.Context, conn DBConn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT name FROM dolt_remotes")
	if err != nil {
		return nil, fmt.Errorf("federation filter: list peers: %w", err)
	}
	var others []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("federation filter: list peers: %w", err)
		}
		if name != p.Peer && name != federationSyncRemote {
			others = append(others, name)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("federation filter: list peers: %w", err)
	}
	slices.Sort(others)
	var refs []string
	if p.Peer != "" {
		refs = append(refs, "remotes/"+p.Peer+"/"+p.Source)
	}
	for _, name := range others {
		refs = append(refs, "remotes/"+name+"/"+p.Source)
	}
	return refs, nil
}

// deleteExcludedIssues removes the issues of excludeTypes from the committed
// issues table on conn's current branch, limited by notHeld (see
// notHeldByPeers), and reports how many rows went.
func deleteExcludedIssues(ctx context.Context, conn DBConn, excludeTypes []string, notHeld string) (int64, error) {
	var removed int64
	for _, excludeType := range excludeTypes {
		query, args := "DELETE FROM issues WHERE issue_type = ?"+notHeld, []any{excludeType}
		if excludeType == "wisp" {
			query, args = "DELETE FROM issues WHERE ephemeral = 1"+notHeld, nil
		}
		result, err := conn.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, fmt.Errorf("federation filter: delete excluded type %s: %w", excludeType, err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("federation filter: count deleted %s issues: %w", excludeType, err)
		}
		removed += n
	}
	return removed, nil
}

// CommitFilteredStaging recomputes is_blocked for the filtered graph on
// conn's current branch in one SQL transaction, then commits everything
// (Dolt procedures cannot run inside that transaction). The transaction does
// not activate the events journal: is_blocked is derived from the dependency
// graph, so a consumer replaying the journal needs no record of the
// recompute, the reason the dolt package's journal-scope audit exempts its
// own post-merge recompute.
func CommitFilteredStaging(ctx context.Context, conn BlockedRecomputeConn, author string) error {
	return (&FilteredPush{Author: author}).commitFiltered(ctx, conn)
}

func (p *FilteredPush) commitFiltered(ctx context.Context, conn BlockedRecomputeConn) error {
	if err := p.Step(FilteredPushStepRecompute); err != nil {
		return err
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
	if err := p.Step(FilteredPushStepCommit); err != nil {
		return err
	}
	query, args := "CALL DOLT_COMMIT('-Am', ?)", []any{FilteredPushCommitMsg}
	if p.Author != "" {
		query, args = "CALL DOLT_COMMIT('-Am', ?, '--author', ?)", []any{FilteredPushCommitMsg, p.Author}
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
	errs := p.Step(FilteredPushStepCleanup)
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
