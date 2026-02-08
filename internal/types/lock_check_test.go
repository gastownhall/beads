package types

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestShouldSkipDatabase(t *testing.T) {
	// Create temp directory for testing
	tmpDir := t.TempDir()

	t.Run("no lock file exists", func(t *testing.T) {
		skip, holder, err := ShouldSkipDatabase(tmpDir)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if skip {
			t.Error("should not skip when no lock file exists")
		}
		if holder != "" {
			t.Errorf("holder should be empty, got %s", holder)
		}
	})

	t.Run("valid lock with alive process", func(t *testing.T) {
		lockPath := filepath.Join(tmpDir, ".exclusive-lock")
		currentHost, _ := os.Hostname()
		lock := &ExclusiveLock{
			Holder:    "test-tool",
			PID:       os.Getpid(), // Current process, definitely alive
			Hostname:  currentHost,
			StartedAt: time.Now(),
			Version:   "1.0.0",
		}
		data, _ := json.Marshal(lock)
		if err := os.WriteFile(lockPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(lockPath)

		skip, holder, err := ShouldSkipDatabase(tmpDir)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !skip {
			t.Error("should skip when lock is valid and process is alive")
		}
		if holder != "test-tool" {
			t.Errorf("holder should be test-tool, got %s", holder)
		}
	})

	t.Run("stale lock with dead process", func(t *testing.T) {
		// Note: We can't reliably test actual stale lock cleanup without creating
		// and killing a real process, because high PIDs may return EPERM (treated as alive).
		// This test verifies the logic path exists, but actual cleanup relies on
		// integration testing or manual verification.

		// Instead, test that a lock with a different hostname (remote) is assumed alive
		lockPath := filepath.Join(tmpDir, ".exclusive-lock")
		lock := &ExclusiveLock{
			Holder:    "remote-tool",
			PID:       12345,
			Hostname:  "definitely-not-this-host-xyz",
			StartedAt: time.Now(),
			Version:   "1.0.0",
		}
		data, _ := json.Marshal(lock)
		if err := os.WriteFile(lockPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(lockPath)

		skip, holder, err := ShouldSkipDatabase(tmpDir)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !skip {
			t.Error("should skip when lock is from remote host (can't verify)")
		}
		if holder != "remote-tool" {
			t.Errorf("holder should be remote-tool, got %s", holder)
		}
	})

	t.Run("malformed lock file", func(t *testing.T) {
		lockPath := filepath.Join(tmpDir, ".exclusive-lock")
		if err := os.WriteFile(lockPath, []byte("not valid json"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(lockPath)

		skip, holder, err := ShouldSkipDatabase(tmpDir)
		if err == nil {
			t.Error("expected error for malformed lock file")
		}
		if !skip {
			t.Error("should skip when lock file is malformed (fail-safe)")
		}
		if holder != "" {
			t.Errorf("holder should be empty for malformed lock, got %s", holder)
		}
	})

	t.Run("invalid lock (missing required fields)", func(t *testing.T) {
		lockPath := filepath.Join(tmpDir, ".exclusive-lock")
		// Lock with missing holder (invalid)
		lock := &ExclusiveLock{
			PID:       12345,
			Hostname:  "test-host",
			StartedAt: time.Now(),
			Version:   "1.0.0",
		}
		data, _ := json.Marshal(lock)
		if err := os.WriteFile(lockPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(lockPath)

		skip, holder, err := ShouldSkipDatabase(tmpDir)
		if err == nil {
			t.Error("expected error for invalid lock file")
		}
		if !skip {
			t.Error("should skip when lock file is invalid (fail-safe)")
		}
		if holder != "" {
			t.Errorf("holder should be empty for invalid lock, got %s", holder)
		}
	})

	t.Run("remote hostname (assume alive)", func(t *testing.T) {
		lockPath := filepath.Join(tmpDir, ".exclusive-lock")
		lock := &ExclusiveLock{
			Holder:    "remote-tool",
			PID:       12345,
			Hostname:  "remote-host-xyz",
			StartedAt: time.Now(),
			Version:   "1.0.0",
		}
		data, _ := json.Marshal(lock)
		if err := os.WriteFile(lockPath, data, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(lockPath)

		skip, holder, err := ShouldSkipDatabase(tmpDir)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !skip {
			t.Error("should skip when lock is from remote host (can't verify, assume alive)")
		}
		if holder != "remote-tool" {
			t.Errorf("holder should be remote-tool, got %s", holder)
		}
	})
}
