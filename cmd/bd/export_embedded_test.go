//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// bdExport runs "bd export" with extra args. Returns combined output.
func bdExport(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"export"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd export %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestEmbeddedExport(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt export tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("default_stdout", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "exstd")
		bdCreateSilent(t, bd, dir, "export test issue 1")
		bdCreateSilent(t, bd, dir, "export test issue 2")

		out := bdExport(t, bd, dir)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) < 2 {
			t.Errorf("expected at least 2 JSONL lines, got %d: %s", len(lines), out)
		}
		// Each line should be valid JSON with an "id" field
		for _, line := range lines {
			if !strings.Contains(line, `"id"`) {
				t.Errorf("expected JSON with 'id' field, got: %s", line)
			}
		}
	})

	t.Run("output_file", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "exfile")
		bdCreateSilent(t, bd, dir, "file export issue")

		outFile := filepath.Join(t.TempDir(), "export.jsonl")
		bdExport(t, bd, dir, "-o", outFile)

		data, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("failed to read export file: %v", err)
		}
		if !strings.Contains(string(data), "file export issue") {
			t.Errorf("export file should contain issue title, got: %s", data)
		}
	})

	t.Run("all_flag", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "exall")
		bdCreateSilent(t, bd, dir, "regular issue")

		// Export without --all
		outDefault := bdExport(t, bd, dir)

		// Export with --all (includes infra, templates, etc.)
		outAll := bdExport(t, bd, dir, "--all")

		// --all should produce at least as many lines
		defaultLines := len(strings.Split(strings.TrimSpace(outDefault), "\n"))
		allLines := len(strings.Split(strings.TrimSpace(outAll), "\n"))
		if allLines < defaultLines {
			t.Errorf("--all should produce >= lines than default: all=%d default=%d", allLines, defaultLines)
		}
	})

	t.Run("include_infra", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "exinfra")
		bdCreateSilent(t, bd, dir, "regular issue for infra test")

		// Just verify it runs without error
		bdExport(t, bd, dir, "--include-infra")
	})

	t.Run("scrub", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "exscrub")
		bdCreateSilent(t, bd, dir, "real issue")
		bdCreateSilent(t, bd, dir, "test issue for scrub")

		out := bdExport(t, bd, dir, "--scrub")
		// Should still have at least one line
		if strings.TrimSpace(out) == "" {
			t.Error("--scrub should still produce output for non-test issues")
		}
	})

	t.Run("no_memories", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "exnomem")
		bdCreateSilent(t, bd, dir, "no memories issue")

		outWith := bdExport(t, bd, dir)
		outWithout := bdExport(t, bd, dir, "--no-memories")

		// --no-memories should produce <= lines (no memory records)
		withLines := len(strings.Split(strings.TrimSpace(outWith), "\n"))
		withoutLines := len(strings.Split(strings.TrimSpace(outWithout), "\n"))
		if withoutLines > withLines {
			t.Errorf("--no-memories should produce <= lines: with=%d without=%d", withLines, withoutLines)
		}
	})

	t.Run("empty_db", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "exempty")

		// Export with --no-memories on empty db should not error
		bdExport(t, bd, dir, "--no-memories")
	})
}

func TestEmbeddedExportConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt export tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "exconc")

	for i := 0; i < 5; i++ {
		bdCreateSilent(t, bd, dir, fmt.Sprintf("concurrent export issue %d", i))
	}

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
			outFile := filepath.Join(t.TempDir(), fmt.Sprintf("export-%d.jsonl", worker))
			cmd := exec.Command(bd, "export", "-o", outFile)
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
		} else if !strings.Contains(r.out, "one writer at a time") {
			t.Errorf("worker %d failed with unexpected error: %v\n%s", r.worker, r.err, r.out)
		}
	}
	if successes < 1 {
		t.Errorf("expected at least 1 successful export, got %d", successes)
	}
	t.Logf("%d/%d export workers succeeded", successes, numWorkers)
}
