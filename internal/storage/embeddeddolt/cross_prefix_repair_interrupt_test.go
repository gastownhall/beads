//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedCrossPrefixRepairInterruptedPassConverges is the interruption
// half of AC-6: a pass that dies after moving rows but before recomputing
// blocked state leaves the marker pending, so the next open refuses on the
// moved, uncommitted rows instead of presenting the store as repaired; once
// they are committed, the retry recomputes the edges the first pass already
// moved and converges.
func TestEmbeddedCrossPrefixRepairInterruptedPassConverges(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	conn, dataDir, closeConn := newCrossPrefixRepairDB(t, ctx)
	seedRefusalFixture(t, ctx, conn)

	errInterrupted := errors.New("interrupted before recompute")
	restore := schema.SetDepLocalTargetRepairFaultHookForTest(func(context.Context, schema.DBConn) error {
		return errInterrupted
	})
	_, err := schema.MigrateUp(ctx, conn)
	restore()
	if !errors.Is(err, errInterrupted) {
		closeConn()
		t.Fatalf("interrupted MigrateUp = %v, want %v", err, errInterrupted)
	}
	requireDepColumn(t, ctx, conn, "dependencies", "ra-blocked", "rb-open", "depends_on_issue_id")
	if blocked, _ := readBlocked(t, ctx, conn, "issues", "ra-blocked"); blocked {
		t.Error("interrupted pass recomputed ra-blocked; the fault fired too late to test anything")
	}
	if ignoredMarkerRecorded(t, ctx, conn, 32) {
		t.Error("interrupted pass recorded ignored marker 0032")
	}
	closeConn()

	conn, closeConn = openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	_, err = schema.MigrateUp(ctx, conn)
	var dirtyErr *schema.DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("retry over the interrupted pass's uncommitted rows = %v, want a DirtyTablesError", err)
	}
	if ignoredMarkerRecorded(t, ctx, conn, 32) {
		t.Error("refused retry recorded ignored marker 0032")
	}

	execFrozenGuard(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'commit the interrupted repair')")
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("retry after commit: %v", err)
	}
	requireRepairConverged(t, ctx, conn)
}
