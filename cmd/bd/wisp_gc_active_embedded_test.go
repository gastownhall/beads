//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestEmbeddedWispGCProtectsActiveWisps is the regression test for GH#4394:
// `bd mol wisp gc --age` deleted active (blocked/in-progress) molecule
// steps. is_blocked is derived state and a recompute deliberately does NOT bump
// updated_at, so a step that has been waiting (blocked) longer than --age looks
// stale and was reclaimed mid-execution, self-destructing the molecule. Active
// wisps must never be reclaimed by age, no matter how old their updated_at is.
func TestEmbeddedWispGCProtectsActiveWisps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gc")

	// Custom statuses participate on the same footing as built-ins, by
	// category. "reviewing" is wip (protected); "triaging" is active, which
	// behaves like plain open and stays reclaimable — that pair also guards
	// against the predicate degenerating into "protect everything".
	bdCommand(t, bd, dir, "config", "set", "status.custom", "reviewing:wip,triaging:active")

	// Genuinely abandoned: an idle open ephemeral wisp. Must be reclaimed.
	idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID

	// A custom status in the active category behaves like plain open.
	customActive := bdCreate(t, bd, dir, "custom active step", "--ephemeral").ID
	bdCommand(t, bd, dir, "update", customActive, "--status", "triaging")

	// Active molecule: parent step (the blocker) + child step blocked on it.
	// The child is is_blocked=1 and must be protected.
	parent := bdCreate(t, bd, dir, "parent step", "--ephemeral").ID
	blocked := bdCreate(t, bd, dir, "blocked step", "--ephemeral").ID
	bdDepAdd(t, bd, dir, blocked, parent)

	// In-progress step must also be protected.
	inProgress := bdCreate(t, bd, dir, "running step", "--ephemeral").ID
	bdCommand(t, bd, dir, "update", inProgress, "--status", "in_progress")

	// Frozen-category statuses are work deliberately put on ice; reclaiming
	// something a user explicitly deferred defeats the point of deferring it.
	deferred := bdCreate(t, bd, dir, "deferred step", "--ephemeral").ID
	bdCommand(t, bd, dir, "update", deferred, "--status", "deferred")
	pinned := bdCreate(t, bd, dir, "pinned step", "--ephemeral").ID
	bdCommand(t, bd, dir, "update", pinned, "--status", "pinned")

	// A custom status in the wip category is protected like a built-in one.
	customWIP := bdCreate(t, bd, dir, "custom wip step", "--ephemeral").ID
	bdCommand(t, bd, dir, "update", customWIP, "--status", "reviewing")

	// --age 0s makes every wisp above old enough to reclaim. Any positive --age
	// can leave the most recently touched one reading as too young, because
	// updated_at is stored at whole-second precision and Dolt rounds the write
	// up to half a second into the future (#7236). Protection is checked
	// before age, so only the genuinely abandoned ones should be reclaimed.
	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--dry-run", "--json")

	var res struct {
		CleanedIDs []string `json:"cleaned_ids"`
	}
	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("gc --json produced no JSON object\nraw:\n%s", out)
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&res); err != nil {
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
	for _, tc := range []struct {
		id   string
		what string
	}{
		{deferred, "deferred"},
		{pinned, "pinned-status"},
		{customWIP, "custom wip-category"},
	} {
		if candidates[tc.id] {
			t.Errorf("%s wisp %s must NOT be reclaimed by age; candidates=%v", tc.what, tc.id, res.CleanedIDs)
		}
	}
	if !candidates[customActive] {
		t.Errorf("custom active-category wisp %s should stay a GC candidate (guard must not over-protect), got candidates=%v", customActive, res.CleanedIDs)
	}
}
