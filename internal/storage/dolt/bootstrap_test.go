package dolt

import (
	"context"
	"strings"
	"testing"
)

// TestBootstrapFromRemoteWithDB_RejectsEmptyDatabase verifies that
// BootstrapFromRemoteWithDB returns an error when called with an
// empty database name. Callers should use cfg.GetDoltDatabase() which
// applies the fallback chain (env var -> config -> default). A silent
// fallback to "beads" here previously masked misconfiguration (GH#3029).
func TestBootstrapFromRemoteWithDB_RejectsEmptyDatabase(t *testing.T) {
	doltDir := t.TempDir()

	_, err := BootstrapFromRemoteWithDB(context.Background(), doltDir, "file:///dev/null", "")
	if err == nil {
		t.Fatal("expected error for empty database name, got nil")
	}
	if !strings.Contains(err.Error(), "database name must not be empty") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestBootstrapFromRemoteWithDB_RejectsWhitespaceDatabase verifies that
// whitespace-only database names are also rejected (defense-in-depth).
func TestBootstrapFromRemoteWithDB_RejectsWhitespaceDatabase(t *testing.T) {
	doltDir := t.TempDir()

	_, err := BootstrapFromRemoteWithDB(context.Background(), doltDir, "file:///dev/null", "   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only database name, got nil")
	}
	if !strings.Contains(err.Error(), "database name must not be empty") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestBootstrapFromRemote_UsesDefaultDatabase verifies that the
// convenience wrapper BootstrapFromRemote explicitly passes the
// default database name rather than an empty string.
func TestBootstrapFromRemote_UsesDefaultDatabase(t *testing.T) {
	// Create a doltDir that already contains a database so the function
	// returns early (skips clone) without needing the dolt CLI.
	doltDir := t.TempDir()

	// BootstrapFromRemote should not error with "database name must not be empty"
	// because it passes configfile.DefaultDoltDatabase explicitly.
	// It will return false (skipped) because doltExists returns false for an
	// empty dir, then it will fail trying to run dolt clone — but the error
	// should be about dolt CLI, not about empty database name.
	_, err := BootstrapFromRemote(context.Background(), doltDir, "file:///dev/null")
	if err != nil && strings.Contains(err.Error(), "database name must not be empty") {
		t.Fatal("BootstrapFromRemote should pass an explicit database name, not empty string")
	}
	// Any other error (dolt CLI not found, clone failure) is fine — we only care
	// that the empty-database guard didn't fire.
}

// TestBootstrapFromGitRemoteWithDB_DeprecatedWrapper verifies that the
// deprecated BootstrapFromGitRemoteWithDB wrapper delegates correctly.
func TestBootstrapFromGitRemoteWithDB_DeprecatedWrapper(t *testing.T) {
	doltDir := t.TempDir()

	_, err := BootstrapFromGitRemoteWithDB(context.Background(), doltDir, "file:///dev/null", "")
	if err == nil {
		t.Fatal("expected error for empty database name, got nil")
	}
	if !strings.Contains(err.Error(), "database name must not be empty") {
		t.Errorf("unexpected error message: %v", err)
	}
}
