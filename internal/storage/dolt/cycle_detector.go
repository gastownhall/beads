package dolt

import (
	"context"
	"database/sql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/issueops"
)

// CycleDetector returns the guarded cycle-report surface for this store.
func (s *DoltStore) CycleDetector() (issueops.CycleDetector, error) {
	if s == nil {
		return nil, &storage.ErrUnsupported{Op: "CycleDetector", Backend: "nil"}
	}
	return &cycleDetector{store: s}, nil
}

// cycleDetector answers the cycle report from one read transaction.
//
// There is no shared constructor package for this role: the work is a graph
// read plus a hydration, both of which need a TRANSACTION — the two planes must
// be read as one snapshot — and a transaction is not reachable through
// storage.DoltStorage. The sharing happens one level down instead: this body
// and the embedded store's are five lines each around
// issueops.DetectCycleReportInTx. Two wrappers over one body is still ONE vote,
// and the conformance contract says so.
type cycleDetector struct{ store *DoltStore }

var _ issueops.CycleDetector = (*cycleDetector)(nil)

func (c *cycleDetector) DetectCycles(ctx context.Context, _ issueops.DetectCyclesRequest) (issueops.CycleReport, error) {
	var report issueops.CycleReport
	err := c.store.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		report, err = storeops.DetectCycleReportInTx(ctx, tx)
		return err
	})
	if err != nil {
		return issueops.CycleReport{}, err
	}
	return report, nil
}
