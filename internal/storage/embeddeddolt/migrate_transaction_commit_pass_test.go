//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// sessionTransactionCommit reads the pinned session's Dolt transaction-commit
// mode as its wire string ("1"/"0"), robust to the int/bool spellings
// different Dolt builds return for boolean system variables.
func sessionTransactionCommit(t *testing.T, ctx context.Context, conn *sql.Conn) string {
	t.Helper()
	var v sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&v); err != nil {
		t.Fatalf("read @@SESSION.dolt_transaction_commit: %v", err)
	}
	return v.String
}

// TestEmbeddedMigrateFromEmptyUnderTransactionCommitReachesLatestAndRestores
// is the fresh-install half of gastownhall/beads#4137: a brand-new database
// on a session with @@dolt_transaction_commit=1 must migrate all the way to
// both latest cursors with a clean working set, and the pass must leave the
// session's setting exactly as it found it.
func TestEmbeddedMigrateFromEmptyUnderTransactionCommitReachesLatestAndRestores(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dir := newEmptyEmbeddedDataDir(t, ctx)
	conn, closeConn := openPinnedConn(t, ctx, dir)
	defer closeConn()

	if _, err := conn.ExecContext(ctx, "SET @@dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp on empty database under dolt_transaction_commit=1: %v", err)
	}

	if got, want := currentMainVersion(t, ctx, conn), schema.LatestVersion(); got != want {
		t.Fatalf("main schema version = %d, want %d", got, want)
	}
	var ignored int
	if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations").Scan(&ignored); err != nil {
		t.Fatalf("read ignored schema version: %v", err)
	}
	if got, want := ignored, schema.LatestIgnoredVersion(); got != want {
		t.Fatalf("ignored schema version = %d, want %d", got, want)
	}
	if dirty := dirtyTableNames(t, ctx, conn); len(dirty) != 0 {
		t.Fatalf("working set after migrate = %v, want clean", dirty)
	}
	if got := sessionTransactionCommit(t, ctx, conn); got != "1" {
		t.Fatalf("session dolt_transaction_commit after migrate = %q, want \"1\" (restored)", got)
	}

	// 0040's inline body still ran and still commits on its own terms: its
	// nonlocal-table commits must be in history (0041 later removes the rows,
	// so dolt_nonlocal_tables itself cannot prove this).
	var wisps int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message LIKE '%create nonlocal table wisps%'").Scan(&wisps); err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	if wisps != 1 {
		t.Fatalf("dolt_log 'create nonlocal table wisps' commits = %d, want 1", wisps)
	}
}

// TestEmbeddedMigratePassSuspendsTransactionCommitUntilRestoredOnFault proves
// the suspension is scoped to the pass and survives a fault: a step fault hook
// observing the session mid-pass (right after migration 40 applies) must see
// the mode suspended (0), the sentinel error must travel out unwrapped enough
// for errors.Is, and the session must be back at 1 when MigrateUp returns.
// Not parallel: the fault hook is a package-global seam.
func TestEmbeddedMigratePassSuspendsTransactionCommitUntilRestoredOnFault(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, 27)

	sentinel := errors.New("injected fault after migration 40")
	var observed string
	var observedOK bool
	restore := schema.SetMigrateStepFaultHookForTest(func(ctx context.Context, q schema.DBConn, version int) error {
		if version != 40 {
			return nil
		}
		var v sql.NullString
		if err := q.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&v); err != nil {
			return fmt.Errorf("observing dolt_transaction_commit at migration 40: %w", err)
		}
		observed, observedOK = v.String, true
		return sentinel
	})
	t.Cleanup(restore)

	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()
	if _, err := conn.ExecContext(ctx, "SET @@dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit: %v", err)
	}

	_, err := schema.MigrateUp(ctx, conn)
	if !errors.Is(err, sentinel) {
		t.Fatalf("MigrateUp err = %v, want the sentinel fault (errors.Is)", err)
	}
	if !observedOK || observed != "0" {
		t.Fatalf("setting observed at migration 40 = (%q, seen=%t), want \"0\" (suspended for the pass)", observed, observedOK)
	}
	if got := sessionTransactionCommit(t, ctx, conn); got != "1" {
		t.Fatalf("session dolt_transaction_commit after faulted pass = %q, want \"1\" (restored)", got)
	}
}
