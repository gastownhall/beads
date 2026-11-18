package dolt

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

var _ storage.ChildRenumberPlanner = (*DoltStore)(nil)

// PlanChildRenumbers implements storage.ChildRenumberPlanner against the
// remote-tracking ref the last fetch or pull left (bd-28sb25).
func (s *DoltStore) PlanChildRenumbers(ctx context.Context, remote string) (*storage.ChildRenumberReport, error) {
	if remote == "" {
		remote = s.remote
	}
	report, err := versioncontrolops.PlanChildRenumbers(ctx, s.db, s.branch, remote+"/"+s.branch)
	if report != nil {
		report.Remote = remote
	}
	return report, err
}
