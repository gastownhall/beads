//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// bdImport runs "bd import" with extra args. Returns combined output.
// bd import writes status lines like "Imported N issues" to stderr
// (see cmd/bd/import.go), so callers grepping for those need both streams.
func bdImport(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"import"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd import %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String() + stderr.String()
}

// writeJSONLFile writes issues as JSONL to the given path.
func writeJSONLFile(t *testing.T, path string, issues []types.Issue) {
	t.Helper()
	var lines []string
	for _, issue := range issues {
		b, err := json.Marshal(issue)
		if err != nil {
			t.Fatalf("marshal issue: %v", err)
		}
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write JSONL: %v", err)
	}
}

// TestEmbeddedImportFromSources was split from TestEmbeddedImport (originally
// ~239s, measured under --config=embedded) into 2 top-level tests over
// disjoint subtest groups, for CI shard balance (see
// scripts/ci/embedded_cmd_test_durations.json and engdocs/TESTING.md). Every original
// subtest is preserved exactly once.
func TestEmbeddedImportFromSources(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("from_explicit_file", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imfile")

		// Create a JSONL file with test issues
		jsonlPath := filepath.Join(t.TempDir(), "import.jsonl")
		now := time.Now().UTC()
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "imfile-aaa", Title: "Import A", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
			{ID: "imfile-bbb", Title: "Import B", Status: types.StatusOpen, IssueType: types.TypeBug, CreatedAt: now, UpdatedAt: now},
		})

		out := bdImport(t, bd, dir, jsonlPath)
		if !strings.Contains(out, "Imported 2 issues") {
			t.Errorf("expected 'Imported 2 issues', got: %s", out)
		}
	})

	t.Run("from_default_path", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imdef")

		// Write issues to the default .beads/issues.jsonl location
		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
		now := time.Now().UTC()
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "imdef-xxx", Title: "Default Path Issue", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})

		out := bdImport(t, bd, dir)
		if !strings.Contains(out, "Imported 1 issue") {
			t.Errorf("expected 'Imported 1 issue', got: %s", out)
		}
	})

	t.Run("from_configured_import_path", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imcfg")

		cmd := exec.Command(bd, "config", "set", "import.path", "beads.jsonl")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bd config set import.path failed: %v\n%s", err, out)
		}

		jsonlPath := filepath.Join(dir, ".beads", "beads.jsonl")
		now := time.Now().UTC()
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "imcfg-xxx", Title: "Configured Import Path Issue", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})

		out := bdImport(t, bd, dir)
		if !strings.Contains(out, "Imported 1 issue") {
			t.Errorf("expected 'Imported 1 issue', got: %s", out)
		}
	})

	t.Run("dry_run", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imdry")

		jsonlPath := filepath.Join(dir, ".beads", "issues.jsonl")
		now := time.Now().UTC()
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "imdry-qqq", Title: "Dry Run Issue", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})

		out := bdImport(t, bd, dir, "--dry-run")
		if !strings.Contains(out, "Would import") {
			t.Errorf("expected 'Would import' in dry-run output, got: %s", out)
		}

		// Verify issue was NOT actually created
		cmd := exec.Command(bd, "show", "imdry-qqq", "--json")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if showOut, err := cmd.CombinedOutput(); err == nil {
			t.Errorf("issue should not exist after dry-run, but show succeeded: %s", showOut)
		}
	})

	t.Run("with_memories", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "immem")

		// Create a JSONL file with an issue and a memory record
		jsonlPath := filepath.Join(t.TempDir(), "import-memories.jsonl")
		now := time.Now().UTC()
		issue := types.Issue{
			ID: "immem-aaa", Title: "Memory Test Issue",
			Status: types.StatusOpen, IssueType: types.TypeTask,
			CreatedAt: now, UpdatedAt: now,
		}
		issueLine, _ := json.Marshal(issue)
		memoryLine, _ := json.Marshal(map[string]string{
			"_type": "memory",
			"key":   "test-key",
			"value": "test-value",
		})
		content := string(issueLine) + "\n" + string(memoryLine) + "\n"
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}

		out := bdImport(t, bd, dir, jsonlPath)
		if !strings.Contains(out, "1 issue") {
			t.Errorf("expected '1 issue' in output, got: %s", out)
		}
		if !strings.Contains(out, "1 memor") {
			t.Errorf("expected '1 memor' in output, got: %s", out)
		}
	})
}

func TestEmbeddedImportUpsertAndPrefixSync(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("empty_file", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imemp")

		jsonlPath := filepath.Join(t.TempDir(), "empty.jsonl")
		if err := os.WriteFile(jsonlPath, []byte{}, 0644); err != nil {
			t.Fatal(err)
		}

		out := bdImport(t, bd, dir, jsonlPath)
		if !strings.Contains(out, "Empty file") {
			t.Errorf("expected 'Empty file' message, got: %s", out)
		}
	})

	t.Run("upsert_existing", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "imups")

		// Create an issue via bd create
		id := bdCreateSilent(t, bd, dir, "Original Title")

		// Import with updated title for the same ID. UpdatedAt must be
		// strictly newer than the created row's second-granularity
		// updated_at: equal-timestamp rows keep the local row (bd-hj85c).
		jsonlPath := filepath.Join(t.TempDir(), "upsert.jsonl")
		now := time.Now().UTC().Add(time.Hour)
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: id, Title: "Updated Title", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})

		bdImport(t, bd, dir, jsonlPath)

		// Verify the title was updated
		showCmd := exec.Command(bd, "show", id, "--json")
		showCmd.Dir = dir
		showCmd.Env = bdEnv(dir)
		stdout, stderr, err := runCommandBuffers(t, showCmd)
		if err != nil {
			t.Fatalf("bd show %s failed: %v\nstdout:\n%s\nstderr:\n%s", id, err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Updated Title") {
			t.Errorf("expected 'Updated Title' after upsert, got: %s", stdout.String())
		}
	})

	t.Run("prefix_sync", func(t *testing.T) {
		// Simulate a stale DB: init with --prefix bd (DB has issue_prefix=bd),
		// then overwrite config.yaml with issue-prefix: be. bd import must sync
		// the DB to match config.yaml (be-llaf).
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "bd")

		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue-prefix: be\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		jsonlPath := filepath.Join(t.TempDir(), "prefix-sync.jsonl")
		now := time.Now().UTC()
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "bd-sync1", Title: "Prefix Sync Issue", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})

		bdImport(t, bd, dir, jsonlPath)

		if val := readBack(t, beadsDir, "bd", "issue_prefix", false); val != "be" {
			t.Errorf("issue_prefix after import: got %q, want %q", val, "be")
		}
	})

	t.Run("prefix_sync_no_config_key", func(t *testing.T) {
		// config.yaml with no issue-prefix key must not touch the DB's
		// issue_prefix; the sync block only fires when yamlPrefix != "" (be-llaf).
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "bd")

		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("export:\n  auto: false\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		jsonlPath := filepath.Join(t.TempDir(), "prefix-sync-no-key.jsonl")
		now := time.Now().UTC()
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: "bd-nokey1", Title: "No Prefix Key Issue", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})

		bdImport(t, bd, dir, jsonlPath)

		if val := readBack(t, beadsDir, "bd", "issue_prefix", false); val != "bd" {
			t.Errorf("issue_prefix after import: got %q, want %q", val, "bd")
		}
	})
}

func TestEmbeddedImportConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "imconc")

	const numWorkers = 5

	type result struct {
		worker int
		out    string
		err    error
	}

	results := make([]result, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			// Each worker imports its own JSONL file with unique IDs
			jsonlPath := filepath.Join(t.TempDir(), fmt.Sprintf("import-%d.jsonl", worker))
			now := time.Now().UTC()
			var issues []types.Issue
			for i := 0; i < 3; i++ {
				issues = append(issues, types.Issue{
					ID:        fmt.Sprintf("imconc-w%d-%d", worker, i),
					Title:     fmt.Sprintf("Worker %d Issue %d", worker, i),
					Status:    types.StatusOpen,
					IssueType: types.TypeTask,
					CreatedAt: now,
					UpdatedAt: now,
				})
			}
			var lines []string
			for _, issue := range issues {
				b, _ := json.Marshal(issue)
				lines = append(lines, string(b))
			}
			_ = os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)

			cmd := exec.Command(bd, "import", jsonlPath)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			results[worker] = result{worker: worker, out: string(out), err: err}
		}(w)
	}
	wg.Wait()

	successes := 0
	for _, r := range results {
		if strings.Contains(r.out, "panic") {
			t.Errorf("worker %d panicked:\n%s", r.worker, r.out)
		}
		if r.err == nil {
			successes++
		} else if strings.Contains(r.out, "one writer at a time") ||
			strings.Contains(r.out, "nothing to commit") {
			// Expected concurrent contention errors
		} else {
			t.Errorf("worker %d failed with unexpected error: %v\n%s", r.worker, r.err, r.out)
		}
	}
	if successes < 1 {
		t.Errorf("expected at least 1 successful import, got %d", successes)
	}
	t.Logf("%d/%d import workers succeeded", successes, numWorkers)
}

// TestEmbeddedStaleSnapshotImportKeepsClose pins the import-side invariant
// behind GH#4767's reported lost-close symptom: a JSONL snapshot older than
// the store — imported explicitly, or delivered by a git merge whose
// post-merge hook auto-imports the tracked export — must add its new rows
// without resurrecting an issue the store already closed. The default
// stale guard (import_shared.go: strictly-older rows are skipped;
// same-updated_at ties keep the local row) is the contract under test;
// --allow-stale is the deliberate escape hatch, exercised here as the
// negative control. These are coverage regressions over the base, not a
// runtime fix: the close-dropping mechanism GH#4767 reports remains open.
func TestEmbeddedStaleSnapshotImportKeepsClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// appendJSONLIssue adds one issue as a JSONL line to an existing
	// snapshot file — the way a branch's export diverges from a committed
	// one.
	appendJSONLIssue := func(t *testing.T, path string, issue types.Issue) {
		t.Helper()
		line, err := json.Marshal(issue)
		if err != nil {
			t.Fatalf("marshal issue: %v", err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open %s for append: %v", path, err)
		}
		defer f.Close()
		if _, err := f.WriteString(string(line) + "\n"); err != nil {
			t.Fatalf("append issue line: %v", err)
		}
	}

	// readJSONLIssueByID returns the snapshot's row for one issue id.
	readJSONLIssueByID := func(t *testing.T, path, id string) types.Issue {
		t.Helper()
		data, err := os.ReadFile(path) // #nosec G304 -- path is a test fixture
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var issue types.Issue
			if err := json.Unmarshal([]byte(line), &issue); err != nil {
				t.Fatalf("parse JSONL line %q: %v", line, err)
			}
			if issue.ID == id {
				return issue
			}
		}
		t.Fatalf("no %s row in %s", id, path)
		return types.Issue{}
	}

	// seedStaleOpen imports an open issue with an hour-old updated_at, so
	// every later store write to that row is strictly newer without any
	// sleeps. Returns the seed file's path (the issue's open snapshot).
	seedStaleOpen := func(t *testing.T, dir, id string) string {
		t.Helper()
		old := time.Now().UTC().Add(-time.Hour)
		seedPath := filepath.Join(t.TempDir(), "seed.jsonl")
		writeJSONLFile(t, seedPath, []types.Issue{{
			ID: id, Title: "Stale snapshot target", Status: types.StatusOpen,
			IssueType: types.TypeTask, CreatedAt: old, UpdatedAt: old,
		}})
		bdImport(t, bd, dir, seedPath)
		return seedPath
	}

	// claimAndClose closes an open issue the reported workflow's way —
	// claim, then close with a reason — and returns its post-close state.
	claimAndClose := func(t *testing.T, dir, id, reason string) *types.Issue {
		t.Helper()
		out, err := bdRunWithFlockRetry(t, bd, dir, "update", id, "--claim")
		if err != nil {
			t.Fatalf("bd update %s --claim failed: %v\n%s", id, err, out)
		}
		bdClose(t, bd, dir, id, "--reason", reason)
		return bdShow(t, bd, dir, id)
	}

	// requireClosedUnchanged asserts the closure fields survived some
	// operation (an import, a merge) byte for byte: status, close_reason,
	// closed_at and updated_at.
	requireClosedUnchanged := func(t *testing.T, label string, got, closed *types.Issue) {
		t.Helper()
		if got.Status != types.StatusClosed {
			t.Errorf("%s: status = %s, want closed", label, got.Status)
		}
		if got.CloseReason != closed.CloseReason {
			t.Errorf("%s: close_reason = %q, want %q", label, got.CloseReason, closed.CloseReason)
		}
		if got.ClosedAt == nil || closed.ClosedAt == nil || !got.ClosedAt.Equal(*closed.ClosedAt) {
			t.Errorf("%s: closed_at = %v, want %v", label, got.ClosedAt, closed.ClosedAt)
		}
		if !got.UpdatedAt.Equal(closed.UpdatedAt) {
			t.Errorf("%s: updated_at = %v, want %v", label, got.UpdatedAt, closed.UpdatedAt)
		}
	}

	t.Run("explicit_import_keeps_close_adds_witness", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "snapimp")
		seedStaleOpen(t, dir, "snapimp-x")

		// A real-CLI snapshot of X open, written outside auto-export's
		// destination (.beads/issues.jsonl): only the explicit import
		// below can feed it to the store.
		snapshotPath := filepath.Join(t.TempDir(), "snapshot.jsonl")
		bdExport(t, bd, dir, "-o", snapshotPath)

		// W exists only in the snapshot: its appearance after the import
		// is the witness that the import ran at all.
		now := time.Now().UTC()
		appendJSONLIssue(t, snapshotPath, types.Issue{
			ID: "snapimp-w", Title: "Snapshot-only witness", Status: types.StatusOpen,
			IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now,
		})

		closed := claimAndClose(t, dir, "snapimp-x", "closed before snapshot import")

		snapX := readJSONLIssueByID(t, snapshotPath, "snapimp-x")
		if !snapX.UpdatedAt.Before(closed.UpdatedAt) {
			t.Fatalf("snapshot X updated_at %v is not before the close %v: the fixture lost its ordering", snapX.UpdatedAt, closed.UpdatedAt)
		}
		if bdShowSucceeds(t, bd, dir, "snapimp-w") {
			t.Fatal("witness snapimp-w exists before the import; the fixture leaked")
		}

		out := bdImport(t, bd, dir, snapshotPath)
		if !strings.Contains(out, "1 stale skipped") {
			t.Errorf("import did not report the stale skip for X:\n%s", out)
		}
		if w := bdShow(t, bd, dir, "snapimp-w"); w.Status != types.StatusOpen {
			t.Errorf("witness after import: status %s, want open", w.Status)
		}
		requireClosedUnchanged(t, "after import", bdShow(t, bd, dir, "snapimp-x"), closed)
	})

	t.Run("equal_updated_at_tie_keeps_closed_row", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "snaptie")
		seedStaleOpen(t, dir, "snaptie-x")
		closed := claimAndClose(t, dir, "snaptie-x", "closed before tie import")

		// The snapshot's X row carries the close's own updated_at but is
		// open with empty closure fields: the tie an equal-second export
		// produces. The default import must keep the closed local row.
		tiePath := filepath.Join(t.TempDir(), "tie.jsonl")
		writeJSONLFile(t, tiePath, []types.Issue{{
			ID: "snaptie-x", Title: "Stale snapshot target", Status: types.StatusOpen,
			IssueType: types.TypeTask, CreatedAt: closed.CreatedAt, UpdatedAt: closed.UpdatedAt,
		}})

		out := bdImport(t, bd, dir, tiePath)
		if !strings.Contains(out, "Kept local state for 1 issue(s)") {
			t.Errorf("import did not report the tie keep for X:\n%s", out)
		}
		requireClosedUnchanged(t, "after tie import", bdShow(t, bd, dir, "snaptie-x"), closed)
	})

	t.Run("allow_stale_overwrites_closed_row", func(t *testing.T) {
		// Negative control in its own fixture: with the guard deliberately
		// disabled, the same snapshot DOES overwrite the closure. The
		// keep in the subtests above is the stale guard's doing, not an
		// inability to resurrect.
		dir, _, _ := bdInit(t, bd, "--prefix", "snapstale")
		seedPath := seedStaleOpen(t, dir, "snapstale-x")
		claimAndClose(t, dir, "snapstale-x", "closed before allow-stale import")

		bdImport(t, bd, dir, "--allow-stale", seedPath)

		after := bdShow(t, bd, dir, "snapstale-x")
		if after.Status != types.StatusOpen {
			t.Errorf("--allow-stale left status %s, want open (the negative control did not overwrite)", after.Status)
		}
		if after.ClosedAt != nil {
			t.Errorf("--allow-stale kept closed_at %v, want empty", after.ClosedAt)
		}
		if after.CloseReason != "" {
			t.Errorf("--allow-stale kept close_reason %q, want empty", after.CloseReason)
		}
	})

	t.Run("post_merge_hook_import_keeps_close", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the installed git hooks are POSIX shell scripts; the merge lane is exercised on unix")
		}
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "snapmerge")

		// bd init installed its hooks and pointed core.hooksPath at them;
		// without this the merge below would silently skip the import and
		// the subtest would fail for the wrong reason.
		if _, err := os.Stat(filepath.Join(beadsDir, "hooks", "post-merge")); err != nil {
			t.Fatalf("bd init installed no post-merge hook: %v", err)
		}

		// The hooks resolve bd through PATH (generateHookSection's
		// `command -v bd`), so every git subprocess in this subtest — the
		// hooks' own bd included — must see the test binary first.
		binDir := t.TempDir()
		if err := os.Symlink(bd, filepath.Join(binDir, "bd")); err != nil {
			t.Fatalf("symlink test bd into a PATH dir: %v", err)
		}
		gitEnv := append(bdEnv(dir), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		runGit := func(dir string, args ...string) string {
			t.Helper()
			cmd := gitCommand(args...)
			cmd.Dir = dir
			cmd.Env = gitEnv
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %s failed: %v\n%s", args[0], err, out)
			}
			return string(out)
		}

		seedStaleOpen(t, dir, "snapmerge-x")

		// Commit X's pre-close snapshot as the tracked export; import.auto
		// (on by default) makes the post-merge hook import it after merges.
		// --no-verify keeps the pre-commit export from replacing the
		// crafted file.
		bdExport(t, bd, dir, "-o", filepath.Join(beadsDir, "issues.jsonl"))
		runGit(dir, "add", ".beads/issues.jsonl")
		runGit(dir, "commit", "--no-verify", "-m", "track pre-close snapshot")

		// The nested linked worktree shares the root's embedded store and
		// is created before W exists anywhere.
		wtDir := filepath.Join(t.TempDir(), "wt")
		runGit(dir, "worktree", "add", "-b", "snapmerge-wt", wtDir)

		closed := claimAndClose(t, dir, "snapmerge-x", "closed before merge")
		if wtView := bdShow(t, bd, wtDir, "snapmerge-x"); wtView.Status != types.StatusClosed {
			t.Fatalf("worktree sees %s, want the shared store's closed state", wtView.Status)
		}

		// W enters only the worktree's copy of the tracked snapshot.
		now := time.Now().UTC()
		appendJSONLIssue(t, filepath.Join(wtDir, ".beads", "issues.jsonl"), types.Issue{
			ID: "snapmerge-w", Title: "Branch witness", Status: types.StatusOpen,
			IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now,
		})
		runGit(wtDir, "add", ".beads/issues.jsonl")
		runGit(wtDir, "commit", "--no-verify", "-m", "add witness W to snapshot")

		// Advance main so the merge is a real merge: git does not run the
		// post-merge hook on a fast-forward.
		if err := os.WriteFile(filepath.Join(dir, "UNRELATED.md"), []byte("merge driver\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(dir, "add", "UNRELATED.md")
		runGit(dir, "commit", "--no-verify", "-m", "advance main")

		if bdShowSucceeds(t, bd, dir, "snapmerge-w") {
			t.Fatal("witness snapmerge-w exists before the merge; the fixture leaked")
		}

		mergeOut := runGit(dir, "merge", "--no-edit", "snapmerge-wt")
		// The hook's import must not have failed; the separate "no Dolt
		// remote configured" advisory is this lane's expected warning.
		if strings.Contains(mergeOut, "beads: post-merge import warning:") {
			t.Errorf("post-merge hook import failed:\n%s", mergeOut)
		}

		for label, showDir := range map[string]string{"root": dir, "worktree": wtDir} {
			if w := bdShow(t, bd, showDir, "snapmerge-w"); w.Status != types.StatusOpen {
				t.Errorf("%s: witness after merge: status %s, want open", label, w.Status)
			}
			requireClosedUnchanged(t, label+" after merge", bdShow(t, bd, showDir, "snapmerge-x"), closed)
		}
	})
}
