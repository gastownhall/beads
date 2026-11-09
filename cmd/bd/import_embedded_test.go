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

// staleSnapshotFixture is one embedded workspace for
// TestEmbeddedStaleSnapshotImportKeepsClose: issue X, seeded an hour in the
// past, then claimed and closed through the real CLI, and a JSONL snapshot
// taken (with bd export) while X was still open, plus a witness issue W that
// exists only in the snapshot. Importing the snapshot must create W, which
// proves the import ran, without reopening X (GH#4767).
type staleSnapshotFixture struct {
	dir      string
	xID, wID string
	// snapshot is the exported open-state rows plus W, as written to
	// snapshotPath.
	snapshot     []types.Issue
	snapshotPath string
	// closed is X as bd show reported it right after the close.
	closed *types.Issue
}

const staleSnapshotCloseReason = "closed by a worker before the stale snapshot came back"

// openStaleSnapshotFixture builds the workspace and the snapshot with X
// still open; closeX closes it.
func openStaleSnapshotFixture(t *testing.T, bd, prefix string) *staleSnapshotFixture {
	t.Helper()
	dir, _, _ := bdInit(t, bd, "--prefix", prefix)
	f := &staleSnapshotFixture{dir: dir, xID: prefix + "-x", wID: prefix + "-w"}

	// An hour-old seed puts the close strictly after the snapshot's
	// updated_at without sleeping across a second boundary.
	seededAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	seedPath := filepath.Join(t.TempDir(), "seed.jsonl")
	writeJSONLFile(t, seedPath, []types.Issue{
		{ID: f.xID, Title: "Issue X", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: seededAt, UpdatedAt: seededAt},
	})
	bdImport(t, bd, dir, seedPath)

	// Export X's open state outside auto-export's destination
	// (.beads/issues.jsonl), so nothing bd writes later can replace it.
	exportPath := filepath.Join(t.TempDir(), "export.jsonl")
	exportCmd := exec.Command(bd, "export", "-o", exportPath)
	exportCmd.Dir = dir
	exportCmd.Env = bdEnv(dir)
	if stdout, stderr, err := runCommandBuffers(t, exportCmd); err != nil {
		t.Fatalf("bd export failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	f.snapshot = readJSONLIssues(t, exportPath)
	snapX := findIssue(t, f.snapshot, f.xID)
	if snapX.Status != types.StatusOpen || !snapX.UpdatedAt.Equal(seededAt) {
		t.Fatalf("exported X = status %q updated_at %s, want open at %s", snapX.Status, snapX.UpdatedAt, seededAt)
	}
	f.snapshot = append(f.snapshot, types.Issue{
		ID: f.wID, Title: "Witness W", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: seededAt, UpdatedAt: seededAt,
	})
	f.snapshotPath = filepath.Join(t.TempDir(), "snapshot.jsonl")
	writeJSONLFile(t, f.snapshotPath, f.snapshot)
	return f
}

func newStaleSnapshotFixture(t *testing.T, bd, prefix string) *staleSnapshotFixture {
	t.Helper()
	f := openStaleSnapshotFixture(t, bd, prefix)
	f.closeX(t, bd, f.dir)
	return f
}

// closeX claims and closes X through bd run in dir, records the result, and
// checks that W is not in the store yet.
func (f *staleSnapshotFixture) closeX(t *testing.T, bd, dir string) {
	t.Helper()
	snapX := findIssue(t, f.snapshot, f.xID)
	bdUpdate(t, bd, dir, f.xID, "--claim")
	bdClose(t, bd, dir, f.xID, "--reason", staleSnapshotCloseReason)
	f.closed = bdShow(t, bd, dir, f.xID)
	if f.closed.Status != types.StatusClosed || f.closed.ClosedAt == nil || f.closed.CloseReason != staleSnapshotCloseReason {
		t.Fatalf("after bd close, X = status %q closed_at %v close_reason %q; want closed with reason %q",
			f.closed.Status, f.closed.ClosedAt, f.closed.CloseReason, staleSnapshotCloseReason)
	}
	if !f.closed.UpdatedAt.After(snapX.UpdatedAt) {
		t.Fatalf("closed X updated_at %s is not after the snapshot's %s", f.closed.UpdatedAt, snapX.UpdatedAt)
	}
	requireIssueAbsent(t, bd, dir, f.wID)
}

// requireClosedUnchanged fails unless X, read through bd show in dir, still
// carries the closure fields captured right after the close.
func (f *staleSnapshotFixture) requireClosedUnchanged(t *testing.T, bd, dir string) {
	t.Helper()
	got := bdShow(t, bd, dir, f.xID)
	if got.Status != types.StatusClosed {
		t.Errorf("%s: X status = %q, want %q (the stale snapshot reopened it)", dir, got.Status, types.StatusClosed)
	}
	if got.ClosedAt == nil || !got.ClosedAt.Equal(*f.closed.ClosedAt) {
		t.Errorf("%s: X closed_at = %v, want %v", dir, got.ClosedAt, *f.closed.ClosedAt)
	}
	if got.CloseReason != f.closed.CloseReason {
		t.Errorf("%s: X close_reason = %q, want %q", dir, got.CloseReason, f.closed.CloseReason)
	}
	if !got.UpdatedAt.Equal(f.closed.UpdatedAt) {
		t.Errorf("%s: X updated_at = %s, want %s", dir, got.UpdatedAt, f.closed.UpdatedAt)
	}
}

func readJSONLIssues(t *testing.T, path string) []types.Issue {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var issues []types.Issue
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			t.Fatalf("parse %s line %q: %v", path, line, err)
		}
		if issue.ID != "" {
			issues = append(issues, issue)
		}
	}
	return issues
}

func findIssue(t *testing.T, issues []types.Issue, id string) *types.Issue {
	t.Helper()
	for i := range issues {
		if issues[i].ID == id {
			return &issues[i]
		}
	}
	t.Fatalf("issue %s not in snapshot", id)
	return nil
}

func requireIssueAbsent(t *testing.T, bd, dir, id string) {
	t.Helper()
	cmd := exec.Command(bd, "show", id, "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("%s: %s should not exist yet, but bd show succeeded:\n%s", dir, id, out)
	}
}

// TestEmbeddedStaleSnapshotImportKeepsClose pins that a successful close
// survives an older JSONL snapshot coming back through import, one path that
// can make a close reported as successful read as open again (GH#4767). The
// import guard keeps a local row unless the incoming updated_at is strictly
// newer; --allow-stale is the one deliberate way around it.
func TestEmbeddedStaleSnapshotImportKeepsClose(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt import tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("explicit_import_keeps_close", func(t *testing.T) {
		f := newStaleSnapshotFixture(t, bd, "stimp")

		bdImport(t, bd, f.dir, f.snapshotPath)

		if w := bdShow(t, bd, f.dir, f.wID); w.Status != types.StatusOpen {
			t.Errorf("witness W status = %q, want %q", w.Status, types.StatusOpen)
		}
		f.requireClosedUnchanged(t, bd, f.dir)
	})

	t.Run("equal_second_snapshot_keeps_close", func(t *testing.T) {
		// The open snapshot carries the close's own updated_at: a tie, which
		// the import keeps local both in its pre-filter and in the upsert.
		f := newStaleSnapshotFixture(t, bd, "sttie")
		snapX := findIssue(t, f.snapshot, f.xID)
		snapX.UpdatedAt = f.closed.UpdatedAt
		snapX.Status = types.StatusOpen
		snapX.ClosedAt = nil
		snapX.CloseReason = ""
		writeJSONLFile(t, f.snapshotPath, f.snapshot)

		bdImport(t, bd, f.dir, f.snapshotPath)

		bdShow(t, bd, f.dir, f.wID)
		f.requireClosedUnchanged(t, bd, f.dir)
	})

	t.Run("allow_stale_control_reopens", func(t *testing.T) {
		// Negative control: with the protection deliberately switched off,
		// the same snapshot does overwrite X, so the cases above would see a
		// reopen if the default guard let one through.
		f := newStaleSnapshotFixture(t, bd, "stctl")

		bdImport(t, bd, f.dir, "--allow-stale", f.snapshotPath)

		bdShow(t, bd, f.dir, f.wID)
		if got := bdShow(t, bd, f.dir, f.xID); got.Status != types.StatusOpen {
			t.Errorf("--allow-stale import: X status = %q, want %q", got.Status, types.StatusOpen)
		}
	})

	t.Run("post_merge_hook_import_from_nested_worktree_keeps_close", func(t *testing.T) {
		// The reported setup: agents in nested linked worktrees sharing the
		// root's embedded store, with bd's git hooks importing the tracked
		// JSONL after a merge. A branch whose JSONL still has X open (plus
		// W) merges after X was closed; the hook's import must bring in W
		// and leave the close alone.
		if runtime.GOOS == "windows" {
			t.Skip("git hooks run through sh")
		}
		f := openStaleSnapshotFixture(t, bd, "sthook")
		root := f.dir

		// The hooks call `bd` from PATH: put this test's binary there.
		binDir := t.TempDir()
		if err := os.Symlink(bd, filepath.Join(binDir, "bd")); err != nil {
			t.Fatal(err)
		}
		env := func(dir string) []string {
			return append(envWithout(bdEnv(dir), "PATH"), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
		git := func(dir string, args ...string) string {
			t.Helper()
			cmd := gitCommand(args...)
			cmd.Dir = dir
			cmd.Env = env(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
			}
			return string(out)
		}
		bdConfigSet := func(key, value string) {
			t.Helper()
			cmd := exec.Command(bd, "config", "set", key, value)
			cmd.Dir = root
			cmd.Env = bdEnv(root)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bd config set %s %s: %v\n%s", key, value, err, out)
			}
		}

		// Repo-local hooks, absolute so the linked worktree runs the same
		// ones, installed by the real CLI.
		hooksDir := filepath.Join(root, ".git", "hooks")
		git(root, "config", "--local", "core.hooksPath", hooksDir)
		bdHooks(t, bd, root, "install")
		if data, err := os.ReadFile(filepath.Join(hooksDir, "post-merge")); err != nil || !strings.Contains(string(data), "bd hooks run post-merge") {
			t.Fatalf("post-merge hook not installed in %s: %v\n%s", hooksDir, err, data)
		}
		bdConfigSet("import.auto", "true")
		// Keep bd's own writes out of the tracked JSONL, so the merge is the
		// only thing that changes it.
		bdConfigSet("export.auto", "false")
		if remotes := strings.TrimSpace(git(root, "remote")); remotes != "" {
			t.Fatalf("fixture must have no sync remote, got git remotes %q", remotes)
		}

		// Base commit: the tracked JSONL holds X open (without W).
		jsonlRel := filepath.Join(".beads", "issues.jsonl")
		var openRows []types.Issue
		for _, issue := range f.snapshot {
			if issue.ID != f.wID {
				openRows = append(openRows, issue)
			}
		}
		writeJSONLFile(t, filepath.Join(root, jsonlRel), openRows)
		if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".worktrees/\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(root, "add", "-A")
		for _, staged := range strings.Fields(git(root, "diff", "--cached", "--name-only")) {
			if strings.Contains(staged, "embeddeddolt") {
				t.Fatalf("fixture would commit the live database: %s", staged)
			}
		}
		git(root, "commit", "-q", "--no-verify", "-m", "base")

		// The nested linked worktree, created before W exists anywhere.
		worktree := filepath.Join(root, ".worktrees", "agent")
		git(root, "worktree", "add", "-q", "-b", "agent", worktree)

		// A worker closes X from its worktree; both checkouts see it.
		f.closeX(t, bd, worktree)
		f.requireClosedUnchanged(t, bd, root)
		requireIssueAbsent(t, bd, root, f.wID)

		// The branch brings back X's pre-close row plus W, committed with
		// --no-verify so a pre-commit export cannot replace the crafted file.
		writeJSONLFile(t, filepath.Join(worktree, jsonlRel), f.snapshot)
		git(worktree, "commit", "-q", "--no-verify", "-am", "stale snapshot")
		requireIssueAbsent(t, bd, root, f.wID)
		requireIssueAbsent(t, bd, worktree, f.wID)

		mergeOut := git(root, "merge", "--no-edit", "agent")
		if strings.Contains(mergeOut, "beads: post-merge import warning:") {
			t.Fatalf("post-merge hook import failed:\n%s", mergeOut)
		}

		for _, dir := range []string{root, worktree} {
			if w := bdShow(t, bd, dir, f.wID); w.Status != types.StatusOpen {
				t.Errorf("%s: witness W status = %q, want %q (the hook import did not run?)\nmerge output:\n%s", dir, w.Status, types.StatusOpen, mergeOut)
			}
			f.requireClosedUnchanged(t, bd, dir)
		}
	})
}
