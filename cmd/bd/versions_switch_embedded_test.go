//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestVersionedHistorySwitchRoundTrip_Embedded drives the REAL
// `bd config set versioned-history.enabled true` through to a real
// `bd versions`, against a real store.
//
// This exists because the two ends disagreed and no unit test could see it.
// `bd config set` routes a key that is not yaml-only to the workspace
// DATABASE; the readers called config.GetBool, which is viper (yaml + env)
// only. So the command the help text and the off-refusal both named wrote one
// plane while the reader read the other: the user ran exactly what they were
// told, was told it worked, and recording stayed off. A fake-backed unit test
// cannot catch that, because the bug IS the difference between the two real
// planes. (bee-ghosttrack, #6661 review, finding 1: "a test that drives the
// real bd config set -> reader round trip would have caught this and would
// keep it caught.")
//
// The env var is explicitly cleared for the write and read below, so a
// passing run proves the STORE setting alone carried it.
func TestVersionedHistorySwitchRoundTrip_Embedded(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	// No t.Parallel: t.Setenv below is incompatible with it, and clearing the
	// env var is the whole point — a pass has to prove the STORE setting
	// carried the switch on its own.
	t.Setenv("BD_VERSIONED_HISTORY_ENABLED", "")

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "vsw")

	created := bdRunOK(t, bd, dir, "create", "switch round trip")
	id := extractFirstID(created, "vsw-")
	if id == "" {
		t.Fatalf("could not find a vsw- id in create output: %s", created)
	}

	// Off by default: a bead with no recorded versions must refuse, not
	// report an empty history.
	if out, code := bdRunFailCode(t, bd, dir, "versions", id); code == 0 {
		t.Errorf("bd versions succeeded with recording off and nothing recorded; want a refusal. out=%s", out)
	} else if !strings.Contains(out, "bd config set versioned-history.enabled true") {
		t.Errorf("the refusal must name the command that actually turns it on; got: %s", out)
	}

	// The advertised command. If this writes a plane the reader does not
	// consult, everything below fails.
	bdConfig(t, bd, dir, "set", "versioned-history.enabled", "true")

	bdRunOK(t, bd, dir, "update", id, "--title", "switch round trip, edited")

	out := bdRunOK(t, bd, dir, "versions", id)
	if !strings.Contains(out, "Versions of "+id) {
		t.Fatalf("no versions listed after enabling via bd config set — the write and read planes have diverged again. out=%s", out)
	}
	if strings.Contains(out, "Recording is currently OFF") {
		t.Errorf("recording reported OFF immediately after bd config set enabled it: %s", out)
	}

	// Turning it back off must not hide what was already recorded: that is a
	// claim about the store made from a fact about this invocation's config
	// (same review, finding 3).
	bdConfig(t, bd, dir, "set", "versioned-history.enabled", "false")

	off := bdRunOK(t, bd, dir, "versions", id)
	if !strings.Contains(off, "Versions of "+id) {
		t.Errorf("recorded versions vanished when recording was switched off; they are still real. out=%s", off)
	}
	if !strings.Contains(off, "Recording is currently OFF") {
		t.Errorf("a listing taken while recording is off must say so, or it reads as current: %s", off)
	}
}

// extractFirstID pulls the first whitespace-delimited token carrying prefix
// out of command output.
func extractFirstID(out, prefix string) string {
	for _, f := range strings.FieldsFunc(out, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == ':' || r == '\r'
	}) {
		if strings.HasPrefix(f, prefix) {
			return strings.TrimRight(f, ".,")
		}
	}
	return ""
}
