package main

import (
	"strings"
	"testing"
)

// TestValidateNotesUpdateRejectsInlineEmpty pins the deliberate contrast with
// the description guard: an inline empty value IS refused here, because a
// dead command substitution reaches the flag layer as exactly that (GH#6021)
// and notes has no stdin/file input to scope the guard to. The refusal must
// name the verb that clears deliberately.
func TestValidateNotesUpdateRejectsInlineEmpty(t *testing.T) {
	err := validateNotesUpdate("")
	if err == nil {
		t.Fatal("expected inline empty notes to be rejected")
	}
	if !strings.Contains(err.Error(), "--clear-notes") {
		t.Fatalf("expected refusal to name --clear-notes, got: %v", err)
	}
}

func TestValidateNotesUpdateAllowsNonEmpty(t *testing.T) {
	if err := validateNotesUpdate("handoff context"); err != nil {
		t.Fatalf("expected non-empty notes to succeed, got: %v", err)
	}
}

// TestUpdateRegistersClearNotes guards the wiring: the update command must
// carry the verb the guard's refusal advertises. create does not register it
// — a new issue has no notes to wipe, so `bd create --notes ""` is not the
// GH#6021 hazard.
func TestUpdateRegistersClearNotes(t *testing.T) {
	if updateCmd.Flags().Lookup("clear-notes") == nil {
		t.Fatal("expected update to register --clear-notes")
	}
	if createCmd.Flags().Lookup("clear-notes") != nil {
		t.Fatal("create registers --clear-notes but has no wipe hazard; move the guard if this is now intended")
	}
}
