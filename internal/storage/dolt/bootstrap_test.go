package dolt

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	if !strings.Contains(err.Error(), "invalid database name") {
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
	if !strings.Contains(err.Error(), "invalid database name") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestBootstrapFromRemoteWithDB_RejectsPathLikeDatabase verifies that
// database names containing path separators or traversal segments are
// rejected before a clone is attempted. Such names would let the pre-clone
// doltExists() existence check miss a database that was already cloned
// under a different-looking path, which previously allowed the
// failed-clone cleanup to RemoveAll a pre-existing Dolt repo
// (P1 data-loss finding, cross-vendor review 2026-07-07).
func TestBootstrapFromRemoteWithDB_RejectsPathLikeDatabase(t *testing.T) {
	for _, name := range []string{"foo/bar", "../other-db", "foo\\bar"} {
		doltDir := t.TempDir()
		_, err := BootstrapFromRemoteWithDB(context.Background(), doltDir, "file:///dev/null", name)
		if err == nil {
			t.Fatalf("expected error for path-like database name %q, got nil", name)
		}
		if !strings.Contains(err.Error(), "invalid database name") {
			t.Errorf("database name %q: unexpected error message: %v", name, err)
		}
	}
}

// TestBootstrapFromRemote_UsesDefaultDatabase verifies that the
// convenience wrapper BootstrapFromRemote explicitly passes the
// default database name rather than an empty string.
func TestBootstrapFromRemote_UsesDefaultDatabase(t *testing.T) {
	// Create a doltDir that already contains a database so the function
	// returns early (skips clone) without needing the dolt CLI.
	doltDir := t.TempDir()

	// BootstrapFromRemote should not error with "invalid database name"
	// because it passes configfile.DefaultDoltDatabase explicitly.
	// It will return false (skipped) because doltExists returns false for an
	// empty dir, then it will fail trying to run dolt clone — but the error
	// should be about dolt CLI, not about an invalid database name.
	_, err := BootstrapFromRemote(context.Background(), doltDir, "file:///dev/null")
	if err != nil && strings.Contains(err.Error(), "invalid database name") {
		t.Fatal("BootstrapFromRemote should pass an explicit, valid database name")
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
	if !strings.Contains(err.Error(), "invalid database name") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestBootstrapFromRemoteWithDB_PreservesPreExistingCloneTarget verifies
// that a failed clone never runs cleanup on a clone target that already
// existed before this bootstrap attempt started. This is the defense in
// depth requested alongside database-name validation for the P1 data-loss
// finding (cross-vendor review 2026-07-07): a target directory not created
// by this attempt must be left untouched, even if `dolt clone` fails
// because the target already exists.
func TestBootstrapFromRemoteWithDB_PreservesPreExistingCloneTarget(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt CLI not available")
	}

	doltDir := t.TempDir()
	cloneTarget := filepath.Join(doltDir, "beads")
	if err := os.MkdirAll(cloneTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(cloneTarget, "README.txt")
	if err := os.WriteFile(marker, []byte("pre-existing content"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := BootstrapFromRemoteWithDB(context.Background(), doltDir, "file:///dev/null", "beads")
	if err == nil {
		t.Fatal("expected error from failed clone, got nil")
	}
	if !strings.Contains(err.Error(), "already existed before this attempt") {
		t.Errorf("expected error to note the pre-existing target, got: %v", err)
	}
	if data, statErr := os.ReadFile(marker); statErr != nil || string(data) != "pre-existing content" {
		t.Fatalf("pre-existing clone target content was not preserved: data=%q err=%v", data, statErr)
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
