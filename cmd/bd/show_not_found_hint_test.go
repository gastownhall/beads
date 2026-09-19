package main

import (
	"strings"
	"testing"
)

// TestShowNotFoundHint_PointsAtHistoryAndAcknowledgesDeletion guards
// ga-m6inyb: bd show's "not found" message must stop implying an ID never
// existed (it may have existed and been deleted/purged, which leaves no
// trace in the live tables) and must tell the reader how to check further.
func TestShowNotFoundHint_PointsAtHistoryAndAcknowledgesDeletion(t *testing.T) {
	hint := showNotFoundHint("bd-1234")

	if !strings.Contains(hint, "bd history bd-1234") {
		t.Errorf("expected hint to name the exact id in a 'bd history' pointer, got: %q", hint)
	}
	if !strings.Contains(hint, "deleted") && !strings.Contains(hint, "purged") {
		t.Errorf("expected hint to acknowledge deletion/purge as a possibility, got: %q", hint)
	}
	if !strings.Contains(hint, "never existed") {
		t.Errorf("expected hint to name 'never existed' as only ONE of the possibilities, got: %q", hint)
	}
}

func TestShowNotFoundHint_IsIDSpecific(t *testing.T) {
	a := showNotFoundHint("bd-aaa")
	b := showNotFoundHint("bd-bbb")

	if a == b {
		t.Errorf("expected hint text to vary with id, got identical output: %q", a)
	}
	if !strings.Contains(a, "bd-aaa") || strings.Contains(a, "bd-bbb") {
		t.Errorf("expected hint for bd-aaa to reference only its own id, got: %q", a)
	}
}
