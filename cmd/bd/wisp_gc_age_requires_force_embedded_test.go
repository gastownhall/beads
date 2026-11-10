//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestEmbeddedWispGCAgeWithoutForceDoesNotDelete reproduces GH#5152:
// `bd mol wisp gc --age` deletes abandoned wisps even without --force, although
// --force is documented as "Actually delete (default: preview only)" and the
// sibling `gc --closed` path does preview without it.
func TestEmbeddedWispGCAgeWithoutForceDoesNotDelete(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gc")

	idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID

	// No --force and no --dry-run: per the --force contract this must only preview.
	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s")
	t.Logf("gc --age 0s (no --force) output:\n%s", out)

	// The wisp must still exist: a dry-run must still list it as a candidate.
	dry := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--dry-run", "--json")
	var res struct {
		CleanedIDs []string `json:"cleaned_ids"`
	}
	start := strings.Index(dry, "{")
	if start < 0 {
		t.Fatalf("gc --json produced no JSON object\nraw:\n%s", dry)
	}
	if err := json.NewDecoder(strings.NewReader(dry[start:])).Decode(&res); err != nil {
		t.Fatalf("parse gc --json output: %v\nraw:\n%s", err, dry)
	}
	for _, id := range res.CleanedIDs {
		if id == idle {
			return
		}
	}
	t.Fatalf("gc --age 0s without --force deleted wisp %s (want preview only); remaining candidates=%v\nfirst run output:\n%s", idle, res.CleanedIDs, out)
}
