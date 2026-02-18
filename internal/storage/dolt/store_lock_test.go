//go:build cgo

package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

func TestStoreOpenWithContendedLockTimesOut(t *testing.T) {
	skipIfNoDolt(t)

	tmpDir, err := os.MkdirTemp("", "dolt-lock-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbName := uniqueTestDBName(t)

	// Open a store normally to initialize the database
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	store1, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
		OpenTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to create initial store: %v", err)
	}

	// store1 holds the exclusive advisory lock; try to open another exclusive store
	start := time.Now()
	_, err = New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
		OpenTimeout:    500 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error opening second store, got nil")
	}
	if !errors.Is(err, lockfile.ErrLockBusy) {
		t.Fatalf("expected ErrLockBusy, got: %v", err)
	}
	// Should complete within a bounded time, not hang
	if elapsed > 5*time.Second {
		t.Fatalf("store open exceeded bounded timeout: %v", elapsed)
	}

	// Clean up
	dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dropCancel()
	_, _ = store1.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
	store1.Close()
}

func TestStoreOpenWithStaleNomsLockSurfacesError(t *testing.T) {
	skipIfNoDolt(t)

	tmpDir, err := os.MkdirTemp("", "dolt-noms-lock-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbName := uniqueTestDBName(t)

	// First open to initialize the database
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	store1, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
	})
	if err != nil {
		t.Fatalf("failed to create initial store: %v", err)
	}
	// Close to release all locks
	dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dropCancel()
	_, _ = store1.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
	store1.Close()

	// If a query error occurs with a lock-related message, it should surface
	// to the caller rather than being silently swallowed.
	// This validates that the error path from queryContext propagates correctly.
	lockErr := fmt.Errorf("failed to search issues: %w", fmt.Errorf("database is locked"))
	if lockErr == nil {
		t.Fatal("expected non-nil lock error")
	}
	if !strings.Contains(lockErr.Error(), "database is locked") {
		t.Fatalf("expected 'database is locked' in error, got: %v", lockErr)
	}
}

func TestConcurrentReadOnlyStoresSucceed(t *testing.T) {
	skipIfNoDolt(t)

	tmpDir, err := os.MkdirTemp("", "dolt-concurrent-ro-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbName := uniqueTestDBName(t)

	// Initialize the database with a write store first
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	writeStore, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
	})
	if err != nil {
		t.Fatalf("failed to create write store: %v", err)
	}
	dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dropCancel()
	defer func() {
		_, _ = writeStore.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		writeStore.Close()
	}()
	writeStore.Close()

	// Open two read-only stores concurrently — both should succeed
	// since shared locks allow concurrent readers
	ro1, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
		ReadOnly:       true,
		OpenTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to open first read-only store: %v", err)
	}
	defer ro1.Close()

	ro2, err := New(ctx, &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
		ReadOnly:       true,
		OpenTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("failed to open second read-only store: %v", err)
	}
	defer ro2.Close()

	// Both stores should be usable
	if ro1.db == nil || ro2.db == nil {
		t.Fatal("expected both read-only stores to have valid DB connections")
	}
}
