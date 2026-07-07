//go:build cgo

package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd prune %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
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

	t.Run("prune_last_issue_removes_gitignored_auto_export", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "pjx")
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".beads/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"config", "set", "export.auto", "true"},
			{"config", "set", "export.git-add", "true"},
		} {
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bd %s failed: %v\n%s", strings.Join(args, " "), err, out)
			}
		}

		target := createAndClose(t, bd, dir, "Closed exported task")
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		cmd := exec.Command(bd, "export", "-o", jsonlPath)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd export failed: %v\n%s", err, out)
		}
		if _, err := os.Stat(jsonlPath); err != nil {
			t.Fatalf("expected stale JSONL before prune: %v", err)
		}
		if err := os.Remove(filepath.Join(beadsDir, exportAutoStateFile)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}

		bdPrune(t, bd, dir, "--pattern", target, "--force")
		if _, err := os.Stat(jsonlPath); !os.IsNotExist(err) {
			t.Fatalf("expected prune of last issue to remove stale JSONL, stat err=%v", err)
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

// TestPrune_protectsBeadCitedByCustomStatusBead is a regression test for the
// maphew review of PR #4023 (be-e2nb): reference-aware prune must scan beads in
// active *custom* statuses, not only the built-in non-closed statuses. A closed
// bead cited by a bead in an active custom status must be protected from prune,
// exactly like one cited by a built-in open bead.
func TestPrune_protectsBeadCitedByCustomStatusBead(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rcs")

	// Define an active custom status.
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "status.custom", "in_review:active") //nolint:errcheck

	// A closed bead that will be cited, and a closed bead nobody cites.
	cited := createAndClose(t, bd, dir, "Closed bead cited by a reviewer bead")
	orphan := createAndClose(t, bd, dir, "Closed orphan bead")

	// An open bead moved into the active custom status, whose description cites
	// the closed bead. With the fix this protects `cited`; without it `cited`
	// is pruned because the custom status is never scanned.
	citing := bdCreate(t, bd, dir, "Reviewer bead", "--description", "depends on "+cited)
	bdRunWithFlockRetry(t, bd, dir, "update", citing.ID, "--status", "in_review") //nolint:errcheck

	// Guard the test's own premise: the citing bead must really be in the custom
	// status, not a built-in non-closed status (which would protect `cited`
	// regardless of the fix and make this test vacuous).
	if got := bdShow(t, bd, dir, citing.ID); got.Status != "in_review" {
		t.Fatalf("setup: expected citing bead %s in custom status in_review, got %q", citing.ID, got.Status)
	}

	bdPrune(t, bd, dir, "--pattern", "rcs-*", "--force")

	// `cited` must survive (protected by the in_review reference); `orphan` must
	// be gone (proving prune actually deleted something).
	if got := bdShow(t, bd, dir, cited); got.ID != cited {
		t.Errorf("expected cited bead %s to survive prune, got %q", cited, got.ID)
	}
	bdShowFail(t, bd, dir, orphan)
}
