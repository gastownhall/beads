package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDoltVersionAtLeast(t *testing.T) {
	tests := []struct {
		name   string
		output string
		minVer string
		want   bool
	}{
		{"exact match", "dolt version 1.52.1\n", "1.52.1", true},
		{"newer patch", "dolt version 1.52.3\n", "1.52.1", true},
		{"newer minor", "dolt version 1.53.0\n", "1.52.1", true},
		{"newer major (sequential dolt 2.x)", "dolt version 2.2.2\n", "1.52.1", true},
		{"older patch", "dolt version 1.52.0\n", "1.52.1", false},
		{"older minor", "dolt version 1.51.9\n", "1.52.1", false},
		{"older major", "dolt version 0.99.9\n", "1.52.1", false},
		{"extra trailing lines ignored", "dolt version 1.52.1\ndatabase storage format: __DOLT__\n", "1.52.1", true},
		{"missing patch segment treated as 0", "dolt version 1.52\n", "1.52.1", false},
		{"missing patch segment, still newer", "dolt version 1.53\n", "1.52.1", true},
		{"empty output", "", "1.52.1", false},
		{"non-numeric version", "dolt version dev\n", "1.52.1", false},
		{"malformed no version token", "\n", "1.52.1", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := doltVersionAtLeast(tc.output, tc.minVer); got != tc.want {
				t.Errorf("doltVersionAtLeast(%q, %q) = %v, want %v", tc.output, tc.minVer, got, tc.want)
			}
		})
	}
}

// TestSupportsArchiveLevelConfig_FailsClosed asserts that a doltBin which
// cannot be executed at all (nonexistent path) is treated as unsupported
// rather than panicking or erroring the caller.
func TestSupportsArchiveLevelConfig_FailsClosed(t *testing.T) {
	if got := SupportsArchiveLevelConfig(filepath.Join(t.TempDir(), "no-such-dolt-binary")); got {
		t.Errorf("SupportsArchiveLevelConfig with a nonexistent binary = true, want false (fail closed)")
	}
}

// TestSupportsArchiveLevelConfig_WithFakeBinary exercises the exec.Command
// wrapper end-to-end against a stub script that mimics `dolt version`
// output, rather than only the pure-parsing doltVersionAtLeast helper.
// Skipped on Windows: the stub is a POSIX shell script.
func TestSupportsArchiveLevelConfig_WithFakeBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub dolt binary is a POSIX shell script")
	}
	t.Cleanup(ResetArchiveLevelSupportCacheForTest)

	newStub := func(t *testing.T, versionLine string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "dolt")
		script := fmt.Sprintf("#!/bin/sh\necho %q\n", versionLine)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, intentionally executable
			t.Fatalf("write stub dolt: %v", err)
		}
		return path
	}

	t.Run("new enough", func(t *testing.T) {
		bin := newStub(t, "dolt version 2.2.2")
		if !SupportsArchiveLevelConfig(bin) {
			t.Errorf("expected support for dolt version 2.2.2 (>= %s)", MinDoltVersionForArchiveLevelConfig)
		}
	})

	t.Run("too old", func(t *testing.T) {
		bin := newStub(t, "dolt version 1.40.0")
		if SupportsArchiveLevelConfig(bin) {
			t.Errorf("expected no support for dolt version 1.40.0 (< %s)", MinDoltVersionForArchiveLevelConfig)
		}
	})
}

// callCountingStub writes a stub "dolt" script at dir/dolt that appends one
// byte to countPath on every invocation (so probeCount below can observe
// exactly how many times it actually ran) before echoing versionLine, and
// returns (binPath, writeVersion, probeCount).
func callCountingStub(t *testing.T, dir, versionLine string) (bin string, writeVersion func(string), probeCount func() int) {
	t.Helper()
	bin = filepath.Join(dir, "dolt")
	countPath := filepath.Join(dir, "calls")

	writeVersion = func(line string) {
		t.Helper()
		script := fmt.Sprintf("#!/bin/sh\nprintf x >> %q\necho %q\n", countPath, line)
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, intentionally executable
			t.Fatalf("write stub dolt: %v", err)
		}
	}
	probeCount = func() int {
		t.Helper()
		data, err := os.ReadFile(countPath)
		if err != nil {
			if os.IsNotExist(err) {
				return 0
			}
			t.Fatalf("read call counter: %v", err)
		}
		return len(data)
	}

	writeVersion(versionLine)
	return bin, writeVersion, probeCount
}

// TestSupportsArchiveLevelConfig_MemoizesUnchangedBinary covers the nit fix
// (gastownhall/beads#4986 round 2): repeated calls against the SAME,
// UNCHANGED on-disk binary must not re-fork `dolt version`. Proven directly
// via a call counter embedded in the stub script, rather than only
// inferring it from the returned bool.
func TestSupportsArchiveLevelConfig_MemoizesUnchangedBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub dolt binary is a POSIX shell script")
	}
	t.Cleanup(ResetArchiveLevelSupportCacheForTest)

	bin, _, probeCount := callCountingStub(t, t.TempDir(), "dolt version 2.2.2")

	if !SupportsArchiveLevelConfig(bin) {
		t.Fatalf("expected support for dolt version 2.2.2 (>= %s)", MinDoltVersionForArchiveLevelConfig)
	}
	if got := probeCount(); got != 1 {
		t.Fatalf("expected exactly 1 probe after the first call, got %d", got)
	}

	if !SupportsArchiveLevelConfig(bin) {
		t.Errorf("cached result changed on a second call against the unchanged binary")
	}
	if got := probeCount(); got != 1 {
		t.Errorf("expected the second call against an unchanged binary to hit the cache (still 1 probe), got %d", got)
	}
}

// TestSupportsArchiveLevelConfig_InvalidatesOnFileIdentityChange covers the
// round-3 minor fix (gastownhall/beads#4986): the cache key includes
// size+mtime, not just the path string, so an in-place dolt
// upgrade/downgrade at the same path is re-probed on its very next call
// rather than serving a stale verdict for the rest of the process's
// lifetime. os.Chtimes forces a distinct mtime deterministically (no real
// sleep, robust to coarse filesystem mtime resolution); both stub scripts
// have identical byte length, so this specifically exercises mtime-based
// invalidation rather than accidentally relying on a size change.
func TestSupportsArchiveLevelConfig_InvalidatesOnFileIdentityChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub dolt binary is a POSIX shell script")
	}
	t.Cleanup(ResetArchiveLevelSupportCacheForTest)

	bin, writeVersion, probeCount := callCountingStub(t, t.TempDir(), "dolt version 2.2.2")

	info1, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("os.Stat: %v", err)
	}

	if !SupportsArchiveLevelConfig(bin) {
		t.Fatalf("expected support for dolt version 2.2.2 (>= %s)", MinDoltVersionForArchiveLevelConfig)
	}
	if got := probeCount(); got != 1 {
		t.Fatalf("expected exactly 1 probe after the first call, got %d", got)
	}

	// Rewrite the SAME path with an older version (same byte length as the
	// first stub — this isolates the mtime-based half of the cache key from
	// the size-based half), then force a distinct mtime.
	writeVersion("dolt version 1.40.0")
	newMtime := info1.ModTime().Add(time.Second)
	if err := os.Chtimes(bin, newMtime, newMtime); err != nil {
		t.Fatalf("os.Chtimes: %v", err)
	}

	if SupportsArchiveLevelConfig(bin) {
		t.Errorf("expected the rewritten (older) binary to invalidate the cache and report unsupported")
	}
	if got := probeCount(); got != 2 {
		t.Errorf("expected a fresh probe (2 total) after the binary's file identity changed, got %d", got)
	}
}
