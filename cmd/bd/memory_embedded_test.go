//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdRemember runs "bd remember" with the given args and returns stdout.
func bdRemember(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"remember"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd remember %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdRememberFail runs "bd remember" expecting failure.
func bdRememberFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"remember"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd remember %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdRecall runs "bd recall" with the given args and returns stdout.
func bdRecall(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"recall"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd recall %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdRecallFail runs "bd recall" expecting failure.
func bdRecallFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"recall"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd recall %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdMemories runs "bd memories" with the given args and returns stdout.
func bdMemories(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"memories"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd memories %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdForget runs "bd forget" with the given args and returns stdout.
func bdForget(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"forget"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd forget %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// bdForgetFail runs "bd forget" expecting failure.
func bdForgetFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"forget"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd forget %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedMemory(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tm")

	// ===== Remember and Recall =====

	t.Run("remember_and_recall", func(t *testing.T) {
		bdRemember(t, bd, dir, "always run tests with -race flag")
		// Recall using auto-generated key — list memories to find it
		out := bdMemories(t, bd, dir)
		if !strings.Contains(out, "always run tests with -race flag") {
			t.Errorf("expected memory in list: %s", out)
		}
	})

	t.Run("remember_with_key", func(t *testing.T) {
		bdRemember(t, bd, dir, "auth module uses JWT not sessions", "--key", "auth-jwt")
		out := bdRecall(t, bd, dir, "auth-jwt")
		if !strings.Contains(out, "JWT") {
			t.Errorf("expected 'JWT' in recall output: %s", out)
		}
	})

	t.Run("remember_overwrite", func(t *testing.T) {
		bdRemember(t, bd, dir, "first version", "--key", "overwrite-test")
		bdRemember(t, bd, dir, "second version", "--key", "overwrite-test")
		out := bdRecall(t, bd, dir, "overwrite-test")
		if !strings.Contains(out, "second version") {
			t.Errorf("expected 'second version' after overwrite: %s", out)
		}
	})

	// Desire path: `bd remember <existing-key>` (treating remember as a getter) is a READ --
	// it recalls the memory instead of overwriting it with its own key as content.
	t.Run("remember_bare_existing_key_recalls", func(t *testing.T) {
		bdRemember(t, bd, dir, "the real insight worth keeping", "--key", "guard-key")
		// Fat-finger a "read": positional arg is the existing bare key. Succeeds as a recall.
		out := bdRemember(t, bd, dir, "guard-key")
		if !strings.Contains(out, "the real insight worth keeping") {
			t.Errorf("expected bare existing key to recall the memory content, got: %s", out)
		}
		// The original content must be intact, not clobbered to "guard-key".
		recalled := bdRecall(t, bd, dir, "guard-key")
		if !strings.Contains(recalled, "the real insight worth keeping") {
			t.Errorf("recall-instead-of-write must preserve original content, got: %s", recalled)
		}
	})

	t.Run("remember_guard_bypass_with_explicit_key", func(t *testing.T) {
		bdRemember(t, bd, dir, "original", "--key", "bypass-key")
		// Explicit --key signals deliberate intent and bypasses the guard.
		bdRemember(t, bd, dir, "bypass-key", "--key", "bypass-key")
		out := bdRecall(t, bd, dir, "bypass-key")
		if !strings.Contains(out, "bypass-key") {
			t.Errorf("explicit --key should bypass guard and overwrite, got: %s", out)
		}
	})

	t.Run("remember_bare_slug_without_memory_refuses", func(t *testing.T) {
		// A bare slug naming NO existing memory is a mistyped read (or junk content) --
		// refuse rather than silently creating a memory whose content is its own key.
		out := bdRememberFail(t, bd, dir, "brand-new-slug-memory")
		if !strings.Contains(out, "memories") {
			t.Errorf("expected refusal to point at `bd memories`, got: %s", out)
		}
		// Nothing may have been stored.
		bdRecallFail(t, bd, dir, "brand-new-slug-memory")
	})

	t.Run("remember_new_slug_with_explicit_key_stores", func(t *testing.T) {
		// --key states write intent: slug-like content stores fine.
		bdRemember(t, bd, dir, "brand-new-slug-memory", "--key", "brand-new-slug-memory")
		out := bdRecall(t, bd, dir, "brand-new-slug-memory")
		if !strings.Contains(out, "brand-new-slug-memory") {
			t.Errorf("expected --key to store slug content, got: %s", out)
		}
	})

	// ===== Memories List =====

	t.Run("memories_list", func(t *testing.T) {
		out := bdMemories(t, bd, dir)
		// Should show at least the memories we stored above
		if !strings.Contains(out, "auth-jwt") {
			t.Errorf("expected 'auth-jwt' in memories list: %s", out)
		}
	})

	t.Run("memories_search", func(t *testing.T) {
		bdRemember(t, bd, dir, "dolt phantom DBs hide in three places", "--key", "dolt-phantoms")
		out := bdMemories(t, bd, dir, "dolt")
		if !strings.Contains(out, "dolt") {
			t.Errorf("expected 'dolt' related memory in search: %s", out)
		}
	})

	t.Run("memories_search_no_match", func(t *testing.T) {
		out := bdMemories(t, bd, dir, "zzz_nonexistent_term_xyz")
		// Should succeed but show no results or empty
		_ = out
	})

	// ===== Forget =====

	t.Run("forget", func(t *testing.T) {
		bdRemember(t, bd, dir, "temporary memory to forget", "--key", "forget-me")
		out := bdRecall(t, bd, dir, "forget-me")
		if !strings.Contains(out, "temporary memory") {
			t.Fatalf("expected memory before forget: %s", out)
		}
		bdForget(t, bd, dir, "forget-me")
		// After forget, recall should fail
		bdRecallFail(t, bd, dir, "forget-me")
	})

	// ===== Error Cases =====

	t.Run("recall_missing_key", func(t *testing.T) {
		bdRecallFail(t, bd, dir, "nonexistent-key-xyz")
	})

	t.Run("forget_missing_key", func(t *testing.T) {
		bdForgetFail(t, bd, dir, "nonexistent-key-xyz")
	})

	t.Run("remember_no_args", func(t *testing.T) {
		bdRememberFail(t, bd, dir)
	})

	t.Run("recall_no_args", func(t *testing.T) {
		bdRecallFail(t, bd, dir)
	})

	t.Run("forget_no_args", func(t *testing.T) {
		bdForgetFail(t, bd, dir)
	})
}

// TestEmbeddedMemoryConcurrent exercises memory operations concurrently.
func TestEmbeddedMemoryConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mx")

	// Disable auto-export: this test exercises concurrent memory
	// flock contention, not export behavior. With export.auto=true
	// (the default since GH#2973), 8 concurrent writers also trigger
	// post-write read paths that race with in-flight commits.
	//
	// The underlying race is not flock-level (flock already serializes
	// bd subprocesses) but engine-shutdown-level: Dolt's working-set
	// persistence can lag behind flock release, so the next subprocess
	// sometimes commits with a stale view and overwrites a prior forget.
	// See GH#3260 for the investigation (PR #3269 was the CI probe).
	// Fix would require synchronous flush on engine close or a long-lived
	// engine per store; both are substantial work. Until then, this
	// workaround keeps the test deterministic.
	disableAutoExport := exec.Command(bd, "config", "set", "export.auto", "false")
	disableAutoExport.Dir = dir
	disableAutoExport.Env = bdEnv(dir)
	if out, err := disableAutoExport.CombinedOutput(); err != nil {
		t.Fatalf("disable export.auto: %v\n%s", err, out)
	}

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

			// Each worker stores memories with unique keys
			for i := 0; i < 3; i++ {
				key := fmt.Sprintf("w%d-mem%d", worker, i)
				content := fmt.Sprintf("worker %d memory %d content", worker, i)

				cmd := exec.Command(bd, "remember", content, "--key", key)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("remember %s: %v\n%s", key, err, out)
					results[worker] = r
					return
				}
			}

			// Recall each memory
			for i := 0; i < 3; i++ {
				key := fmt.Sprintf("w%d-mem%d", worker, i)
				expected := fmt.Sprintf("worker %d memory %d content", worker, i)

				cmd := exec.Command(bd, "recall", key)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("recall %s: %v\n%s", key, err, out)
					results[worker] = r
					return
				}
				if !strings.Contains(string(out), expected) {
					r.err = fmt.Errorf("recall %s: expected %q, got %q", key, expected, string(out))
					results[worker] = r
					return
				}
			}

			// Forget one memory
			forgetKey := fmt.Sprintf("w%d-mem0", worker)
			cmd := exec.Command(bd, "forget", forgetKey)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("forget %s: %v\n%s", forgetKey, err, out)
				results[worker] = r
				return
			}

			// List memories
			cmd = exec.Command(bd, "memories")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err = cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("memories: %v\n%s", err, out)
				results[worker] = r
				return
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

	// Verify memories only for workers that succeeded (err==nil).
	// With exclusive flock, some workers may fail with "one writer at a time".
	out := bdMemories(t, bd, dir)
	var successCount int
	for _, r := range results {
		if r.err != nil {
			continue
		}
		successCount++
		w := r.worker
		forgottenKey := fmt.Sprintf("w%d-mem0", w)
		if strings.Contains(out, forgottenKey) {
			t.Errorf("expected %s to be forgotten", forgottenKey)
		}
		for i := 1; i < 3; i++ {
			key := fmt.Sprintf("w%d-mem%d", w, i)
			if !strings.Contains(out, key) {
				t.Errorf("expected %s to still exist in memories", key)
			}
		}
	}
	if successCount == 0 {
		t.Fatal("expected at least 1 worker to succeed")
	}
}
