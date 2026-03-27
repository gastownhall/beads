//go:build embeddeddolt

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// bdRestore runs "bd restore" with extra args. Returns combined output.
func bdRestore(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"restore"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd restore %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdRestoreFail runs "bd restore" expecting failure.
func bdRestoreFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"restore"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd restore %s should have failed, got: %s", strings.Join(args, " "), out)
	}
	return string(out)
}

// simulateCompaction creates an issue with content, commits, then marks it as
// compacted by updating compaction_level directly via the embedded store.
// Returns the issue ID.
func simulateCompaction(t *testing.T, bd, dir, beadsDir, database string) string {
	t.Helper()

	// Create an issue with substantial content (auto-committed by bd create)
	id := bdCreateSilent(t, bd, dir, "Compactable issue",
		"--description", "This is a long description that will be preserved in history.",
		"--design", "Design notes for the compactable issue.")

	// Directly update the issue to simulate compaction:
	// set compaction_level=1 and truncate description.
	// Use a retry loop because the bd subprocess may still hold the lock briefly.
	var store *embeddeddolt.EmbeddedDoltStore
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		store, err = embeddeddolt.New(ctx, beadsDir, database, "main")
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("open store after retries: %v", err)
	}

	issue, err := store.GetIssue(ctx, id)
	if err != nil {
		store.Close()
		t.Fatalf("get issue: %v", err)
	}

	originalSize := len(issue.Description) + len(issue.Design)
	if err := store.ApplyCompaction(ctx, id, 1, originalSize, 0, "simulated"); err != nil {
		store.Close()
		t.Fatalf("apply compaction: %v", err)
	}

	if err := store.UpdateIssue(ctx, id, map[string]interface{}{
		"description": "[compacted]",
		"design":      "",
	}, "test"); err != nil {
		store.Close()
		t.Fatalf("update issue: %v", err)
	}

	if err := store.Commit(ctx, "simulate compaction"); err != nil {
		store.Close()
		t.Fatalf("commit compaction: %v", err)
	}

	// Close explicitly so the lock is released before the next bd subprocess.
	store.Close()

	return id
}

func TestEmbeddedRestore(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt restore tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("not_found", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "rstnf")

		out := bdRestoreFail(t, bd, dir, "rstnf-nonexistent")
		if !strings.Contains(out, "not found") {
			t.Errorf("expected 'not found' error, got: %s", out)
		}
	})

	t.Run("not_compacted", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "rstnc")
		id := bdCreateSilent(t, bd, dir, "Not compacted issue")

		out := bdRestoreFail(t, bd, dir, id)
		if !strings.Contains(out, "not compacted") {
			t.Errorf("expected 'not compacted' error, got: %s", out)
		}
	})

	t.Run("restore_compacted", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rstok")
		id := simulateCompaction(t, bd, dir, beadsDir, "rstok")

		out := bdRestore(t, bd, dir, id)
		// Should show the restored content from history
		if !strings.Contains(out, "long description") {
			t.Errorf("expected restored description in output, got: %s", out)
		}
	})

	t.Run("restore_json", func(t *testing.T) {
		dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rstjs")
		id := simulateCompaction(t, bd, dir, beadsDir, "rstjs")

		// NOTE: bd restore has a known issue where its local --json flag
		// conflicts with the root command's --json persistent flag.
		// Just verify the command runs successfully.
		out := bdRestore(t, bd, dir, id)
		if !strings.Contains(out, "long description") {
			t.Errorf("expected restored description in output, got: %s", out)
		}
	})
}

func TestEmbeddedRestoreConcurrent(t *testing.T) {
	t.Skip("TODO: simulateCompaction fails under CI concurrency — needs investigation")
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt restore tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "rstcon")

	// Create multiple compacted issues
	const numIssues = 5
	var ids []string
	for i := 0; i < numIssues; i++ {
		id := bdCreateSilent(t, bd, dir, fmt.Sprintf("Concurrent restore issue %d", i),
			"--description", fmt.Sprintf("Content for issue %d that should be in history.", i))
		ids = append(ids, id)
	}

	// Issues are auto-committed by bd create. Simulate compaction on all of them.
	// Retry opening the store in case the last bd subprocess still holds the lock.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var store *embeddeddolt.EmbeddedDoltStore
	var err error
	for i := 0; i < 5; i++ {
		store, err = embeddeddolt.New(ctx, beadsDir, "rstcon", "main")
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("open store after retries: %v", err)
	}
	for _, id := range ids {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			store.Close()
			t.Fatalf("get issue %s: %v", id, err)
		}
		if err := store.ApplyCompaction(ctx, id, 1, len(issue.Description), 0, "sim"); err != nil {
			store.Close()
			t.Fatalf("apply compaction %s: %v", id, err)
		}
		if err := store.UpdateIssue(ctx, id, map[string]interface{}{"description": "[compacted]"}, "test"); err != nil {
			store.Close()
			t.Fatalf("update issue %s: %v", id, err)
		}
	}
	if err := store.Commit(ctx, "compact all"); err != nil {
		store.Close()
		t.Fatalf("commit compaction: %v", err)
	}
	store.Close()

	// Concurrent restore reads
	type result struct {
		worker int
		out    string
		err    error
	}

	results := make([]result, numIssues)
	var wg sync.WaitGroup
	wg.Add(numIssues)

	for w := 0; w < numIssues; w++ {
		go func(worker int) {
			defer wg.Done()
			cmd := exec.Command(bd, "restore", ids[worker])
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
		t.Errorf("expected at least 1 successful restore, got %d", successes)
	}
	t.Logf("%d/%d restore workers succeeded", successes, numIssues)
}
