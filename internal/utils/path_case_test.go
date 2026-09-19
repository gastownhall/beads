//go:build darwin

package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestResolveCanonicalCase_FastPathMatchesWalk pins the F_GETPATH fast path to
// the semantics of the portable component walk it short-circuits: for the same
// input both must produce the same true-cased path, however the caller spelled
// it, and both must decline the same way for a path that does not exist.
func TestResolveCanonicalCase_FastPathMatchesWalk(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("true-case resolution only applies on darwin")
	}

	root := t.TempDir()
	// The walk starts at "/" and matches every ancestor, so it only agrees with
	// the kernel when the input is already symlink-resolved (/var -> /private/var
	// on macOS). CanonicalizePath guarantees that before calling in.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", root, err)
	}

	dir := filepath.Join(realRoot, "MixedCase.Dir")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(dir, "SomeFile.JSONL")
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cases := []struct {
		name  string
		input string
	}{
		{"dir_exact", dir},
		{"dir_lowered", filepath.Join(realRoot, "mixedcase.dir")},
		{"dir_uppered", filepath.Join(realRoot, "MIXEDCASE.DIR")},
		{"file_exact", file},
		{"file_lowered", filepath.Join(realRoot, "mixedcase.dir", "somefile.jsonl")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fast := resolveCanonicalCase(tc.input)
			walk := resolveCanonicalCaseWalk(tc.input)
			if walk == "" {
				t.Fatalf("walk declined %q; test setup is wrong", tc.input)
			}
			if fast != walk {
				t.Fatalf("resolveCanonicalCase(%q) = %q, walk = %q", tc.input, fast, walk)
			}
			// And it really is the authored case, not the caller's spelling.
			if !strings.Contains(fast, "MixedCase.Dir") {
				t.Fatalf("resolved %q did not recover the authored case", fast)
			}
		})
	}

	t.Run("missing_component", func(t *testing.T) {
		missing := filepath.Join(realRoot, "MixedCase.Dir", "nope", "deeper")
		if got := resolveCanonicalCase(missing); got != "" {
			t.Fatalf("resolveCanonicalCase(%q) = %q, want \"\"", missing, got)
		}
		if got := resolveCanonicalCaseWalk(missing); got != "" {
			t.Fatalf("resolveCanonicalCaseWalk(%q) = %q, want \"\"", missing, got)
		}
	})

	t.Run("fifo_falls_back_to_walk_without_blocking", func(t *testing.T) {
		// Opening a FIFO O_RDONLY blocks until a writer appears; the fast path
		// must decline it and let the walk (which opens nothing) resolve it.
		fifo := filepath.Join(dir, "Pipe.Fifo")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Skipf("mkfifo unavailable: %v", err)
		}
		done := make(chan string, 1)
		go func() { done <- resolveCanonicalCase(filepath.Join(dir, "pipe.fifo")) }()
		select {
		case got := <-done:
			if want := resolveCanonicalCaseWalk(filepath.Join(dir, "pipe.fifo")); got != want {
				t.Fatalf("resolveCanonicalCase(fifo) = %q, walk = %q", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("resolveCanonicalCase blocked on a FIFO")
		}
	})

	t.Run("not_a_directory_component", func(t *testing.T) {
		// A regular file used as an intermediate component: ENOTDIR, which the
		// walk also declines.
		through := filepath.Join(file, "child")
		if got := resolveCanonicalCase(through); got != "" {
			t.Fatalf("resolveCanonicalCase(%q) = %q, want \"\"", through, got)
		}
	})
}

// TestCanonicalizePath_DoesNotScanAncestorDirectories is the regression test
// for wy-9ai3u: CanonicalizePath sits on the hot path of FindBeadsDir, so its
// cost must not grow with the number of entries in an ancestor directory. The
// pre-fix component walk did an os.ReadDir of every ancestor on every call,
// which on a $TMPDIR holding ~100k leftover test directories cost ~640ms per
// call and turned whole test packages (internal/audit, embeddeddolt, cmd/bd)
// into apparent hangs.
func TestCanonicalizePath_DoesNotScanAncestorDirectories(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only darwin takes the true-case resolution path")
	}

	root := t.TempDir()
	crowded := filepath.Join(root, "crowded")
	if err := os.MkdirAll(crowded, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Enough siblings that an O(entries) walk is measurably slower than an
	// O(1) kernel query, but cheap enough to create in a unit test.
	const siblings = 4000
	for i := 0; i < siblings; i++ {
		if err := os.Mkdir(filepath.Join(crowded, "sib"+strconv.Itoa(i)), 0o750); err != nil {
			t.Fatalf("mkdir sibling %d: %v", i, err)
		}
	}
	target := filepath.Join(crowded, "sib0")

	// Time the two implementations against each other rather than against a
	// wall-clock budget: this suite runs on loaded multi-agent machines where
	// an absolute per-call budget is a flake, but the ratio between the two
	// holds because both pay the same scheduling tax.
	const (
		fastIters = 200
		walkIters = 10 // the walk is ~1000x slower; 10 is plenty to rate it
	)
	perCall := func(f func(string) string, n int) time.Duration {
		start := time.Now()
		for i := 0; i < n; i++ {
			if got := f(target); got == "" {
				t.Fatalf("resolver returned empty for %q", target)
			}
		}
		return time.Since(start) / time.Duration(n)
	}
	// Warm the vnode/dirent caches so neither implementation absorbs the
	// cold-start cost of the other.
	perCall(resolveCanonicalCase, 1)
	perCall(resolveCanonicalCaseWalk, 1)

	fast := perCall(resolveCanonicalCase, fastIters)
	walk := perCall(resolveCanonicalCaseWalk, walkIters)

	// The walk does a full ReadDir of |crowded| per call; the kernel query does
	// not read the directory at all. At this fan-out the real gap is three
	// orders of magnitude, so 4x is a floor that only trips if the ancestor
	// scan has crept back onto the hot path.
	if fast*4 > walk {
		t.Fatalf("per call: fast path %v vs component walk %v — expected the fast path to be >4x cheaper; the O(entries) ancestor scan is back",
			fast, walk)
	}
	t.Logf("per call: fast path %v, component walk %v (%d siblings)", fast, walk, siblings)
}
