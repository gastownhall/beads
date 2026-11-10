//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

const closeWhitespaceHintText = "pass each ID as its own argument"

// TestEmbeddedCloseWhitespaceIDHint is GH#5152's close-side papercut: a shell
// variable holding several IDs, passed as one word (zsh does not split
// unquoted $IDS), reached `bd close` as a single argument and failed with a
// bare not-found. The not-found error stays, and gains a hint to pass each ID
// separately; nothing is closed, and a single unknown ID keeps its existing
// error without the hint.
func TestEmbeddedCloseWhitespaceIDHint(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cw")

	a := bdCreate(t, bd, dir, "first", "--type", "task").ID
	b := bdCreate(t, bd, dir, "second", "--type", "task").ID

	for _, sep := range []string{" ", "\t", "\n"} {
		joined := a + sep + b
		for _, extra := range [][]string{nil, {"--json"}} {
			out := bdCloseFail(t, bd, dir, append([]string{joined}, extra...)...)
			if !strings.Contains(out, "no issue found matching") {
				t.Errorf("bd close %q %v lost its not-found error:\n%s", joined, extra, out)
			}
			if !strings.Contains(out, closeWhitespaceHintText) {
				t.Errorf("bd close %q %v has no separate-argument hint:\n%s", joined, extra, out)
			}
		}
	}
	for _, id := range []string{a, b} {
		if got := bdShow(t, bd, dir, id); got.Status != types.StatusOpen {
			t.Errorf("%s status = %q after the failed close, want open", id, got.Status)
		}
	}

	out := bdCloseFail(t, bd, dir, "cw-does-not-exist")
	if !strings.Contains(out, "no issue found matching") || strings.Contains(out, closeWhitespaceHintText) {
		t.Errorf("single unknown ID error changed:\n%s", out)
	}
}
