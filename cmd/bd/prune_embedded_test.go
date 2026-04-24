//go:build cgo

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// bdPrune runs "bd prune" with the given args and returns stdout.
func bdPrune(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"prune"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd prune %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdPruneFail runs "bd prune" expecting failure and returns combined output.
func bdPruneFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"prune"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd prune %s to fail, got success:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// createAndClose creates a non-ephemeral task issue and closes it. Returns the ID.
func createAndClose(t *testing.T, bd, dir, title string) string {
	t.Helper()
	issue := bdCreate(t, bd, dir, title)
	bdClose(t, bd, dir, issue.ID)
	return issue.ID
}

func TestEmbeddedPrune(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Safety gate =====

	t.Run("prune_requires_older_than_or_pattern", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pr")
		createAndClose(t, bd, dir, "Some closed task")

		// `bd prune --force` alone should fail — the gate blocks it.
		out := bdPruneFail(t, bd, dir, "--force")
		if !strings.Contains(out, "--older-than or --pattern") {
			t.Errorf("expected safety-gate error to mention --older-than or --pattern, got: %s", out)
		}

		// Confirm the bead wasn't deleted.
		listing := bdList(t, bd, dir, "--status=closed", "--json")
		if !strings.Contains(listing, "Some closed task") {
			t.Error("bd prune --force without a filter must be a no-op, but the bead was deleted")
		}
	})

	// ===== Scope: non-ephemeral closed only =====

	t.Run("prune_deletes_closed_non_ephemeral", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pnx")
		target := createAndClose(t, bd, dir, "Closed non-ephemeral task")

		// --pattern is a scope filter AND the safety gate.
		out := bdPrune(t, bd, dir, "--pattern", "pnx-*", "--force")
		if !strings.Contains(out, "Pruned") && !strings.Contains(out, "pruned") {
			t.Errorf("expected success message, got: %s", out)
		}

		// The target should be gone from the closed list.
		listing := bdList(t, bd, dir, "--status=closed", "--json")
		if strings.Contains(listing, target) {
			t.Errorf("expected %s to be deleted, still present in: %s", target, listing)
		}
	})

	// ===== Scope: must NOT touch ephemeral =====

	t.Run("prune_leaves_closed_ephemeral_for_purge", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pw")
		wispID := createAndCloseEphemeral(t, bd, dir, "Closed wisp")

		out := bdPrune(t, bd, dir, "--pattern", "pw-*", "--force")
		// Prune's non-ephemeral scope must not match the wisp at all.
		if strings.Contains(out, wispID) {
			t.Errorf("prune wrongly touched ephemeral %s: %s", wispID, out)
		}
		if !strings.Contains(strings.ToLower(out), "no closed beads to prune") {
			t.Errorf("expected prune to skip closed ephemeral %s; got: %s", wispID, out)
		}
	})

	// ===== Scope: must NOT touch open beads =====

	t.Run("prune_leaves_open_beads_alone", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "po")
		openID := bdCreate(t, bd, dir, "Still-open task").ID

		out := bdPrune(t, bd, dir, "--pattern", "po-*", "--force")
		if strings.Contains(out, openID) {
			t.Errorf("prune wrongly touched open bead %s: %s", openID, out)
		}
	})

	// ===== Dry-run shows stats, doesn't touch data =====

	t.Run("prune_dry_run_reports_without_deleting", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pd")
		target := createAndClose(t, bd, dir, "For dry-run")

		out := bdPrune(t, bd, dir, "--pattern", "pd-*", "--dry-run")
		if !strings.Contains(out, "Would prune") {
			t.Errorf("expected 'Would prune' in dry-run output, got: %s", out)
		}

		// The bead must still exist.
		listing := bdList(t, bd, dir, "--status=closed", "--json")
		if !strings.Contains(listing, target) {
			t.Errorf("dry-run deleted %s; listing: %s", target, listing)
		}
	})

	// ===== Preview mode (no --force) fails without deleting =====

	t.Run("prune_preview_fails_without_force", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pp")
		target := createAndClose(t, bd, dir, "For preview")

		// Passing a scope filter but no --force: preview-and-abort path.
		out := bdPruneFail(t, bd, dir, "--pattern", "pp-*")
		if !strings.Contains(out, "would prune") && !strings.Contains(out, "Found") {
			t.Errorf("expected preview output, got: %s", out)
		}

		listing := bdList(t, bd, dir, "--status=closed", "--json")
		if !strings.Contains(listing, target) {
			t.Errorf("preview deleted %s; listing: %s", target, listing)
		}
	})

	// ===== --older-than excludes recent beads =====

	t.Run("prune_older_than_skips_recent", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "pot")
		// Just-closed — should NOT match --older-than 30d.
		target := createAndClose(t, bd, dir, "Recent close")

		out := bdPrune(t, bd, dir, "--older-than", "30d", "--force")
		if strings.Contains(out, target) {
			t.Errorf("prune with --older-than 30d wrongly touched just-closed %s: %s", target, out)
		}

		// A "no beads to prune" message (or a zero-count result) is the
		// expected outcome.
		lower := strings.ToLower(out)
		if !strings.Contains(lower, "no ") && !strings.Contains(lower, "0 ") {
			// Not fatal — wording may vary. Ensure target survives.
			listing := bdList(t, bd, dir, "--status=closed", "--json")
			if !strings.Contains(listing, target) {
				t.Errorf("--older-than 30d deleted recent bead %s; listing: %s", target, listing)
			}
		}
	})
}
