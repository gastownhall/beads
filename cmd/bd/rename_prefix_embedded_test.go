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

	// ===== Mint-only (GH#6591) =====
	// --mint-only points new issues at the new prefix and leaves every
	// existing ID and text reference alone; the old prefix stays valid.

	t.Run("mint_only_keeps_existing_ids", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "taxdb")
		bdCreate(t, bd, dir, "Tax parent", "--id", "taxdb-u6ch", "--type", "epic")
		ref := bdCreate(t, bd, dir, "Tax ref", "--description", "see taxdb-u6ch")

		stdout, stderr := bdRunSplit(t, bd, dir, "rename-prefix", "kb", "--mint-only", "--json")
		result := decodeMintOnlyResult(t, stdout)
		if result.OldPrefix != "taxdb" || result.NewPrefix != "kb" || !result.MintOnly || result.DryRun ||
			fmt.Sprint(result.AllowedPrefixesAdded) != "[taxdb]" {
			t.Errorf("result = %+v, want taxdb -> kb with taxdb added to allowed_prefixes", result)
		}
		// An edge between taxdb-* and kb-* is stored across prefixes and
		// never blocks; the command must say so.
		if !strings.Contains(stderr, "A dependency between a taxdb-* and a kb-* issue does not block") {
			t.Errorf("stderr does not warn that taxdb-*/kb-* dependencies do not block:\n%s", stderr)
		}

		if got := bdShow(t, bd, dir, "taxdb-u6ch"); got.Title != "Tax parent" {
			t.Errorf("taxdb-u6ch = %+v, want it kept", got)
		}
		if got := bdShow(t, bd, dir, ref.ID); got.Description != "see taxdb-u6ch" {
			t.Errorf("%s description = %q, want the text unchanged", ref.ID, got.Description)
		}
		if root := bdCreate(t, bd, dir, "New root", "--type", "task"); !strings.HasPrefix(root.ID, "kb-") {
			t.Errorf("new root = %s, want a kb-* ID", root.ID)
		}
		if child := bdCreate(t, bd, dir, "Tax child", "--parent", "taxdb-u6ch"); !strings.HasPrefix(child.ID, "taxdb-u6ch.") {
			t.Errorf("child = %s, want a taxdb-u6ch.N ID", child.ID)
		}
	})

	t.Run("mint_only_dry_run_writes_nothing", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "taxdb")
		bdCreate(t, bd, dir, "Tax parent", "--id", "taxdb-u6ch")

		result := decodeMintOnlyResult(t, bdRenamePrefix(t, bd, dir, "kb", "--mint-only", "--dry-run", "--json"))
		if result.OldPrefix != "taxdb" || result.NewPrefix != "kb" || !result.MintOnly || !result.DryRun ||
			fmt.Sprint(result.AllowedPrefixesAdded) != "[taxdb]" {
			t.Errorf("result = %+v, want a dry run planning taxdb -> kb and adding taxdb", result)
		}
		if out := bdRenamePrefix(t, bd, dir, "kb", "--mint-only", "--dry-run"); !strings.Contains(out, "DRY RUN") {
			t.Errorf("text dry run did not say so: %s", out)
		}
		if root := bdCreate(t, bd, dir, "Still taxdb", "--type", "task"); !strings.HasPrefix(root.ID, "taxdb-") {
			t.Errorf("dry run changed the prefix: new root = %s", root.ID)
		}
	})

	t.Run("mint_only_with_repair_fails", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "taxdb")
		bdCreate(t, bd, dir, "Tax parent", "--id", "taxdb-u6ch")

		if out := bdRenamePrefixFail(t, bd, dir, "kb", "--mint-only", "--repair"); !strings.Contains(out, "--mint-only") {
			t.Errorf("refusal does not name --mint-only: %s", out)
		}
		if root := bdCreate(t, bd, dir, "Still taxdb", "--type", "task"); !strings.HasPrefix(root.ID, "taxdb-") {
			t.Errorf("refused run changed the prefix: new root = %s", root.ID)
		}
		if got := bdShow(t, bd, dir, "taxdb-u6ch"); got.Title != "Tax parent" {
			t.Errorf("refused run moved taxdb-u6ch: %+v", got)
		}
	})

	t.Run("mint_only_then_repair_renames", func(t *testing.T) {
		// After --mint-only the config already names kb while every row is
		// still taxdb-*. The --repair its warning advises must rename those
		// rows instead of refusing kb as the current prefix.
		dir, _, _ := bdInit(t, bd, "--prefix", "taxdb")
		bdCreate(t, bd, dir, "Tax parent", "--id", "taxdb-u6ch")
		bdRenamePrefix(t, bd, dir, "kb", "--mint-only")

		if out := bdRenamePrefix(t, bd, dir, "kb", "--repair"); !strings.Contains(out, "Successfully renamed") {
			t.Errorf("expected 'Successfully renamed' in output: %s", out)
		}
		if got := bdShow(t, bd, dir, "kb-u6ch"); got.Title != "Tax parent" {
			t.Errorf("kb-u6ch = %+v, want taxdb-u6ch renamed onto kb", got)
		}
	})

	t.Run("repair_repoints_cross_prefix_edges", func(t *testing.T) {
		// taxdb-x blocking on a kb-* issue is stored across prefixes, where
		// it never blocks. --repair moves the edge in a transaction of its
		// own once taxdb-x is kb-x, and the command's post-run commit still
		// records every write, issue_prefix (taxdb -> kb) included: a Dolt
		// commit of the edge's own would mark the command committed and
		// leave the config row out.
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "taxdb")
		blocker := bdCreate(t, bd, dir, "Open blocker", "--id", "kb-hand", "--type", "task", "--force")
		bdCreate(t, bd, dir, "Tax work", "--id", "taxdb-x", "--type", "task")
		bdDep(t, bd, dir, "add", "taxdb-x", blocker.ID)
		if _, dirty := embeddedLogCountAndDirtyTables(t, beadsDir); len(dirty) != 0 {
			t.Fatalf("fixture left the working set dirty: %v", dirty)
		}

		var result struct {
			Renames []struct {
				OldID string `json:"old_id"`
				NewID string `json:"new_id"`
			} `json:"renames"`
			UnrepointedEdges []json.RawMessage `json:"unrepointed_edges"`
		}
		out := bdRenamePrefix(t, bd, dir, "kb", "--repair", "--json")
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("--repair --json stdout is not one result object: %v\n%s", err, out)
		}
		if len(result.Renames) != 1 || result.Renames[0].OldID != "taxdb-x" || result.Renames[0].NewID != "kb-x" ||
			result.UnrepointedEdges == nil || len(result.UnrepointedEdges) != 0 {
			t.Errorf("result = %+v, want taxdb-x -> kb-x and an empty unrepointed_edges", result)
		}
		if _, dirty := embeddedLogCountAndDirtyTables(t, beadsDir); len(dirty) != 0 {
			t.Errorf("--repair left the working set dirty: %v, want every write in the command's commit", dirty)
		}
		if ready := wakeReadyIDs(t, bd, dir); ready["kb-x"] || !ready[blocker.ID] {
			t.Errorf("ready = %v, want %s ready and kb-x blocked by it", ready, blocker.ID)
		}
	})
}

// mintOnlyJSON is the single object bd rename-prefix --mint-only --json writes.
type mintOnlyJSON struct {
	OldPrefix            string   `json:"old_prefix"`
	NewPrefix            string   `json:"new_prefix"`
	MintOnly             bool     `json:"mint_only"`
	DryRun               bool     `json:"dry_run"`
	AllowedPrefixesAdded []string `json:"allowed_prefixes_added"`
}

func decodeMintOnlyResult(t *testing.T, out string) mintOnlyJSON {
	t.Helper()
	var result mintOnlyJSON
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		t.Fatalf("--mint-only --json stdout is not one result object: %v\n%s", err, out)
	}
	if dec.More() {
		t.Fatalf("--mint-only --json stdout holds more than one JSON value:\n%s", out)
	}
	return result
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
