//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedWispGCProtectedLabel is the end-to-end regression for the data
// loss behind wisp.protected_labels: `bd mol wisp gc --age` deleted unread mail
// and open escalations an orchestration layer stored as ephemeral beads. Such a
// record sits in plain open status for as long as nobody acts on it, and open
// is age-reclaimable by design, so the longer it went unread the more certainly
// GC deleted it.
//
// It runs against a throwaway store from bdInit, never a live one: this command
// deletes, without a digest.
func TestEmbeddedWispGCProtectedLabel(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcl")
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "keep:mail")

	openPlain := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID
	closedPlain := bdCreate(t, bd, dir, "spent wisp", "--ephemeral").ID
	otherLabel := bdCreate(t, bd, dir, "unrelated label", "--ephemeral", "--label", "thread").ID
	unreadMail := bdCreate(t, bd, dir, "unread mail", "--ephemeral", "--label", "keep:mail").ID
	readMail := bdCreate(t, bd, dir, "read mail", "--ephemeral", "--label", "thread,keep:mail").ID
	bdCommand(t, bd, dir, "close", closedPlain, readMail)

	// --age 0s makes every wisp old enough (see TestEmbeddedWispGCProtectsActiveWisps).
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--all", "--force")

	for _, id := range []string{unreadMail, readMail} {
		if !bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("wisp %s carries protected label keep:mail and must survive wisp gc --all", id)
		}
	}
	for _, id := range []string{openPlain, closedPlain, otherLabel} {
		if bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("wisp %s carries no protected label and must be reclaimed by wisp gc --all", id)
		}
	}
}
