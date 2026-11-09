package dolt

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// The offline child-id collision repair (GH#4796, bd-28sb25) on the server
// store: the read-only planner a plain pull's conflict failure reports as a
// hint. It funnels into the versioncontrolops core, same as the embedded
// store; planning reads only dolt_branches/dolt_remote_branches and revision
// reads, so it needs no pinned session and never mutates anything.

// PlanChildRenumbers implements storage.ChildRenumberPlanner: the read-only
// repair proposal for a pull from remote ("" is the store's default remote).
func (s *DoltStore) PlanChildRenumbers(ctx context.Context, remote string) (*storage.ChildRenumberPlan, error) {
	if remote == "" {
		remote = s.remote
	}
	return versioncontrolops.PlanChildRenumbers(ctx, s.db, s.branch, remote)
}
