package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func probeLeftovers(t *testing.T, prefix string) []string {
	t.Helper()
	m, err := filepath.Glob(prefix + ".probe-*")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// ProbeBusy runs only after a lock reported busy, and must tell a real
// holder (the private probe file locks fine) from a filesystem whose locks
// cannot work (the private file fails the same way).
func TestProbeBusy(t *testing.T) {
	t.Run("healthy filesystem", func(t *testing.T) {
		prefix := filepath.Join(t.TempDir(), ".beads.gate.lock")
		var probed string
		err := ProbeBusy(prefix, func(f *os.File) error {
			probed = f.Name()
			return FlockExclusiveNonBlock(f)
		})
		if err != nil {
			t.Fatalf("ProbeBusy on a lockable filesystem = %v, want nil", err)
		}
		if filepath.Dir(probed) != filepath.Dir(prefix) || probed == prefix {
			t.Errorf("probe locked %q, want a private file beside %q", probed, prefix)
		}
		if left := probeLeftovers(t, prefix); len(left) != 0 {
			t.Errorf("probe files left behind: %v", left)
		}
	})

	t.Run("lock reports busy on a private file", func(t *testing.T) {
		prefix := filepath.Join(t.TempDir(), ".beads.gate.lock")
		err := ProbeBusy(prefix, func(*os.File) error { return ErrLockBusy })
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("ProbeBusy with an always-busy lock = %v, want ErrUnsupported", err)
		}
		if errors.Is(err, ErrLockBusy) || IsLocked(err) {
			t.Errorf("ProbeBusy result %v still reads as busy", err)
		}
		if left := probeLeftovers(t, prefix); len(left) != 0 {
			t.Errorf("probe files left behind: %v", left)
		}
	})

	t.Run("lock fails with an unsupported errno", func(t *testing.T) {
		prefix := filepath.Join(t.TempDir(), ".beads.gate.lock")
		err := ProbeBusy(prefix, func(*os.File) error { return errors.ErrUnsupported })
		if !errors.Is(err, ErrUnsupported) || !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("ProbeBusy with an unsupported lock = %v, want ErrUnsupported wrapping the cause", err)
		}
		if left := probeLeftovers(t, prefix); len(left) != 0 {
			t.Errorf("probe files left behind: %v", left)
		}
	})

	t.Run("other lock error is unverifiable", func(t *testing.T) {
		prefix := filepath.Join(t.TempDir(), ".beads.gate.lock")
		if err := ProbeBusy(prefix, func(*os.File) error { return os.ErrPermission }); err != nil {
			t.Fatalf("ProbeBusy with an unrelated lock error = %v, want nil", err)
		}
	})

	t.Run("unwritable directory is unverifiable", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("directory mode bits do not stop file creation on Windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		called := false
		err := ProbeBusy(filepath.Join(dir, ".beads.gate.lock"), func(*os.File) error {
			called = true
			return ErrLockBusy
		})
		if err != nil {
			t.Fatalf("ProbeBusy where no probe file can be created = %v, want nil", err)
		}
		if called {
			t.Error("lock attempted without a probe file")
		}
	})
}
