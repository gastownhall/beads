package dolt

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBenchDBPurgeDoesNotLeak is the regression gate for be-pq5: dropBenchDB
// must DROP and then PURGE so the dropped-databases dir does not grow across
// repeated bench samples. Without the PURGE call inside dropBenchDB, looped
// setupBenchStore + cleanup leaks a benchdb_* dir into
// .dolt_dropped_databases/ on every iteration.
//
// Dolt 1.86 exposes no SQL view for the dropped-databases list, so the only
// way to detect a leak is to count entries in the server's
// .dolt_dropped_databases/ directory. This requires knowing the server's
// data dir, which only the BEADS_TEST_EXTERNAL_DOLT_PORT branch can supply
// (the testcontainer's data dir is opaque from the test's perspective). The
// developer points BEADS_TEST_EXTERNAL_DOLT_DATA_DIR at the same dir as the
// scratch dolt sql-server's --data-dir flag; the test reads from there.
//
// In all other modes the test self-skips. Run it manually after any change
// to dropBenchDB:
//
//	SCRATCH=$(mktemp -d)
//	dolt sql-server --port 33999 --host 127.0.0.1 --data-dir "$SCRATCH" &
//	BEADS_TEST_EXTERNAL_DOLT_PORT=33999 \
//	BEADS_TEST_EXTERNAL_DOLT_DATA_DIR="$SCRATCH" \
//	  go test -run TestBenchDBPurgeDoesNotLeak ./internal/storage/dolt/...
func TestBenchDBPurgeDoesNotLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping disk-leak regression in -short mode")
	}
	skipIfNoServer(t)

	dataDir := os.Getenv("BEADS_TEST_EXTERNAL_DOLT_DATA_DIR")
	if dataDir == "" {
		t.Skip("BEADS_TEST_EXTERNAL_DOLT_DATA_DIR not set; cannot inspect dropped-databases dir")
	}

	droppedDir := filepath.Join(dataDir, ".dolt_dropped_databases")
	baseline := countDroppedEntries(t, droppedDir)

	const iterations = 5
	for i := 0; i < iterations; i++ {
		_, cleanup := setupBenchStore(t)
		cleanup()
	}

	post := countDroppedEntries(t, droppedDir)
	if post > baseline {
		t.Fatalf("dolt_dropped_databases grew from %d to %d across %d setup/cleanup cycles; "+
			"dropBenchDB likely missing PURGE step (be-pq5)",
			baseline, post, iterations)
	}
}

// countDroppedEntries returns the number of entries in
// .dolt_dropped_databases/, or 0 if the directory does not yet exist (the
// server only creates it lazily after the first DROP DATABASE).
func countDroppedEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read dropped-databases dir %q: %v", dir, err)
	}
	return len(entries)
}
