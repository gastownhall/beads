//go:build cgo

package embeddeddolt

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// The CLI reaches the renumber capability through storage.UnwrapStore, so
// the assertions must keep holding on the concrete store.
var (
	_ storage.ChildRenumberPlanner = (*EmbeddedDoltStore)(nil)
	_ storage.ChildRenumberPuller  = (*EmbeddedDoltStore)(nil)
)

// PlanChildRenumbers implements storage.ChildRenumberPlanner (GH#4796,
// bd-28sb25): the read-only repair proposal for a pull from remote ("" is
// the store's default remote, "origin"). Planning reads only the local
// branch and the current remote-tracking ref, so a plain pull that already
// fetched — and then failed — can call it for the hint without a second
// fetch and without any writes.
func (s *EmbeddedDoltStore) PlanChildRenumbers(ctx context.Context, remote string) (*storage.ChildRenumberPlan, error) {
	if remote == "" {
		remote = defaultRemote
	}
	var plan *storage.ChildRenumberPlan
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		plan, err = versioncontrolops.PlanChildRenumbers(ctx, db, s.branch, remote)
		return err
	})
	return plan, err
}

// PullWithChildRenumbers implements storage.ChildRenumberPuller for
// `bd dolt pull --renumber-children`: fetch from remote, recompute the repair
// plan against the fetched tracking ref, rename the colliding local children
// in one transaction, commit the rename, and merge the pinned revision through
// normal settlement. See versioncontrolops.PullWithChildRenumbers.
//
// Like Pull: pending changes are auto-committed first (GH#2474), the fetch
// runs under the peer's credentials, the whole repair runs on one pinned
// session, and a successful merge recomputes is_blocked for the merged rows.
func (s *EmbeddedDoltStore) PullWithChildRenumbers(ctx context.Context, remote string) (*storage.ChildRenumberResult, error) {
	if remote == "" {
		remote = defaultRemote
	}
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		return nil, fmt.Errorf("commit pending before pull: %w", err)
	}
	preHead := s.preMergeHead(ctx)
	// The rename transaction runs on the pinned session rather than through
	// commitConn, so the journal scope it would set there travels in the ctx
	// instead (issueops.WithEventsJournal is the same lookup precedence).
	ctx = issueops.WithEventsJournal(ctx, s.eventsJournalEnabled.Load())
	var result *storage.ChildRenumberResult
	err := s.withPeerAuth(ctx, remote, func(user string) error {
		return s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
			var pullErr error
			result, pullErr = versioncontrolops.PullWithChildRenumbers(ctx, db, remote, s.branch, user, commitAuthor)
			return pullErr
		})
	})
	if err != nil {
		// A merge that failed after the renames still carries the partial
		// result: the rename commit is retained in history.
		return result, err
	}
	if err := s.recomputeBlockedAfterPull(ctx, preHead); err != nil {
		return result, fmt.Errorf("pull succeeded but is_blocked recompute failed: %w", err)
	}
	return result, nil
}
