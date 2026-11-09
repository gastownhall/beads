package dolt

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// The offline child-id collision repair (GH#4796, bd-28sb25) on the server
// store: the read-only planner and the authorized --renumber-children pull.
// Both funnel into the versioncontrolops core, same as the embedded store;
// only the transport differs — the fetch goes through refreshTrackingRef (the
// SQL route's DOLT_FETCH), so the revision the merge targets is the one the
// plan was computed against.

// PlanChildRenumbers implements storage.ChildRenumberPlanner: the read-only
// repair proposal for a pull from remote ("" is the store's default remote).
// Planning reads only dolt_branches and revision reads, so it needs no pinned
// session and never mutates anything.
func (s *DoltStore) PlanChildRenumbers(ctx context.Context, remote string) (*storage.ChildRenumberPlan, error) {
	if remote == "" {
		remote = s.remote
	}
	return versioncontrolops.PlanChildRenumbers(ctx, s.db, s.branch, remote)
}

// PullWithChildRenumbers implements storage.ChildRenumberPuller for
// `bd dolt pull --renumber-children`: fetch from remote, recompute the repair
// plan against the fetched tracking ref, rename the colliding local children
// in one transaction, commit the rename, and merge the pinned revision
// through normal settlement. See versioncontrolops.PullWithChildRenumbers.
func (s *DoltStore) PullWithChildRenumbers(ctx context.Context, remote string) (*storage.ChildRenumberResult, error) {
	if remote == "" {
		remote = s.remote
	}
	if s.readOnly {
		return nil, errors.New("dolt: store is read-only; child renumbering requires a writable store")
	}
	// Same contract as pullFromRemote: auto-commit pending changes so the
	// merge cannot refuse on a dirty working set.
	if err := s.commitBeforePull(ctx, "auto-commit before pull"); err != nil && !isDoltNothingToCommit(err) {
		return nil, fmt.Errorf("failed to commit pending changes before pull: %w", err)
	}
	preHead, _ := s.branchHash(ctx, s.branch)

	// The repair merges the revision the fetch pinned, so it must take the
	// SQL fetch route. CLI-routed remotes (git protocol, credentials, cloud
	// auth) are refused up front: their fetch goes through a subprocess this
	// store cannot pin, and the design supports one authoritative remote.
	creds := s.credentialsForRemote(remote)
	for _, route := range []func() (bool, error){
		func() (bool, error) { return s.prepareCLIRouteForGitProtocol(ctx, remote) },
		func() (bool, error) { return s.prepareCLIRouteForCredentials(ctx, remote, creds) },
		func() (bool, error) { return s.prepareCLIRouteForCloudAuth(ctx, remote) },
	} {
		useCLI, err := route()
		if err != nil {
			return nil, err
		}
		if useCLI {
			return nil, fmt.Errorf("renumber-children is not supported for remote %q: its fetch routes through the dolt CLI; pull from it once without the flag, or use an embedded workspace", remote)
		}
	}

	// Fetch once: refreshTrackingRef leaves remotes/<remote>/<branch> at the
	// revision the plan and the merge both target.
	if err := s.refreshTrackingRef(ctx, remote); err != nil {
		return nil, fmt.Errorf("fetch from %s/%s: %w", remote, s.branch, err)
	}

	// Pin one long-timeout session for the plan, the rename transaction, and
	// the merge: the settle machinery's session flags and the head
	// revalidation must describe one connection (versioncontrolops.TxSession).
	db, err := s.openLongTimeoutConn()
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for renumber pull: %w", err)
	}
	defer func() { _ = conn.Close() }()

	result, err := versioncontrolops.PullWithChildRenumbersFromFetched(ctx, conn, remote, s.branch, s.commitAuthorString())
	if err != nil {
		// A merge that failed after the renames still carries the partial
		// result: the rename commit is retained in history.
		return result, err
	}
	if rerr := s.recomputeBlockedAfterPull(ctx, preHead); rerr != nil {
		return result, fmt.Errorf("pull succeeded but is_blocked recompute failed: %w", rerr)
	}
	return result, nil
}
