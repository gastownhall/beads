//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
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

// cascadeContainmentFixture is issue #6301's fixture F: epic A with parent-child
// children A.1 (open) and A.2 (closed) and grandchild A.1.1 (in_progress); the
// unrelated epic B (with child B.1) discovered-from A; epic C's child C.1, whose
// only blocking dependency is on A.1; and standalone S, related to A. Every
// dependency runs from dependent to target.
type cascadeContainmentFixture struct {
	a, a1, a2, a11 string
	b, b1          string
	c, c1          string
	s              string
}

// seedCascadeContainmentFixture writes one fixture F into the workspace. Each
// call mints fresh rows, so several subtests can seed their own.
func seedCascadeContainmentFixture(t *testing.T, bd, dir string) cascadeContainmentFixture {
	t.Helper()
	a := bdCreate(t, bd, dir, "Cascade epic A", "--type", "epic")
	a1 := bdCreate(t, bd, dir, "Cascade A child one", "--type", "task")
	a2 := bdCreate(t, bd, dir, "Cascade A child two", "--type", "task")
	a11 := bdCreate(t, bd, dir, "Cascade A grandchild", "--type", "task")
	b := bdCreate(t, bd, dir, "Cascade epic B", "--type", "epic")
	b1 := bdCreate(t, bd, dir, "Cascade B child", "--type", "task")
	c := bdCreate(t, bd, dir, "Cascade epic C", "--type", "epic")
	c1 := bdCreate(t, bd, dir, "Cascade C child", "--type", "task")
	s := bdCreate(t, bd, dir, "Cascade standalone S", "--type", "task")

	// A's parent-child tree, the only edges a cascade may follow.
	bdDepAdd(t, bd, dir, a1.ID, a.ID, "--type", "parent-child")
	bdDepAdd(t, bd, dir, a2.ID, a.ID, "--type", "parent-child")
	bdDepAdd(t, bd, dir, a11.ID, a1.ID, "--type", "parent-child")
	// B's own tree, then the association that used to pull both rows in.
	bdDepAdd(t, bd, dir, b1.ID, b.ID, "--type", "parent-child")
	bdDepAdd(t, bd, dir, b.ID, a.ID, "--type", "discovered-from")
	// C's tree, and the blocking edge that used to take C.1 with it.
	bdDepAdd(t, bd, dir, c1.ID, c.ID, "--type", "parent-child")
	bdDepAdd(t, bd, dir, c1.ID, a1.ID, "--type", "blocks")
	// The association that used to take S with it.
	bdDepAdd(t, bd, dir, s.ID, a.ID, "--type", "related")

	bdClose(t, bd, dir, a2.ID)
	bdUpdate(t, bd, dir, a11.ID, "--status", "in_progress")

	return cascadeContainmentFixture{
		a: a.ID, a1: a1.ID, a2: a2.ID, a11: a11.ID,
		b: b.ID, b1: b1.ID,
		c: c.ID, c1: c1.ID,
		s: s.ID,
	}
}

// bdDeleteJSON runs "bd delete" with --json and returns the decoded stdout
// object. Fatals when the command fails or the stdout is not one JSON object.
func bdDeleteJSON(t *testing.T, bd, dir string, args ...string) map[string]interface{} {
	t.Helper()
	fullArgs := append([]string{"delete", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), "BD_JSON_ENVELOPE=0")
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd delete %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("bd delete %s stdout is not exactly one JSON object: %v\nstdout:\n%s", strings.Join(args, " "), err, stdout.String())
	}
	return out
}

// TestEmbeddedDeleteCascadeFollowsOnlyParentChild pins the containment fix for
// issue #6301's root cause: a cascade follows parent-child edges only, so an
// association into another epic (discovered-from, blocks, related) no longer
// deletes that epic, its children, or a bystander with it. The previews must
// also name every row the cascade would take — a count alone is what let the
// 48-row deletion on v1.2.2 look like a bounded one.
func TestEmbeddedDeleteCascadeFollowsOnlyParentChild(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tdc")

	t.Run("cascade_deletes_only_the_parent_child_descendants", func(t *testing.T) {
		fx := seedCascadeContainmentFixture(t, bd, dir)

		out := bdDeleteJSON(t, bd, dir, fx.a, "--cascade", "--force")
		if got := out["deleted_count"]; got != float64(4) {
			t.Fatalf("deleted_count = %v, want 4 (A, A.1, A.1.1, A.2 — the parent-child closure only)", got)
		}
		for _, id := range []string{fx.a, fx.a1, fx.a11, fx.a2} {
			bdShowFail(t, bd, dir, id)
		}
		for _, id := range []string{fx.b, fx.b1, fx.c, fx.c1, fx.s} {
			if got := bdShow(t, bd, dir, id); got.ID != id {
				t.Errorf("expected %s to survive a cascade rooted at %s", id, fx.a)
			}
		}
	})

	// The three association dependents a cascade leaves behind are not silent
	// collateral: the delete names them as orphans, drops the edges that joined
	// them to the deleted set, and settles their blocked state — C.1 loses the
	// A.1 edge it was blocked over.
	t.Run("cascade_reports_and_settles_the_association_dependents", func(t *testing.T) {
		fx := seedCascadeContainmentFixture(t, bd, dir)

		if !issueIsBlockedRaw(t, beadsDir, "tdc", fx.c1) {
			t.Fatalf("expected %s to be blocked by %s before the delete", fx.c1, fx.a1)
		}

		out := bdDeleteJSON(t, bd, dir, fx.a, "--cascade", "--force")

		rawOrphans, ok := out["orphaned_issues"].([]interface{})
		if !ok {
			t.Fatalf("orphaned_issues = %#v, want a list naming %s, %s and %s",
				out["orphaned_issues"], fx.b, fx.c1, fx.s)
		}
		gotOrphans := make([]string, 0, len(rawOrphans))
		for _, v := range rawOrphans {
			id, ok := v.(string)
			if !ok {
				t.Fatalf("orphaned_issues entry %#v is not a string", v)
			}
			gotOrphans = append(gotOrphans, id)
		}
		wantOrphans := []string{fx.b, fx.c1, fx.s}
		sort.Strings(wantOrphans)
		if !slices.Equal(gotOrphans, wantOrphans) {
			t.Errorf("orphaned_issues = %v, want exactly %v", gotOrphans, wantOrphans)
		}

		// The edges into the deleted set are gone; the survivors' own
		// parent-child edges stay.
		assertDepNotExists(t, beadsDir, "tdc", fx.b, fx.a)
		assertDepNotExists(t, beadsDir, "tdc", fx.c1, fx.a1)
		assertDepNotExists(t, beadsDir, "tdc", fx.s, fx.a)
		assertDepExists(t, beadsDir, "tdc", fx.b1, fx.b)
		assertDepExists(t, beadsDir, "tdc", fx.c1, fx.c)

		if issueIsBlockedRaw(t, beadsDir, "tdc", fx.c1) {
			t.Errorf("expected %s to be unblocked after %s was deleted", fx.c1, fx.a1)
		}
	})

	// A preview that lists only the requested ids with counts beside them is
	// what let the 48-row deletion on v1.2.2 look like a bounded one: the text
	// preview must name every row the cascade would take and every row it
	// would orphan, before the caller confirms anything.
	t.Run("preview_names_every_row_the_cascade_would_take", func(t *testing.T) {
		fx := seedCascadeContainmentFixture(t, bd, dir)

		out := bdDelete(t, bd, dir, fx.a, "--cascade")

		for _, id := range []string{fx.a, fx.a1, fx.a11, fx.a2} {
			if !strings.Contains(out, id) {
				t.Errorf("preview does not name would-delete %s:\n%s", id, out)
			}
		}
		for _, id := range []string{fx.b, fx.c1, fx.s} {
			if !strings.Contains(out, id) {
				t.Errorf("preview does not name would-orphan %s:\n%s", id, out)
			}
		}

		// The preview is a read: nothing went anywhere.
		for _, id := range []string{fx.a, fx.a1, fx.a11, fx.a2, fx.b, fx.b1, fx.c, fx.c1, fx.s} {
			if got := bdShow(t, bd, dir, id); got.ID != id {
				t.Errorf("preview deleted %s", id)
			}
		}
	})
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
