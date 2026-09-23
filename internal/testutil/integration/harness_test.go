//go:build integration && !windows

package integration

import (
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
		r.Build(&testing.T{})
	}()
	<-done

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
}
