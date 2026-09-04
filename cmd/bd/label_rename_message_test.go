package main

import (
	"errors"
	"strings"
	"testing"
)

// The partial-failure wording is a semantic claim, not cosmetics: the counts are
// assigned inside the transaction closure on both routes, so a Commit failure or
// retry exhaustion leaves them holding an attempt that never landed. Nothing else
// in the suite drives a failing rename with renamed > 0, so without this test the
// hunk can be reverted and every other test stays green.
func TestLabelRenamePartialFailureMessageIsAnUpperBound(t *testing.T) {
	msg := labelRenamePartialFailureMessage(6, 2, errors.New("commit failed"))

	for _, want := range []string{
		"up to 6 issue(s)",
		"of which up to 2 merged",
		"may have been renamed before failing",
		"commit failed",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}

	// No clause may assert that a write landed. These are the phrasings the
	// message carried before the correction, and the ones a future edit is
	// most likely to reintroduce.
	for _, forbidden := range []string{
		"renamed 6 issue(s)",
		"(2 merged)",
	} {
		if strings.Contains(msg, forbidden) {
			t.Errorf("message %q asserts a landed count via %q; every number must stay hedged", msg, forbidden)
		}
	}
}
