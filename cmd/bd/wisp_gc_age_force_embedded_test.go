//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// wispGCAgeJSON is the age-mode `bd mol wisp gc --json` answer: the preview
// shape (WispGCResult) and the deletion shape (deleteBatch) decoded together.
type wispGCAgeJSON struct {
	CleanedIDs   []string `json:"cleaned_ids"`
	CleanedCount int      `json:"cleaned_count"`
	Candidates   int      `json:"candidates"`
	DryRun       bool     `json:"dry_run"`
	Deleted      []string `json:"deleted"`
	DeletedCount int      `json:"deleted_count"`
}

func runWispGCAgeJSON(t *testing.T, bd, dir string, args ...string) wispGCAgeJSON {
	t.Helper()
	full := append([]string{"mol", "wisp", "gc"}, args...)
	full = append(full, "--json")
	stdout, _ := bdRunSplit(t, bd, dir, full...)
	start := strings.Index(stdout, "{")
	if start < 0 {
		t.Fatalf("bd %s produced no JSON object:\n%s", strings.Join(full, " "), stdout)
	}
	var got wispGCAgeJSON
	if err := json.NewDecoder(strings.NewReader(stdout[start:])).Decode(&got); err != nil {
		t.Fatalf("parse bd %s: %v\n%s", strings.Join(full, " "), err, stdout)
	}
	return got
}

// TestEmbeddedWispGCAgeRequiresForce is the regression test for GH#5152:
// age-mode `bd mol wisp gc` deleted abandoned wisps without --force, although
// --force is documented as "Actually delete (default: preview only)" and the
// --closed path already previews without it. Without --force every age-mode
// form (bare, explicit --age, --all, empty selection) must exit 0, delete
// nothing, and answer a preview: candidate ids and their types in text,
// dry_run true in JSON, and a footer saying to re-run with --force.
func TestEmbeddedWispGCAgeRequiresForce(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gf")

	idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral", "--type", "chore").ID

	// Bare gc: the default 1h threshold finds nothing in a fresh wisp, and
	// the empty answer is still a preview, not a zero-row deletion.
	if got := runWispGCAgeJSON(t, bd, dir); !got.DryRun || len(got.CleanedIDs) != 0 || got.CleanedCount != 0 {
		t.Errorf("bare gc --json = %+v, want an empty preview with dry_run true", got)
	}

	// Text preview: the candidate, its type and the --force footer.
	stdout, _ := bdRunSplit(t, bd, dir, "mol", "wisp", "gc", "--age", "0s")
	for _, want := range []string{idle, "chore", "Re-run with --force to delete", "keeping the same selection flags"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("gc --age 0s text preview missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Deleted") {
		t.Errorf("gc --age 0s without --force reported a deletion:\n%s", stdout)
	}
	bdShow(t, bd, dir, idle)

	// JSON preview, for explicit --age and for --all.
	for _, args := range [][]string{{"--age", "0s"}, {"--all", "--age", "0s"}} {
		got := runWispGCAgeJSON(t, bd, dir, args...)
		if !got.DryRun || got.CleanedCount != 0 || got.Candidates != 1 || len(got.CleanedIDs) != 1 || got.CleanedIDs[0] != idle {
			t.Errorf("gc %v --json = %+v, want a preview naming only %s with dry_run true", args, got, idle)
		}
		if got.DeletedCount != 0 || len(got.Deleted) != 0 {
			t.Errorf("gc %v --json without --force deleted %v", args, got.Deleted)
		}
	}

	// An exclusion that empties the selection is still a preview.
	if got := runWispGCAgeJSON(t, bd, dir, "--age", "0s", "--exclude-type", "chore"); !got.DryRun || len(got.CleanedIDs) != 0 {
		t.Errorf("gc --age 0s --exclude-type chore --json = %+v, want an empty preview", got)
	}

	// Nothing above deleted the wisp.
	bdShow(t, bd, dir, idle)
}

// TestEmbeddedWispGCAgeForceDeletesSelection pins the other side of
// GH#5152: --force is the one path that deletes, and it deletes exactly the
// age selector's eligible set without cascade. Too-young, live, excluded and
// (without --all) closed wisps survive, and so does a live dependent of a
// deleted wisp. --dry-run beside --force still deletes nothing.
func TestEmbeddedWispGCAgeForceDeletesSelection(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "gd")

	idle := bdCreate(t, bd, dir, "idle wisp", "--ephemeral").ID
	running := bdCreate(t, bd, dir, "running step", "--ephemeral").ID
	bdCommand(t, bd, dir, "update", running, "--status", "in_progress")
	excluded := bdCreate(t, bd, dir, "excluded wisp", "--ephemeral", "--type", "bug").ID
	closed := bdCreate(t, bd, dir, "closed wisp", "--ephemeral").ID
	bdClose(t, bd, dir, closed)
	// Blocked on idle, so live: cascade would take it with idle.
	dependent := bdCreate(t, bd, dir, "dependent step", "--ephemeral").ID
	bdDepAdd(t, bd, dir, dependent, idle)

	// Every wisp is younger than an hour: --force finds nothing to delete.
	if got := runWispGCAgeJSON(t, bd, dir, "--age", "1h", "--force"); got.DryRun || len(got.CleanedIDs) != 0 || got.DeletedCount != 0 {
		t.Errorf("gc --age 1h --force --json = %+v, want an empty non-preview answer", got)
	}

	// --dry-run wins over --force.
	got := runWispGCAgeJSON(t, bd, dir, "--age", "0s", "--force", "--dry-run", "--exclude-type", "bug")
	if !got.DryRun || got.DeletedCount != 0 || len(got.CleanedIDs) != 1 || got.CleanedIDs[0] != idle {
		t.Errorf("gc --age 0s --force --dry-run --json = %+v, want a preview naming only %s", got, idle)
	}
	bdShow(t, bd, dir, idle)

	got = runWispGCAgeJSON(t, bd, dir, "--age", "0s", "--force", "--exclude-type", "bug")
	if got.DeletedCount != 1 || len(got.Deleted) != 1 || got.Deleted[0] != idle {
		t.Errorf("gc --age 0s --force --json = %+v, want exactly %s deleted", got, idle)
	}
	bdShowFail(t, bd, dir, idle)
	for _, id := range []string{running, excluded, closed, dependent} {
		bdShow(t, bd, dir, id)
	}
}
