//go:build cgo

package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// newTestSQLiteDB opens an in-memory SQLite3 database for unit tests.
// It does not require a running Dolt server.
func newTestSQLiteDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open SQLite test DB: %v", err)
	}
	return db, func() { _ = db.Close() }
}

// TestExecContext_Commit verifies that execContext wraps writes in an explicit
// BEGIN/COMMIT, making them durable even when the session's autocommit is off.
func TestExecContext_Commit(t *testing.T) {
	db, cleanup := newTestSQLiteDB(t)
	defer cleanup()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE items (id TEXT PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	store := &DoltStore{db: db}

	result, err := store.execContext(ctx, "INSERT INTO items (id, val) VALUES (?, ?)", "x1", "hello")
	if err != nil {
		t.Fatalf("execContext failed: %v", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row affected, got %d", n)
	}

	// Row must be visible in a separate query (i.e. the transaction was committed).
	var val string
	if err := db.QueryRowContext(ctx, "SELECT val FROM items WHERE id = ?", "x1").Scan(&val); err != nil {
		t.Fatalf("row not visible after execContext commit: %v", err)
	}
	if val != "hello" {
		t.Errorf("expected 'hello', got %q", val)
	}
}

// TestExecContext_RollbackOnError verifies that when the statement fails,
// execContext rolls back the transaction and returns the error.
func TestExecContext_RollbackOnError(t *testing.T) {
	db, cleanup := newTestSQLiteDB(t)
	defer cleanup()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE items (id TEXT PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	store := &DoltStore{db: db}

	// First insert succeeds.
	if _, err := store.execContext(ctx, "INSERT INTO items (id, val) VALUES (?, ?)", "dupe", "first"); err != nil {
		t.Fatalf("initial insert failed: %v", err)
	}

	// Second insert with the same PK must fail and roll back.
	if _, err := store.execContext(ctx, "INSERT INTO items (id, val) VALUES (?, ?)", "dupe", "second"); err == nil {
		t.Fatal("expected error for duplicate primary key, got nil")
	}

	// The original row must survive.
	var val string
	if err := db.QueryRowContext(ctx, "SELECT val FROM items WHERE id = ?", "dupe").Scan(&val); err != nil {
		t.Fatalf("original row missing after rollback: %v", err)
	}
	if val != "first" {
		t.Errorf("expected 'first' to survive rollback, got %q", val)
	}
}

// TestGetAdaptiveIDLength exercises every branch in getAdaptiveIDLengthFromTable.
func TestGetAdaptiveIDLength(t *testing.T) {
	db, cleanup := newTestSQLiteDB(t)
	defer cleanup()

	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "CREATE TABLE test_ids (id TEXT NOT NULL)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	populate := func(n int) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "DELETE FROM test_ids"); err != nil {
			t.Fatalf("DELETE: %v", err)
		}
		// Bulk-insert in a single transaction so large counts stay fast.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		for i := 0; i < n; i++ {
			if _, err := tx.ExecContext(ctx, "INSERT INTO test_ids VALUES (?)", fmt.Sprintf("pfx-%06d", i)); err != nil {
				_ = tx.Rollback()
				t.Fatalf("INSERT %d: %v", i, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}

	tests := []struct {
		count   int
		wantLen int
	}{
		{0, 4},
		{50, 4},
		{99, 4},
		{100, 5},
		{500, 5},
		{999, 5},
		{1000, 6},
		{5000, 6},
		{9999, 6},
		{10000, 7},
	}

	for _, tt := range tests {
		populate(tt.count)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("count=%d: BeginTx: %v", tt.count, err)
		}
		got := getAdaptiveIDLengthFromTable(ctx, tx, "test_ids", "pfx-")
		_ = tx.Rollback()
		if got != tt.wantLen {
			t.Errorf("count=%d: want length %d, got %d", tt.count, tt.wantLen, got)
		}
	}
}

// TestGetAdaptiveIDLength_QueryError verifies the fallback when the query fails
// (e.g. the table does not exist).
func TestGetAdaptiveIDLength_QueryError(t *testing.T) {
	db, cleanup := newTestSQLiteDB(t)
	defer cleanup()

	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Table does not exist — function must return the safe default of 4.
	got := getAdaptiveIDLengthFromTable(ctx, tx, "nonexistent_table", "pfx-")
	if got != 4 {
		t.Errorf("expected fallback length 4, got %d", got)
	}
}
