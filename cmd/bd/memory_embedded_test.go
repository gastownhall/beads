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
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd remember %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd recall %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd memories %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdForget runs "bd forget" with the given args and returns stdout.
func bdForget(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"forget"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd forget %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
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
		if r.err != nil {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}

	// Verify: mem0 forgotten, mem1 and mem2 still present for each worker
	out := bdMemories(t, bd, dir)
	for w := 0; w < numWorkers; w++ {
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
}
