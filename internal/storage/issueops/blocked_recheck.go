package issueops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// BlockedRecheck names the dependents whose is_blocked a write recomputed
// inside a transaction, so the store can recompute them again on a fresh
// snapshot once that transaction has committed.
//
// The in-transaction recompute reads the transaction's start snapshot. The
// writes that record here are the ones that take a blocker away from a
// dependent: a close, an update to an inactive status, a dependency removal,
// a delete. Two such writes racing on the blockers of one dependent each see
// the other blocker still in place, each leaves the dependent blocked without
// writing its row, and both commit with no conflicting cell — so the
// dependent stays hidden from `bd ready` until a repair
// (gastownhall/beads#6716, first seen as two closes of sibling blockers). A
// recheck over the same ids after commit sees both writes and settles the
// flag. The recheck is a full recompute, so it is correct whichever way the
// flag has to move — removing the one closed child of an any-children gate
// can block its waiter, and the recheck settles that too. Writes that add a
// blocker (a reopen, a dependency add, a parent-child add) recompute the same
// way and are not recorded here; a racing pair where the last committer adds
// a blocker can leave a dependent flagged ready while blocked.
//
// # What is scoped, and what is not
//
// Recording only reaches a recheck through a transaction a store scoped (see
// ScopeBlockedRecheckTransaction), and exactly two write runners scope one:
// DoltStore's commitWriteTx and EmbeddedDoltStore's commitConn. A write that
// reaches this package on any other transaction records nothing and behaves
// exactly as it did before the recheck existed — the skew above survives on
// it. Those routes are known and deliberately out of scope here:
//
//   - the uow/domain-db routing system (internal/storage/uow, with the
//     in-transaction recomputes at internal/storage/domain/db/issue.go and
//     dependency.go), which serves `bd batch` when proxied and every write a
//     uow provider serves. Scoping it needs more than these two lines:
//     doltServerTx.Runner() hands out the pinned *sql.Conn, not the *sql.Tx
//     this package keys its scope on, so the key has to be threaded through
//     the wrapper — the caveat below, already real rather than hypothetical.
//   - storage.Transaction (internal/storage/dolt/transaction.go, reached
//     through DoltStore.RunInTransaction on a pinned connection), which is
//     how `bd batch` closes, status updates and dep removals land when not
//     proxied, and how `bd cook`'s proto-subgraph delete, `bd mol squash` and
//     `bd mol burn` delete and close.
//   - the legacy DoltStore.RemoveDependencyWithOptions (raw BeginTx in
//     internal/storage/dolt/dependencies.go), which `bd duplicates --merge`
//     uses to remove parent-child edges.
//   - the wisp writers (internal/storage/dolt/wisps.go closeWisp/updateWisp/
//     deleteWisp and ephemeral_routing.go's demote-to-wisp), which run raw
//     unscoped transactions. The recompute joins depends_on_wisp_id, so a
//     permanent issue blocked by two wisps whose removals race keeps the same
//     stale flag.
//
// Every one of these leaves the pre-existing, operator-repairable state
// (`bd doctor`, `bd recompute-blocked`); none is made worse by the recheck.
type BlockedRecheck struct {
	IssueIDs []string
	WispIDs  []string
	// Sources label the writes that recorded the dependents, one short human
	// phrase each ("close of rb-a", "dependency removal rb-c -> rb-a",
	// "delete of rb-b"); they name the Dolt commit a recheck mints. At most
	// recheckSourceLimit are kept — SourceCount counts them all.
	Sources []string
	// SourceCount is how many writes recorded ids in this transaction,
	// including the ones past recheckSourceLimit that Sources does not name.
	SourceCount int
}

// recheckSourceLimit is how many names a bounded recheck line prints in full —
// the writes a commit message names, and the dependents a failure message
// names — before the rest reach it as a count. One transaction can close,
// update or delete an unbounded number of issues (batch_closer.go closes N in
// one), so an uncapped line would grow with N — the same unbounded growth
// deleteRecheckLabel already bounds within a single delete.
const recheckSourceLimit = 3

// Empty reports whether there is nothing to recheck.
func (r BlockedRecheck) Empty() bool {
	return len(r.IssueIDs) == 0 && len(r.WispIDs) == 0
}

// CommitMessage is the Dolt commit message a recheck that changed a row mints.
//
// It always names the writes Sources kept and only counts the ones past
// recheckSourceLimit: this commit is the sole record of which write triggered a
// repair, so dropping the retained names for a bare count would discard exactly
// what they are kept for. The count is of the unnamed writes, not of all of
// them, which also keeps the line honest when Sources deduplicated two writes
// that rendered the same label (two equal-sized bulk deletes) and SourceCount
// therefore runs ahead of it.
func (r BlockedRecheck) CommitMessage() string {
	message := "bd: recheck blocked after " + strings.Join(r.Sources, ", ")
	if unnamed := r.SourceCount - len(r.Sources); unnamed > 0 {
		message += fmt.Sprintf(" and %d more", unnamed)
	}
	return message
}

// blockedRecheckTransactions holds the open scopes, one per transaction. It is
// keyed by the DBTX interface value: a write records into a scope only when
// the very same *sql.Tx the store scoped reaches noteBlockedRecheck. A future
// DBTX wrapper around that tx would compare unequal and silently record
// nothing — the same shape, and the same caveat, as the events-journal scope
// in journal.go.
var blockedRecheckTransactions sync.Map // map[DBTX]*BlockedRecheck; entries live for one transaction

// ErrBlockedRecheckFailed marks a failure of the post-commit recheck of
// dependents' blocked state. The write that preceded it is committed and
// durable; only the recheck failed, so at worst a dependent carries the stale
// is_blocked flag that `bd doctor` and `bd recompute-blocked` already repair.
//
// A store never returns it to the caller of the write. An error out of a
// store call means the write did not land, and every caller reads it that
// way: surfacing a post-commit failure would make automated callers retry an
// applied mutation and double-apply it (the contract at
// internal/storage/dolt/issue_operations_tx.go). Stores wrap the failure with
// this sentinel and log it instead, which is what makes the two
// distinguishable in a log line and keeps the cause reachable for anything
// that inspects one.
var ErrBlockedRecheckFailed = errors.New("blocked-state recheck after a committed write failed")

// BlockedRecheckFailed wraps a recheck failure so both ErrBlockedRecheckFailed
// and the underlying cause stay reachable through errors.Is and errors.As.
func BlockedRecheckFailed(err error) error {
	return fmt.Errorf("%w: %w", ErrBlockedRecheckFailed, err)
}

// BlockedRecheckFailureMessage is what a store says about a failed recheck.
// Both stores log the same sentence (each through its own package's sink) so
// this text lives next to the sentinel it reports rather than twice in two
// store packages.
//
// It names the rows left unrechecked and the command that repairs them because
// the failure reaches nobody else: the write committed, its caller was handed
// nil, and the only other signal is the counter beside this line. An operator
// who cannot tell which dependents went stale cannot act on it, and the whole
// defect the recheck repairs is invisible staleness.
func BlockedRecheckFailureMessage(pending BlockedRecheck, err error) string {
	return fmt.Sprintf("%v; left unrechecked and possibly stale: %s — repair with `bd recompute-blocked`",
		err, pending.unrecheckedNames())
}

// unrecheckedNames names the recorded dependents, bounded by
// recheckSourceLimit: one transaction can record an unbounded number of them.
func (r BlockedRecheck) unrecheckedNames() string {
	ids := make([]string, 0, len(r.IssueIDs)+len(r.WispIDs))
	ids = append(ids, r.IssueIDs...)
	ids = append(ids, r.WispIDs...)
	if len(ids) <= recheckSourceLimit {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ids[:recheckSourceLimit], ", "), len(ids)-recheckSourceLimit)
}

type blockedRecheckContextKey struct{}

// BlockedRecheckTimeout bounds one post-commit recheck. It sits above the
// stores' own write-retry budget so the retry policy, not this ceiling, is
// what normally ends a struggling recheck; it exists so a wedged connection
// cannot hold the caller of an already-committed write indefinitely.
const BlockedRecheckTimeout = 30 * time.Second

// BlockedRecheckContext returns the context a post-commit recheck runs on.
//
// It is detached from the caller's cancellation: the write being repaired has
// already committed, so a context canceled between the commit and the recheck
// (a CLI deadline, a disconnected HTTP client, shutdown) must not skip the
// repair and leave exactly the stale flag the recheck exists to settle. It
// carries its own deadline instead, and it is marked so that InBlockedRecheck
// reports true inside it.
func BlockedRecheckContext(ctx context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithValue(context.WithoutCancel(ctx), blockedRecheckContextKey{}, struct{}{})
	return context.WithTimeout(detached, BlockedRecheckTimeout)
}

// InBlockedRecheck reports whether ctx is already running a post-commit
// recheck, so a store can refuse to start another one from inside it.
//
// A recheck runs on the store's ordinary write-transaction runner, which
// scopes its transaction and rechecks whatever that transaction recorded. It
// terminates today only because the recompute the recheck runs
// (RecomputeIsBlockedInTxWithResult) records nothing — an invariant no
// signature enforces. A change that made the recompute record would otherwise
// recurse once per level until the context died. This is the guard rail:
// one write, at most one recheck.
func InBlockedRecheck(ctx context.Context) bool {
	_, ok := ctx.Value(blockedRecheckContextKey{}).(struct{})
	return ok
}

// ScopeBlockedRecheckTransaction lets the unblocking writes in tx record the
// dependents they recomputed, for TakeBlockedRecheck once tx has committed.
// Store implementations call it right after BeginTx and run the returned
// cleanup when the transaction ends. An unscoped transaction records nothing.
func ScopeBlockedRecheckTransaction(tx DBTX) func() {
	if tx == nil {
		return func() {}
	}
	blockedRecheckTransactions.Store(tx, &BlockedRecheck{})
	return func() { blockedRecheckTransactions.Delete(tx) }
}

// TakeBlockedRecheck returns the dependents recorded in tx and clears them.
func TakeBlockedRecheck(tx DBTX) BlockedRecheck {
	scope, ok := blockedRecheckTransactions.Load(tx)
	if !ok {
		return BlockedRecheck{}
	}
	pending := scope.(*BlockedRecheck)
	taken := *pending
	*pending = BlockedRecheck{}
	return taken
}

// noteBlockedRecheck records the dependents a write recomputed in tx. source
// labels the write for the recheck's commit message. exclude names ids this
// call leaves out: for a close or status update, the issue whose row this
// transaction wrote (a concurrent writer conflicts on that row instead of
// racing past it); for a delete, the rows that no longer exist. A dependency
// removal excludes nothing — its dependent is exactly the row that needs
// rechecking. exclude filters this call's ids only: an id an earlier write in
// the same transaction recorded stays pending, and rechecking a row that a
// later write deleted is a no-op, so that is harmless.
//
// A call that contributes no new id contributes no source either: the recheck
// it would name has nothing of its own to recheck, and naming it would put
// writes in the commit message that the recheck never touched.
func noteBlockedRecheck(tx DBTX, source string, exclude []string, issueIDs, wispIDs []string) {
	scope, ok := blockedRecheckTransactions.Load(tx)
	if !ok {
		return
	}
	pending := scope.(*BlockedRecheck)
	issues, addedIssues := appendRecheckIDs(pending.IssueIDs, issueIDs, exclude)
	wisps, addedWisps := appendRecheckIDs(pending.WispIDs, wispIDs, exclude)
	pending.IssueIDs, pending.WispIDs = issues, wisps
	if !addedIssues && !addedWisps {
		return
	}
	pending.SourceCount++
	if len(pending.Sources) < recheckSourceLimit {
		pending.Sources, _ = appendRecheckIDs(pending.Sources, []string{source}, nil)
	}
}

// appendRecheckIDs adds the ids of one write that are neither excluded nor
// already pending, and reports whether it added any.
func appendRecheckIDs(pending, ids, exclude []string) ([]string, bool) {
	seen := make(map[string]bool, len(pending)+len(exclude))
	for _, id := range pending {
		seen[id] = true
	}
	for _, id := range exclude {
		seen[id] = true
	}
	added := false
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			pending = append(pending, id)
			added = true
		}
	}
	return pending, added
}

// deleteRecheckLabel is the Sources label for a delete of ids. Up to three
// ids are named; beyond that only the count, so a bulk delete cannot grow a
// commit message without bound. scope, when set, says where the ids came
// from ("from <sourceRepo>").
func deleteRecheckLabel(ids []string, scope string) string {
	var label string
	if len(ids) <= 3 {
		label = "delete of " + strings.Join(ids, ", ")
	} else {
		label = fmt.Sprintf("delete of %d issues", len(ids))
	}
	if scope != "" {
		label += " " + scope
	}
	return label
}

// statusChangeRecheckLabel is the Sources label for a status change of id to
// the inactive newStatus. A close through update reads the same as a close
// through CloseIssue, so the two paths mint the same commit message.
func statusChangeRecheckLabel(id, newStatus string) string {
	if newStatus == string(types.StatusClosed) {
		return "close of " + id
	}
	return fmt.Sprintf("status change of %s to %s", id, newStatus)
}
