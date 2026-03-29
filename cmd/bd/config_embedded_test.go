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

// bdConfig runs "bd config" with the given args and returns stdout.
func bdConfig(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"config"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd config %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdConfigFail runs "bd config" expecting failure.
func bdConfigFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"config"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd config %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	return string(out)
}

// bdConfigListJSON runs "bd config list --json" and returns parsed map.
func bdConfigListJSON(t *testing.T, bd, dir string) map[string]string {
	t.Helper()
	cmd := exec.Command(bd, "config", "list", "--json")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd config list --json failed: %v\n%s", err, out)
	}
	s := strings.TrimSpace(string(out))
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in config list output: %s", s)
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s[start:]), &m); err != nil {
		t.Fatalf("parse config list JSON: %v\n%s", err, s)
	}
	return m
}

func TestEmbeddedConfig(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "tc")

	// ===== Set and Get =====

	t.Run("config_set_and_get", func(t *testing.T) {
		bdConfig(t, bd, dir, "set", "test.key1", "hello")
		out := bdConfig(t, bd, dir, "get", "test.key1")
		if !strings.Contains(out, "hello") {
			t.Errorf("expected 'hello' in get output: %s", out)
		}
	})

	t.Run("config_set_overwrite", func(t *testing.T) {
		bdConfig(t, bd, dir, "set", "test.overwrite", "first")
		bdConfig(t, bd, dir, "set", "test.overwrite", "second")
		out := bdConfig(t, bd, dir, "get", "test.overwrite")
		if !strings.Contains(out, "second") {
			t.Errorf("expected 'second' after overwrite: %s", out)
		}
	})

	t.Run("config_set_namespaced", func(t *testing.T) {
		bdConfig(t, bd, dir, "set", "jira.url", "https://example.atlassian.net")
		out := bdConfig(t, bd, dir, "get", "jira.url")
		if !strings.Contains(out, "https://example.atlassian.net") {
			t.Errorf("expected jira URL in output: %s", out)
		}
	})

	// ===== List =====

	t.Run("config_list", func(t *testing.T) {
		out := bdConfig(t, bd, dir, "list")
		// issue_prefix is always set by init
		if !strings.Contains(out, "issue_prefix") {
			t.Errorf("expected issue_prefix in list output: %s", out)
		}
	})

	t.Run("config_list_json", func(t *testing.T) {
		m := bdConfigListJSON(t, bd, dir)
		if _, ok := m["issue_prefix"]; !ok {
			t.Error("expected issue_prefix in JSON config list")
		}
		// Verify keys we set earlier are present
		if v, ok := m["test.key1"]; !ok || v != "hello" {
			t.Errorf("expected test.key1=hello, got %q", v)
		}
	})

	// ===== Unset =====

	t.Run("config_unset", func(t *testing.T) {
		bdConfig(t, bd, dir, "set", "test.removeme", "temp")
		// Verify it exists
		out := bdConfig(t, bd, dir, "get", "test.removeme")
		if !strings.Contains(out, "temp") {
			t.Fatalf("expected 'temp' before unset: %s", out)
		}
		// Unset it
		bdConfig(t, bd, dir, "unset", "test.removeme")
		// Verify it's gone — get returns "(not set)" with exit 0
		out = bdConfig(t, bd, dir, "get", "test.removeme")
		if !strings.Contains(out, "not set") {
			t.Errorf("expected 'not set' after unset: %s", out)
		}
		// Verify it's gone from list
		m := bdConfigListJSON(t, bd, dir)
		if _, ok := m["test.removeme"]; ok {
			t.Error("expected test.removeme to be absent from config list after unset")
		}
	})

	// ===== Validate =====
	// Note: config validate checks dolt server connectivity which doesn't
	// apply to embedded mode, so we skip it here.

	// ===== Error Cases =====

	t.Run("config_get_missing_key", func(t *testing.T) {
		// get on missing key returns "(not set)" with exit 0
		out := bdConfig(t, bd, dir, "get", "nonexistent.key.xyz")
		if !strings.Contains(out, "not set") {
			t.Errorf("expected 'not set' for missing key: %s", out)
		}
	})

	t.Run("config_set_no_args", func(t *testing.T) {
		bdConfigFail(t, bd, dir, "set")
	})

	t.Run("config_unset_no_args", func(t *testing.T) {
		bdConfigFail(t, bd, dir, "unset")
	})
}

// TestEmbeddedConfigConcurrent exercises config operations concurrently.
func TestEmbeddedConfigConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cx")

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

			// Each worker sets its own namespaced keys
			for i := 0; i < 5; i++ {
				key := fmt.Sprintf("worker%d.key%d", worker, i)
				value := fmt.Sprintf("value-%d-%d", worker, i)

				cmd := exec.Command(bd, "config", "set", key, value)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("set %s: %v\n%s", key, err, out)
					results[worker] = r
					return
				}
			}

			// Read back and verify
			for i := 0; i < 5; i++ {
				key := fmt.Sprintf("worker%d.key%d", worker, i)
				expected := fmt.Sprintf("value-%d-%d", worker, i)

				cmd := exec.Command(bd, "config", "get", key)
				cmd.Dir = dir
				cmd.Env = bdEnv(dir)
				out, err := cmd.CombinedOutput()
				if err != nil {
					r.err = fmt.Errorf("get %s: %v\n%s", key, err, out)
					results[worker] = r
					return
				}
				if !strings.Contains(string(out), expected) {
					r.err = fmt.Errorf("worker %d: key %s expected %q, got %q", worker, key, expected, string(out))
					results[worker] = r
					return
				}
			}

			// List all config
			cmd := exec.Command(bd, "config", "list", "--json")
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				r.err = fmt.Errorf("list --json: %v\n%s", err, out)
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

	// Verify all keys exist after concurrent writes
	m := bdConfigListJSON(t, bd, dir)
	for w := 0; w < numWorkers; w++ {
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("worker%d.key%d", w, i)
			expected := fmt.Sprintf("value-%d-%d", w, i)
			if v, ok := m[key]; !ok || v != expected {
				t.Errorf("after concurrent writes: key %s expected %q, got %q (exists=%v)", key, expected, v, ok)
			}
		}
	}
}
