package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/sqlite"
)

const windowsOS = "windows"

// newTestStore creates a SQLite store with issue_prefix configured (bd-166)
// This prevents "database not initialized" errors in tests
func newTestStore(t *testing.T, dbPath string) *sqlite.SQLiteStorage {
	t.Helper()
	
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create database directory: %v", err)
	}
	
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	
	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	
	t.Cleanup(func() { store.Close() })
	return store
}

// newTestStoreWithPrefix creates a SQLite store with custom issue_prefix configured
func newTestStoreWithPrefix(t *testing.T, dbPath string, prefix string) *sqlite.SQLiteStorage {
	t.Helper()
	
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create database directory: %v", err)
	}
	
	store, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	
	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}
	
	t.Cleanup(func() { store.Close() })
	return store
}

// openExistingTestDB opens an existing database without modifying it.
// Used in tests where the database was already created by the code under test.
func openExistingTestDB(t *testing.T, dbPath string) (*sqlite.SQLiteStorage, error) {
	t.Helper()
	return sqlite.New(dbPath)
}

// runCommandInDir runs a command in the specified directory
func runCommandInDir(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

// runCommandInDirWithOutput runs a command in the specified directory and returns its output
func runCommandInDirWithOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
