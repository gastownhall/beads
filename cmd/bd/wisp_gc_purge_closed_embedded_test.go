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

// TestWispGCPurgeClosedDoesNotCascadeIntoLiveSteps is the regression test for
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
func TestWispGCPurgeClosedDoesNotCascadeIntoLiveSteps(t *testing.T) {
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

// TestWispGCPurgeClosedStillPurgesFullyClosedMolecules guards the other side
// of the fix: when EVERY step is closed, gc --closed --force must still remove
// the whole molecule. Restricting cascade must not break complete cleanup.
func TestWispGCPurgeClosedStillPurgesFullyClosedMolecules(t *testing.T) {
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
