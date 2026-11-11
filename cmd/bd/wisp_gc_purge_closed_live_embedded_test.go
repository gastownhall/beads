//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// wispPurgeClosedJSON decodes every closed-mode `bd mol wisp gc --json`
// shape: the unconfirmed preview, the --dry-run delete preview, the deletion
// and the nothing-to-delete answer.
type wispPurgeClosedJSON struct {
	Candidates           int      `json:"candidates"`
	DryRun               bool     `json:"dry_run"`
	IssueIDs             []string `json:"issue_ids"`
	Deleted              []string `json:"deleted"`
	DeletedCount         int      `json:"deleted_count"`
	LiveDependentSkipped *int     `json:"live_dependent_skipped"`
}

func runWispPurgeClosedJSON(t *testing.T, bd, dir string, args ...string) wispPurgeClosedJSON {
	t.Helper()
	full := append([]string{"mol", "wisp", "gc", "--closed"}, args...)
	full = append(full, "--json")
	stdout, _ := bdRunSplit(t, bd, dir, full...)
	start := strings.Index(stdout, "{")
	if start < 0 {
		t.Fatalf("bd %s produced no JSON object:\n%s", strings.Join(full, " "), stdout)
	}
	var got wispPurgeClosedJSON
	if err := json.NewDecoder(strings.NewReader(stdout[start:])).Decode(&got); err != nil {
		t.Fatalf("parse bd %s: %v\n%s", strings.Join(full, " "), err, stdout)
	}
	return got
}

func liveDependentSkipped(got wispPurgeClosedJSON) int {
	if got.LiveDependentSkipped == nil {
		return -1
	}
	return *got.LiveDependentSkipped
}

// TestEmbeddedWispGCPurgeClosedHoldsBackLiveChildren is the regression test for
// GH#5152's second half: `bd mol wisp gc --closed --force` deleted a closed
// root whose child was still open, and with it the child's parent-child edge,
// so the surviving child could no longer be attributed to anything.
//
// A closed wisp that a live row depends on through parent-child or tracks is
// held back, and so is a closed ancestor of a held-back wisp; the unrelated
// closed wisp is still deleted. Preview, --dry-run and --force agree, every
// JSON shape reports live_dependent_skipped, and once the live rows close the
// next run collects everything.
func TestEmbeddedWispGCPurgeClosedHoldsBackLiveChildren(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ph")

	grandparent := bdCreate(t, bd, dir, "molecule grandparent", "--ephemeral").ID
	root := bdCreate(t, bd, dir, "molecule root", "--ephemeral").ID
	child := bdCreate(t, bd, dir, "live step", "--ephemeral").ID
	bdDepAdd(t, bd, dir, root, grandparent, "--type", "parent-child")
	bdDepAdd(t, bd, dir, child, root, "--type", "parent-child")
	tracked := bdCreate(t, bd, dir, "tracked wisp", "--ephemeral").ID
	tracker := bdCreate(t, bd, dir, "live tracker", "--ephemeral").ID
	bdDepAdd(t, bd, dir, tracker, tracked, "--type", "tracks")
	unrelated := bdCreate(t, bd, dir, "unrelated wisp", "--ephemeral").ID

	// Roots routinely close over live children (bd close --force).
	bdClose(t, bd, dir, root, "--force")
	bdClose(t, bd, dir, grandparent, "--force")
	bdClose(t, bd, dir, tracked)
	bdClose(t, bd, dir, unrelated)

	// Unconfirmed: text names the held-back count; JSON counts only unrelated.
	stdout, _ := bdRunSplit(t, bd, dir, "mol", "wisp", "gc", "--closed")
	if !strings.Contains(stdout, "Skipping 3 closed wisp(s) with live dependents") {
		t.Errorf("gc --closed text does not report the held-back wisps:\n%s", stdout)
	}
	got := runWispPurgeClosedJSON(t, bd, dir)
	if got.Candidates != 1 || !got.DryRun || liveDependentSkipped(got) != 3 {
		t.Errorf("gc --closed --json = %+v (live_dependent_skipped %d), want 1 candidate, 3 held back", got, liveDependentSkipped(got))
	}

	// --dry-run previews the same selection.
	got = runWispPurgeClosedJSON(t, bd, dir, "--dry-run")
	if len(got.IssueIDs) != 1 || got.IssueIDs[0] != unrelated || liveDependentSkipped(got) != 3 {
		t.Errorf("gc --closed --dry-run --json = %+v (live_dependent_skipped %d), want only %s, 3 held back", got, liveDependentSkipped(got), unrelated)
	}

	// --force deletes only the unrelated wisp.
	got = runWispPurgeClosedJSON(t, bd, dir, "--force")
	if got.DeletedCount != 1 || len(got.Deleted) != 1 || got.Deleted[0] != unrelated || liveDependentSkipped(got) != 3 {
		t.Errorf("gc --closed --force --json = %+v (live_dependent_skipped %d), want only %s deleted, 3 held back", got, liveDependentSkipped(got), unrelated)
	}
	bdShowFail(t, bd, dir, unrelated)
	for _, id := range []string{grandparent, root, child, tracked, tracker} {
		bdShow(t, bd, dir, id)
	}
	// The live child is still attributable to its root.
	if out := bdDepList(t, bd, dir, child, "parent-child"); !strings.Contains(out, root) {
		t.Errorf("live child %s lost its parent-child edge to %s:\n%s", child, root, out)
	}
	if out := bdDepList(t, bd, dir, tracker, "tracks"); !strings.Contains(out, tracked) {
		t.Errorf("live tracker %s lost its tracks edge to %s:\n%s", tracker, tracked, out)
	}

	// Everything left is held back: the empty answer still says why.
	got = runWispPurgeClosedJSON(t, bd, dir, "--force")
	if got.DeletedCount != 0 || liveDependentSkipped(got) != 3 {
		t.Errorf("all-skipped gc --closed --force --json = %+v (live_dependent_skipped %d), want nothing deleted, 3 held back", got, liveDependentSkipped(got))
	}

	// Once the live rows close, the next run collects the whole molecule.
	bdClose(t, bd, dir, child)
	bdClose(t, bd, dir, tracker)
	got = runWispPurgeClosedJSON(t, bd, dir, "--force")
	want := []string{grandparent, root, child, tracked, tracker}
	sort.Strings(want)
	sort.Strings(got.Deleted)
	if strings.Join(got.Deleted, ",") != strings.Join(want, ",") || liveDependentSkipped(got) != 0 {
		t.Errorf("converged gc --closed --force --json = %+v (live_dependent_skipped %d), want %v deleted", got, liveDependentSkipped(got), want)
	}
}

// bdDepList returns `bd dep list <id> --type <depType> --json`: the rows id
// depends on through that edge type.
func bdDepList(t *testing.T, bd, dir, id, depType string) string {
	t.Helper()
	stdout, _ := bdRunSplit(t, bd, dir, "dep", "list", id, "--type", depType, "--json")
	return stdout
}

// TestEmbeddedWispGCPurgeClosedReclaimsBlocksOnlyAndClosedMolecules guards
// the other side of GH#5152's live-dependent protection: it must not hold
// back what gc --closed reclaimed before. A closed step whose live
// dependents reach it only through blocks is still deleted (GH#5735: a
// closed blocker blocks nothing), and a molecule whose root and steps are
// all closed is reclaimed whole. Live rows survive, nothing cascades.
func TestEmbeddedWispGCPurgeClosedReclaimsBlocksOnlyAndClosedMolecules(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "pb")

	// Running molecule: open root, first step closed, second step blocked
	// on it and still open.
	liveRoot := bdCreate(t, bd, dir, "running molecule", "--ephemeral", "--type", "molecule").ID
	doneStep := bdCreate(t, bd, dir, "finished step", "--ephemeral").ID
	nextStep := bdCreate(t, bd, dir, "next step", "--ephemeral").ID
	bdDepAdd(t, bd, dir, doneStep, liveRoot, "--type", "parent-child")
	bdDepAdd(t, bd, dir, nextStep, liveRoot, "--type", "parent-child")
	bdDepAdd(t, bd, dir, nextStep, doneStep)
	bdClose(t, bd, dir, doneStep)

	// Finished molecule: root and both steps closed.
	doneRoot := bdCreate(t, bd, dir, "finished molecule", "--ephemeral", "--type", "molecule").ID
	stepA := bdCreate(t, bd, dir, "finished step a", "--ephemeral").ID
	stepB := bdCreate(t, bd, dir, "finished step b", "--ephemeral").ID
	bdDepAdd(t, bd, dir, stepA, doneRoot, "--type", "parent-child")
	bdDepAdd(t, bd, dir, stepB, doneRoot, "--type", "parent-child")
	bdDepAdd(t, bd, dir, stepB, stepA)
	bdClose(t, bd, dir, stepA)
	bdClose(t, bd, dir, stepB)
	bdClose(t, bd, dir, doneRoot)

	got := runWispPurgeClosedJSON(t, bd, dir, "--force")
	want := []string{doneStep, doneRoot, stepA, stepB}
	sort.Strings(want)
	sort.Strings(got.Deleted)
	if strings.Join(got.Deleted, ",") != strings.Join(want, ",") || liveDependentSkipped(got) != 0 {
		t.Errorf("gc --closed --force --json = %+v (live_dependent_skipped %d), want %v deleted, none held back", got, liveDependentSkipped(got), want)
	}
	for _, id := range want {
		bdShowFail(t, bd, dir, id)
	}
	for _, id := range []string{liveRoot, nextStep} {
		bdShow(t, bd, dir, id)
	}
	if out := bdDepList(t, bd, dir, nextStep, "parent-child"); !strings.Contains(out, liveRoot) {
		t.Errorf("live step %s lost its parent-child edge to %s:\n%s", nextStep, liveRoot, out)
	}
}
