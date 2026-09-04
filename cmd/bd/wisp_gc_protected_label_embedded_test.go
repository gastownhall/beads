//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestWispGCProtectsLabeledWisps is the regression test for the data loss that
// motivated the label guard: `bd mol wisp gc --age` deleted unread mail and
// open escalations that an orchestration layer had stored as ephemeral beads.
//
// Nothing else in the predicate could have saved them. Such a record sits in
// plain `open` status for exactly as long as nobody has acted on it, and open is
// CategoryActive — age-reclaimable by design — so the longer a message went
// unread, the more certainly GC deleted it. --exclude-type could not help
// either: these records are ordinary `task`-typed beads, so excluding their
// type excludes nearly the whole store.
//
// The assertions run against a throwaway store created by bdInit, never a live
// one; this command deletes, and deletes without a digest.
func TestWispGCProtectsLabeledWisps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcl")

	// A configured value REPLACES the built-in default, so name every label
	// this store wants protected.
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "gt:message,gt:escalation")

	// Ordering matters for the same reason as in TestWispGCProtectsActiveWisps:
	// wisps expected to be RECLAIMED are created first, so the age predicate
	// cannot mistake the most recently touched bead for not-yet-stale if the DB
	// clock runs ahead of the test process. Protected wisps are excluded
	// regardless of age.
	idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID
	otherLabel := bdCreate(t, bd, dir, "unrelated label", "--ephemeral", "--label", "gt:thread").ID

	// The two classes that were actually destroyed. Both are plain open tasks.
	unreadMail := bdCreate(t, bd, dir, "unread mail", "--ephemeral", "--label", "gt:message").ID
	escalation := bdCreate(t, bd, dir, "open escalation", "--ephemeral", "--label", "gt:escalation").ID

	// Protection is per-label, not per-bead: a record carrying a protected
	// label alongside unprotected ones is still protected.
	mixed := bdCreate(t, bd, dir, "mail in a thread", "--ephemeral", "--label", "gt:thread,gt:message").ID

	// An ad-hoc label named only on the command line adds to the configured set.
	adHoc := bdCreate(t, bd, dir, "ad-hoc protected", "--ephemeral", "--label", "keep:me").ID

	candidates := wispGCCandidates(t, bd, dir, "--age", "1ms", "--dry-run", "--exclude-label", "keep:me")

	for _, tc := range []struct {
		id   string
		what string
	}{
		{unreadMail, "unread mail"},
		{escalation, "open escalation"},
		{mixed, "mail carrying an extra unprotected label"},
		{adHoc, "wisp protected by --exclude-label"},
	} {
		if candidates[tc.id] {
			t.Errorf("%s wisp %s must NOT be reclaimed by age; candidates=%v", tc.what, tc.id, keys(candidates))
		}
	}

	// The guard must not degenerate into "protect everything": a wisp with no
	// label, and a wisp whose only label is unprotected, stay reclaimable.
	for _, tc := range []struct {
		id   string
		what string
	}{
		{idle, "unlabeled idle"},
		{otherLabel, "unprotected-label"},
	} {
		if !candidates[tc.id] {
			t.Errorf("%s wisp %s should stay a GC candidate (guard must not over-protect); candidates=%v", tc.what, tc.id, keys(candidates))
		}
	}
}

// TestWispGCClosedPurgeProtectsLabeledWisps pins the other half of the rule:
// `wisp gc` never reclaims a protected label in ANY mode. Closing a record does
// not make it disposable — a read message and a resolved escalation are exactly
// the ones worth keeping — and --closed selects purely by status, so without
// this the age guard would just move the deletion to a different flag.
func TestWispGCClosedPurgeProtectsLabeledWisps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gcc")

	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", "gt:message")

	readMail := bdCreate(t, bd, dir, "read mail", "--ephemeral", "--label", "gt:message").ID
	spent := bdCreate(t, bd, dir, "spent wisp", "--ephemeral").ID
	bdCommand(t, bd, dir, "close", readMail)
	bdCommand(t, bd, dir, "close", spent)

	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--dry-run")

	if !strings.Contains(out, "Found 1 closed wisp(s)") {
		t.Errorf("closed purge should have exactly one candidate (%s), got:\n%s", spent, out)
	}
	if !strings.Contains(out, "protected label") {
		t.Errorf("closed purge must report the wisp it skipped and why; got:\n%s", out)
	}

	// The labeled wisp must still be there afterwards; the unlabeled one is the
	// purge's business.
	if !strings.Contains(bdCommand(t, bd, dir, "show", readMail), readMail) {
		t.Errorf("closed wisp %s carrying a protected label must survive --closed", readMail)
	}
}

// wispGCCandidates runs `bd mol wisp gc --json` and returns the candidate IDs
// as a set.
func wispGCCandidates(t *testing.T, bd, dir string, args ...string) map[string]bool {
	t.Helper()
	out := bdCommand(t, bd, dir, append([]string{"mol", "wisp", "gc", "--json"}, args...)...)

	start := strings.Index(out, "{")
	if start < 0 {
		t.Fatalf("gc --json produced no JSON object\nraw:\n%s", out)
	}
	var res struct {
		CleanedIDs []string `json:"cleaned_ids"`
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&res); err != nil {
		t.Fatalf("parse gc --json output: %v\nraw:\n%s", err, out)
	}
	candidates := make(map[string]bool, len(res.CleanedIDs))
	for _, id := range res.CleanedIDs {
		candidates[id] = true
	}
	return candidates
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
