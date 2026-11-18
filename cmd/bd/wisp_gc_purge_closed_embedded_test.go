//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
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
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")

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

	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")

	for _, id := range stepIDs {
		bdShowFail(t, bd, dir, id)
	}
}

// TestEmbeddedWispGCKeepsAClosedWispAFallbackWaitsOn is GH#5577 end to end on
// the closed-wisp cleanup verbs: a wisp closed with a reason that is not a
// failure still holds its conditional-blocks fallback back, so deleting it
// would put the fallback in ready work after a success. The age GC under
// --all, `--closed --force` and `bd purge` all keep it while the fallback
// waits, the two GC modes still collect a wisp whose failure close released
// its fallback, and once the unused fallback is closed the next GC collects
// the wisp and the fallback.
func TestEmbeddedWispGCKeepsAClosedWispAFallbackWaitsOn(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cgh")

	// conditionalPair seeds a wisp, a wisp fallback that runs only if it
	// fails, and closes the first with reason.
	conditionalPair := func(name, reason string) (target, fallback string) {
		target = bdCreate(t, bd, dir, name, "--ephemeral").ID
		fallback = bdCreate(t, bd, dir, name+" fallback", "--ephemeral").ID
		bdDepAdd(t, bd, dir, fallback, target, "--type", "conditional-blocks")
		bdClose(t, bd, dir, target, "--reason", reason)
		return target, fallback
	}
	held, fallback := conditionalPair("main flow", "all steps complete")
	assertHeld := func(phase string) {
		t.Helper()
		if !bdShowSucceeds(t, bd, dir, held) {
			t.Fatalf("%s deleted %s, which its conditional-blocks fallback %s still waits on", phase, held, fallback)
		}
		if wakeReadyIDs(t, bd, dir, "--include-ephemeral")[fallback] {
			t.Fatalf("%s put fallback %s in bd ready after %s closed with a success reason (GH#5577)", phase, fallback, held)
		}
	}
	assertHeld("closing the main flow")

	// The age GC reaches closed wisps under --all; --age 0s makes every wisp
	// old enough.
	failed, _ := conditionalPair("canary", "failed: smoke test red")
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--all", "--age", "0s")
	assertHeld("bd mol wisp gc --all")
	bdShowFail(t, bd, dir, failed)

	failed, _ = conditionalPair("second canary", "failed: smoke test red")
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")
	assertHeld("bd mol wisp gc --closed --force")
	bdShowFail(t, bd, dir, failed)

	bdCommand(t, bd, dir, "purge", "--force")
	assertHeld("bd purge --force")

	// Disposing of the unused fallback lets the main flow go.
	bdClose(t, bd, dir, fallback, "--reason", "skipped: main flow succeeded")
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--closed", "--force")
	bdShowFail(t, bd, dir, held)
	bdShowFail(t, bd, dir, fallback)
}
