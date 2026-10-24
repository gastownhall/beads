package main

import (
	"os"
	"testing"
)

// usePipedStdinForInteractionTest makes terminal detection deterministic even
// when the test binary is launched directly from a terminal. Tests stay serial.
func usePipedStdinForInteractionTest(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = r.Close()
		_ = w.Close()
	})
}
