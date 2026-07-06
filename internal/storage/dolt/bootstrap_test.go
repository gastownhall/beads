package dolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestDoltCloneArgs(t *testing.T) {
	t.Setenv("DOLT_REMOTE_USER", "")
	got := doltCloneArgs("https://example.com/repo", "/tmp/clone")
	want := []string{"clone", "https://example.com/repo", "/tmp/clone"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("doltCloneArgs() = %q, want %q", got, want)
	}

	t.Setenv("DOLT_REMOTE_USER", "alice")
	got = doltCloneArgs("https://example.com/repo", "/tmp/clone")
	want = []string{"clone", "--user", "alice", "https://example.com/repo", "/tmp/clone"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("doltCloneArgs() = %q, want %q", got, want)
	}
}

func TestRemoveFailedCloneTargetWithRetryRemovesPartialClone(t *testing.T) {
	oldDelays := failedCloneCleanupRetryDelays
	failedCloneCleanupRetryDelays = []time.Duration{0}
	t.Cleanup(func() { failedCloneCleanupRetryDelays = oldDelays })

	target := filepath.Join(t.TempDir(), "beads")
	if err := os.MkdirAll(filepath.Join(target, ".dolt", "noms"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".dolt", "noms", "LOCK"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleaned, err := removeFailedCloneTargetWithRetry(target)
	if err != nil {
		t.Fatalf("removeFailedCloneTargetWithRetry() error = %v", err)
	}
	if !cleaned {
		t.Fatal("expected cleanup to report removing a partial Dolt clone")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected failed clone target to be removed, stat err=%v", err)
	}
}

func TestRemoveFailedCloneTargetWithRetryPreservesNonDoltTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "beads")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "README.txt"), []byte("not a dolt clone"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleaned, err := removeFailedCloneTargetWithRetry(target)
	if err != nil {
		t.Fatalf("removeFailedCloneTargetWithRetry() error = %v", err)
	}
	if cleaned {
		t.Fatal("non-Dolt target should not report cleanup")
	}
	if _, err := os.Stat(filepath.Join(target, "README.txt")); err != nil {
		t.Fatalf("non-Dolt target should be preserved, stat err=%v", err)
	}
}

func TestRemoveFailedCloneTargetWithRetryPreservesDotDoltFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "beads")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".dolt"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleaned, err := removeFailedCloneTargetWithRetry(target)
	if err != nil {
		t.Fatalf("removeFailedCloneTargetWithRetry() error = %v", err)
	}
	if cleaned {
		t.Fatal("target with non-directory .dolt marker should not report cleanup")
	}
	if _, err := os.Stat(filepath.Join(target, ".dolt")); err != nil {
		t.Fatalf("non-directory .dolt marker should be preserved, stat err=%v", err)
	}
}

func TestFormatFailedCloneTargetErrorCleanupSucceeded(t *testing.T) {
	err := formatFailedCloneTargetError(errors.New("exit status 1"), []byte("clone failed"), `C:\tmp\beads`, true, nil)

	msg := err.Error()
	if !strings.Contains(msg, "Cleaned up failed clone target") {
		t.Fatalf("expected cleanup success note, got:\n%s", msg)
	}
	if !strings.Contains(msg, "retry `bd bootstrap`") {
		t.Fatalf("expected retry guidance, got:\n%s", msg)
	}
}

func TestFormatFailedCloneTargetErrorNoCleanup(t *testing.T) {
	err := formatFailedCloneTargetError(errors.New("exit status 1"), []byte("clone failed before target"), `C:\tmp\beads`, false, nil)

	msg := err.Error()
	if strings.Contains(msg, "Cleaned up failed clone target") {
		t.Fatalf("should not claim cleanup when no cleanup ran, got:\n%s", msg)
	}
	if !strings.Contains(msg, "clone failed before target") {
		t.Fatalf("expected original output, got:\n%s", msg)
	}
}

func TestFormatFailedCloneTargetErrorCleanupFailed(t *testing.T) {
	err := formatFailedCloneTargetError(errors.New("exit status 1"), []byte("unable to clean up failed clone"), `C:\tmp\beads`, true, errors.New("file is in use"))

	msg := err.Error()
	for _, want := range []string{"Could not clean up failed clone target", "Windows", ".dolt/noms/LOCK", "Stop stuck dolt/bd processes"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected %q in error, got:\n%s", want, msg)
		}
	}
}
