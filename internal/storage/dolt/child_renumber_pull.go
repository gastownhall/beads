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
// (bd-28sb25). Unlike Pull it never commits pending changes first: the
// rename commit must hold the renames alone, so a dirty working set is
// refused and left as it was.
//
// The fetch takes the route Pull's transport would take for the remote
// (fetchRemote). The plan, the rename transaction, its commit and the merge
// then run on one pooled session checked out to the store's branch, and the
// rename transaction carries the store's journal scoping, so the renames
// journal as `bd rename` does. The merge targets the remote head the plan
// was made against, so a fetch landing in between cannot slip unplanned
// commits into it.
func (s *DoltStore) PullRenumberChildren(ctx context.Context, remote, actor string) (report *storage.ChildRenumberReport, retErr error) {
	if remote == "" {
		remote = s.remote
	}
	if s.readOnly {
		return nil, fmt.Errorf("renumber children: the store is read-only")
	}
	err := s.withCircuitWrite(ctx, func(ctx context.Context) error {
		preHead := ""
		if h, err := s.branchHash(ctx, s.branch); err == nil {
			preHead = h
		}
		if err := s.fetchRemote(ctx, remote); err != nil {
			return err
		}
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("acquire connection for pull: %w", err)
		}
		// A pooled session can sit on the database's default branch
		// (be-b0am); the renames and the merge must land on the store's.
		runErr := checkoutBranchIfNeeded(ctx, conn, s.branch)
		if runErr == nil {
			report, runErr = versioncontrolops.RenumberChildrenAndMerge(ctx, conn, s.branch, remote+"/"+s.branch, actor, s.scopeEventsJournalTransaction)
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

// fetchRemote fetches remote into this database's remote-tracking refs, and
// merges nothing, over the route pullTransportReporting would pull it: the
// dolt CLI for git-protocol, credentialed and cloud-auth remotes, and
// otherwise a dedicated long-timeout connection, as refreshTrackingRef
// fetches. A pooled connection serves neither: its 10s read timeout cuts a
// slow transfer short, and a sql-server never sees the credentials bd holds
// in its own environment, which the CLI routes hand to the dolt they run.
// Either route only moves remote-tracking refs, which every session of the
// database sees, so the session that plans and merges reads what was
// fetched. (Fetch is not used: it takes a federation peer's credentials, not
// the default remote's, and has no cloud-auth route.)
func (s *DoltStore) fetchRemote(ctx context.Context, remote string) error {
	creds := s.credentialsForRemote(remote)
	cliRoutes := []func() (bool, error){
		func() (bool, error) { return s.prepareCLIRouteForGitProtocol(ctx, remote) },
		func() (bool, error) { return s.prepareCLIRouteForCredentials(ctx, remote, creds) },
		func() (bool, error) { return s.prepareCLIRouteForCloudAuth(ctx, remote) },
	}
	for _, useCLI := range cliRoutes {
		cli, err := useCLI()
		if err != nil {
			return err
		}
		if cli {
			logRouteDecision("fetch", remote, true)
			return s.doltCLIFetchFromPeer(ctx, remote, creds)
		}
	}
	logRouteDecision("fetch", remote, false)
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return err
	}
	defer db.Close()
	user := ""
	if remote == s.remote {
		user = s.remoteUser
	}
	return withRemoteOperationEnv(creds, s.isS3Remote(ctx, remote), func() error {
		return versioncontrolops.Fetch(ctx, db, remote, user)
	})
}
