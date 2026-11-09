package main

import (
	"strings"
	"testing"
)

// GH#4492: a JSONL import aborted in its entirety on the first record the
// writer refused, and reported nothing useful about which record or why. These
// tests, adapted from @maphew's PR #5202, pin the replacement contract at the
// screen's seams: the abort stays the default, but it names the offending line
// and the way past it.

// Strict-by-default keeps the reporter of GH#4492 in exactly the position they
// started in: plain `bd import` on a file with one bad row still imports
// nothing. The only thing that changes their outcome is the failure telling
// them the escape hatch exists, so that hint is load-bearing, not decoration.
func TestFirstRejectErrorNamesTheEscapeHatch(t *testing.T) {
	err := strictImportError([]importRejection{
		{Line: 2, ID: "tst-2", Reason: "invalid status: verify"},
		{Line: 4, ID: "tst-4", Reason: "title is required"},
	})
	if err == nil {
		t.Fatal("strictImportError = nil, want an error")
	}
	msg := err.Error()
	for _, want := range []string{"line 2", "tst-2", "invalid status: verify", "and 1 more"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	for _, want := range []string{"--skip-invalid", "--rejects"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not tell the user about %s; without it the default leaves\n"+
				"the reporter of GH#4492 with the same empty import they filed about:\n%s", want, msg)
		}
	}
	// Nothing partial landed, and the message has to say so: a user who thinks
	// some rows imported will fix the file and re-import, double-applying it.
	if !strings.Contains(msg, "nothing was imported") {
		t.Errorf("error does not say the import was a no-op:\n%s", msg)
	}
}
