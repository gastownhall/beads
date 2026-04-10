package doltserver

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeFileSize creates path with content of exactly size bytes. Uses a
// repeating byte pattern so the contents are verifiable when the test asserts
// the rotated file received the original data.
func writeFileSize(t *testing.T, path string, size int, pattern byte) {
	t.Helper()
	buf := bytes.Repeat([]byte{pattern}, size)
	if err := os.WriteFile(path, buf, 0600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// mustStat returns os.Stat info, failing the test on error.
func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

// TestRotateLogIfOversized_RotatesWhenOverThreshold verifies the core happy
// path: a file larger than the cap is renamed to <name>.1 and the primary
// path becomes available for a fresh file.
func TestRotateLogIfOversized_RotatesWhenOverThreshold(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "dolt-server.log")
	writeFileSize(t, primary, 1024, 'A')

	rotated, err := rotateLogIfOversized(primary, 512)
	if err != nil {
		t.Fatalf("rotateLogIfOversized: %v", err)
	}
	if !rotated {
		t.Fatal("expected rotated=true when file exceeds threshold")
	}

	// Primary must no longer exist (it was renamed out of the way).
	if _, err := os.Stat(primary); !os.IsNotExist(err) {
		t.Fatalf("expected primary to be gone after rotation, got err=%v", err)
	}

	// Rotated file must exist and carry the original contents.
	rotatedPath := primary + ".1"
	data, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("reading rotated: %v", err)
	}
	if len(data) != 1024 {
		t.Fatalf("rotated size = %d, want 1024", len(data))
	}
	if data[0] != 'A' || data[len(data)-1] != 'A' {
		t.Fatalf("rotated contents corrupted: first=%q last=%q", data[0], data[len(data)-1])
	}
}

// TestRotateLogIfOversized_LeavesSmallFileAlone verifies a file at or below
// the cap is left in place and no rotated file is created.
func TestRotateLogIfOversized_LeavesSmallFileAlone(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "dolt-server.log")
	writeFileSize(t, primary, 100, 'B')

	rotated, err := rotateLogIfOversized(primary, 1024)
	if err != nil {
		t.Fatalf("rotateLogIfOversized: %v", err)
	}
	if rotated {
		t.Fatal("expected rotated=false when file is under threshold")
	}

	// Primary must still exist, untouched.
	info := mustStat(t, primary)
	if info.Size() != 100 {
		t.Fatalf("primary size changed: got %d, want 100", info.Size())
	}

	// No rotated file should have appeared.
	if _, err := os.Stat(primary + ".1"); !os.IsNotExist(err) {
		t.Fatalf("expected no .1 file, got err=%v", err)
	}
}

// TestRotateLogIfOversized_LeavesExactThresholdAlone verifies the boundary:
// a file whose size equals the threshold is not rotated. Only strictly
// greater triggers rotation, matching the documented policy.
func TestRotateLogIfOversized_LeavesExactThresholdAlone(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "dolt-server.log")
	writeFileSize(t, primary, 512, 'C')

	rotated, err := rotateLogIfOversized(primary, 512)
	if err != nil {
		t.Fatalf("rotateLogIfOversized: %v", err)
	}
	if rotated {
		t.Fatal("expected rotated=false when file size equals threshold")
	}
	if info := mustStat(t, primary); info.Size() != 512 {
		t.Fatalf("primary size changed: got %d, want 512", info.Size())
	}
}

// TestRotateLogIfOversized_OverwritesExistingRotated verifies that a prior
// .log.1 is silently overwritten when a new rotation happens. This is the
// single-generation retention contract.
func TestRotateLogIfOversized_OverwritesExistingRotated(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "dolt-server.log")
	rotatedPath := primary + ".1"

	// Pre-existing rotated file from a previous cycle — should be clobbered.
	writeFileSize(t, rotatedPath, 200, 'X')
	// Current primary is over threshold and carries fresh content.
	writeFileSize(t, primary, 2000, 'Y')

	rotated, err := rotateLogIfOversized(primary, 1024)
	if err != nil {
		t.Fatalf("rotateLogIfOversized: %v", err)
	}
	if !rotated {
		t.Fatal("expected rotated=true")
	}

	data, err := os.ReadFile(rotatedPath)
	if err != nil {
		t.Fatalf("reading rotated: %v", err)
	}
	if len(data) != 2000 {
		t.Fatalf("rotated size = %d, want 2000 (old .1 should have been overwritten)", len(data))
	}
	if data[0] != 'Y' {
		t.Fatalf("rotated contents = %q, want 'Y' (old 'X' content should be gone)", data[0])
	}
}

// TestRotateLogIfOversized_MissingPrimaryIsNoop verifies that a missing
// primary log file is treated as "nothing to do" rather than an error. This
// is the first-run case.
func TestRotateLogIfOversized_MissingPrimaryIsNoop(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "dolt-server.log")

	rotated, err := rotateLogIfOversized(primary, 1024)
	if err != nil {
		t.Fatalf("rotateLogIfOversized: %v", err)
	}
	if rotated {
		t.Fatal("expected rotated=false for missing file")
	}
	if _, err := os.Stat(primary + ".1"); !os.IsNotExist(err) {
		t.Fatalf("expected no .1 file, got err=%v", err)
	}
}

// TestRotateLogIfOversized_DisabledWhenMaxBytesZero verifies that passing
// maxBytes <= 0 disables rotation entirely, even for huge files. This is the
// escape hatch for users who want unlimited logs.
func TestRotateLogIfOversized_DisabledWhenMaxBytesZero(t *testing.T) {
	dir := t.TempDir()
	primary := filepath.Join(dir, "dolt-server.log")
	writeFileSize(t, primary, 4096, 'Z')

	rotated, err := rotateLogIfOversized(primary, 0)
	if err != nil {
		t.Fatalf("rotateLogIfOversized: %v", err)
	}
	if rotated {
		t.Fatal("expected rotated=false when maxBytes <= 0")
	}
	if info := mustStat(t, primary); info.Size() != 4096 {
		t.Fatalf("primary size changed: got %d, want 4096", info.Size())
	}
}

// TestMaybeRotateLog_EndToEnd exercises the higher-level wrapper used by
// Start(), ensuring it consults logPath() under the given beadsDir and
// rotates correctly. The env-var override path is also covered.
func TestMaybeRotateLog_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	primary := logPath(dir) // use the production path helper
	writeFileSize(t, primary, 10*1024, 'E')

	// Force a very small threshold via the env override so we don't need to
	// materialize a 50 MB file.
	t.Setenv(EnvMaxLogBytes, "1024")

	maybeRotateLog(dir)

	if _, err := os.Stat(primary); !os.IsNotExist(err) {
		t.Fatalf("expected primary rotated away, got err=%v", err)
	}
	if info := mustStat(t, primary+".1"); info.Size() != 10*1024 {
		t.Fatalf("rotated size = %d, want %d", info.Size(), 10*1024)
	}
}

// TestMaxLogBytes_EnvOverride verifies the env-var parsing contract: valid
// ints override the default, invalid values fall back to the default.
func TestMaxLogBytes_EnvOverride(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(EnvMaxLogBytes, "")
		if got := maxLogBytes(); got != DefaultMaxLogBytes {
			t.Fatalf("got %d, want %d", got, DefaultMaxLogBytes)
		}
	})
	t.Run("valid override", func(t *testing.T) {
		t.Setenv(EnvMaxLogBytes, "2048")
		if got := maxLogBytes(); got != 2048 {
			t.Fatalf("got %d, want 2048", got)
		}
	})
	t.Run("invalid falls back to default", func(t *testing.T) {
		t.Setenv(EnvMaxLogBytes, "not-a-number")
		if got := maxLogBytes(); got != DefaultMaxLogBytes {
			t.Fatalf("got %d, want %d", got, DefaultMaxLogBytes)
		}
	})
	t.Run("zero disables", func(t *testing.T) {
		t.Setenv(EnvMaxLogBytes, "0")
		if got := maxLogBytes(); got != 0 {
			t.Fatalf("got %d, want 0", got)
		}
	})
}
