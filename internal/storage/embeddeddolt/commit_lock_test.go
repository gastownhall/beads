//go:build cgo

package embeddeddolt

// AC-8 (GH#8590): an embedded store has no concurrent sessions to exclude, so
// its commit planes issue NO GET_LOCK/RELEASE_LOCK (VersionCommitDefs defer;
// embeddeddolt threads versioncontrolops.WithoutCommitLock), while the commit
// itself still lands. The statements are observed through the
// embeddedConnectorInterceptor seam: the embedded store opens a fresh handle
// per transaction (commitConn → OpenSQL), so the recorder wraps every
// connector this test's store opens and keeps every statement sent through it.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// stmtRecorder collects every statement prepared on the wrapped connector's
// connections. It implements only driver.Conn's mandatory surface: the
// database/sql then routes every statement through Prepare, which is exactly
// the vantage point that records them all.
type stmtRecorder struct {
	mu    sync.Mutex
	stmts []string
}

func (r *stmtRecorder) record(q string) {
	r.mu.Lock()
	r.stmts = append(r.stmts, q)
	r.mu.Unlock()
}

type recordingConnector struct {
	inner driver.Connector
	rec   *stmtRecorder
}

func (c *recordingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &recordingConn{inner: conn, rec: c.rec}, nil
}

func (c *recordingConnector) Driver() driver.Driver { return c.inner.Driver() }

// recordingConn deliberately implements ONLY the mandatory driver.Conn
// surface (no Queryer/Execer fast paths), so every statement the embedded
// store issues goes through Prepare and is recorded.
type recordingConn struct {
	inner driver.Conn
	rec   *stmtRecorder
}

func (c *recordingConn) Prepare(query string) (driver.Stmt, error) {
	c.rec.record(query)
	return c.inner.Prepare(query)
}

func (c *recordingConn) Close() error { return c.inner.Close() }

func (c *recordingConn) Begin() (driver.Tx, error) { return c.inner.Begin() }

// TestEmbeddedRunInTransactionCommitsWithoutLockStatements is AC-8: given an
// embedded store, when RunInTransaction commits, the Dolt commit lands and no
// GET_LOCK or RELEASE_LOCK statement is issued.
func TestEmbeddedRunInTransactionCommitsWithoutLockStatements(t *testing.T) {
	ctx := context.Background()

	rec := &stmtRecorder{}
	prev := embeddedConnectorInterceptor
	embeddedConnectorInterceptor = func(c driver.Connector) driver.Connector {
		return &recordingConnector{inner: c, rec: rec}
	}
	t.Cleanup(func() { embeddedConnectorInterceptor = prev })

	dir := t.TempDir()
	store, err := Open(ctx, dir, "beads_test", "main")
	if err != nil {
		t.Fatalf("open embedded store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The database's config table must name the issue prefix before issues
	// can be created (the same open gate a fresh `bd init` satisfies).
	if err := store.SetConfig(ctx, "issue_prefix", "emblock"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}
	id := "emblock-1"
	if err := store.CreateIssue(ctx, &types.Issue{
		ID: id, Title: "embedded commit lock", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	const msg = "embedded: commit-lock-free commit"
	if err := store.RunInTransaction(ctx, "seed", func(tx storage.Transaction) error {
		return tx.SetMetadata(ctx, "commit_lock_test", "seed-value")
	}); err != nil {
		t.Fatalf("seed RunInTransaction: %v", err)
	}

	rec.mu.Lock()
	rec.stmts = nil
	rec.mu.Unlock()

	if err := store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
		return tx.SetMetadata(ctx, "commit_lock_test", "changed-value")
	}); err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}

	// The commit must have landed: the message is in dolt_log.
	var entries int
	readErr := store.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message = ?", msg).Scan(&entries)
	})
	if readErr != nil {
		t.Fatalf("read dolt_log: %v", readErr)
	}
	if entries == 0 {
		t.Fatal("the embedded commit did not land in dolt_log")
	}

	// And it must have issued no lock statements anywhere on the way.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.stmts) == 0 {
		t.Fatal("no statements were recorded; the recorder was bypassed, so the assertion below would be vacuous")
	}
	for _, s := range rec.stmts {
		if strings.Contains(s, "GET_LOCK") || strings.Contains(s, "RELEASE_LOCK") {
			t.Errorf("embedded commit issued %q: embedded mode takes no lock statements (GH#8590)", s)
		}
	}
	t.Logf("recorded %d statements on the embedded commit; none were lock statements", len(rec.stmts))
}
