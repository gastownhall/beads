package embeddeddolt

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

var _ storage.ChildRenumberPlanner = (*EmbeddedDoltStore)(nil)

// PlanChildRenumbers implements storage.ChildRenumberPlanner against the
// remote-tracking ref the last fetch left (bd-28sb25).
func (s *EmbeddedDoltStore) PlanChildRenumbers(ctx context.Context, remote string) (*storage.ChildRenumberReport, error) {
	if remote == "" {
		remote = defaultRemote
	}
	var report *storage.ChildRenumberReport
	err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		var err error
		report, err = versioncontrolops.PlanChildRenumbers(ctx, db, s.branch, remote+"/"+s.branch)
		return err
	})
	if report != nil {
		report.Remote = remote
	}
	return report, err
}
