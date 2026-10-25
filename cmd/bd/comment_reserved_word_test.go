package main

import (
	"strings"
	"testing"
)

// TestCheckCommentIDNotReservedWord covers a real incident: "bd comment list
// <id>", a typo for "bd comments list", must be rejected before any id
// resolution is attempted — not silently treat "list" as the id.
func TestCheckCommentIDNotReservedWord(t *testing.T) {
	reserved := []string{"list", "add", "rm", "delete"}
	for _, word := range reserved {
		t.Run("rejects reserved word "+word, func(t *testing.T) {
			var err error
			stderr := captureStderr(t, func() {
				err = checkCommentIDNotReservedWord(word)
			})
			if err == nil {
				t.Fatalf("checkCommentIDNotReservedWord(%q) = nil; want error", word)
			}
			if !strings.Contains(stderr, "bd comments") {
				t.Errorf("checkCommentIDNotReservedWord(%q) printed %q; want a hint mentioning bd comments", word, stderr)
			}
		})
	}

	happyPath := []string{
		"bd-123",
		"a3f8e9",
		"ga-wisp-list3t0", // full id containing "list" as a substring must NOT be rejected
		"listicle-42",     // starts with "list" but is not the literal reserved word
		"LIST",            // reserved-word check is intentionally case-sensitive: real ids are lowercase hashes
	}
	for _, id := range happyPath {
		t.Run("allows real id "+id, func(t *testing.T) {
			var err error
			captureStderr(t, func() {
				err = checkCommentIDNotReservedWord(id)
			})
			if err != nil {
				t.Errorf("checkCommentIDNotReservedWord(%q) = %v; want nil (happy path must stay intact)", id, err)
			}
		})
	}
}
