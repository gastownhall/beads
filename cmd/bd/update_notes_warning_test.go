package main

import (
	"testing"
)

// TestWarnNotesReplacement is the D2 guard test: a `bd update --notes` that
// replaces a non-empty notes field warns on stderr, naming --append-notes as
// the history-preserving alternative (the warning itself shipped on main via
// #4743). The predicate deciding WHEN the warning fires is covered by
// TestReplacesExistingNotes.
func TestWarnNotesReplacement(t *testing.T) {
	got := captureStderr(t, func() {
		warnNotesReplacement("tc-dg6")
	})

	want := "warning: tc-dg6: --notes replaced existing notes (use --append-notes to preserve history)\n"
	if got != want {
		t.Fatalf("warnNotesReplacement stderr = %q, want %q", got, want)
	}
}
