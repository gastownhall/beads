//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestWispGCProtectsActiveWisps is the regression test for GH#4394:
// `bd mol wisp gc --age` deleted active (blocked/in-progress) molecule
// steps. is_blocked is derived state and a recompute deliberately does NOT bump
// updated_at, so a step that has been waiting (blocked) longer than --age looks
// stale and was reclaimed mid-execution, self-destructing the molecule. Active
// wisps must never be reclaimed by age, no matter how old their updated_at is.
func TestWispGCProtectsActiveWisps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gc")

	// Genuinely abandoned: an idle open ephemeral wisp. Must be reclaimed.
	idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID

	// Active molecule: parent step (the blocker) + child step blocked on it.
	// The child is is_blocked=1 and must be protected.
	parent := bdCreate(t, bd, dir, "parent step", "--ephemeral").ID
	blocked := bdCreate(t, bd, dir, "blocked step", "--ephemeral").ID
	bdDepAdd(t, bd, dir, blocked, parent)

	// In-progress step must also be protected.
	inProgress := bdCreate(t, bd, dir, "running step", "--ephemeral").ID
	bdCommand(t, bd, dir, "update", inProgress, "--status", "in_progress")

	// Every wisp above was created well over 1ms ago by the time gc runs, so a
	// 1ms threshold makes them all "stale" by updated_at. Only the genuinely
	// abandoned ones should be reclaimed.
	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "1ms", "--dry-run", "--json")

	var res struct {
		CleanedIDs []string `json:"cleaned_ids"`
	}
	s := out[strings.Index(out, "{"):]
	if err := json.NewDecoder(strings.NewReader(s)).Decode(&res); err != nil {
		t.Fatalf("parse gc --json output: %v\nraw:\n%s", err, out)
	}
	candidates := make(map[string]bool, len(res.CleanedIDs))
	for _, id := range res.CleanedIDs {
		candidates[id] = true
	}

	if !candidates[idle] {
		t.Errorf("idle open wisp %s should be a GC candidate, got candidates=%v", idle, res.CleanedIDs)
	}
	if candidates[blocked] {
		t.Errorf("blocked wisp %s must NOT be reclaimed by age (GH#4394); candidates=%v", blocked, res.CleanedIDs)
	}
	if candidates[inProgress] {
		t.Errorf("in-progress wisp %s must NOT be reclaimed by age (GH#4394); candidates=%v", inProgress, res.CleanedIDs)
	}
}
