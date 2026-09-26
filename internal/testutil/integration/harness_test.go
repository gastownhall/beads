//go:build integration && !windows

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildErrorIsMaskedByChmod guards against SubprocessRunner.Build's
// unconditional os.Chmod overwriting a real build failure with a spurious
// ENOENT, discarding the compiler diagnostics captured in stderr (be-4ecr4).
func TestBuildErrorIsMaskedByChmod(t *testing.T) {
	r := NewSubprocessRunner(".", "./this-package-does-not-exist/")
	done := make(chan struct{})
	go func() {
		defer close(done) // Build's t.Fatalf calls Goexit; the deferred close still runs.
		// Build reports failure through t.Fatalf, so a real *testing.T would abort
		// the assertions below instead of letting them inspect r.buildErr.
		r.Build(&testing.T{})
	}()
	<-done // Publishes r.buildErr and r.testBin to this goroutine.

	// The synthetic T is never finalized by the framework, so the Cleanup that
	// its t.TempDir() registered never runs. Remove the directory from the real
	// t instead: TempDir hands back <mkdtemp-parent>/001, so deleting only the
	// binary's own directory would still orphan the parent under $TMPDIR.
	t.Cleanup(func() {
		if r.testBin == "" {
			return
		}
		tmpRoot := filepath.Clean(os.TempDir())
		parent := filepath.Dir(filepath.Dir(r.testBin))
		// Only ever remove a proper descendant of $TMPDIR; if the zero-value
		// TempDir layout this walks up from ever changes, skip rather than
		// delete something we did not create.
		if !strings.HasPrefix(parent, tmpRoot+string(filepath.Separator)) {
			t.Logf("skipping cleanup of unexpected temp path: %s", parent)
			return
		}
		if err := os.RemoveAll(parent); err != nil {
			t.Logf("failed to remove leaked temp dir %s: %v", parent, err)
		}
	})

	if r.buildErr == nil {
		t.Fatalf("expected a build error, got nil")
	}
	got := r.buildErr.Error()
	t.Logf("reported error: %s", got)
	if !strings.Contains(got, "failed to build test binary") {
		t.Errorf("build error does not contain %q: %s", "failed to build test binary", got)
	}
	if strings.Contains(got, "chmod") {
		t.Errorf("build error still mentions chmod — real build failure was masked: %s", got)
	}
	// `go test -c` splits this failure across both channels: the compiler detail
	// goes to stderr, the package verdict to stdout. Pin the stdout half so
	// build.Stdout cannot be unwired without reddening a test. Asserting on the
	// "stdout:" label instead would be vacuous — it lives in the format string
	// and survives even when the buffer is never attached.
	if !strings.Contains(got, "[setup failed]") {
		t.Errorf("build error dropped stdout diagnostics: %s", got)
	}
}
