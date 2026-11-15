//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdDelete runs "bd delete" with the given args and returns stdout.
// Retries on flock contention.
func bdDelete(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"delete"}, args...)
	out, err := bdRunWithFlockRetry(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd delete %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdDeleteFail runs "bd delete" expecting failure.
func bdDeleteFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"delete"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd delete %s to fail, but it succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdShowFail runs "bd show" expecting failure (e.g., deleted issue).
func bdShowFail(t *testing.T, bd, dir, id string) string {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd show %s to fail (deleted), but succeeded:\n%s", id, out)
	}
	return string(out)
}

// setupEmbeddedDelete is the TestEmbeddedDelete* tests' workspace (prefix td).
func setupEmbeddedDelete(t *testing.T) (string, string) {
	t.Helper()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "td")
	return bd, dir
}

func TestEmbeddedDeleteBasic(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd, dir := setupEmbeddedDelete(t)

	t.Run("delete_single_issue", func(t *testing.T) {
		issue := bdCreate(t, bd, dir, "Delete me", "--type", "task")
		bdDelete(t, bd, dir, issue.ID, "--force")
		bdShowFail(t, bd, dir, issue.ID)
	})

	t.Run("delete_cleans_up_dependencies", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Parent", "--type", "task")
		child := bdCreate(t, bd, dir, "Child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		// Delete child; parent should survive.
		bdDelete(t, bd, dir, child.ID, "--force")
		bdShowFail(t, bd, dir, child.ID)
		got := bdShow(t, bd, dir, parent.ID)
		if got.Status == types.StatusClosed {
			t.Error("expected parent to still be open")
		}
	})

	t.Run("delete_nonexistent", func(t *testing.T) {
		bdDeleteFail(t, bd, dir, "td-nonexistent999", "--force")
	})
}

func TestEmbeddedDeletePreviewAndGuards(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd, dir := setupEmbeddedDelete(t)

	t.Run("delete_without_force_shows_preview", func(t *testing.T) {
		target := bdCreate(t, bd, dir, "Lonely", "--type", "task")

		// Without --force, bd delete shows a preview (exits 0) but does not delete.
		out := bdDelete(t, bd, dir, target.ID)
		if !strings.Contains(out, "PREVIEW") && !strings.Contains(out, "preview") {
			t.Logf("expected preview output: %s", out)
		}
		got := bdShow(t, bd, dir, target.ID)
		if got.ID != target.ID {
			t.Errorf("expected the target to still exist after preview")
		}
	})

	// The one-id preview over a bead with an OUTSIDE dependent now refuses
	// rather than exiting 0, which is a change and is the direct route
	// converging with itself: the batch path and `--dry-run` have always
	// refused here (see TestEmbeddedDeleteJSONDependencyErrorContract, which
	// runs both), and only the unconfirmed single-id preview did not. The
	// refusal is the role's, so both routes and all three of this command's
	// paths now give the same answer to the same question.
	t.Run("delete_without_force_refuses_over_an_outside_dependent", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Parent strict", "--type", "task")
		child := bdCreate(t, bd, dir, "Child strict", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		out := bdDeleteFail(t, bd, dir, parent.ID)
		if !strings.Contains(out, "dependents not in deletion set") {
			t.Errorf("refusal did not name the guard: %s", out)
		}
		if !strings.Contains(out, "--cascade") || !strings.Contains(out, "--force") {
			t.Errorf("refusal did not say what to send instead: %s", out)
		}
		// Nothing was deleted, on either end of the edge.
		if got := bdShow(t, bd, dir, parent.ID); got.ID != parent.ID {
			t.Errorf("expected the parent to survive a refused delete")
		}
		if got := bdShow(t, bd, dir, child.ID); got.ID != child.ID {
			t.Errorf("expected the dependent to survive a refused delete")
		}
	})

	t.Run("delete_single_quiet_forced_dry_run_is_payload_blind", func(t *testing.T) {
		titleMarker := "EMBEDDED_QUIET_TITLE_MARKER"
		descriptionMarker := "EMBEDDED_QUIET_DESCRIPTION_MARKER"
		notesMarker := "EMBEDDED_QUIET_NOTES_MARKER"
		payloadMarker := "EMBEDDED_QUIET_PAYLOAD_MARKER"
		parent := bdCreate(t, bd, dir, titleMarker,
			"--type", "task",
			"--description", descriptionMarker,
			"--notes", notesMarker,
			"--metadata", `{"marker":"`+payloadMarker+`"}`)
		child := bdCreate(t, bd, dir, "Embedded quiet dependent", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		cmd := exec.Command(bd, "delete", parent.ID, "--force", "--dry-run", "--quiet")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("single forced quiet dry-run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("quiet dry-run produced stdout: %s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		for _, marker := range []string{titleMarker, descriptionMarker, notesMarker, payloadMarker} {
			if strings.Contains(combined, marker) {
				t.Fatalf("quiet dry-run leaked %q: %s", marker, combined)
			}
		}
		if got := bdShowDetails(t, bd, dir, parent.ID); got["id"] != parent.ID {
			t.Fatalf("forced dry-run removed parent: got %v, want %q", got["id"], parent.ID)
		}
		if got := bdShowDetails(t, bd, dir, child.ID); got["id"] != child.ID {
			t.Fatalf("forced dry-run removed dependent: got %v, want %q", got["id"], child.ID)
		}
	})
}

func TestEmbeddedDeleteForceAndBatch(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd, dir := setupEmbeddedDelete(t)

	t.Run("delete_force_orphans_dependents", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Force parent", "--type", "task")
		child := bdCreate(t, bd, dir, "Force child", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		bdDelete(t, bd, dir, parent.ID, "--force")
		bdShowFail(t, bd, dir, parent.ID)
		// Child should still exist (orphaned).
		got := bdShow(t, bd, dir, child.ID)
		if got.ID != child.ID {
			t.Errorf("expected orphaned child to survive, got %s", got.ID)
		}
	})

	t.Run("delete_batch", func(t *testing.T) {
		issue1 := bdCreate(t, bd, dir, "Batch 1", "--type", "task")
		issue2 := bdCreate(t, bd, dir, "Batch 2", "--type", "task")
		issue3 := bdCreate(t, bd, dir, "Batch 3", "--type", "task")

		bdDelete(t, bd, dir, issue1.ID, issue2.ID, issue3.ID, "--force")
		bdShowFail(t, bd, dir, issue1.ID)
		bdShowFail(t, bd, dir, issue2.ID)
		bdShowFail(t, bd, dir, issue3.ID)
	})
}

func TestEmbeddedDeleteJSONDependencyErrorContract(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "jde")

	for _, tc := range []struct {
		name  string
		batch bool
	}{
		{name: "single"},
		{name: "batch", batch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			titleMarker := "EMBEDDED_JSON_ERROR_TITLE_" + tc.name
			descriptionMarker := "EMBEDDED_JSON_ERROR_DESCRIPTION_" + tc.name
			notesMarker := "EMBEDDED_JSON_ERROR_NOTES_" + tc.name
			metadataMarker := "EMBEDDED_JSON_ERROR_METADATA_" + tc.name
			parent := bdCreate(t, bd, dir, titleMarker,
				"--type", "task",
				"--description", descriptionMarker,
				"--notes", notesMarker,
				"--metadata", `{"marker":"`+metadataMarker+`"}`)
			child := bdCreate(t, bd, dir, "JSON error dependent "+tc.name, "--type", "task")
			bdDepAdd(t, bd, dir, child.ID, parent.ID)

			issueIDs := []string{parent.ID}
			if tc.batch {
				other := bdCreate(t, bd, dir, "JSON error batch companion", "--type", "task")
				issueIDs = append(issueIDs, other.ID)
			}

			runDelete := func(flags ...string) (string, string, error) {
				args := append([]string{"delete"}, issueIDs...)
				args = append(args, flags...)
				cmd := exec.Command(bd, args...)
				cmd.Dir = dir
				cmd.Env = append(bdEnv(dir), "BD_JSON_ENVELOPE=0")
				stdout, stderr, err := runCommandBuffers(t, cmd)
				return stdout.String(), stderr.String(), err
			}

			stdout, stderr, err := runDelete("--dry-run", "--json")
			if err == nil {
				t.Fatalf("unforced dependency-blocked JSON dry-run succeeded\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			var preview map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &preview); err != nil {
				t.Fatalf("unforced preview stdout is not exactly one JSON object: %v\nstdout:\n%s", err, stdout)
			}
			if preview["preview"] != true || preview["dry_run"] != true {
				t.Fatalf("unforced preview missing structural fields: %#v", preview)
			}
			if _, ok := preview["error"].(string); !ok {
				t.Fatalf("unforced preview missing dependency error: %#v", preview)
			}
			var jsonErr map[string]interface{}
			if err := json.Unmarshal([]byte(stderr), &jsonErr); err != nil {
				t.Fatalf("unforced stderr is not exactly one JSON error object: %v\nstderr:\n%s", err, stderr)
			}
			if _, ok := jsonErr["error"].(string); !ok {
				t.Fatalf("unforced stderr missing error field: %#v", jsonErr)
			}
			if strings.Contains(stderr, "Error:") {
				t.Fatalf("unforced stderr contains plaintext error prefix: %s", stderr)
			}
			for _, marker := range []string{titleMarker, descriptionMarker, notesMarker, metadataMarker} {
				if strings.Contains(stdout+stderr, marker) {
					t.Fatalf("unforced JSON dry-run leaked %q", marker)
				}
			}

			stdout, stderr, err = runDelete("--force", "--dry-run", "--quiet", "--json")
			if err != nil {
				t.Fatalf("forced quiet JSON dry-run failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			if strings.TrimSpace(stderr) != "" {
				t.Fatalf("forced quiet JSON dry-run wrote stderr: %s", stderr)
			}
			var forcedPreview map[string]interface{}
			if err := json.Unmarshal([]byte(stdout), &forcedPreview); err != nil {
				t.Fatalf("forced quiet JSON stdout is not exactly one JSON object: %v\nstdout:\n%s", err, stdout)
			}
			for _, key := range []string{"preview", "dry_run", "would_delete", "would_orphan"} {
				if _, ok := forcedPreview[key]; !ok {
					t.Fatalf("forced quiet JSON preview missing %q: %#v", key, forcedPreview)
				}
			}
			for _, marker := range []string{titleMarker, descriptionMarker, notesMarker, metadataMarker} {
				if strings.Contains(stdout, marker) {
					t.Fatalf("forced quiet JSON dry-run leaked %q", marker)
				}
			}

			if got := bdShowDetails(t, bd, dir, parent.ID); got["id"] != parent.ID {
				t.Fatalf("dry-run removed parent: got %v, want %q", got["id"], parent.ID)
			}
			if got := bdShowDetails(t, bd, dir, child.ID); got["id"] != child.ID {
				t.Fatalf("dry-run removed dependent: got %v, want %q", got["id"], child.ID)
			}
			assertDepExists(t, beadsDir, "jde", child.ID, parent.ID)
		})
	}
}

func TestEmbeddedGetDependencies(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "gd")

	parent := bdCreate(t, bd, dir, "Parent", "--type", "task")
	child := bdCreate(t, bd, dir, "Child", "--type", "task")
	bdDepAdd(t, bd, dir, child.ID, parent.ID)

	store := openStore(t, beadsDir, "gd")

	t.Run("get_dependencies", func(t *testing.T) {
		deps, err := store.GetDependencies(t.Context(), child.ID)
		if err != nil {
			t.Fatalf("GetDependencies: %v", err)
		}
		if len(deps) != 1 {
			t.Fatalf("expected 1 dependency, got %d", len(deps))
		}
		if deps[0].ID != parent.ID {
			t.Errorf("expected dependency on %s, got %s", parent.ID, deps[0].ID)
		}
	})

	t.Run("get_dependents", func(t *testing.T) {
		deps, err := store.GetDependents(t.Context(), parent.ID)
		if err != nil {
			t.Fatalf("GetDependents: %v", err)
		}
		if len(deps) != 1 {
			t.Fatalf("expected 1 dependent, got %d", len(deps))
		}
		if deps[0].ID != child.ID {
			t.Errorf("expected dependent %s, got %s", child.ID, deps[0].ID)
		}
	})

	t.Run("get_dependencies_empty", func(t *testing.T) {
		deps, err := store.GetDependencies(t.Context(), parent.ID)
		if err != nil {
			t.Fatalf("GetDependencies: %v", err)
		}
		if len(deps) != 0 {
			t.Errorf("expected 0 dependencies for parent, got %d", len(deps))
		}
	})
}

// cascadeFixture is the GH#6301 cascade-boundary workspace: epic A with a
// two-level containment tree in mixed statuses, and rows joined to that tree
// by association edges only.
type cascadeFixture struct {
	A, A1, A2, A11 string // the tree a cascade from A deletes
	B, B1          string // an unrelated epic discovered-from A, and its child
	C, C1          string // another epic whose child C1 is blocked by A1
	S              string // a standalone issue related to A
}

func (f cascadeFixture) deleted() []string { return []string{f.A, f.A1, f.A11, f.A2} }

func (f cascadeFixture) survivors() []string { return []string{f.B, f.B1, f.C, f.C1, f.S} }

func setupCascadeFixture(t *testing.T, bd, dir string) cascadeFixture {
	t.Helper()
	var f cascadeFixture
	f.A = bdCreate(t, bd, dir, "Epic A", "--type", "epic").ID
	f.A1 = bdCreate(t, bd, dir, "A child one", "--type", "task", "--parent", f.A).ID
	f.A2 = bdCreate(t, bd, dir, "A child two", "--type", "task", "--parent", f.A).ID
	f.A11 = bdCreate(t, bd, dir, "A grandchild", "--type", "task", "--parent", f.A1).ID
	f.B = bdCreate(t, bd, dir, "Unrelated epic B", "--type", "epic").ID
	f.B1 = bdCreate(t, bd, dir, "B child", "--type", "task", "--parent", f.B).ID
	f.C = bdCreate(t, bd, dir, "Epic C", "--type", "epic").ID
	f.C1 = bdCreate(t, bd, dir, "C child blocked by A child", "--type", "task", "--parent", f.C).ID
	f.S = bdCreate(t, bd, dir, "Standalone", "--type", "task").ID
	bdClose(t, bd, dir, f.A2)
	bdUpdate(t, bd, dir, f.A11, "--status", "in_progress")
	bdDepAdd(t, bd, dir, f.B, f.A, "--type", "discovered-from")
	bdDepAdd(t, bd, dir, f.C1, f.A1)
	bdDepAdd(t, bd, dir, f.S, f.A, "--type", "related")
	return f
}

// TestEmbeddedDeleteCascadeFollowsOnlyParentChild is GH#6301's reproduction:
// `bd delete <epic> --cascade` followed every dependency type, so one
// discovered-from edge took an unrelated epic and its child, a blocks edge took
// another epic's child, and a related edge took a standalone issue.
func TestEmbeddedDeleteCascadeFollowsOnlyParentChild(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("forced_cascade_deletes_only_the_containment_tree", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cpc")
		f := setupCascadeFixture(t, bd, dir)

		bdDelete(t, bd, dir, f.A, "--cascade", "--force")
		for _, id := range f.deleted() {
			bdShowFail(t, bd, dir, id)
		}
		for _, id := range f.survivors() {
			if got := bdShowDetails(t, bd, dir, id); got["id"] != id {
				t.Errorf("cascade from %s deleted %s, which is outside its containment tree", f.A, id)
			}
		}
	})

	t.Run("cascade_reports_and_releases_surviving_dependents", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cpo")
		f := setupCascadeFixture(t, bd, dir)
		if !blockedIDs(t, bd, dir)[f.C1] {
			t.Fatalf("precondition: %s must be blocked by %s before the delete", f.C1, f.A1)
		}

		got := bdDeleteJSON(t, bd, dir, f.A, "--cascade", "--force")
		want := []string{f.B, f.C1, f.S}
		sort.Strings(want)
		if orphans := jsonStrings(got["orphaned_issues"]); !reflect.DeepEqual(orphans, want) {
			t.Errorf("orphaned_issues = %v, want %v — the surviving direct dependents of the deleted tree", orphans, want)
		}
		for _, id := range want {
			for _, dep := range dependsOnIDs(bdShowDetails(t, bd, dir, id)) {
				if slices.Contains(f.deleted(), dep) {
					t.Errorf("survivor %s still depends on deleted %s", id, dep)
				}
			}
		}
		if blockedIDs(t, bd, dir)[f.C1] {
			t.Errorf("%s is still blocked after its only blocker %s was deleted", f.C1, f.A1)
		}
	})

	t.Run("cascade_preview_lists_every_id_and_changes_nothing", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cpp")
		f := setupCascadeFixture(t, bd, dir)

		historyBefore := make(map[string]int)
		for _, id := range append(f.deleted(), f.survivors()...) {
			historyBefore[id] = len(bdHistoryJSON(t, bd, dir, id))
		}

		for _, args := range [][]string{{"--cascade"}, {"--cascade", "--dry-run"}} {
			out := bdDelete(t, bd, dir, append([]string{f.A}, args...)...)
			if !strings.Contains(out, "Would delete: 4 issues") {
				t.Errorf("bd delete %s %v: preview does not count the 4-row tree:\n%s", f.A, args, out)
			}
			// The heading counts the whole resolved set, not the one named id.
			if !strings.Contains(out, "Issues to delete (4):") {
				t.Errorf("bd delete %s %v: deletion heading does not count the 4-row tree:\n%s", f.A, args, out)
			}
			listed := previewListedIDs(out)
			for _, id := range append(f.deleted(), f.B, f.C1, f.S) {
				if listed[id] != 1 {
					t.Errorf("bd delete %s %v: preview lists %s %d times, want once:\n%s", f.A, args, id, listed[id], out)
				}
			}
			for _, id := range []string{f.B1, f.C} {
				if listed[id] > 0 {
					t.Errorf("bd delete %s %v: preview lists %s, which neither goes nor loses an edge:\n%s", f.A, args, id, out)
				}
			}
		}
		for _, id := range append(f.deleted(), f.survivors()...) {
			if got := bdShowDetails(t, bd, dir, id); got["id"] != id {
				t.Errorf("a preview deleted %s", id)
			}
			if got := len(bdHistoryJSON(t, bd, dir, id)); got != historyBefore[id] {
				t.Errorf("a preview changed %s's history: %d entries, was %d", id, got, historyBefore[id])
			}
		}
	})

	t.Run("cascade json preview names exactly what the deletion then reports", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cpj")
		f := setupCascadeFixture(t, bd, dir)
		wantDeleted := f.deleted()
		sort.Strings(wantDeleted)
		wantOrphans := []string{f.B, f.C1, f.S}
		sort.Strings(wantOrphans)

		preview := bdDeleteJSON(t, bd, dir, f.A, "--cascade", "--dry-run")
		if got := jsonStrings(preview["would_delete_ids"]); !reflect.DeepEqual(got, wantDeleted) {
			t.Errorf("would_delete_ids = %v, want %v", got, wantDeleted)
		}
		if got := jsonStrings(preview["would_orphan_ids"]); !reflect.DeepEqual(got, wantOrphans) {
			t.Errorf("would_orphan_ids = %v, want %v", got, wantOrphans)
		}
		if preview["would_delete"] != float64(4) || preview["would_orphan"] != float64(3) {
			t.Errorf("preview counts: would_delete %v, would_orphan %v; want 4 and 3", preview["would_delete"], preview["would_orphan"])
		}

		done := bdDeleteJSON(t, bd, dir, f.A, "--cascade", "--force")
		if got, want := jsonStrings(done["deleted_ids"]), jsonStrings(preview["would_delete_ids"]); !reflect.DeepEqual(got, want) {
			t.Errorf("deleted_ids = %v, but the preview promised %v", got, want)
		}
		if got, want := jsonStrings(done["orphaned_issues"]), jsonStrings(preview["would_orphan_ids"]); !reflect.DeepEqual(got, want) {
			t.Errorf("orphaned_issues = %v, but the preview promised %v", got, want)
		}
		if done["deleted_count"] != preview["would_delete"] {
			t.Errorf("deleted_count = %v, but the preview promised %v", done["deleted_count"], preview["would_delete"])
		}
		if got := jsonStrings(done["deleted"]); !reflect.DeepEqual(got, []string{f.A}) {
			t.Errorf("deleted = %v, want the requested ids [%s]", got, f.A)
		}
	})

	// The confirmed run's text names the rows too: a count is what the 48-row
	// deletion on v1.2.2 hid behind.
	t.Run("cascade success text names every deleted row", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "cpt")
		f := setupCascadeFixture(t, bd, dir)
		wantDeleted := f.deleted()
		sort.Strings(wantDeleted)

		out := bdDelete(t, bd, dir, f.A, "--cascade", "--force")
		if want := "  Deleted: " + strings.Join(wantDeleted, ", ") + "\n"; !strings.Contains(out, want) {
			t.Errorf("success text does not list the deleted rows %q:\n%s", want, out)
		}
	})
}

// previewListedIDs counts the ids a delete preview lists one per line: the
// first field of every indented line, without a trailing colon.
func previewListedIDs(out string) map[string]int {
	listed := make(map[string]int)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			listed[strings.TrimSuffix(fields[0], ":")]++
		}
	}
	return listed
}

// bdDeleteJSON runs "bd delete <args> --json" and parses its object.
func bdDeleteJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	stdout, _ := bdRunSplit(t, bd, dir, append(append([]string{"delete"}, args...), "--json")...)
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("parse bd delete --json: %v\n%s", err, stdout)
	}
	return out
}

// jsonStrings converts a decoded JSON array of strings to []string.
func jsonStrings(v interface{}) []string {
	items, _ := v.([]interface{})
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// dependsOnIDs is the ids a "bd show --json" issue depends on.
func dependsOnIDs(details map[string]interface{}) []string {
	deps, _ := details["dependencies"].([]interface{})
	var out []string
	for _, dep := range deps {
		if m, ok := dep.(map[string]interface{}); ok {
			if id, ok := m["id"].(string); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

// blockedIDs is the set of ids "bd blocked" lists.
func blockedIDs(t *testing.T, bd, dir string) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	for _, entry := range bdBlockedJSON(t, bd, dir) {
		if id, ok := entry["id"].(string); ok {
			out[id] = true
		}
	}
	return out
}
