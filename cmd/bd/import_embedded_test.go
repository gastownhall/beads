//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// bdImport runs "bd import" with extra args. Returns combined output.
func bdImport(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"import"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd import %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
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

func TestEmbeddedImport(t *testing.T) {
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

		// Import with updated title for the same ID
		jsonlPath := filepath.Join(t.TempDir(), "upsert.jsonl")
		now := time.Now().UTC()
		writeJSONLFile(t, jsonlPath, []types.Issue{
			{ID: id, Title: "Updated Title", Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: now, UpdatedAt: now},
		})

		bdImport(t, bd, dir, jsonlPath)

		// Verify the title was updated
		showCmd := exec.Command(bd, "show", id, "--json")
		showCmd.Dir = dir
		showCmd.Env = bdEnv(dir)
		showOut, err := showCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd show %s failed: %v\n%s", id, err, showOut)
		}
		if !strings.Contains(string(showOut), "Updated Title") {
			t.Errorf("expected 'Updated Title' after upsert, got: %s", showOut)
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
