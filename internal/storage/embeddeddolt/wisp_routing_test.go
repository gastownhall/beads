//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	doltembed "github.com/dolthub/driver"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// openTxForTest opens a raw SQL connection to the testEnv's store and begins
// a read-only transaction. The caller must call cleanup() when done.
func openTxForTest(t *testing.T, te *testEnv) (context.Context, *sql.Tx, func()) {
	t.Helper()
	ctx := t.Context()
	db, dbCleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = dbCleanup()
		t.Fatalf("BeginTx: %v", err)
	}
	cleanup := func() {
		_ = tx.Rollback()
		_ = dbCleanup()
	}
	return ctx, tx, cleanup
}

// seedIssue creates a regular (permanent) issue via the store.
func seedIssue(t *testing.T, te *testEnv, id string) {
	t.Helper()
	ctx := t.Context()
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID:        id,
		Title:     "perm " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue(%q): %v", id, err)
	}
}

// seedWisp creates an ephemeral (wisp) issue via the store.
func seedWisp(t *testing.T, te *testEnv, id string) {
	t.Helper()
	ctx := t.Context()
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID:        id,
		Title:     "wisp " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp(%q): %v", id, err)
	}
}

func TestActiveWispIDsInTx_Empty(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "aw1")
	ctx, tx, cleanup := openTxForTest(t, te)
	defer cleanup()

	for _, ids := range [][]string{nil, {}} {
		got, err := issueops.ActiveWispIDsInTx(ctx, tx, ids)
		if err != nil {
			t.Fatalf("ActiveWispIDsInTx(%v): %v", ids, err)
		}
		if got == nil {
			t.Error("expected non-nil map, got nil")
		}
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	}
}

func TestActiveWispIDsInTx_AllPermanent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "aw2")
	seedIssue(t, te, "aw2-1")
	seedIssue(t, te, "aw2-2")

	ctx, tx, cleanup := openTxForTest(t, te)
	defer cleanup()

	got, err := issueops.ActiveWispIDsInTx(ctx, tx, []string{"aw2-1", "aw2-2"})
	if err != nil {
		t.Fatalf("ActiveWispIDsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map for permanent issues, got %v", got)
	}
}

func TestActiveWispIDsInTx_AllWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "aw3")
	seedWisp(t, te, "aw3-wisp-1")
	seedWisp(t, te, "aw3-wisp-2")

	ctx, tx, cleanup := openTxForTest(t, te)
	defer cleanup()

	ids := []string{"aw3-wisp-1", "aw3-wisp-2"}
	got, err := issueops.ActiveWispIDsInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("ActiveWispIDsInTx: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 wisp IDs, got %d: %v", len(got), got)
	}
	for _, id := range ids {
		if !got[id] {
			t.Errorf("expected %q in result, missing", id)
		}
	}
}

func TestActiveWispIDsInTx_Mixed(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "aw4")
	seedIssue(t, te, "aw4-perm-1")
	seedWisp(t, te, "aw4-wisp-1")
	seedIssue(t, te, "aw4-perm-2")
	seedWisp(t, te, "aw4-wisp-2")

	ctx, tx, cleanup := openTxForTest(t, te)
	defer cleanup()

	ids := []string{"aw4-perm-1", "aw4-wisp-1", "aw4-perm-2", "aw4-wisp-2"}
	got, err := issueops.ActiveWispIDsInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("ActiveWispIDsInTx: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 wisp IDs, got %d: %v", len(got), got)
	}
	if !got["aw4-wisp-1"] {
		t.Error("expected aw4-wisp-1 in result")
	}
	if !got["aw4-wisp-2"] {
		t.Error("expected aw4-wisp-2 in result")
	}
	if got["aw4-perm-1"] {
		t.Error("did not expect aw4-perm-1 in result")
	}
	if got["aw4-perm-2"] {
		t.Error("did not expect aw4-perm-2 in result")
	}
}

func TestActiveWispIDsInTx_Batching(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// queryBatchSize is 200; use 2*200+1 = 401 IDs with half as wisps.
	const n = 401
	te := newTestEnv(t, "aw5")

	wispSet := make(map[string]bool)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("aw5-item-%03d", i)
		ids[i] = id
		if i%2 == 0 {
			seedWisp(t, te, id)
			wispSet[id] = true
		} else {
			seedIssue(t, te, id)
		}
	}

	ctx, tx, cleanup := openTxForTest(t, te)
	defer cleanup()

	got, err := issueops.ActiveWispIDsInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("ActiveWispIDsInTx: %v", err)
	}
	if len(got) != len(wispSet) {
		t.Fatalf("expected %d wisp IDs, got %d", len(wispSet), len(got))
	}
	for id := range wispSet {
		if !got[id] {
			t.Errorf("expected %q in result, missing", id)
		}
	}
}

func TestActiveWispIDsInTx_ParityWithLoop(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "aw6")
	// Seed 10 wisps and 10 perm issues.
	for i := 0; i < 10; i++ {
		seedWisp(t, te, fmt.Sprintf("aw6-wisp-%02d", i))
		seedIssue(t, te, fmt.Sprintf("aw6-perm-%02d", i))
	}

	ids := make([]string, 20)
	for i := 0; i < 10; i++ {
		ids[i*2] = fmt.Sprintf("aw6-wisp-%02d", i)
		ids[i*2+1] = fmt.Sprintf("aw6-perm-%02d", i)
	}

	ctx, tx, cleanup := openTxForTest(t, te)
	defer cleanup()

	// Legacy: per-ID probe
	legacySet := make(map[string]bool)
	for _, id := range ids {
		if issueops.IsActiveWispInTx(ctx, tx, id) {
			legacySet[id] = true
		}
	}

	// New: batch probe
	got, err := issueops.ActiveWispIDsInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("ActiveWispIDsInTx: %v", err)
	}

	if len(got) != len(legacySet) {
		t.Fatalf("result length mismatch: batch=%d legacy=%d", len(got), len(legacySet))
	}
	for id, v := range legacySet {
		if got[id] != v {
			t.Errorf("mismatch for %q: batch=%v legacy=%v", id, got[id], v)
		}
	}
}

func TestPartitionByWispInTx_Ordering(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "pw1")
	seedWisp(t, te, "pw1-w1")
	seedIssue(t, te, "pw1-p1")
	seedWisp(t, te, "pw1-w2")
	seedIssue(t, te, "pw1-p2")
	seedWisp(t, te, "pw1-w3")

	ctx, tx, cleanup := openTxForTest(t, te)
	defer cleanup()

	// Input order: w1, p1, w2, p2, w3
	ids := []string{"pw1-w1", "pw1-p1", "pw1-w2", "pw1-p2", "pw1-w3"}
	wispIDs, permIDs, err := issueops.PartitionByWispInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("PartitionByWispInTx: %v", err)
	}

	wantWisps := []string{"pw1-w1", "pw1-w2", "pw1-w3"}
	wantPerms := []string{"pw1-p1", "pw1-p2"}

	if len(wispIDs) != len(wantWisps) {
		t.Fatalf("wispIDs length: got %d, want %d", len(wispIDs), len(wantWisps))
	}
	for i, want := range wantWisps {
		if wispIDs[i] != want {
			t.Errorf("wispIDs[%d] = %q, want %q", i, wispIDs[i], want)
		}
	}

	if len(permIDs) != len(wantPerms) {
		t.Fatalf("permIDs length: got %d, want %d", len(permIDs), len(wantPerms))
	}
	for i, want := range wantPerms {
		if permIDs[i] != want {
			t.Errorf("permIDs[%d] = %q, want %q", i, permIDs[i], want)
		}
	}
}

func TestPartitionByWispInTx_Empty(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "pw2")
	ctx, tx, cleanup := openTxForTest(t, te)
	defer cleanup()

	for _, ids := range [][]string{nil, {}} {
		wispIDs, permIDs, err := issueops.PartitionByWispInTx(ctx, tx, ids)
		if err != nil {
			t.Fatalf("PartitionByWispInTx(%v): %v", ids, err)
		}
		if wispIDs != nil {
			t.Errorf("expected nil wispIDs, got %v", wispIDs)
		}
		if permIDs != nil {
			t.Errorf("expected nil permIDs, got %v", permIDs)
		}
	}
}

// --- test-only counting driver wrapper ---------------------------------
//
// The dolt driver does not implement driver.QueryerContext / ExecerContext
// on its *DoltConn, so every *sql.Tx.QueryContext path in the sql package
// flows through Conn.Prepare + Stmt.Query. Counting Prepare calls is
// therefore equivalent to counting queries. We wrap the connector so
// every driver connection's Prepare increments a shared atomic counter.

type countingConnector struct {
	inner driver.Connector
	count *atomic.Int64
}

func (c *countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	inner, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countingConn{inner: inner, count: c.count}, nil
}

func (c *countingConnector) Driver() driver.Driver { return c.inner.Driver() }

type countingConn struct {
	inner driver.Conn
	count *atomic.Int64
}

func (c *countingConn) Prepare(query string) (driver.Stmt, error) {
	c.count.Add(1)
	return c.inner.Prepare(query)
}

func (c *countingConn) Close() error              { return c.inner.Close() }
func (c *countingConn) Begin() (driver.Tx, error) { return c.inner.Begin() } //nolint:staticcheck

// BeginTx is implemented by DoltConn; delegate so tx semantics stay intact.
func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.inner.Begin() //nolint:staticcheck
}

// buildBenchDSN replicates the unexported buildDSN in open.go. Kept in
// sync manually — the driver params below are public constants.
func buildBenchDSN(dir, database string) string {
	v := url.Values{}
	v.Set(doltembed.CommitNameParam, "beads")
	v.Set(doltembed.CommitEmailParam, "beads@local")
	v.Set(doltembed.MultiStatementsParam, "true")
	if strings.TrimSpace(database) != "" {
		v.Set(doltembed.DatabaseParam, database)
	}
	path := dir
	if os.PathSeparator == '\\' {
		path = strings.ReplaceAll(path, `\`, `/`)
	}
	return "file://" + path + "?" + v.Encode()
}

// benchSQLStringLiteral mirrors the unexported sqlStringLiteral in open.go.
// Inlined here because embeddeddolt does not export it; the benchmark
// interpolates a branch name into a SET statement and must match the
// production escape rules exactly.
func benchSQLStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(strings.TrimSpace(s), "'", "''") + "'"
}

// openCountingDB builds a *sql.DB whose driver.Conn increments count on
// every Prepare call. Mirrors embeddeddolt.OpenSQL so the benchmark uses
// the same connection config (SetMaxOpenConns(1), explicit USE + branch).
// The returned cleanup func logs any close error via tb.Logf but never
// blocks cleanup — a failed close shouldn't poison subsequent iterations.
func openCountingDB(ctx context.Context, tb testing.TB, dir, database, branch string) (*sql.DB, *atomic.Int64, func(), error) {
	dsn := buildBenchDSN(dir, database)
	cfg, err := doltembed.ParseDSN(dsn)
	if err != nil {
		return nil, nil, nil, err
	}
	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = 0
	bo.MaxInterval = 5 * time.Second
	cfg.BackOff = bo

	inner, err := doltembed.NewConnector(cfg)
	if err != nil {
		return nil, nil, nil, err
	}

	count := &atomic.Int64{}
	wrapped := &countingConnector{inner: inner, count: count}
	db := sql.OpenDB(wrapped)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)

	cleanup := func() {
		if err := db.Close(); err != nil {
			tb.Logf("counting db close: %v", err)
		}
		if err := inner.Close(); err != nil {
			tb.Logf("counting connector close: %v", err)
		}
	}

	if err := db.PingContext(ctx); err != nil {
		cleanup()
		return nil, nil, nil, err
	}
	if strings.TrimSpace(database) != "" {
		if _, err := db.ExecContext(ctx, "USE `"+database+"`"); err != nil {
			cleanup()
			return nil, nil, nil, err
		}
		if strings.TrimSpace(branch) != "" {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(
				"SET @@%s_head_ref = %s", database, benchSQLStringLiteral(branch))); err != nil {
				cleanup()
				return nil, nil, nil, err
			}
		}
	}
	return db, count, cleanup, nil
}

func BenchmarkBulkPartitioning(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping embedded dolt benchmark in short mode")
	}

	const N = 500

	ctx := b.Context()
	beadsDir := b.TempDir() + "/.beads"
	store, err := embeddeddolt.New(ctx, beadsDir, "bench", "main")
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "bench"); err != nil {
		b.Fatalf("SetConfig: %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		b.Fatalf("Commit: %v", err)
	}

	// Seed N issues: half wisps, half perm.
	ids := make([]string, N)
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("bench-item-%04d", i)
		ids[i] = id
		if i%2 == 0 {
			if err := store.CreateIssue(ctx, &types.Issue{
				ID: id, Title: "wisp", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
			}, "bench"); err != nil {
				b.Fatalf("CreateIssue wisp: %v", err)
			}
		} else {
			if err := store.CreateIssue(ctx, &types.Issue{
				ID: id, Title: "perm", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask,
			}, "bench"); err != nil {
				b.Fatalf("CreateIssue perm: %v", err)
			}
		}
	}

	// The embedded Dolt driver holds an exclusive flock on the data directory.
	// Close the store before opening the counting DB on the same path to avoid
	// a deadlock on lock acquisition.
	dataDir := beadsDir + "/embeddeddolt"
	database := "bench"
	if err := store.Close(); err != nil {
		b.Fatalf("store.Close: %v", err)
	}

	// queryBatchSize = 200: ceil(500/200) = 3 batches.
	//
	// The plan and design doc quote the ceiling as `ceil(N/queryBatchSize) + 1`.
	// We drop the `+1` here because our counter intercepts driver.Conn.Prepare
	// only; BeginTx never invokes Prepare. The new PartitionByWispInTx issues
	// exactly ceil(N/batchSize) SELECT statements — each one goes through
	// Prepare — so the tight bound is the correct assertion.
	const queryBatchSize = 200
	expectedMax := (N + queryBatchSize - 1) / queryBatchSize

	var lastObserved int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db, count, dbCleanup, dbErr := openCountingDB(ctx, b, dataDir, database, "main")
		if dbErr != nil {
			b.Fatalf("openCountingDB: %v", dbErr)
		}
		tx, txErr := db.BeginTx(ctx, nil)
		if txErr != nil {
			dbCleanup()
			b.Fatalf("BeginTx: %v", txErr)
		}

		before := count.Load()
		_, _, partErr := issueops.PartitionByWispInTx(ctx, tx, ids)
		after := count.Load()

		_ = tx.Rollback()
		dbCleanup()

		if partErr != nil {
			b.Fatalf("partition: %v", partErr)
		}

		delta := after - before
		lastObserved = delta
		if delta > int64(expectedMax) {
			b.Fatalf("N+1 regression: %d queries (max %d) for N=%d batchSize=%d",
				delta, expectedMax, N, queryBatchSize)
		}
	}
	b.ReportMetric(float64(lastObserved), "observed_queries")
	b.ReportMetric(float64(expectedMax), "max_batch_queries")
}
