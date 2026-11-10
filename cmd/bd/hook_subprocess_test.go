package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestHookSubprocessBinary covers the hook child's binary selection
// (GH#5922): run the resolved self outside a test harness, with symlinks
// canonicalized, and fall back to the historical bare "bd" PATH lookup only
// for a self that cannot be resolved or a test binary — the fallback the
// stub-on-PATH hook fixtures rely on, and the guarantee the test executable
// is never launched recursively.
func TestHookSubprocessBinary(t *testing.T) {
	// selfPath creates a stand-in executable and returns its path, with the
	// temp dir canonicalized first so the assertions below compare resolved
	// paths even where the OS temp tree is itself reached through a symlink
	// (macOS /var).
	selfPath := func(t *testing.T, name string) string {
		t.Helper()
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("runs the resolved self outside a test harness", func(t *testing.T) {
		exe := selfPath(t, "bd")
		if got, want := hookSubprocessBinaryFor(exe, nil, false), exe; got != want {
			t.Errorf("hookSubprocessBinaryFor(%q, nil, false) = %q, want %q", exe, got, want)
		}
	})

	t.Run("canonicalizes a symlinked self to its target", func(t *testing.T) {
		target := selfPath(t, "bd-real")
		dir := filepath.Dir(target)
		link := filepath.Join(dir, "bd")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		if got, want := hookSubprocessBinaryFor(link, nil, false), target; got != want {
			t.Errorf("hookSubprocessBinaryFor(%q, nil, false) = %q, want the canonical %q", link, got, want)
		}
	})

	t.Run("keeps the absolute path when canonicalization fails", func(t *testing.T) {
		// A self whose path cannot be canonicalized (the binary was deleted
		// without being replaced) keeps its absolute path: matching
		// resolvePrimeExecutable, the PATH fallback is not taken when
		// os.Executable itself succeeded.
		exe := filepath.Join(t.TempDir(), "gone", "bd")
		if got, want := hookSubprocessBinaryFor(exe, nil, false), exe; got != want {
			t.Errorf("hookSubprocessBinaryFor(%q, nil, false) = %q, want the path kept %q", exe, got, want)
		}
	})

	t.Run("falls back to the PATH lookup when self cannot be resolved", func(t *testing.T) {
		if got, want := hookSubprocessBinaryFor("", errors.New("exec: no such file"), false), "bd"; got != want {
			t.Errorf("hookSubprocessBinaryFor with a discovery error = %q, want %q", got, want)
		}
	})

	t.Run("never re-execs the Bazel test binary", func(t *testing.T) {
		// rules_go links cmd/bd's test binary as bd_test, which no ".test"
		// name check can see; testing.Testing() is the authoritative signal
		// for it (mirroring doctor/fix's isTestBinary), so the harness arm is
		// what must refuse.
		if got, want := hookSubprocessBinaryFor("/rules/cache/bd_test", nil, true), "bd"; got != want {
			t.Errorf("hookSubprocessBinaryFor(bd_test in the harness) = %q, want the PATH fallback %q", got, want)
		}
	})

	t.Run("never re-execs a go test -c binary", func(t *testing.T) {
		for _, name := range []string{"bd.test", "bd.test.a1b2c3"} {
			exe := filepath.Join(t.TempDir(), name)
			if got, want := hookSubprocessBinaryFor(exe, nil, false), "bd"; got != want {
				t.Errorf("hookSubprocessBinaryFor(%q) = %q, want the PATH fallback %q", name, got, want)
			}
		}
	})

	t.Run("in a test process the default stays on the PATH lookup", func(t *testing.T) {
		// The in-process default must resolve the stub-on-PATH fixtures'
		// stand-in bd and never the test executable itself.
		if got, want := hookSubprocessBinary(), "bd"; got != want {
			t.Errorf("hookSubprocessBinary() in a test process = %q, want %q", got, want)
		}
	})
}
