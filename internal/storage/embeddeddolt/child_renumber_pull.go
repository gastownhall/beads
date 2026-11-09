package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

var _ storage.ChildRenumberingPuller = (*EmbeddedDoltStore)(nil)

// PullRenumberChildren implements storage.ChildRenumberingPuller: fetch,
// rename the local colliding children, commit, then merge the fetched head
// (bd-28sb25). Pending changes are committed first, as Pull does.
func (s *EmbeddedDoltStore) PullRenumberChildren(ctx context.Context, remote, actor string) (*storage.ChildRenumberReport, error) {
	if remote == "" {
		remote = defaultRemote
	}
	if _, err := s.CommitPending(ctx, "beads"); err != nil {
		return nil, fmt.Errorf("commit pending before pull: %w", err)
	}
	preHead := s.preMergeHead(ctx)
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
