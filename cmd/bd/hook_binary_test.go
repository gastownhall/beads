package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestHookSubprocessBinary pins how the git-hook lanes pick their `bd export`
// and `bd import` child (GH#5922): the running bd, symlinks resolved, and
// plain `bd` from PATH only when the running binary is a test binary or
// cannot be discovered.
func TestHookSubprocessBinary(t *testing.T) {
	// Under go test and Bazel (cmd/bd's test binary is bd_test, which no
	// ".test" name check sees) the lanes must find the stub bd hook tests put
	// on PATH, never launch this test binary again.
	t.Run("test executable selects bd from PATH", func(t *testing.T) {
		if got := hookSubprocessBinary(); got != "bd" {
			t.Fatalf("hookSubprocessBinary() = %q inside a test binary, want %q", got, "bd")
		}
	})

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(dir, "install dir", "bd")
	if err := os.MkdirAll(filepath.Dir(installed), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	notTest := func(string) bool { return false }

	t.Run("running binary", func(t *testing.T) {
		if got := pickHookSubprocessBinary(installed, nil, notTest); got != installed {
			t.Fatalf("pick = %q, want the running binary %q", got, installed)
		}
	})
	t.Run("symlink resolves to its target", func(t *testing.T) {
		link := filepath.Join(dir, "link-bd")
		if err := os.Symlink(installed, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := pickHookSubprocessBinary(link, nil, notTest); got != installed {
			t.Fatalf("pick = %q, want the symlink's target %q", got, installed)
		}
	})
	t.Run("unresolvable path is kept, not traded for PATH", func(t *testing.T) {
		gone := filepath.Join(dir, "deleted", "bd")
		if got := pickHookSubprocessBinary(gone, nil, notTest); got != gone {
			t.Fatalf("pick = %q, want the running binary's path %q", got, gone)
		}
	})
	t.Run("undiscoverable binary falls back to bd", func(t *testing.T) {
		if got := pickHookSubprocessBinary("", errors.New("no /proc"), notTest); got != "bd" {
			t.Fatalf("pick = %q, want %q", got, "bd")
		}
	})
	t.Run("test binary falls back to bd", func(t *testing.T) {
		var checked string
		isTest := func(exe string) bool { checked = exe; return true }
		if got := pickHookSubprocessBinary(installed, nil, isTest); got != "bd" {
			t.Fatalf("pick = %q, want %q", got, "bd")
		}
		if checked != installed {
			t.Fatalf("test-binary check saw %q, want the resolved path %q", checked, installed)
		}
	})
}
