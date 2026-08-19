package main

import (
	"strings"
	"testing"
)

// TestWarnNotesReplacement is the D2 guard test: a `bd update --notes` that
// replaces a non-empty notes field warns on stderr, naming --append-notes as
// the history-preserving alternative. The wording pins the dev pseudo-version
// line (v1.1.1-0.20260805231652-392231d76029, upstream fix #4743) that first
// shipped the warning and is what operators saw in the 2026-08-19 Westlands
// repro; the predicate deciding WHEN the warning fires is covered by
// TestReplacesExistingNotes.
func TestWarnNotesReplacement(t *testing.T) {
	got := captureStderr(t, func() {
		warnNotesReplacement("tc-dg6")
	})

	want := "warning: tc-dg6: --notes replaced existing notes (use --append-notes to preserve history)\n"
	if got != want {
		t.Fatalf("warnNotesReplacement stderr = %q, want %q", got, want)
	}
	if !strings.Contains(got, "--append-notes") {
		t.Errorf("warning must name --append-notes, got %q", got)
	}
}
