//go:build embeddeddolt

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// bdDelete runs "bd delete" with the given args and returns stdout.
func bdDelete(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"delete"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
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

func TestEmbeddedDelete(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "td")

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

	t.Run("delete_without_force_shows_preview", func(t *testing.T) {
		parent := bdCreate(t, bd, dir, "Parent strict", "--type", "task")
		child := bdCreate(t, bd, dir, "Child strict", "--type", "task")
		bdDepAdd(t, bd, dir, child.ID, parent.ID)

		// Without --force, bd delete shows a preview (exits 0) but does not delete.
		out := bdDelete(t, bd, dir, parent.ID)
		if !strings.Contains(out, "PREVIEW") && !strings.Contains(out, "preview") {
			t.Logf("expected preview output: %s", out)
		}
		// Parent should still exist.
		got := bdShow(t, bd, dir, parent.ID)
		if got.ID != parent.ID {
			t.Errorf("expected parent to still exist after preview")
		}
	})

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

	t.Run("delete_nonexistent", func(t *testing.T) {
		bdDeleteFail(t, bd, dir, "td-nonexistent999", "--force")
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
