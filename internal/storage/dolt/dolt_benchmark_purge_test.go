package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
)

// TestBenchDBPurgeDoesNotLeak is the regression gate for be-pq5: dropBenchDB
// must DROP and then PURGE so the dropped-databases dir does not grow across
// repeated bench samples. Without the PURGE call inside dropBenchDB, looped
// store setup + cleanup leaks a benchdb_* dir into .dolt_dropped_databases/
// on every iteration.
//
// Dolt 1.86 exposes no SQL view for the dropped-databases list, so the only
// way to detect a leak is to count entries in the server's
// .dolt_dropped_databases/ directory. The shared TestMain Dolt server runs
// inside a Docker testcontainer with no host-visible data dir, so this reads
// the directory by exec'ing into the container itself via
// testutil.DoltContainerExec. It runs against the same shared container as
// every other test in this package (via testServerPort) and needs no
// external server, manual setup, or -short opt-out (be-eh6 round 2).
func TestBenchDBPurgeDoesNotLeak(t *testing.T) {
	skipIfNoServer(t)
	ctx := context.Background()

	baseline := countDroppedDatabaseEntries(t, ctx)

	const iterations = 5
	for i := 0; i < iterations; i++ {
		dbName := benchDatabaseName()
		store := newPurgeRegressionStore(t, ctx, dbName)
		dropBenchDB(t, store, dbName)
		store.Close()
	}

	post := countDroppedDatabaseEntries(t, ctx)
	if post > baseline {
		t.Fatalf("dolt_dropped_databases grew from %d to %d across %d setup/cleanup cycles; "+
			"dropBenchDB likely missing PURGE step (be-pq5)",
			baseline, post, iterations)
	}
}

// newPurgeRegressionStore creates a throwaway store against the shared
// TestMain-managed Dolt container, mirroring setupBenchStore's schema-init
// shape without setupBenchStore's BEADS_BENCH_DOLT_PORT opt-in — that opt-in
// firewalls real `go test -bench` runs from ambient production Dolt ports
// (be-cfm3z) and is never set in CI, so a regression test that must actually
// run under plain `go test` cannot depend on it.
func newPurgeRegressionStore(t *testing.T, ctx context.Context, dbName string) *DoltStore {
	t.Helper()
	cfg := &Config{
		Path:            t.TempDir(),
		CommitterName:   "bench",
		CommitterEmail:  "bench@example.com",
		Database:        dbName,
		ServerHost:      "127.0.0.1",
		ServerPort:      testServerPort,
		CreateIfMissing: true,
	}
	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create purge-regression store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "bench"); err != nil {
		store.Close()
		t.Fatalf("failed to set issue_prefix: %v", err)
	}
	return store
}

// countDroppedDatabaseEntries returns the number of entries in the shared
// test container's .dolt_dropped_databases/ directory, or 0 if the
// directory does not exist yet (the server only creates it lazily after the
// first DROP DATABASE) or PURGE has removed it entirely.
func countDroppedDatabaseEntries(t *testing.T, ctx context.Context) int {
	t.Helper()

	dir := findDroppedDatabasesDir(t, ctx)
	if dir == "" {
		return 0
	}

	code, out, err := testutil.DoltContainerExec(ctx, []string{"find", dir, "-mindepth", "1", "-maxdepth", "1"})
	if err != nil {
		t.Fatalf("exec find in container to list %q: %v", dir, err)
	}
	if code != 0 {
		t.Fatalf("find %q in container exited %d: %s", dir, code, out)
	}

	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// findDroppedDatabasesDir locates the .dolt_dropped_databases directory
// inside the shared test container's filesystem. Returns "" if it has not
// been created yet (no DROP DATABASE has ever run against this container) or
// has since been removed entirely by PURGE.
func findDroppedDatabasesDir(t *testing.T, ctx context.Context) string {
	t.Helper()

	// -xdev keeps the search inside the container's single root filesystem
	// (skips /proc, /sys, and similar mounts) and 2>/dev/null suppresses
	// permission-denied noise; find's exit status is unreliable under
	// suppressed errors so only stdout is trusted below.
	_, out, err := testutil.DoltContainerExec(ctx, []string{
		"sh", "-c", "find / -xdev -maxdepth 6 -type d -name .dolt_dropped_databases 2>/dev/null",
	})
	if err != nil {
		t.Fatalf("exec find in container to locate dropped-databases dir: %v", err)
	}

	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			return p
		}
	}
	return ""
}
