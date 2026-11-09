package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

var _ storage.ChildRenumberingPuller = (*EmbeddedDoltStore)(nil)

// PullRenumberChildren implements storage.ChildRenumberingPuller: fetch,
// rename the local colliding children, commit, then merge the fetched head
// (bd-28sb25). Unlike Pull it never commits pending changes first: the
// rename commit must hold the renames alone, so a dirty working set is
// refused and left as it was.
func (s *EmbeddedDoltStore) PullRenumberChildren(ctx context.Context, remote, actor string) (*storage.ChildRenumberReport, error) {
	if remote == "" {
		remote = defaultRemote
	}
	preHead := s.preMergeHead(ctx)
	// The rename transaction runs on the pinned connection rather than
	// through withConn, so the journal scope withConn would set travels in
	// the context instead.
	ctx = issueops.WithEventsJournal(ctx, s.eventsJournalEnabled.Load())
	var report *storage.ChildRenumberReport
	err := s.withPeerAuth(ctx, remote, func(user string) error {
		return s.withMutatingPinnedDBConn(ctx, func(db versioncontrolops.DBConn) error {
			conn, ok := db.(*sql.Conn)
			if !ok {
				return fmt.Errorf("renumber children: need a pinned connection, got %T", db)
			}
			if err := versioncontrolops.Fetch(ctx, conn, remote, user); err != nil {
				return err
			}
			var err error
			report, err = versioncontrolops.RenumberChildrenAndMerge(ctx, conn, s.branch, remote+"/"+s.branch, actor)
			return err
		})
	})
	if report != nil {
		report.Remote = remote
	}
	if err != nil {
		return report, err
	}
	return report, s.recomputeBlockedAfterPull(ctx, preHead)
}
