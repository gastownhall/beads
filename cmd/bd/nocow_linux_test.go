//go:build linux

package main

import (
	"testing"
)

// TestApplyNoCOW_Linux verifies that applyNoCOW sets FS_NOCOW_FL on a
// directory when the underlying filesystem is btrfs. On non-btrfs
// temp-dirs the call must succeed as a silent no-op, because the ioctl
// is unsupported there. We skip the flag-set assertion unless we're
// actually on btrfs.
func TestApplyNoCOW_Linux(t *testing.T) {
	dir := t.TempDir()

	// applyNoCOW must not error on any filesystem (btrfs sets the flag,
	// ext4/tmpfs/etc. are no-ops).
	if err := applyNoCOW(dir); err != nil {
		t.Fatalf("applyNoCOW returned unexpected error: %v", err)
	}

	onBtrfs, err := isBtrfs(dir)
	if err != nil {
		t.Fatalf("isBtrfs: %v", err)
	}
	if !onBtrfs {
		t.Skipf("temp dir %s is not on btrfs; skipping flag-set assertion", dir)
	}

	set, err := hasNoCOW(dir)
	if err != nil {
		t.Fatalf("hasNoCOW: %v", err)
	}
	if !set {
		t.Errorf("expected FS_NOCOW_FL to be set on %s after applyNoCOW", dir)
	}
}

// TestApplyNoCOW_Idempotent verifies that calling applyNoCOW twice is
// safe and does not error on the second invocation. This matters because
// `bd doctor --fix` may re-apply the flag.
func TestApplyNoCOW_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := applyNoCOW(dir); err != nil {
		t.Fatalf("first applyNoCOW: %v", err)
	}
	if err := applyNoCOW(dir); err != nil {
		t.Fatalf("second applyNoCOW: %v", err)
	}
}

// TestHasNoCOW_UnsetOnFreshTempDir sanity-checks that a fresh temp dir
// does NOT have FS_NOCOW_FL set until we apply it. If the temp dir lives
// on a non-btrfs filesystem, hasNoCOW returns (false, nil) via the
// unsupported-error shim, which also satisfies this test.
func TestHasNoCOW_UnsetOnFreshTempDir(t *testing.T) {
	dir := t.TempDir()
	set, err := hasNoCOW(dir)
	if err != nil {
		t.Fatalf("hasNoCOW: %v", err)
	}
	if set {
		// It's technically possible for a temp dir to inherit the flag
		// from a parent directory that already had it set — skip in that
		// case rather than fail.
		t.Skipf("temp dir %s already has FS_NOCOW_FL inherited from parent", dir)
	}
}
