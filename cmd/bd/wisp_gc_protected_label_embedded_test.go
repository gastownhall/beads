//go:build cgo

package main

import (
	"os"
	"strings"
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

// TestEmbeddedWispGCProtectedLabelCascade drives the cascade site end to end:
// without --all the parent scan passes over closed wisps, so only the cascade
// from an abandoned parent can reach its closed steps, and a step carrying a
// protected label must not go down with the parent.
func TestEmbeddedWispGCProtectedLabelCascade(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcc")
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "keep:mail")

	parent := bdCreate(t, bd, dir, "abandoned parent", "--ephemeral").ID
	plainStep := bdCreate(t, bd, dir, "plain step", "--ephemeral").ID
	keptStep := bdCreate(t, bd, dir, "kept step", "--ephemeral", "--label", "keep:mail").ID
	// Closed before the edges are added: a step blocked on its open parent
	// cannot be closed without --force.
	bdCommand(t, bd, dir, "close", plainStep, keptStep)
	bdDepAdd(t, bd, dir, plainStep, parent)
	bdDepAdd(t, bd, dir, keptStep, parent)

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--force")

	if !bdShowSucceeds(t, bd, dir, keptStep) {
		t.Errorf("closed step %s carries protected label keep:mail and must survive its parent's GC", keptStep)
	}
	for _, id := range []string{parent, plainStep} {
		if bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("unprotected wisp %s must be reclaimed (abandoned parent or its closed step)", id)
		}
	}
}

// TestEmbeddedWispGCClosedPurgeProtectedLabel pins that `wisp gc --closed`
// honors the setting on the direct route: the preview names only the
// unlabeled wisps and reports the skip, and --force deletes only those.
func TestEmbeddedWispGCClosedPurgeProtectedLabel(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcp")
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "keep:mail")

	readMail := bdCreate(t, bd, dir, "read mail", "--ephemeral", "--label", "keep:mail").ID
	spent := bdCreate(t, bd, dir, "spent wisp", "--ephemeral").ID
	other := bdCreate(t, bd, dir, "spent thread", "--ephemeral", "--label", "thread").ID
	bdCommand(t, bd, dir, "close", readMail, spent, other)

	preview := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed")
	if !strings.Contains(preview, "Found 2 closed wisp(s) to delete") {
		t.Errorf("closed purge preview should find exactly the 2 unlabeled wisps; got:\n%s", preview)
	}
	if !strings.Contains(preview, "Skipping 1 wisp(s) carrying a protected label") {
		t.Errorf("closed purge preview must report the protected-label skip; got:\n%s", preview)
	}

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")
	if !bdShowSucceeds(t, bd, dir, readMail) {
		t.Errorf("closed wisp %s carries protected label keep:mail and must survive wisp gc --closed --force", readMail)
	}
	for _, id := range []string{spent, other} {
		if bdShowSucceeds(t, bd, dir, id) {
			t.Errorf("closed wisp %s carries no protected label and must be purged", id)
		}
	}
}
