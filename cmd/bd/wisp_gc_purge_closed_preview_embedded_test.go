//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestWispGCPurgeClosedPreviewSkipsLiveDependents is the regression test for
// #5753: `bd mol wisp gc --closed --dry-run` hard-failed the whole batch with
// DependentsOutsideRequestError the moment a single closed candidate still had
// a live dependent outside the batch. One live molecule step therefore blocked
// the preview of every other eligible closed wisp. The non-force preview must
// skip-with-notice the protected candidates and preview the rest, exiting 0.
func TestWispGCPurgeClosedPreviewSkipsLiveDependents(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pcs")

	// A live molecule prefix: step1 <- step2 <- step3, where step1 and step2 are
	// closed but step3 (open) still depends on step2. step2 has a live dependent
	// (step3) and step1 gates the surviving step2 — so BOTH closed steps must be
	// skipped. This chained shape is why the safe set has to be a fixed point:
	// dropping step2 makes step1 unsafe too, and a single pass would wrongly mark
	// step1 deletable and re-trip DependentsOutsideRequestError.
	step1 := bdCreate(t, bd, dir, "closed chain root", "--ephemeral").ID
	step2 := bdCreate(t, bd, dir, "closed step gating live work", "--ephemeral").ID
	step3 := bdCreate(t, bd, dir, "live step", "--ephemeral").ID
	bdDepAdd(t, bd, dir, step2, step1) // step2 depends on step1
	bdDepAdd(t, bd, dir, step3, step2) // step3 depends on step2
	bdClose(t, bd, dir, step1)
	bdClose(t, bd, dir, step2)

	// A standalone closed wisp with no dependents — safe to purge.
	safeWisp := bdCreate(t, bd, dir, "standalone closed wisp", "--ephemeral").ID
	bdClose(t, bd, dir, safeWisp)

	out, err := bdRunWithFlockRetry(t, bd, dir, "mol", "wisp", "gc", "--closed", "--dry-run")
	if err != nil {
		t.Fatalf("gc --closed --dry-run must not fail the whole batch over per-candidate hazards; got error: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, safeWisp) {
		t.Errorf("preview should list the safe closed wisp %s as deletable; output:\n%s", safeWisp, got)
	}
	for _, id := range []string{step1, step2} {
		if !strings.Contains(got, id) {
			t.Errorf("preview should report the protected closed wisp %s as skipped; output:\n%s", id, got)
		}
	}
	if !strings.Contains(got, step3) {
		t.Errorf("skip notice should name the live dependent %s that protected the chain; output:\n%s", step3, got)
	}
}
