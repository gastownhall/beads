package createbatchequiv

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The retired scenario: an import-shaped batch naming ids a retaining
// retention sweep retired (issueops.SweepRequest.RetainIDs). The fast path
// answers "retired and not live" from the createBatchCache's up-front
// tombstone read and keeps those rows out of the multi-row INSERT; the per-row
// path answers it with one occupancy probe per row. Both must skip and report
// the same rows, write nothing of them, drop the edge into one, and still
// upsert the id that is BOTH live and tombstoned (a live row wins).

func seedRetired() []*types.Issue {
	return []*types.Issue{issue("rl", "live and tombstoned"), issue("rk", "kept")}
}

// plantTombstones retires eq-r1 (durable plane) and eq-rw (wisps plane), and
// writes a tombstone for the LIVE eq-rl too — the shape a merge leaves when it
// brings a live row onto a retired id.
func plantTombstones(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		"INSERT INTO id_tombstones (id, closed_at) VALUES ('eq-r1', '2026-01-01 00:00:00'), ('eq-rl', '2026-01-01 00:00:00')",
		"INSERT INTO wisp_id_tombstones (id, closed_at) VALUES ('eq-rw', '2026-01-01 00:00:00')",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("plant tombstones: %v", err)
		}
	}
}

func batchRetired() []*types.Issue {
	r1 := issue("r1", "retired durable", "gone")
	r1.Dependencies = []*types.Dependency{dep(id("r1"), id("rk"), types.DepBlocks)}
	rw := issue("rw", "retired wisp")
	rw.Ephemeral = true
	rl := issue("rl", "live and tombstoned, again", "kept")
	fresh := issue("rf", "fresh", "f")
	fresh.Dependencies = []*types.Dependency{dep(id("rf"), id("r1"), types.DepBlocks), dep(id("rf"), id("rk"), types.DepRelated)}
	fresh2 := issue("rf2", "fresh two")
	return []*types.Issue{r1, rw, rl, fresh, fresh2}
}
