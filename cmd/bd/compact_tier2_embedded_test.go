//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedAdminCompactTier2Honesty verifies that the unimplemented Tier 2
// is not advertised as a working capability: invoking it errors with a clear
// "not yet implemented" message, and --stats does not promise Tier 2 savings.
func TestEmbeddedAdminCompactTier2Honesty(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "t2")

	t.Run("tier2_rejected", func(t *testing.T) {
		// --analyze reaches the tier guard (read-only, direct-mode path).
		cmd := exec.Command(bd, "admin", "compact", "--analyze", "--tier", "2")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected --tier 2 to fail, but succeeded:\n%s", out)
		}
		if !strings.Contains(string(out), "not yet implemented") {
			t.Errorf("expected 'not yet implemented' message, got:\n%s", out)
		}
	})

	t.Run("stats_does_not_advertise_tier2", func(t *testing.T) {
		cmd := exec.Command(bd, "admin", "compact", "--stats")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd admin compact --stats failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "not yet implemented") {
			t.Errorf("expected Tier 2 to be marked 'not yet implemented' in --stats, got:\n%s", out)
		}
		if strings.Contains(out, "95%") {
			t.Errorf("--stats still advertises a Tier 2 savings estimate (95%%):\n%s", out)
		}
	})
}
