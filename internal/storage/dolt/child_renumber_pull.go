package dolt

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

var _ storage.ChildRenumberingPuller = (*DoltStore)(nil)

// PullRenumberChildren implements storage.ChildRenumberingPuller: fetch,
// rename the local colliding children, commit, then merge the fetched head
// (bd-28sb25). Pending changes are committed first, as Pull does. The fetch
// runs over SQL on the pinned connection for every remote kind; the CLI
// routes Pull takes for git-protocol remotes merge as they fetch, which this
// command must not do.
func (s *DoltStore) PullRenumberChildren(ctx context.Context, remote, actor string) (report *storage.ChildRenumberReport, retErr error) {
	if remote == "" {
		remote = s.remote
	}
	if s.readOnly {
		return nil, fmt.Errorf("renumber children: the store is read-only")
	}
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		if err := s.commitBeforePull(ctx, "auto-commit before pull"); err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("failed to commit pending changes before pull: %w", err)
		}
		preHead := ""
		if h, err := s.branchHash(ctx, s.branch); err == nil {
			preHead = h
		}
		user := ""
		if remote == s.remote {
			user = s.remoteUser
		}
		// Resolve the remote's credentials before pinning a connection: they
		// read through the pool, which may hold only the one this pins.
		creds, isS3 := s.credentialsForRemote(remote), s.isS3Remote(ctx, remote)
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("acquire connection for pull: %w", err)
		}
		runErr := withRemoteOperationEnv(creds, isS3, func() error {
			return versioncontrolops.Fetch(ctx, conn, remote, user)
		})
		if runErr == nil {
			report, runErr = versioncontrolops.RenumberChildrenAndMerge(ctx, conn, s.branch, remote+"/"+s.branch, actor)
		}
		// Release the pinned connection before the recompute, which takes its
		// own (see MergeWithStrategy).
		closeErr := conn.Close()
		if runErr != nil {
			return runErr
		}
		if closeErr != nil {
			return fmt.Errorf("release pull connection: %w", closeErr)
		}
		if err := s.recomputeBlockedAfterPull(ctx, preHead); err != nil {
			return fmt.Errorf("pull succeeded but is_blocked recompute failed: %w", err)
		}
		return nil
	})
	if report != nil {
		report.Remote = remote
	}
	return report, err
}
