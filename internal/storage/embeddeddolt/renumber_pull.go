//go:build cgo

package embeddeddolt

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// The CLI reaches the renumber capability through storage.UnwrapStore, so
// the assertion must keep holding on the concrete store.
var _ storage.ChildRenumberPlanner = (*EmbeddedDoltStore)(nil)

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
