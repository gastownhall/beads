//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// CycleDetector returns the guarded cycle-report surface for this store.
func (s *EmbeddedDoltStore) CycleDetector() (issueops.CycleDetector, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "CycleDetector", Backend: "nil"}
	}
	return &cycleDetector{store: s}, nil
}

// cycleDetector answers the cycle report from one connection's transaction.
//
// It is a sibling of the server-backed store's body rather than a shared
// package for the reason that one gives: the work needs a TRANSACTION, which
// storage.DoltStorage does not publish, so the sharing happens below both of
// them at issueops.DetectCycleReportInTx. The two stores differ here only in how
// they reach a transaction, which is the same thing their legacy DetectCycles
// differ by.
type cycleDetector struct{ store *EmbeddedDoltStore }

var _ issueops.CycleDetector = (*cycleDetector)(nil)

func (c *cycleDetector) DetectCycles(ctx context.Context, _ issueops.DetectCyclesRequest) (issueops.CycleReport, error) {
	var report issueops.CycleReport
	err := c.store.withConn(ctx, false, func(tx *sql.Tx) error {
		var err error
		report, err = storeops.DetectCycleReportInTx(ctx, tx)
		return err
	})
	if err != nil {
		return issueops.CycleReport{}, err
	}
	return report, nil
}
