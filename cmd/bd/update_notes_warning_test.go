package main

import (
	"testing"
)

// TestWarnNotesReplacement is the D2 guard test: since the notes-overwrite
// fence, an unforced `bd update --notes` over existing notes is refused
// (errNotesOverwriteRefusal), so this warning fires only after a FORCED
// overwrite succeeded. It is the audit trail for a habitual --force that
// never saw the refusal — a statement of what happened, still naming
// --append-notes as the history-preserving alternative. The predicate
// deciding WHEN it fires is covered by TestReplacesExistingNotes.
func TestWarnNotesReplacement(t *testing.T) {
	got := captureStderr(t, func() {
		warnNotesReplacement("tc-dg6")
	})

	want := "warning: tc-dg6: --force replaced existing notes (--append-notes preserves history)\n"
	if got != want {
		t.Fatalf("warnNotesReplacement stderr = %q, want %q", got, want)
	}
}
