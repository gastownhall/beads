package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// This file settles what a CLI-route pull leaves behind (GH#7839). `dolt
// pull` runs as a subprocess in the database directory the store's server
// serves, so its merge lands in the shared working set with no transaction the
// store can roll back: unlike the SQL route (settleMergeInTx rolls its own
// transaction back) and the embedded route (SettleMerge calls
// DOLT_MERGE('--abort')), a conflict the auto-resolver declined used to be
// left open in dolt_merge_status with the conflicts still in dolt_conflicts —
// after which every bd write and every later pull fails with Dolt's
// "Merge conflict detected" autocommit rejection until an operator intervenes
// by hand. Settlement here mirrors SettleMerge's abort-only contract: capture
// the conflicts the resolver declined, abort the merge this pull provably
// started, verify the branch and working set came back, and report a
// MergeConflictsError. A merge the pull cannot attribute to itself — another
// session's deliberate work — is never resolved and never aborted.

// openMergeGuidance is the operator remedy every open-merge diagnosis here
// names. `bd conflicts list` shows the conflicted rows the merge carries, and
// DOLT_MERGE('--abort') restores the branch (bd sql --readonly refuses CALL,
// hence the plain bd sql form; `bd conflicts resolve <id> --ours|--theirs` is
// the per-row alternative when the operator wants the merge to conclude
// instead of abort).
const openMergeGuidance = "inspect with 'bd conflicts list'; abort it with 'bd sql \"CALL DOLT_MERGE('--abort')\"', or resolve per row with 'bd conflicts resolve <id> --ours|--theirs'"

// cliPullSettlementError reports a CLI-route pull that could not be settled:
// either a merge was already open before the pull (the transport is refused,
// the merge and its conflicts preserved untouched), or the subprocess left
// merge state this pull cannot attribute to itself, or a merge it did start
// could not be restored. Unlike MergeConflictsError it is never
// conflicts-as-data: the working set still needs an operator, so federation's
// conflict-reporting contract (peerPullOutcome) must surface it as an error,
// not convert it to data. Unwrap returns the original cause, when there was
// one.
type cliPullSettlementError struct {
	// state describes what was observed and what was (or was not) done
	// about it, naming the merge's refs and tables so the operator can act
	// without re-deriving them.
	state string
	cause error
}

func (e *cliPullSettlementError) Error() string {
	if e.cause == nil {
		return e.state + "; " + openMergeGuidance
	}
	return e.state + ": " + e.cause.Error() + "; " + openMergeGuidance
}

func (e *cliPullSettlementError) Unwrap() error { return e.cause }

// openMergeState is a readable snapshot of Dolt's merge state on the store's
// branch: what dolt_merge_status reports between a halted DOLT_MERGE and its
// concluding commit. It is the fact every settlement decision below turns on —
// whether a merge is open at all decides whether there is anything to settle,
// and the refs decide whether THIS pull may touch it.
type openMergeState struct {
	merging bool
	// source and target are dolt_merge_status's refs: a pull's merge names
	// refs/remotes/<remote>/<branch> as its source and the store branch as
	// its target; another session's merge of a local branch names a
	// refs/heads/* source.
	source string
	target string
}

// describe renders the state for an operator-facing diagnosis.
func (st openMergeState) describe() string {
	if !st.merging {
		return "no merge is open"
	}
	return fmt.Sprintf("a merge is open (source %s, target %s)", st.source, st.target)
}

// readOpenMergeState reads dolt_merge_status on a pool connection pinned to
// the store's live working branch. Merge state is session-scoped (it describes
// the current working branch's working set), so an unpinned pooled connection
// parked on the database's default branch would report the wrong branch's
// merge.
func (s *DoltStore) readOpenMergeState(ctx context.Context) (openMergeState, error) {
	branch := s.liveBranchFromPool(ctx)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return openMergeState{}, fmt.Errorf("acquire connection to read merge state: %w", err)
	}
	defer conn.Close()
	if err := checkoutBranchOnConn(ctx, conn, branch); err != nil {
		return openMergeState{}, err
	}
	return readMergeStateOnConn(ctx, conn)
}

// liveBranchFromPool reads the branch the store's pooled session is actually
// working on — the working branch the CLI `dolt pull` subprocess merges into
// and the one whose merge state settlement must read. The recorded s.branch
// is only ever set inside Checkout() and can go stale (the test harness, and
// any checkout run directly against s.db, leave it behind — see
// pinStoreBranch's caveat), so the live read is authoritative; s.branch is the
// fallback when the read cannot run. Callers must run this BEFORE pinning a
// connection: on a pool capped at one connection a pool-level read after
// pinning waits for the very connection the caller holds.
func (s *DoltStore) liveBranchFromPool(ctx context.Context) string {
	var branch string
	if err := s.db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil || branch == "" {
		return s.branch
	}
	return branch
}

// checkoutBranchOnConn checks conn out to branch when it is parked on another
// one (a pooled connection left on the database's default branch would read
// the wrong branch's merge state, be-b0am). Unlike pinStoreBranch it does not
// read active_branch() through the pool: callers here hold a pinned s.db.Conn,
// and a pool-level read on a pool capped at one connection would wait for the
// very connection it already holds — the branch is read from the pool before
// the connection is pinned instead (liveBranchFromPool). An empty branch needs
// nothing (embedded stores without a recorded branch work on the pool's
// session branch, which the conn already shares).
func checkoutBranchOnConn(ctx context.Context, conn *sql.Conn, branch string) error {
	if branch == "" {
		return nil
	}
	return checkoutBranchIfNeeded(ctx, conn, branch)
}

func readMergeStateOnConn(ctx context.Context, conn *sql.Conn) (openMergeState, error) {
	var st openMergeState
	var source, target sql.NullString
	err := conn.QueryRowContext(ctx,
		"SELECT is_merging, source, target FROM dolt_merge_status").Scan(&st.merging, &source, &target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return openMergeState{}, nil
		}
		return openMergeState{}, fmt.Errorf("query merge status: %w", err)
	}
	st.source, st.target = source.String, target.String
	return st, nil
}

// cliPullOwnsMergeState reports whether the open merge in st is one this
// request may resolve or abort. branch is the store's live working branch (the
// pull's target). remote is the name the CLI pull pulled from, or "" when the
// caller has no remote context (finishCLIPull's historical signature): then a
// merge open on the store branch is the only attribution available and it is
// trusted. With a remote named, the merge must be exactly what `dolt pull
// <remote> <branch>` produces — source refs/remotes/<remote>/<branch>, target
// the store branch — so a merge another session started concurrently (a local
// branch, a different remote) is left untouched rather than aborted out from
// under its owner. Both the full and the bare ref forms are accepted; the
// integration tests pin which ones this Dolt records.
func cliPullOwnsMergeState(st openMergeState, remote, branch string) bool {
	if !st.merging {
		return false
	}
	if st.target != "refs/heads/"+branch && st.target != branch {
		return false
	}
	if remote == "" {
		return true
	}
	return st.source == "refs/remotes/"+remote+"/"+branch || st.source == remote+"/"+branch
}

// runCLIPullSettled runs one CLI-route pull (git-protocol, credentialed, or
// cloud-auth) and settles its result. It wraps every doltCLIPull /
// doltCLIPullFromPeer call site: a merge already open on the store branch
// stops the transport before the subprocess runs (the operator's open merge
// is preserved untouched), and whatever the subprocess leaves behind is
// settled by settleCLIPull — SQL-route parity for declined conflicts, and a
// settlement error instead of silent state for anything unattributable.
func (s *DoltStore) runCLIPullSettled(ctx context.Context, remote string, pull func() error) error {
	if s.readOnly {
		// A read-only store cannot resolve, commit or abort; surface the
		// pull result as-is.
		return pull()
	}
	pre, preErr := s.readOpenMergeState(ctx)
	if preErr == nil && pre.merging {
		return &cliPullSettlementError{
			state: pre.describe() + " that was already open before this pull; the pull was refused so the merge is preserved",
		}
	}
	pullErr := pull()
	if preErr != nil {
		// The pre-pull state was unreadable, so a merge the pull leaves
		// open cannot be attributed to the request by timeline either;
		// settleCLIPull's ref matching is the only attribution left.
		return s.settleCLIPull(ctx, remote, pullErr)
	}
	return s.settleCLIPull(ctx, remote, pullErr)
}

// finishCLIPull runs the merge-conflict auto-resolver after a CLI-based pull,
// settling what the subprocess left in the shared working set (GH#7839).
// pullErr is what doltCLIPull returned: a pull that fails *because* of
// conflicts is recoverable once they resolve, so the working set is inspected
// regardless and pullErr is surfaced only when nothing was resolved.
//
// It is the remote-agnostic settlement entry: the caller has no remote name,
// so attribution falls to "a merge is open on the store branch after the
// pull". The five transport call sites use runCLIPullSettled instead, which
// records pre-pull merge state and names the remote, and only then lands
// here through settleCLIPull.
func (s *DoltStore) finishCLIPull(ctx context.Context, pullErr error) error {
	if s.readOnly {
		// A read-only store cannot resolve or commit; surface the pull result as-is.
		return pullErr
	}
	return s.settleCLIPull(ctx, "", pullErr)
}

// settleCLIPull finishes a CLI pull's merge state (GH#7839): it runs the
// auto-resolver, and for a declined conflict on a merge this request owns it
// captures the conflicts, aborts the merge, verifies the restoration, and
// reports a MergeConflictsError — the CLI-route counterpart of
// settleMergeInTx's transaction rollback. remote is the pulled remote, or ""
// for remote-agnostic attribution (see cliPullOwnsMergeState).
func (s *DoltStore) settleCLIPull(ctx context.Context, remote string, pullErr error) error {
	// The store's live working branch, read from the pool before any
	// connection is pinned (see liveBranchFromPool): it is the pull's merge
	// target, and the ownership check below turns on it.
	branch := s.liveBranchFromPool(ctx)

	// Post-pull state, checked BEFORE resolving: attribution decides
	// whether this request may touch the merge at all. The resolver can
	// mutate conflicts (it stages resolved tables), so a merge this pull
	// does not own must never reach it.
	state, stateErr := s.readOpenMergeState(ctx)
	if stateErr != nil {
		// Unreadable state: no restoration can be claimed or attempted.
		// Diagnostic, never success — and the pull's own cause travels.
		return &cliPullSettlementError{
			state: "the CLI pull finished but the merge state could not be read",
			cause: errors.Join(pullErr, stateErr),
		}
	}
	if state.merging && !cliPullOwnsMergeState(state, remote, branch) {
		return &cliPullSettlementError{
			state: "the CLI pull finished but " + state.describe() + " that it cannot attribute to itself; the merge was left untouched",
			cause: pullErr,
		}
	}
	if !state.merging {
		// Conflicts or blockers without an open merge get a diagnostic —
		// never a reset, and never a silent success over state bd cannot
		// settle. The resolver's own queries double as the read here: a
		// failed detail read falls through to the resolver, which either
		// handles what it can see or reports why it could not.
		if err := s.reportUnsettledWithoutMerge(ctx, pullErr); err != nil {
			return err
		}
	}

	resolved, resolveErr := s.autoResolveConflictsAfterCLIPull(ctx)
	if errors.Is(resolveErr, ErrCommitIndeterminate) {
		// The settlement commit may or may not have landed; an abort could
		// discard it, so none is attempted. Today's error selection stands
		// (the indeterminate error stays visible when there was no pull
		// error to prefer).
		if pullErr != nil {
			return pullErr
		}
		return resolveErr
	}

	// Inspect the remaining state regardless of the resolved boolean or the
	// pull error: the resolver's repair-only arm can conclude a merge while
	// row conflicts it declined are still present, and a declined conflict
	// leaves the merge open by construction.
	post, postErr := s.readOpenMergeState(ctx)
	if postErr != nil {
		// A resolution may have committed already; claiming either success
		// or an abort without a readable state would be a guess.
		return &cliPullSettlementError{
			state: "the CLI pull's conflicts were processed but the merge state could not be read afterwards",
			cause: errors.Join(pullErr, errors.Join(resolveErr, postErr)),
		}
	}
	if post.merging {
		if !cliPullOwnsMergeState(post, remote, branch) {
			// The resolver can mutate unrelated merges; whatever is open
			// now is not provably this request's.
			return &cliPullSettlementError{
				state: "the CLI pull's auto-resolution left " + post.describe() + " that it cannot attribute to itself; the merge was left untouched",
				cause: errors.Join(pullErr, resolveErr),
			}
		}
		return s.abortDeclinedCLIPullMerge(ctx, state, branch, pullErr, resolveErr)
	}
	if err := s.reportUnsettledWithoutMerge(ctx, pullErr); err != nil {
		return err
	}

	// No merge and no blockers remain: today's resolved-aware selection.
	if resolveErr != nil {
		if pullErr != nil {
			return pullErr
		}
		return resolveErr
	}
	if pullErr != nil && !resolved {
		// Pull failed for a non-conflict reason, or conflicts are not
		// auto-resolvable; the working set is clean, so the operator can
		// retry or resolve deliberately.
		return pullErr
	}
	return nil
}

// reportUnsettledWithoutMerge returns a settlement error when conflicts or
// blockers remain with no open merge — state bd must neither reset nor call
// success over. It returns nil when everything reads clear (or is unreadable,
// which stays the caller's next step's problem, matching the diagnosis
// helper's contract of never being the thing that fails the pull).
func (s *DoltStore) reportUnsettledWithoutMerge(ctx context.Context, pullErr error) error {
	conflicts, cErr := versioncontrolops.GetConflicts(ctx, s.db)
	blockers, bErr := versioncontrolops.GetMergeBlockers(ctx, s.db)
	if cErr != nil || bErr != nil {
		return nil
	}
	if len(conflicts) == 0 && !blockers.Blocked() {
		return nil
	}
	tables := conflictTables(conflicts, blockers)
	return &cliPullSettlementError{
		state: "no merge is open but conflicts remain in " + strings.Join(tables, ", ") + "; they were left untouched",
		cause: pullErr,
	}
}

// conflictTables lists the tables named by the captured conflicts and
// blockers, for operator-facing messages.
func conflictTables(conflicts []storage.Conflict, blockers storage.MergeBlockers) []string {
	seen := make(map[string]bool)
	var tables []string
	for _, c := range conflicts {
		if c.Field != "" && !seen[c.Field] {
			seen[c.Field] = true
			tables = append(tables, c.Field)
		}
	}
	for _, t := range blockers.SchemaConflictTables {
		if !seen[t] {
			seen[t] = true
			tables = append(tables, t)
		}
	}
	for _, v := range blockers.ConstraintViolations {
		if !seen[v.Table] {
			seen[v.Table] = true
			tables = append(tables, v.Table)
		}
	}
	if tables == nil {
		tables = []string{"(unreadable tables)"}
	}
	return tables
}

// abortDeclinedCLIPullMerge restores the pre-pull state after a CLI pull's
// merge could not be settled: it captures the conflicts and blockers the
// resolver declined (before the abort wipes them, bd-578h9.15), runs
// DOLT_MERGE('--abort') on one pinned connection, and verifies the merge and
// its conflicts actually cleared. Only a confirmed restoration reports a
// MergeConflictsError carrying the captured conflicts — everything else
// (failed abort, unreadable verification, residual blockers) is a settlement
// error that preserves the pull's cause and never claims a restoration it
// could not confirm. DOLT_RESET('--hard') is never issued: a shared server's
// working set is not this request's to reset.
func (s *DoltStore) abortDeclinedCLIPullMerge(ctx context.Context, state openMergeState, branch string, pullErr, resolveErr error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return &cliPullSettlementError{
			state: state.describe() + " after a declined CLI pull conflict, and no connection could be pinned to abort it",
			cause: errors.Join(pullErr, errors.Join(resolveErr, err)),
		}
	}
	defer conn.Close()
	if err := checkoutBranchOnConn(ctx, conn, branch); err != nil {
		return &cliPullSettlementError{
			state: state.describe() + " after a declined CLI pull conflict, and the store branch could not be pinned to abort it",
			cause: errors.Join(pullErr, errors.Join(resolveErr, err)),
		}
	}

	// Capture BEFORE the abort wipes merge state: a post-abort read of
	// dolt_conflicts sees an empty set, which would turn the report into an
	// empty-conflict claim (bd-578h9.15). Ownership is already proven, so a
	// detail-read failure does not stop the abort — it only changes what
	// can be reported afterwards.
	conflicts, confErr := versioncontrolops.GetConflicts(ctx, conn)
	blockers, blockErr := versioncontrolops.GetMergeBlockers(ctx, conn)

	abortErr := schema.DrainCall(ctx, conn, "CALL DOLT_MERGE('--abort')")
	if abortErr != nil {
		return &cliPullSettlementError{
			state: "aborting " + state.describe() + " after a declined CLI pull conflict failed; the merge state was left as it was",
			cause: errors.Join(pullErr, errors.Join(resolveErr, abortErr)),
		}
	}

	// Verify the restoration: the merge must be closed and its conflicts
	// and blockers gone. An unreadable verification is an unconfirmed
	// restoration, never a success.
	postState, vErr := readMergeStateOnConn(ctx, conn)
	postConflicts, pcErr := versioncontrolops.GetConflicts(ctx, conn)
	postBlockers, pbErr := versioncontrolops.GetMergeBlockers(ctx, conn)
	if vErr != nil || pcErr != nil || pbErr != nil || postState.merging || postBlockers.Merging || len(postConflicts) > 0 || postBlockers.Blocked() {
		return &cliPullSettlementError{
			state: "the declined CLI pull conflict's merge was aborted but the restoration could not be confirmed",
			cause: errors.Join(pullErr, errors.Join(resolveErr, errors.Join(errors.Join(vErr, errors.Join(pcErr, pbErr)), fmt.Errorf("post-abort state: %s, %d conflict table(s), %d blocker table(s)", postState.describe(), len(postConflicts), len(postBlockers.SchemaConflictTables)+len(postBlockers.ConstraintViolations))))),
		}
	}

	switch {
	case confErr == nil && len(conflicts) > 0:
		// Restoration confirmed and the conflicts were captured: the
		// SQL route's contract, conflicts-as-error carrying the pull's
		// own cause for Unwrap.
		return &versioncontrolops.MergeConflictsError{Conflicts: conflicts, MergeErr: pullErr}
	case confErr == nil && blockErr == nil && blockers.Blocked():
		// Restored, but what it carried was not row conflicts — schema
		// conflicts and constraint violations dolt_conflicts does not
		// show. A MergeConflictsError would claim conflicts the caller
		// cannot list; name them instead.
		return fmt.Errorf("the CLI pull's merge was aborted; it left %s that require operator resolution: %w",
			strings.Join(conflictTables(nil, blockers), ", "), errors.Join(pullErr, resolveErr))
	case confErr != nil || blockErr != nil:
		// The abort was verified but the pre-abort detail read failed:
		// report the diagnostic, never an empty-conflict claim.
		return &cliPullSettlementError{
			state: "the declined CLI pull conflict's merge was aborted, but its conflicts could not be read before the abort",
			cause: errors.Join(pullErr, errors.Join(resolveErr, errors.Join(confErr, blockErr))),
		}
	default:
		// No captured row conflicts or blockers at all: the merge the
		// subprocess left open held nothing resolvable, so the pull's own
		// error (or a diagnostic, on a silent pull) is the honest report.
		if pullErr != nil {
			return pullErr
		}
		return &cliPullSettlementError{
			state: "the CLI pull's merge was open with no readable conflicts; it was aborted",
			cause: resolveErr,
		}
	}
}
