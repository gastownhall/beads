//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The embedded end-to-end tests for the wisp GC protected-label guard
// (bd-961adp): a wisp carrying a label the workspace named in
// wisp.protected_labels is never reclaimed by `bd mol wisp gc`, on any route
// or mode, while unlabeled peers stay reclaimable. Every test asserts both
// directions, and every test runs against a throwaway bdInit store — this
// command deletes, and deletes without a digest.

// TestEmbeddedWispGCProtectedLabel is the headline regression for bd-961adp:
// `bd mol wisp gc --age` reclaimed a stale open wisp carrying the protected
// label, destroying unread mail and open escalations that an orchestration
// layer stores as write-once ephemeral beads. With --all the sweep also takes
// closed wisps: closing a record does not make it disposable.
func TestEmbeddedWispGCProtectedLabel(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "wpl")

	// Workspace policy: a wisp carrying keep:mail is never reclaimed by GC.
	// Deliberate deletion stays available (`bd delete`); only the sweep is
	// fenced.
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`)

	// An unread escalation: plain open status for exactly as long as nobody
	// has acted on it — the shape no pre-existing protection could save.
	openKept := bdCreate(t, bd, dir, "unread escalation", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", openKept, "keep:mail")
	openPlain := bdCreate(t, bd, dir, "stale noise", "--ephemeral").ID

	// The same pair, closed: --all reaches closed wisps, and the label must
	// protect them there too.
	closedKept := bdCreate(t, bd, dir, "kept record, closed", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", closedKept, "keep:mail")
	bdClose(t, bd, dir, closedKept)
	closedPlain := bdCreate(t, bd, dir, "closed noise", "--ephemeral").ID
	bdClose(t, bd, dir, closedPlain)

	// --age 0s makes every wisp old enough to reclaim; protection is checked
	// before age, so only the unlabeled pair should go.
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--all")

	if bdShowSucceeds(t, bd, dir, openPlain) {
		t.Errorf("unlabeled open wisp %s should have been reclaimed", openPlain)
	}
	if bdShowSucceeds(t, bd, dir, closedPlain) {
		t.Errorf("unlabeled closed wisp %s should have been reclaimed by --all", closedPlain)
	}
	if !bdShowSucceeds(t, bd, dir, openKept) {
		t.Errorf("open wisp %s carrying protected label keep:mail was reclaimed by age GC", openKept)
	}
	if !bdShowSucceeds(t, bd, dir, closedKept) {
		t.Errorf("closed wisp %s carrying protected label keep:mail was reclaimed by --all", closedKept)
	}
}

// TestEmbeddedWispGCCascadeSparesLabeledDependent carries the guard onto the
// cascade expansion, which reaches wisps the parent scan never saw: two old
// closed dependents of an abandoned parent, only one labeled. Closed wisps
// are invisible to the parent scan without --all, so only the cascade can
// collect either — the unlabeled one goes with its parent, the labeled one
// survives, orphaned.
func TestEmbeddedWispGCCascadeSparesLabeledDependent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "wpc")

	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`)

	parent := bdCreate(t, bd, dir, "abandoned parent", "--ephemeral").ID
	keptChild := bdCreate(t, bd, dir, "kept dependent", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", keptChild, "keep:mail")
	bdClose(t, bd, dir, keptChild)
	plainChild := bdCreate(t, bd, dir, "gone dependent", "--ephemeral").ID
	bdClose(t, bd, dir, plainChild)
	bdDepAdd(t, bd, dir, keptChild, parent)
	bdDepAdd(t, bd, dir, plainChild, parent)

	// No --all: the closed dependents are reachable only through the cascade.
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s")

	if bdShowSucceeds(t, bd, dir, parent) {
		t.Errorf("abandoned parent %s should have been reclaimed", parent)
	}
	if bdShowSucceeds(t, bd, dir, plainChild) {
		t.Errorf("unlabeled closed dependent %s should have been cascaded with its parent", plainChild)
	}
	if !bdShowSucceeds(t, bd, dir, keptChild) {
		t.Errorf("labeled closed dependent %s was cascaded; want it spared (orphaned, not deleted)", keptChild)
	}
}

// TestEmbeddedWispGCProtectedLabelReporting covers what the operator sees:
// setting the guard up through its exact key warns nothing, a preview says
// how many wisps the labels held back, and the JSON wire stays a report of
// the eligible candidates only — skips are reported in text, never mixed
// into the delete list.
func TestEmbeddedWispGCProtectedLabelReporting(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "wpr")

	// The exact key is registered, so the feature's own setup command must
	// not warn about an unrecognized key on every adopter's first use.
	cfgOut := bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`)
	if strings.Contains(cfgOut, "not a recognized config key") {
		t.Fatalf("bd config set wisp.protected_labels warned about an unrecognized key:\n%s", cfgOut)
	}

	keptOne := bdCreate(t, bd, dir, "kept record one", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", keptOne, "keep:mail")
	keptTwo := bdCreate(t, bd, dir, "kept record two", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", keptTwo, "keep:mail")
	plain := bdCreate(t, bd, dir, "stale noise", "--ephemeral").ID

	preview := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--dry-run")
	if !strings.Contains(preview, "Skipping 2 wisp(s) carrying a protected label") {
		t.Errorf("preview does not report the two label skips:\n%s", preview)
	}

	raw := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--dry-run", "--json")
	var got struct {
		CleanedIDs   []string `json:"cleaned_ids"`
		Candidates   int      `json:"candidates"`
		CleanedCount int      `json:"cleaned_count"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal gc preview json: %v\n%s", err, raw)
	}
	if len(got.CleanedIDs) != 1 || got.CleanedIDs[0] != plain {
		t.Errorf("json preview lists %v; want only the eligible unprotected wisp %s", got.CleanedIDs, plain)
	}
	if got.Candidates != 1 {
		t.Errorf("json candidates = %d; want 1 (the labeled wisps are skips, not candidates)", got.Candidates)
	}
	if got.CleanedCount != 0 {
		t.Errorf("json cleaned_count = %d in a preview; want 0", got.CleanedCount)
	}
}

// TestEmbeddedWispGCExcludeLabelFlag is the end-to-end leg of the additive
// --exclude-label flag: a label named on the command line protects for this
// invocation alone, with nothing configured — and there is no built-in
// default, so a wisp carrying only bd:protected stays reclaimable until the
// workspace opts in (flag or setting).
func TestEmbeddedWispGCExcludeLabelFlag(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "wpe")

	// Deliberately no wisp.protected_labels setting: everything protected
	// below is protected by the flag alone.
	flagKept := bdCreate(t, bd, dir, "kept by the flag", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", flagKept, "keep:mail")
	builtinOnly := bdCreate(t, bd, dir, "carries only bd:protected", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", builtinOnly, "bd:protected")
	plain := bdCreate(t, bd, dir, "stale noise", "--ephemeral").ID

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--exclude-label", "keep:mail")

	if bdShowSucceeds(t, bd, dir, plain) {
		t.Errorf("unlabeled wisp %s should have been reclaimed", plain)
	}
	if bdShowSucceeds(t, bd, dir, builtinOnly) {
		t.Errorf("wisp %s carrying only bd:protected survived; bd:protected is not a built-in default (config or flag must name it)", builtinOnly)
	}
	if !bdShowSucceeds(t, bd, dir, flagKept) {
		t.Errorf("wisp %s carrying the flag's label keep:mail was reclaimed; want it spared by --exclude-label", flagKept)
	}
}

// TestEmbeddedWispGCClosedPurgeSparesLabeled covers `--closed`: closing a
// record does not make it disposable. The preview must count only the
// unlabeled closed wisp as a candidate, and --force must delete only it; the
// labeled one survives both, while `bd delete` remains the deliberate way
// out.
func TestEmbeddedWispGCClosedPurgeSparesLabeled(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "wpx")

	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`)

	kept := bdCreate(t, bd, dir, "kept closed record", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", kept, "keep:mail")
	bdClose(t, bd, dir, kept)
	plain := bdCreate(t, bd, dir, "plain closed record", "--ephemeral").ID
	bdClose(t, bd, dir, plain)

	preview := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--dry-run")
	if !strings.Contains(preview, "Found 1 closed wisp(s)") {
		t.Errorf("preview counted more than the unlabeled closed wisp as a candidate:\n%s", preview)
	}

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")

	if bdShowSucceeds(t, bd, dir, plain) {
		t.Errorf("unlabeled closed wisp %s should have been purged", plain)
	}
	if !bdShowSucceeds(t, bd, dir, kept) {
		t.Errorf("labeled closed wisp %s was purged by --closed --force; closing a record does not make it disposable", kept)
	}
}
