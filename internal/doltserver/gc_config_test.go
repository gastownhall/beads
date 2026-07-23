package doltserver

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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

// TestSupportsArchiveLevelConfig_Memoizes covers the nit fix
// (gastownhall/beads#4986 round 2): repeated calls for the same doltBin
// must not re-fork `dolt version`. We prove memoization behaviorally by
// rewriting the stub script in place after the first call — if
// SupportsArchiveLevelConfig actually re-exec'd, the second call would
// observe the new (older) version and flip to false; it must not.
// ResetArchiveLevelSupportCacheForTest then clears the cache so a
// subsequent call picks up the rewritten script, proving the cache (not
// some other invariant) was what made the second call stale.
func TestSupportsArchiveLevelConfig_Memoizes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub dolt binary is a POSIX shell script")
	}
	t.Cleanup(ResetArchiveLevelSupportCacheForTest)

	dir := t.TempDir()
	bin := filepath.Join(dir, "dolt")
	writeStub := func(versionLine string) {
		script := fmt.Sprintf("#!/bin/sh\necho %q\n", versionLine)
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, intentionally executable
			t.Fatalf("write stub dolt: %v", err)
		}
	}

	writeStub("dolt version 2.2.2")
	if !SupportsArchiveLevelConfig(bin) {
		t.Fatalf("expected support for dolt version 2.2.2 (>= %s)", MinDoltVersionForArchiveLevelConfig)
	}

	// Rewrite the SAME path to report an old version. A cached result must
	// survive this; an uncached (re-exec'd) call would flip to false.
	writeStub("dolt version 1.40.0")
	if !SupportsArchiveLevelConfig(bin) {
		t.Errorf("SupportsArchiveLevelConfig changed after rewriting the binary at the same path; expected the memoized result to stick")
	}

	ResetArchiveLevelSupportCacheForTest()
	if SupportsArchiveLevelConfig(bin) {
		t.Errorf("after ResetArchiveLevelSupportCacheForTest, expected the rewritten (old) version to be re-probed and return false")
	}
}
