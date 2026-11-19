//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// bdShowSucceeds reports whether "bd show <id> --json" succeeds. Non-fatal
// so a cascade sweep reports every deleted step instead of aborting on the
// first one.
func bdShowSucceeds(t *testing.T, bd, dir, id string) bool {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("bd show %s failed (expected for deleted): %v\n%s", id, err, out)
		return false
	}
	return true
}

// TestEmbeddedWispGCPurgeClosedDoesNotCascadeIntoLiveSteps is the regression test for
// the patrol self-destruct bug: `bd mol wisp gc --closed --force` on a
// molecule that has closed its first step deleted the ENTIRE molecule.
//
// purgeClosed handed cascade=true to deleteBatch, and deleteMany(Cascade)
// expands every closed wisp to ALL transitive dependents. In a linear
// molecule DAG, step 1's dependents are every other step, so the first
// closed step dragged all 26 open steps of mol-deacon-patrol into deletion
// (plus its dependency links and events). Wisps live in an ignored
// (non-versioned) table, so the deletion was permanent.
//
// The closed set is complete: every closed, non-pinned, non-infra wisp is
// already a GC candidate. Cascade can only ever add NON-closed dependents to
// the batch — exactly the live steps it must not touch.
func TestEmbeddedWispGCPurgeClosedDoesNotCascadeIntoLiveSteps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc")

	// Deacon patrol shape: an open mol root (the husk that survives) plus a
	// linear chain of 26 steps. Each step depends on its predecessor (the
	// first step is dependency-free — the patrol closes it, so it cannot be
	// blocked on an open root), and step 1's transitive dependents are every
	// other step.
	root := bdCreate(t, bd, dir, "patrol molecule", "--ephemeral", "--type", "molecule")

	const steps = 26
	stepIDs := make([]string, steps)
	prev := ""
	for i := 0; i < steps; i++ {
		id := bdCreate(t, bd, dir, fmt.Sprintf("patrol step %d", i+1), "--ephemeral").ID
		stepIDs[i] = id
		if prev != "" {
			bdDepAdd(t, bd, dir, id, prev) // step N depends on step N-1
		}
		prev = id
	}

	// Patrol cycle: the completed first step is closed, then the formula's GC
	// step purges closed wisps. The other 25 steps are live work (open) and
	// must survive the purge.
	bdClose(t, bd, dir, stepIDs[0])
	// --age 0s: the step closed moments ago, and closed GC now keeps wisps
	// updated within --age (GH#8202); this test is about cascade, not age.
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age", "0s")

	// The closed step itself is gone.
	bdShowFail(t, bd, dir, stepIDs[0])

	// Every live step survives.
	for i := 1; i < steps; i++ {
		if bdShowSucceeds(t, bd, dir, stepIDs[i]) {
			continue
		}
		t.Errorf("live step %s (index %d) was cascade-deleted by purgeClosed; only the first closed step should be removed", stepIDs[i], i)
	}

	// The open molecule root survives too (the husk must keep its steps).
	if bdShowSucceeds(t, bd, dir, root.ID) {
		return
	}
	t.Error("open molecule root was deleted by purgeClosed")
}

// TestEmbeddedWispGCPurgeClosedStillPurgesFullyClosedMolecules guards the other side
// of the fix: when EVERY step is closed, gc --closed --force must still remove
// the whole molecule. Restricting cascade must not break complete cleanup.
func TestEmbeddedWispGCPurgeClosedStillPurgesFullyClosedMolecules(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc2")

	const steps = 5
	stepIDs := make([]string, steps)
	prev := ""
	for i := 0; i < steps; i++ {
		id := bdCreate(t, bd, dir, fmt.Sprintf("finished step %d", i+1), "--ephemeral").ID
		stepIDs[i] = id
		if prev != "" {
			bdDepAdd(t, bd, dir, id, prev)
		}
		prev = id
	}
	for _, id := range stepIDs {
		bdClose(t, bd, dir, id)
	}

	// --age 0s: the steps closed moments ago (see GH#8202).
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age", "0s")

	for _, id := range stepIDs {
		bdShowFail(t, bd, dir, id)
	}
}

// TestEmbeddedWispGCPurgeClosedSparesFreshAuditBearingWisp asserts the
// behaviour GH#8202 expects: `bd mol wisp gc --closed --force` must not
// hard-delete a wisp that closed moments ago and carries a report in its
// notes. The wisps table is Dolt-ignored, so the delete is unrecoverable.
func TestEmbeddedWispGCPurgeClosedSparesFreshAuditBearingWisp(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc3")

	report := bdCreate(t, bd, dir, "patrol report step", "--ephemeral").ID
	bdUpdate(t, bd, dir, report, "--notes", "patrol report: 0 reaped / 0 purged / 0 anomalies")
	bdClose(t, bd, dir, report)

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")

	if !bdShowSucceeds(t, bd, dir, report) {
		t.Errorf("gc --closed --force hard-deleted wisp %s seconds after it closed, along with its report notes", report)
	}
}

// TestEmbeddedWispGCPurgeClosedKeepsRecentlyClosedWisps pins GH#8202 end to
// end: closed GC honors --age (default 1h), so a wisp closed moments ago is
// neither deleted nor counted, and --age 0s still purges it at once.
func TestEmbeddedWispGCPurgeClosedKeepsRecentlyClosedWisps(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc4")

	fresh := bdCreate(t, bd, dir, "just finished step", "--ephemeral").ID
	bdClose(t, bd, dir, fresh)

	out := bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--json")
	if !strings.Contains(out, `"deleted_count": 0`) {
		t.Errorf("default gc --closed --force counted a wisp closed moments ago as deleted:\n%s", out)
	}
	if !bdShowSucceeds(t, bd, dir, fresh) {
		t.Fatalf("default gc --closed --force deleted wisp %s moments after it closed", fresh)
	}

	// --age 0s is the explicit purge-all escape hatch.
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age", "0s")
	bdShowFail(t, bd, dir, fresh)
}

// TestEmbeddedWispGCPurgeClosedZeroAgeIsPurgeAll guards the escape hatch of
// the GH#8202 age floor: --closed --force with --age 0s — or any non-positive
// duration — still selects every closed ephemeral wisp regardless of age, the
// behavior --closed had before the floor. The floor must not overreach.
func TestEmbeddedWispGCPurgeClosedZeroAgeIsPurgeAll(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pc5")

	first := bdCreate(t, bd, dir, "closed moments ago", "--ephemeral").ID
	second := bdCreate(t, bd, dir, "also closed moments ago", "--ephemeral").ID
	bdClose(t, bd, dir, first)
	bdClose(t, bd, dir, second)

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age", "0s")
	bdShowFail(t, bd, dir, first)
	bdShowFail(t, bd, dir, second)

	// A non-positive duration omits the age bound the same way zero does.
	third := bdCreate(t, bd, dir, "negative age window", "--ephemeral").ID
	bdClose(t, bd, dir, third)
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force", "--age=-1s")
	bdShowFail(t, bd, dir, third)
}
