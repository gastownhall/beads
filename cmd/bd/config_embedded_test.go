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
)

// bdConfig runs "bd config" with the given args and returns stdout.
func bdConfig(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"config"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd config %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
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
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd config list --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	s := strings.TrimSpace(stdout.String())
	start := strings.Index(s, "{")
	if start < 0 {
		t.Fatalf("no JSON object in config list output: %s", s)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(s[start:]), &raw); err != nil {
		t.Fatalf("parse config list JSON: %v\n%s", err, s)
	}
	m := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "schema_version" {
			continue
		}
		if sv, ok := v.(string); ok {
			m[k] = sv
		}
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

	t.Run("config_set_and_get_linear_state_map_dotted_key", func(t *testing.T) {
		bdConfig(t, bd, dir, "set", "linear.state_map.closed", "Done")
		out := bdConfig(t, bd, dir, "get", "linear.state_map.closed")
		if strings.TrimSpace(out) != "Done" {
			t.Errorf("expected exact state_map value, got: %s", out)
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

	// A key that config.yaml carries but IsYamlOnlyKey does not claim was
	// unset from the database alone, so it stayed effective while bd reported
	// success.
	t.Run("config_unset_clears_yaml_layer", func(t *testing.T) {
		configPath := filepath.Join(dir, ".beads", "config.yaml")
		original, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		if err := os.WriteFile(configPath, append(original, []byte("\ntest.fromyaml: yamlvalue\n")...), 0600); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}

		out := bdConfig(t, bd, dir, "unset", "test.fromyaml")
		if !strings.Contains(out, "config.yaml") {
			t.Errorf("unset output should name config.yaml as a cleared location: %s", out)
		}

		after, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config.yaml after unset: %v", err)
		}
		if strings.Contains(string(after), "\ntest.fromyaml: yamlvalue") {
			t.Errorf("test.fromyaml still set in config.yaml after unset:\n%s", after)
		}
		if !strings.Contains(string(after), "# test.fromyaml: yamlvalue") {
			t.Errorf("expected the unset key to remain as a comment:\n%s", after)
		}
	})

	// The location bd reports must be a record of the writes it made, not a
	// guess made before them. GetYamlConfig reads viper's MERGED value -
	// SetDefault values and AutomaticEnv included - so a database-backed key
	// with a non-empty default looked present in config.yaml even in a
	// workspace whose config.yaml never mentioned it, and bd claimed to have
	// cleared a file it did not touch.
	t.Run("config_unset_does_not_claim_a_yaml_write_it_did_not_make", func(t *testing.T) {
		configPath := filepath.Join(dir, ".beads", "config.yaml")
		before, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		// no-hooks is not a yaml-only key and carries the default "false", so
		// GetYamlConfig("no-hooks") is non-empty with or without a
		// config.yaml entry. It is absent from this workspace's config.yaml.
		if strings.Contains(string(before), "no-hooks") {
			t.Skipf("this workspace's config.yaml already carries no-hooks; the probe needs a key it does not")
		}

		bdConfig(t, bd, dir, "set", "no-hooks", "true")
		out := bdConfig(t, bd, dir, "unset", "no-hooks")

		if strings.Contains(out, "config.yaml") {
			t.Errorf("unset named config.yaml as a cleared location, but the key was never written there: %s", out)
		}
		if !strings.Contains(out, "in database") {
			t.Errorf("unset should report the database as the cleared location: %s", out)
		}

		after, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config.yaml after unset: %v", err)
		}
		if string(after) != string(before) {
			t.Errorf("config.yaml was rewritten by an unset that had nothing to clear:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})

	// With no project config.yaml at all, a database-backed unset of a
	// defaulted key used to delete the row and then fail on "no
	// .beads/config.yaml found" - a non-zero exit over a half-applied unset,
	// where before this PR bd printed "Unset <key>" and exited 0. "Not in
	// config.yaml" is an answer, not a failure. The key has to be one with a
	// non-empty default (no-hooks), since that is what made the old
	// GetYamlConfig pre-check believe there was a config.yaml layer to clear.
	t.Run("config_unset_succeeds_with_no_project_config_yaml", func(t *testing.T) {
		configPath := filepath.Join(dir, ".beads", "config.yaml")
		original, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read config.yaml: %v", err)
		}
		t.Cleanup(func() {
			if err := os.WriteFile(configPath, original, 0600); err != nil {
				t.Fatalf("restore config.yaml: %v", err)
			}
		})

		bdConfig(t, bd, dir, "set", "no-hooks", "true")
		if err := os.Remove(configPath); err != nil {
			t.Fatalf("remove config.yaml: %v", err)
		}

		out := bdConfig(t, bd, dir, "unset", "no-hooks")
		if strings.Contains(out, "config.yaml") {
			t.Errorf("unset named config.yaml with no config.yaml present: %s", out)
		}
		if _, err := os.Stat(configPath); !os.IsNotExist(err) {
			t.Errorf("unset recreated config.yaml: %v", err)
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
		if r.err != nil && !strings.Contains(r.err.Error(), "one writer at a time") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}

	// Verify keys only for workers that succeeded (err==nil).
	// With exclusive flock, some workers may fail with "one writer at a time".
	m := bdConfigListJSON(t, bd, dir)
	var successCount int
	for _, r := range results {
		if r.err != nil {
			continue
		}
		successCount++
		w := r.worker
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("worker%d.key%d", w, i)
			expected := fmt.Sprintf("value-%d-%d", w, i)
			if v, ok := m[key]; !ok || v != expected {
				t.Errorf("after concurrent writes: key %s expected %q, got %q (exists=%v)", key, expected, v, ok)
			}
		}
	}
	if successCount == 0 {
		t.Fatal("expected at least 1 worker to succeed")
	}
}
