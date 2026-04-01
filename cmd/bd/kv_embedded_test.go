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

// bdKV runs "bd kv" with the given args and returns stdout.
func bdKV(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"kv"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd kv %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdKVFail runs "bd kv" expecting failure.
func bdKVFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"kv"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd kv %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdKVListJSON runs "bd kv list --json" and returns parsed map.
func bdKVListJSON(t *testing.T, bd, dir string) map[string]string {
	t.Helper()
	cmd := exec.Command(bd, "kv", "list", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd kv list --json failed: %v\n%s", err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "{")
	if start < 0 {
		// Empty KV store may return empty object or nothing
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse kv list JSON: %v\n%s", err, s)
	}
	return m
}

func TestEmbeddedKV(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tk")

	// ===== Set and Get =====

	t.Run("kv_set_and_get", func(t *testing.T) {
		bdKV(t, bd, dir, "set", "mykey", "myvalue")
		out := bdKV(t, bd, dir, "get", "mykey")
		if !strings.Contains(out, "myvalue") {
			t.Errorf("expected 'myvalue' in get output: %s", out)
		}
	})

	t.Run("kv_set_overwrite", func(t *testing.T) {
		bdKV(t, bd, dir, "set", "overkey", "first")
		bdKV(t, bd, dir, "set", "overkey", "second")
		out := bdKV(t, bd, dir, "get", "overkey")
		if !strings.Contains(out, "second") {
			t.Errorf("expected 'second' after overwrite: %s", out)
		}
	})

	t.Run("kv_set_special_chars", func(t *testing.T) {
		bdKV(t, bd, dir, "set", "special", "hello world with spaces")
		out := bdKV(t, bd, dir, "get", "special")
		if !strings.Contains(out, "hello world with spaces") {
			t.Errorf("expected value with spaces: %s", out)
		}
	})

	// ===== List =====

	t.Run("kv_list", func(t *testing.T) {
		out := bdKV(t, bd, dir, "list")
		if !strings.Contains(out, "mykey") {
			t.Errorf("expected 'mykey' in list output: %s", out)
		}
	})

	t.Run("kv_list_json", func(t *testing.T) {
		m := bdKVListJSON(t, bd, dir)
		if v, ok := m["mykey"]; !ok || v != "myvalue" {
			t.Errorf("expected mykey=myvalue in JSON, got %q (exists=%v)", v, ok)
		}
	})

	// ===== Clear =====

	t.Run("kv_clear", func(t *testing.T) {
		bdKV(t, bd, dir, "set", "clearme", "temporary")
		out := bdKV(t, bd, dir, "get", "clearme")
		if !strings.Contains(out, "temporary") {
			t.Fatalf("expected 'temporary' before clear: %s", out)
		}
		bdKV(t, bd, dir, "clear", "clearme")
		// Verify it's gone from list
		m := bdKVListJSON(t, bd, dir)
		if _, ok := m["clearme"]; ok {
			t.Error("expected clearme to be absent from kv list after clear")
		}
	})

	// ===== Error Cases =====

	t.Run("kv_get_missing_key", func(t *testing.T) {
		// get on nonexistent key — check behavior
		cmd := exec.Command(bd, "kv", "get", "nonexistent_key_xyz")
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		// May succeed with empty or fail — either way no crash
		_ = err
		_ = out
	})

	t.Run("kv_set_no_args", func(t *testing.T) {
		bdKVFail(t, bd, dir, "set")
	})

	t.Run("kv_get_no_args", func(t *testing.T) {
		bdKVFail(t, bd, dir, "get")
	})

	t.Run("kv_clear_no_args", func(t *testing.T) {
		bdKVFail(t, bd, dir, "clear")
	})
}

// TestEmbeddedKVConcurrent exercises kv operations concurrently.
func TestEmbeddedKVConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "kx")

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

			// Each worker sets its own keys
			for i := 0; i < 5; i++ {
				key := fmt.Sprintf("w%d-k%d", worker, i)
				value := fmt.Sprintf("v%d-%d", worker, i)

				cmd := exec.Command(bd, "kv", "set", key, value)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("set %s: %v\n%s", key, err, out)
					results[worker] = r
					return
				}
			}

			// Read back
			for i := 0; i < 5; i++ {
				key := fmt.Sprintf("w%d-k%d", worker, i)
				expected := fmt.Sprintf("v%d-%d", worker, i)

				cmd := exec.Command(bd, "kv", "get", key)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("get %s: %v\n%s", key, err, out)
					results[worker] = r
					return
				}
				if !strings.Contains(string(out), expected) {
					r.err = fmt.Errorf("key %s expected %q, got %q", key, expected, string(out))
					results[worker] = r
					return
				}
			}

			// Clear one key
			clearKey := fmt.Sprintf("w%d-k0", worker)
			cmd := exec.Command(bd, "kv", "clear", clearKey)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("clear %s: %v\n%s", clearKey, err, out)
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

	// Verify remaining keys (k1-k4 for each worker, k0 was cleared)
	m := bdKVListJSON(t, bd, dir)
	for w := 0; w < numWorkers; w++ {
		clearedKey := fmt.Sprintf("w%d-k0", w)
		if _, ok := m[clearedKey]; ok {
			t.Errorf("expected %s to be cleared", clearedKey)
		}
		for i := 1; i < 5; i++ {
			key := fmt.Sprintf("w%d-k%d", w, i)
			expected := fmt.Sprintf("v%d-%d", w, i)
			if v, ok := m[key]; !ok || v != expected {
				t.Errorf("key %s expected %q, got %q (exists=%v)", key, expected, v, ok)
			}
		}
	}
}
