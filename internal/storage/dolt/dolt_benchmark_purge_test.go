package dolt

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
)

// findDroppedDirCmd locates the .dolt_dropped_databases directory anywhere in
// the container.
//
// It deliberately does NOT use `find -xdev`. The Dolt image declares
// VOLUME /var/lib/dolt (`docker image inspect dolthub/dolt-sql-server:2.2.0`
// -> `{"/var/lib/dolt":{}}`), so the data dir is a *separate mount* from `/`:
// inside the container `stat -c %d / /var/lib/dolt` reports different device
// IDs. `-xdev` stops the walk dead at that boundary, so it never reaches the
// data dir at all — the probe returned "" on every run, the entry count was
// always 0, and the leak assertion below was vacuously true whether or not
// dropBenchDB purged anything (PR #5792 review, finding 1). The pseudo
// filesystems `-xdev` was there to skip are pruned explicitly instead.
const findDroppedDirCmd = `find / -maxdepth 6 \( -path /proc -o -path /sys -o -path /dev \) -prune ` +
	`-o -type d -name .dolt_dropped_databases -print 2>/dev/null`

// TestBenchDBPurgeDoesNotLeak is the regression gate for be-pq5: dropBenchDB
// must DROP and then PURGE so the dropped-databases dir does not grow across
// repeated bench samples. Without the PURGE call inside dropBenchDB, looped
// store setup + cleanup leaks a benchdb_* dir into .dolt_dropped_databases/
// on every iteration.
//
// Dolt 1.86 exposes no SQL view for the dropped-databases list, so the only
// way to detect a leak is to count entries in the server's
// .dolt_dropped_databases/ directory, which has no host-visible path — hence
// reading it by exec'ing into the container.
//
// This runs against its own isolated container, not the shared TestMain one.
// Six sites in this package refuse to DROP DATABASE against the shared
// container because rapid drops crash it (create_guard_test.go:101,
// cross_project_test.go:107,272, dolt_test.go:242, schema_skew_test.go:91,
// store_unit_test.go:49), and this test does five in a row. On top of that
// both the dropped-databases directory and DOLT_PURGE_DROPPED_DATABASES are
// server-global, so on a shared server the baseline/post counts would race
// whichever test holds the other testSem slot (PR #5792 review, finding 4).
func TestBenchDBPurgeDoesNotLeak(t *testing.T) {
	ctr := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(ctr.Port)
	if err != nil {
		t.Fatalf("parse container port %q: %v", ctr.Port, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: port, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer admin.Close()

	baseline := countDroppedDatabaseEntries(t, ctx, ctr)
	requireLeakIsObservable(t, ctx, ctr, admin, baseline)

	const iterations = 5
	for i := 0; i < iterations; i++ {
		dbName := benchDatabaseName()
		store := newPurgeRegressionStore(t, ctx, port, dbName)
		dropBenchDB(t, store, dbName)
		requireDatabaseDropped(t, ctx, admin, dbName)
		store.Close()
	}

	post := countDroppedDatabaseEntries(t, ctx, ctr)
	if post > baseline {
		t.Fatalf("dolt_dropped_databases grew from %d to %d across %d setup/cleanup cycles; "+
			"dropBenchDB likely missing PURGE step (be-pq5)",
			baseline, post, iterations)
	}
}

// requireLeakIsObservable is the positive control: it proves the probe can
// actually see a leak before the caller asserts there isn't one.
//
// It drops a throwaway database *without* purging, requires the count to go
// up, then purges and requires it to come back down. Without this the test is
// unfalsifiable — any probe that silently reports 0 for ever (the `-xdev` bug
// above being exactly that) makes the real assertion pass vacuously, and a
// green run says nothing about whether PURGE works. A PASS that cannot fail is
// not evidence (PR #5792 review, findings 1 and 6).
func requireLeakIsObservable(t *testing.T, ctx context.Context, ctr *testutil.IsolatedDoltContainer, admin *sql.DB, baseline int) {
	t.Helper()

	const controlDB = "beads_test_purge_control"
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+controlDB+"`"); err != nil {
		t.Fatalf("control: create database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+controlDB+"`"); err != nil {
		t.Fatalf("control: drop database: %v", err)
	}

	leaked := countDroppedDatabaseEntries(t, ctx, ctr)
	if leaked <= baseline {
		t.Fatalf("control: dropping %q without PURGE left the dropped-databases count at %d "+
			"(baseline %d); the probe cannot observe a leak, so this test could not fail and "+
			"proves nothing about dropBenchDB", controlDB, leaked, baseline)
	}

	if _, err := admin.ExecContext(ctx, "CALL DOLT_PURGE_DROPPED_DATABASES()"); err != nil {
		t.Fatalf("control: purge dropped databases: %v", err)
	}
	if got := countDroppedDatabaseEntries(t, ctx, ctr); got > baseline {
		t.Fatalf("control: PURGE left the dropped-databases count at %d, want <= baseline %d", got, baseline)
	}
}

// requireDatabaseDropped fails if dbName is still listed by SHOW DATABASES.
//
// dropBenchDB only Logf's when its DROP fails (dolt_benchmark_test.go:155), so
// a silently-failed drop would leave the database live, leak nothing into the
// dropped-databases dir, and let the leak assertion pass for the wrong reason
// (PR #5792 review, finding 3).
func requireDatabaseDropped(t *testing.T, ctx context.Context, admin *sql.DB, dbName string) {
	t.Helper()

	rows, err := admin.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		t.Fatalf("SHOW DATABASES: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan database name: %v", err)
		}
		if strings.EqualFold(name, dbName) {
			t.Fatalf("database %q is still present after dropBenchDB: the DROP failed silently, "+
				"so the leak count measures nothing", dbName)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate SHOW DATABASES: %v", err)
	}
}

// newPurgeRegressionStore creates a throwaway store against the test's own
// Dolt container, mirroring setupBenchStore's schema-init shape without
// setupBenchStore's BEADS_BENCH_DOLT_PORT opt-in — that opt-in firewalls real
// `go test -bench` runs from ambient production Dolt ports (be-cfm3z) and is
// never set in CI, so a regression test that must actually run under plain
// `go test` cannot depend on it.
func newPurgeRegressionStore(t *testing.T, ctx context.Context, port int, dbName string) *DoltStore {
	t.Helper()
	cfg := &Config{
		Path:            t.TempDir(),
		CommitterName:   "bench",
		CommitterEmail:  "bench@example.com",
		Database:        dbName,
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
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

// countDroppedDatabaseEntries returns the number of entries in the container's
// .dolt_dropped_databases/ directory, or 0 if the directory does not exist yet
// (the server only creates it lazily after the first DROP DATABASE) or PURGE
// has removed it entirely.
func countDroppedDatabaseEntries(t *testing.T, ctx context.Context, ctr *testutil.IsolatedDoltContainer) int {
	t.Helper()

	dir := findDroppedDatabasesDir(t, ctx, ctr)
	if dir == "" {
		return 0
	}

	code, out, err := ctr.Exec(ctx, []string{"find", dir, "-mindepth", "1", "-maxdepth", "1"})
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

// findDroppedDatabasesDir locates the .dolt_dropped_databases directory inside
// the container's filesystem. Returns "" if it has not been created yet (no
// DROP DATABASE has ever run against this container) or has since been removed
// entirely by PURGE.
func findDroppedDatabasesDir(t *testing.T, ctx context.Context, ctr *testutil.IsolatedDoltContainer) string {
	t.Helper()

	// find's exit status is unreliable with errors suppressed, so only stdout
	// is trusted here.
	_, out, err := ctr.Exec(ctx, []string{"sh", "-c", findDroppedDirCmd})
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
