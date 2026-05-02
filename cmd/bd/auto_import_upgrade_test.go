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

// TestEmbeddedAutoImportJSONLNoExplicitCommit verifies that auto-import
// uses a single transaction and defers the Dolt commit to PersistentPostRun
// (no separate DOLT_COMMIT during pre-run).
func TestEmbeddedAutoImportJSONLNoExplicitCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Create a source store with 3 issues, then export to JSONL.
	srcDir, _, _ := bdInit(t, bd, "--prefix", "src")
	for i := 0; i < 3; i++ {
		bdCreate(t, bd, srcDir, fmt.Sprintf("auto-import-test-%d", i), "--type", "task")
	}
	srcJSONL := filepath.Join(srcDir, "export.jsonl")
	cmd := exec.Command(bd, "export", "-o", srcJSONL)
	cmd.Dir = srcDir
	cmd.Env = bdEnv(srcDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bd export failed: %v\n%s", err, out)
	}

	// Create a fresh empty store in a new directory.
	dstDir, dstBeadsDir, _ := bdInit(t, bd, "--prefix", "dst")

	// Place the JSONL file where auto-import looks for it.
	srcData, err := os.ReadFile(srcJSONL)
	if err != nil {
		t.Fatalf("reading exported JSONL: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstBeadsDir, "issues.jsonl"), srcData, 0644); err != nil {
		t.Fatalf("writing issues.jsonl: %v", err)
	}

	// Run "bd list --json" — triggers maybeAutoImportJSONL in pre-run,
	// auto-commit in post-run.
	issues := bdListJSON(t, bd, dstDir)
	if len(issues) != 3 {
		t.Fatalf("expected 3 imported issues, got %d", len(issues))
	}

	// Verify commit count: should be exactly 2 (init + post-run auto-commit).
	// If the old code path were used, there would be 3 (init + auto-import
	// DOLT_COMMIT + post-run auto-commit).
	logOut := bdDolt(t, bd, dstDir, "log")
	commitCount := strings.Count(logOut, "commit ")
	if commitCount > 2 {
		t.Errorf("expected at most 2 Dolt commits (init + auto-commit), got %d;\nlog:\n%s", commitCount, logOut)
	}
}

// TestEmbeddedAutoImportJSONLConcurrentWriter verifies no data loss when
// auto-import runs concurrently with other writers (the race that prompted
// the single-transaction fix).
func TestEmbeddedAutoImportJSONLConcurrentWriter(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Create a fresh store and place a hand-crafted issues.jsonl.
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cr")

	jsonlIssues := []types.Issue{
		{ID: "cr-import-1", Title: "Imported One", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "cr-import-2", Title: "Imported Two", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "cr-import-3", Title: "Imported Three", Status: types.StatusOpen, Priority: 3, IssueType: types.TypeTask, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	var lines []string
	for _, issue := range jsonlIssues {
		b, _ := json.Marshal(issue)
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Launch concurrent writers + one "bd list" that triggers auto-import.
	const numWriters = 4

	type result struct {
		id  string
		err error
	}

	var wg sync.WaitGroup
	writerResults := make([]result, numWriters)

	// Concurrent create workers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			issue := bdCreateAllowError(t, bd, dir, fmt.Sprintf("concurrent-%d", idx), "--type", "task")
			if issue != nil {
				writerResults[idx] = result{id: issue.ID}
			} else {
				writerResults[idx] = result{err: fmt.Errorf("create failed for worker %d", idx)}
			}
		}(i)
	}

	// Trigger auto-import via bd list (runs concurrently with creates)
	wg.Add(1)
	var listIssues []*types.IssueWithCounts
	go func() {
		defer wg.Done()
		listIssues = bdListJSONAllowError(t, bd, dir)
	}()

	wg.Wait()

	// Count successful creates
	var successfulCreates []string
	for _, r := range writerResults {
		if r.err == nil && r.id != "" {
			successfulCreates = append(successfulCreates, r.id)
		}
	}

	// Final authoritative list — after all concurrency is done
	finalIssues := bdListJSON(t, bd, dir)
	finalIDs := make(map[string]bool)
	for _, issue := range finalIssues {
		finalIDs[issue.ID] = true
	}

	// Every successfully created issue must be present (no lost writes)
	for _, id := range successfulCreates {
		if !finalIDs[id] {
			t.Errorf("concurrent create %s succeeded but is missing from final list", id)
		}
	}

	// If the list ran before any creates, the imported issues should be present
	// (they may or may not be, depending on whether auto-import won the flock race)
	t.Logf("final issue count: %d (imports: up to 3, concurrent creates: %d, list saw: %d)",
		len(finalIssues), len(successfulCreates), len(listIssues))
}

// TestEmbeddedAutoImportJSONLSkipsNonEmpty verifies that auto-import is
// a no-op when the database already has issues (atomicity of the
// emptiness check + import within a single transaction).
func TestEmbeddedAutoImportJSONLSkipsNonEmpty(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	// Create a store with one existing issue.
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "ne")
	existing := bdCreate(t, bd, dir, "pre-existing issue", "--type", "task")

	// Place a JSONL file with different issues.
	jsonlIssues := []types.Issue{
		{ID: "ne-should-not-import-1", Title: "Should Not Import", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: "ne-should-not-import-2", Title: "Also Should Not Import", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}
	var lines []string
	for _, issue := range jsonlIssues {
		b, _ := json.Marshal(issue)
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Run bd list — auto-import should skip because DB is non-empty.
	issues := bdListJSON(t, bd, dir)

	// Only the pre-existing issue should be present.
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (pre-existing only), got %d", len(issues))
	}
	if issues[0].ID != existing.ID {
		t.Errorf("expected issue %s, got %s", existing.ID, issues[0].ID)
	}
}

// bdCreateAllowError runs "bd create --json" and returns the issue on success,
// or nil on failure (without failing the test).
func bdCreateAllowError(t *testing.T, bd, dir string, args ...string) *types.Issue {
	t.Helper()
	fullArgs := append([]string{"create", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	s := string(out)
	start := strings.Index(s, "{")
	if start < 0 {
		return nil
	}
	var issue types.Issue
	if err := json.Unmarshal([]byte(s[start:]), &issue); err != nil {
		return nil
	}
	return &issue
}

// bdListJSONAllowError runs "bd list --json" and returns issues on success,
// or nil on failure (without failing the test).
func bdListJSONAllowError(t *testing.T, bd, dir string, args ...string) []*types.IssueWithCounts {
	t.Helper()
	fullArgs := append([]string{"list", "--json"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	s := string(out)
	start := strings.Index(s, "[")
	if start < 0 {
		return nil
	}
	var issues []*types.IssueWithCounts
	if err := json.Unmarshal([]byte(s[start:]), &issues); err != nil {
		return nil
	}
	return issues
}
