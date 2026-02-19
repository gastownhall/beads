package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFlockFunctions(t *testing.T) {
	t.Run("FlockExclusiveBlocking and FlockUnlock", func(t *testing.T) {
		tmpDir := t.TempDir()
		lockPath := filepath.Join(tmpDir, "test.lock")

		if err := os.WriteFile(lockPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create lock file: %v", err)
		}

		f, err := os.OpenFile(lockPath, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to open lock file: %v", err)
		}
		defer f.Close()

		if err := FlockExclusiveBlocking(f); err != nil {
			t.Errorf("FlockExclusiveBlocking failed: %v", err)
		}

		if err := FlockUnlock(f); err != nil {
			t.Errorf("FlockUnlock failed: %v", err)
		}
	})

	t.Run("flockExclusive non-blocking succeeds on unlocked file", func(t *testing.T) {
		tmpDir := t.TempDir()
		lockPath := filepath.Join(tmpDir, "test.lock")

		if err := os.WriteFile(lockPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create lock file: %v", err)
		}

		f, err := os.OpenFile(lockPath, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to open lock file: %v", err)
		}
		defer f.Close()

		if err := flockExclusive(f); err != nil {
			t.Errorf("flockExclusive should succeed on unlocked file: %v", err)
		}

		FlockUnlock(f)
	})

	t.Run("flockExclusive returns errProcessLocked when already locked", func(t *testing.T) {
		tmpDir := t.TempDir()
		lockPath := filepath.Join(tmpDir, "test.lock")

		if err := os.WriteFile(lockPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create lock file: %v", err)
		}

		f1, err := os.OpenFile(lockPath, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to open lock file: %v", err)
		}
		defer f1.Close()

		if err := FlockExclusiveBlocking(f1); err != nil {
			t.Fatalf("failed to acquire first lock: %v", err)
		}
		defer FlockUnlock(f1)

		f2, err := os.OpenFile(lockPath, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to open second lock file handle: %v", err)
		}
		defer f2.Close()

		err = flockExclusive(f2)
		if err != errProcessLocked {
			t.Errorf("expected errProcessLocked, got %v", err)
		}
	})
}

func TestFlockExclusiveNonBlocking(t *testing.T) {
	t.Run("succeeds on unlocked file", func(t *testing.T) {
		tmpDir := t.TempDir()
		lockPath := filepath.Join(tmpDir, "test.lock")

		if err := os.WriteFile(lockPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create lock file: %v", err)
		}

		f, err := os.OpenFile(lockPath, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to open lock file: %v", err)
		}
		defer f.Close()

		err = FlockExclusiveNonBlocking(f)
		if err != nil {
			t.Errorf("FlockExclusiveNonBlocking should succeed on unlocked file: %v", err)
		}

		FlockUnlock(f)
	})

	t.Run("returns ErrLocked when already locked", func(t *testing.T) {
		tmpDir := t.TempDir()
		lockPath := filepath.Join(tmpDir, "test.lock")

		if err := os.WriteFile(lockPath, []byte("test"), 0644); err != nil {
			t.Fatalf("failed to create lock file: %v", err)
		}

		// Acquire lock with first handle
		f1, err := os.OpenFile(lockPath, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to open lock file: %v", err)
		}
		defer f1.Close()

		if err := FlockExclusiveBlocking(f1); err != nil {
			t.Fatalf("failed to acquire first lock: %v", err)
		}
		defer FlockUnlock(f1)

		// Try non-blocking lock with second handle
		f2, err := os.OpenFile(lockPath, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("failed to open second handle: %v", err)
		}
		defer f2.Close()

		err = FlockExclusiveNonBlocking(f2)
		if !IsLocked(err) {
			t.Errorf("expected IsLocked to be true, got error: %v", err)
		}
		if err != ErrLocked {
			t.Errorf("expected ErrLocked, got: %v", err)
		}
	})
}

func TestIsLocked(t *testing.T) {
	t.Run("true for ErrLocked", func(t *testing.T) {
		if !IsLocked(ErrLocked) {
			t.Error("IsLocked should return true for ErrLocked")
		}
	})

	t.Run("false for nil", func(t *testing.T) {
		if IsLocked(nil) {
			t.Error("IsLocked should return false for nil")
		}
	})

	t.Run("false for other errors", func(t *testing.T) {
		if IsLocked(fmt.Errorf("some other error")) {
			t.Error("IsLocked should return false for non-lock errors")
		}
	})
}
