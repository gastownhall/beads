package main

import (
	"errors"
	"testing"
)

func TestFormatDoltAutoCommitMessage(t *testing.T) {
	msg := formatDoltAutoCommitMessage("update", "alice", []string{"bd-2", "bd-1", "bd-2", "", "bd-3"})
	if msg != "bd: update (auto-commit) by alice [bd-1, bd-2, bd-3]" {
		t.Fatalf("unexpected message: %q", msg)
	}

	// Caps IDs (max 5) and sorts
	msg = formatDoltAutoCommitMessage("create", "bob", []string{"z-9", "a-1", "m-3", "b-2", "c-4", "d-5", "e-6"})
	if msg != "bd: create (auto-commit) by bob [a-1, b-2, c-4, d-5, e-6]" {
		t.Fatalf("unexpected capped message: %q", msg)
	}

	// Empty command/actor fallbacks
	msg = formatDoltAutoCommitMessage("", "", nil)
	if msg != "bd: write (auto-commit) by unknown" {
		t.Fatalf("unexpected fallback message: %q", msg)
	}
}

func TestIsDoltNothingToCommit(t *testing.T) {
	if isDoltNothingToCommit(nil) {
		t.Fatal("nil error should not be treated as nothing-to-commit")
	}
	if !isDoltNothingToCommit(errors.New("nothing to commit")) {
		t.Fatal("expected nothing-to-commit to be detected")
	}
	if !isDoltNothingToCommit(errors.New("No changes to commit")) {
		t.Fatal("expected no-changes-to-commit to be detected")
	}
	if isDoltNothingToCommit(errors.New("permission denied")) {
		t.Fatal("unexpected classification")
	}
}

func TestGetDoltAutoCommitMode_Batch(t *testing.T) {
	old := doltAutoCommit
	defer func() { doltAutoCommit = old }()

	doltAutoCommit = "batch"
	mode, err := getDoltAutoCommitMode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != doltAutoCommitBatch {
		t.Fatalf("expected batch, got %q", mode)
	}

	// Also verify the other modes still work
	doltAutoCommit = "on"
	mode, err = getDoltAutoCommitMode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != doltAutoCommitOn {
		t.Fatalf("expected on, got %q", mode)
	}

	doltAutoCommit = "off"
	mode, err = getDoltAutoCommitMode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != doltAutoCommitOff {
		t.Fatalf("expected off, got %q", mode)
	}

	// Invalid mode
	doltAutoCommit = "invalid"
	_, err = getDoltAutoCommitMode()
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}
