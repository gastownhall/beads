//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestEmbeddedCloseWhitespaceIDHint is the direct-route command coverage for
// GH#5152's close remedy: `bd close $LIST` with an unquoted zsh variable
// arrives as ONE argv token, and its not-found error must keep its identity
// while saying how to pass the ids the user meant. A single unknown id keeps
// today's error, byte for byte.
func TestEmbeddedCloseWhitespaceIDHint(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cwh")

	out := bdCloseFail(t, bd, dir, "a b c")
	if !strings.Contains(out, `no issue found matching "a b c"`) {
		t.Errorf("not-found identity changed; output:\n%s", out)
	}
	if !strings.Contains(out, "pass each ID as its own argument") {
		t.Errorf("whitespace argument must carry the separate-argument hint; output:\n%s", out)
	}
	if !strings.Contains(out, "${=IDS}") {
		t.Errorf("hint must name the zsh remedy; output:\n%s", out)
	}

	out = bdCloseFail(t, bd, dir, "no-such-wisp")
	if !strings.Contains(out, `no issue found matching "no-such-wisp"`) {
		t.Errorf("not-found identity changed for a single unknown id; output:\n%s", out)
	}
	if strings.Contains(out, "pass each ID") {
		t.Errorf("single unknown id must not carry the whitespace hint; output:\n%s", out)
	}
}
