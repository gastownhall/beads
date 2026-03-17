package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// hasTestSkip returns true if the given service appears in the BEADS_TEST_SKIP
// env var (comma-separated list). Example: BEADS_TEST_SKIP=dolt,slow
func hasTestSkip(service string) bool {
	val := os.Getenv("BEADS_TEST_SKIP")
	if val == "" {
		return false
	}
	for _, s := range strings.Split(val, ",") {
		if strings.TrimSpace(s) == service {
			return true
		}
	}
	return false
}

// RequireDoltCLI skips the test when the host dolt CLI is unavailable or the
// shared Dolt test skip contract is active via BEADS_TEST_SKIP=dolt.
func RequireDoltCLI(t *testing.T) {
	t.Helper()
	if hasTestSkip("dolt") {
		t.Skip("skipping test: Dolt tests skipped (BEADS_TEST_SKIP=dolt)")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("skipping test: dolt CLI not found on PATH")
	}
}
