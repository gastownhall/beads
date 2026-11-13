//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// bdRenamePrefix runs "bd rename-prefix" with the given args and returns stdout.
func bdRenamePrefix(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"rename-prefix"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd rename-prefix %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdRenamePrefixFail runs "bd rename-prefix" expecting failure.
func bdRenamePrefixFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"rename-prefix"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd rename-prefix %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// TestEmbeddedRenamePrefixBasic was split from TestEmbeddedRenamePrefix
// (originally ~197s, measured under --config=embedded) into 2 top-level
// tests over disjoint subtest groups, for CI shard balance (see
// scripts/ci/embedded_cmd_test_durations.json and engdocs/TESTING.md). Every original
// subtest is preserved exactly once.
func TestEmbeddedRenamePrefixBasic(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Basic Rename =====

	t.Run("rename_basic", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "old")
		bdCreate(t, bd, dir, "Rename test 1", "--type", "task")
		bdCreate(t, bd, dir, "Rename test 2", "--type", "task")

		out := bdRenamePrefix(t, bd, dir, "new")
		if !strings.Contains(out, "Successfully renamed") {
			t.Errorf("expected 'Successfully renamed' in output: %s", out)
		}

		// Verify issues have new prefix
		cmd := exec.Command(bd, "list")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd list after rename failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "new-") {
			t.Errorf("expected 'new-' prefix in list output: %s", stdout.String())
		}
		if strings.Contains(stdout.String(), "old-") {
			t.Errorf("unexpected 'old-' prefix still in list output: %s", stdout.String())
		}
	})

	// ===== Dry Run =====

	t.Run("rename_dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "dr")
		bdCreate(t, bd, dir, "Dry run rename", "--type", "task")

		out := bdRenamePrefix(t, bd, dir, "xx", "--dry-run")
		if !strings.Contains(out, "DRY RUN") {
			t.Errorf("expected 'DRY RUN' in output: %s", out)
		}

		// Verify prefix was NOT changed
		cmd := exec.Command(bd, "list")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd list after dry-run rename failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		if strings.Contains(stdout.String(), "xx-") {
			t.Errorf("dry-run should not have changed prefix: %s", stdout.String())
		}
	})

	// ===== Same Prefix Error =====

	t.Run("rename_same_prefix", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "sp")
		bdCreate(t, bd, dir, "Same prefix test", "--type", "task")

		bdRenamePrefixFail(t, bd, dir, "sp")
	})

	// ===== Invalid Prefix =====

	t.Run("rename_invalid_uppercase", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "iv")
		bdRenamePrefixFail(t, bd, dir, "UPPER")
	})
}

func TestEmbeddedRenamePrefixEdgeCases(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// ===== Invalid Prefix =====

	t.Run("rename_invalid_chars", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "ic")
		bdRenamePrefixFail(t, bd, dir, "BAD!")
	})

	// ===== Empty DB =====

	t.Run("rename_empty_db", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "em")
		// No issues — should just update config
		out := bdRenamePrefix(t, bd, dir, "nw")
		if !strings.Contains(out, "No issues") {
			t.Errorf("expected 'No issues' message: %s", out)
		}
	})

	// ===== Prefix Shortening (PR #5135 review blocker B1) =====
	// Shortening a multi-part prefix to one of its own leading segments
	// (e.g. "beads-vscode-" -> "beads-") must still rewrite every row —
	// "beads-vscode-1" starting with "beads-" is not the same thing as it
	// already being on the target prefix.

	t.Run("rename_prefix_shortening", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "beads-vscode")
		bdCreate(t, bd, dir, "Shortening test 1", "--type", "task")
		bdCreate(t, bd, dir, "Shortening test 2", "--type", "task")

		out := bdRenamePrefix(t, bd, dir, "beads")
		if !strings.Contains(out, "Successfully renamed") {
			t.Errorf("expected 'Successfully renamed' in output: %s", out)
		}

		cmd := exec.Command(bd, "list")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, cmd)
		if err != nil {
			t.Fatalf("bd list after rename failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
		}
		// bd create mints hash IDs (beads-cyc), never sequential ones, so assert the
		// rewritten prefix rather than a number — the beads-vscode- check below is what
		// carries the real weight. Mirrors rename_basic, which asserts "new-" for the
		// same reason.
		if !strings.Contains(stdout.String(), "beads-") {
			t.Errorf("expected shortened 'beads-' prefix in list output: %s", stdout.String())
		}
		if strings.Contains(stdout.String(), "beads-vscode-") {
			t.Errorf("regression: 'beads-vscode-' IDs were left unrewritten: %s", stdout.String())
		}
	})

	// ===== Repair (GH#6591): suffix-preserving consolidation =====

	t.Run("repair_preview_and_apply", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "kb")

		// Seed a corrupted multi-prefix workspace directly: two foreign
		// families plus a hand-named target row. The store is closed before
		// the CLI runs, so the workspace lock is free.
		ctx := t.Context()
		store, err := embeddeddolt.Open(ctx, beadsDir, dbNameFromPrefix("kb"), "main")
		if err != nil {
			t.Fatalf("open embedded store: %v", err)
		}
		for _, issue := range []*types.Issue{
			{ID: "kb-hand", Title: "Hand-named target id", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "taxdb-5su1", Title: "Foreign family", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "cre-db-c2a", Title: "Other foreign family", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		} {
			if err := store.CreateIssue(ctx, issue, "embedded-test"); err != nil {
				t.Fatalf("create %s: %v", issue.ID, err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close embedded store: %v", err)
		}

		// Text dry run previews the suffix-preserving renames.
		text := bdRenamePrefix(t, bd, dir, "kb", "--repair", "--dry-run")
		if !strings.Contains(text, "DRY RUN") || !strings.Contains(text, "cre-db-c2a -> kb-c2a") {
			t.Errorf("dry-run preview missing the suffix-preserving rename: %s", text)
		}

		// JSON dry run: exactly one object, the complete plan, no writes.
		previewOut := bdRenamePrefix(t, bd, dir, "kb", "--repair", "--dry-run", "--json")
		var preview repairJSONResult
		if err := json.Unmarshal([]byte(previewOut), &preview); err != nil {
			t.Fatalf("dry-run --json output is not exactly one object:\n%s\n%v", previewOut, err)
		}
		if !preview.DryRun || len(preview.Renames) != 2 || len(preview.Collisions) != 0 {
			t.Errorf("preview = %+v, want a dry run with 2 renames and no collisions", preview)
		}
		previewByFrom := map[string]string{}
		for _, r := range preview.Renames {
			previewByFrom[r.From] = r.To
		}
		if previewByFrom["cre-db-c2a"] != "kb-c2a" || previewByFrom["taxdb-5su1"] != "kb-5su1" {
			t.Errorf("preview renames = %v, want suffix-preserving kb-c2a and kb-5su1", previewByFrom)
		}
		if old := bdShow(t, bd, dir, "taxdb-5su1"); old == nil || old.ID != "taxdb-5su1" {
			t.Errorf("dry run changed the database: taxdb-5su1 is gone")
		}

		// JSON apply: the same mappings, performed.
		appliedOut := bdRenamePrefix(t, bd, dir, "kb", "--repair", "--json")
		var applied repairJSONResult
		if err := json.Unmarshal([]byte(appliedOut), &applied); err != nil {
			t.Fatalf("apply --json output is not exactly one object:\n%s\n%v", appliedOut, err)
		}
		if applied.DryRun || len(applied.Renames) != 2 {
			t.Errorf("applied = %+v, want dry_run false with 2 renames", applied)
		}
		for _, id := range []string{"kb-c2a", "kb-5su1", "kb-hand"} {
			if shown := bdShow(t, bd, dir, id); shown == nil || shown.ID != id {
				t.Errorf("expected %s after repair", id)
			}
		}
	})

	// ===== Mint-only (GH#6591): switch what new ids use =====

	t.Run("mint_only_switches_new_ids_only", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "taxdb")
		existing := bdCreate(t, bd, dir, "Existing issue", "--type", "task")

		out := bdRenamePrefix(t, bd, dir, "kb", "--mint-only")
		if !strings.Contains(out, "kb") || !strings.Contains(out, "untouched") {
			t.Errorf("mint-only output does not report the switch: %s", out)
		}

		// The existing row kept its id; the next create mints kb-*.
		if shown := bdShow(t, bd, dir, existing.ID); shown == nil || shown.ID != existing.ID {
			t.Errorf("mint-only renamed the existing row: %v", shown)
		}
		fresh := bdCreate(t, bd, dir, "Fresh under the new prefix", "--type", "task")
		if !strings.HasPrefix(fresh.ID, "kb-") {
			t.Errorf("fresh issue minted %q, want a kb- id", fresh.ID)
		}

		// A child of the old-prefix parent still validates.
		child := bdCreate(t, bd, dir, "Child under the old prefix", "--type", "task", "--parent", existing.ID)
		if !strings.HasPrefix(child.ID, existing.ID+".") {
			t.Errorf("child of %s minted %q, want a %s.N id", existing.ID, child.ID, existing.ID)
		}

		// --repair and --mint-only together are refused.
		bdRenamePrefixFail(t, bd, dir, "xx", "--mint-only", "--repair")
	})
}

// TestEmbeddedRenamePrefixConcurrent exercises rename-prefix --dry-run concurrently.
func TestEmbeddedRenamePrefixConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rx")

	bdCreate(t, bd, dir, "Rename concurrent issue", "--type", "task")

	const numWorkers = 8
	type workerResult struct {
		worker int
		err    error
	}
	results := make([]workerResult, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			r := workerResult{worker: worker}
			newPrefix := fmt.Sprintf("r%d", worker)
			cmd := exec.Command(bd, "rename-prefix", newPrefix, "--dry-run")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("rename-prefix --dry-run (worker %d): %v\n%s", worker, err, out)
			}
			results[worker] = r
		}(w)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}
}
