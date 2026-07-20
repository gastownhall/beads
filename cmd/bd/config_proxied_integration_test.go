//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func bdProxiedConfig(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"config"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err != nil {
		t.Fatalf("bd config %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedConfigFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"config"}, args...)
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, fullArgs...)
	if err == nil {
		t.Fatalf("expected bd config %s to fail; got stdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func bdProxiedConfigListJSON(t *testing.T, bd, dir string) map[string]string {
	t.Helper()
	out := bdProxiedConfig(t, bd, dir, "list", "--json")
	s := strings.TrimSpace(out)
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

func proxiedDoltHead(t *testing.T, db *sql.DB) string {
	t.Helper()
	var h string
	if err := db.QueryRowContext(context.Background(), "SELECT HASHOF('HEAD')").Scan(&h); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return h
}

func proxiedDoltCommitCountSince(t *testing.T, db *sql.DB, sinceHash string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM DOLT_LOG('HEAD', '--not', ?)", sinceHash).Scan(&n); err != nil {
		t.Fatalf("count commits since %s: %v", sinceHash, err)
	}
	return n
}

func TestProxiedServerConfig(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("config_set_and_get", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcs")
		bdProxiedConfig(t, bd, p.dir, "set", "test.key1", "hello")
		out := bdProxiedConfig(t, bd, p.dir, "get", "test.key1")
		if !strings.Contains(out, "hello") {
			t.Errorf("expected 'hello' in get output: %s", out)
		}
	})

	t.Run("config_set_overwrite", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pco")
		bdProxiedConfig(t, bd, p.dir, "set", "test.overwrite", "first")
		bdProxiedConfig(t, bd, p.dir, "set", "test.overwrite", "second")
		out := bdProxiedConfig(t, bd, p.dir, "get", "test.overwrite")
		if !strings.Contains(out, "second") {
			t.Errorf("expected 'second' after overwrite: %s", out)
		}
	})

	t.Run("config_set_namespaced", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcn")
		bdProxiedConfig(t, bd, p.dir, "set", "jira.url", "https://example.atlassian.net")
		out := bdProxiedConfig(t, bd, p.dir, "get", "jira.url")
		if !strings.Contains(out, "https://example.atlassian.net") {
			t.Errorf("expected jira URL in output: %s", out)
		}
	})

	t.Run("config_set_and_get_linear_state_map_dotted_key", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcd")
		bdProxiedConfig(t, bd, p.dir, "set", "linear.state_map.closed", "Done")
		out := bdProxiedConfig(t, bd, p.dir, "get", "linear.state_map.closed")
		if strings.TrimSpace(out) != "Done" {
			t.Errorf("expected exact state_map value, got: %s", out)
		}
	})

	t.Run("config_list", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcl")
		out := bdProxiedConfig(t, bd, p.dir, "list")
		if !strings.Contains(out, "issue_prefix") {
			t.Errorf("expected issue_prefix in list output: %s", out)
		}
	})

	t.Run("config_list_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcj")
		bdProxiedConfig(t, bd, p.dir, "set", "test.key1", "hello")
		m := bdProxiedConfigListJSON(t, bd, p.dir)
		if _, ok := m["issue_prefix"]; !ok {
			t.Error("expected issue_prefix in JSON config list")
		}
		if v, ok := m["test.key1"]; !ok || v != "hello" {
			t.Errorf("expected test.key1=hello, got %q", v)
		}
	})

	t.Run("config_unset", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcu")
		bdProxiedConfig(t, bd, p.dir, "set", "test.removeme", "temp")
		out := bdProxiedConfig(t, bd, p.dir, "get", "test.removeme")
		if !strings.Contains(out, "temp") {
			t.Fatalf("expected 'temp' before unset: %s", out)
		}
		bdProxiedConfig(t, bd, p.dir, "unset", "test.removeme")
		out = bdProxiedConfig(t, bd, p.dir, "get", "test.removeme")
		if !strings.Contains(out, "not set") {
			t.Errorf("expected 'not set' after unset: %s", out)
		}
		m := bdProxiedConfigListJSON(t, bd, p.dir)
		if _, ok := m["test.removeme"]; ok {
			t.Error("expected test.removeme to be absent from config list after unset")
		}
	})

	t.Run("config_get_missing_key", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcm")
		out := bdProxiedConfig(t, bd, p.dir, "get", "nonexistent.key.xyz")
		if !strings.Contains(out, "not set") {
			t.Errorf("expected 'not set' for missing key: %s", out)
		}
	})

	t.Run("config_set_no_args", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcsna")
		bdProxiedConfigFail(t, bd, p.dir, "set")
	})

	t.Run("config_unset_no_args", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcuna")
		bdProxiedConfigFail(t, bd, p.dir, "unset")
	})

	t.Run("config_set_creates_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcsc")
		db := openProxiedDB(t, p)
		before := proxiedDoltHead(t, db)

		bdProxiedConfig(t, bd, p.dir, "set", "test.commit", "v1")

		after := proxiedDoltHead(t, db)
		if after == before {
			t.Errorf("HEAD did not advance: before=%s after=%s", before, after)
		}
	})

	t.Run("config_unset_creates_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcuc")
		bdProxiedConfig(t, bd, p.dir, "set", "test.commit", "v1")

		db := openProxiedDB(t, p)
		before := proxiedDoltHead(t, db)

		bdProxiedConfig(t, bd, p.dir, "unset", "test.commit")

		after := proxiedDoltHead(t, db)
		if after == before {
			t.Errorf("HEAD did not advance on unset: before=%s after=%s", before, after)
		}
	})

	t.Run("config_yaml_only_key_skips_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcy")
		db := openProxiedDB(t, p)
		before := proxiedDoltHead(t, db)

		bdProxiedConfig(t, bd, p.dir, "set", "export.auto", "true")

		after := proxiedDoltHead(t, db)
		if after != before {
			t.Errorf("yaml-only set should not advance HEAD: before=%s after=%s", before, after)
		}

		out := bdProxiedConfig(t, bd, p.dir, "get", "export.auto")
		if !strings.Contains(out, "true") {
			t.Errorf("expected export.auto=true via get: %s", out)
		}

		yamlPath := filepath.Join(p.beadsDir, "config.yaml")
		body, err := os.ReadFile(yamlPath)
		if err != nil {
			t.Fatalf("read %s: %v", yamlPath, err)
		}
		if !strings.Contains(string(body), "auto") {
			t.Errorf("expected export.auto written to config.yaml, got:\n%s", body)
		}

		before = proxiedDoltHead(t, db)
		bdProxiedConfig(t, bd, p.dir, "unset", "export.auto")
		after = proxiedDoltHead(t, db)
		if after != before {
			t.Errorf("yaml-only unset should not advance HEAD: before=%s after=%s", before, after)
		}
	})

	t.Run("config_beads_role_skips_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcr")
		db := openProxiedDB(t, p)
		before := proxiedDoltHead(t, db)

		bdProxiedConfig(t, bd, p.dir, "set", "beads.role", "contributor")

		after := proxiedDoltHead(t, db)
		if after != before {
			t.Errorf("beads.role set should not advance HEAD: before=%s after=%s", before, after)
		}

		gitCmd := exec.Command("git", "config", "--get", "beads.role")
		gitCmd.Dir = p.dir
		out, err := gitCmd.Output()
		if err != nil {
			t.Fatalf("git config --get beads.role: %v", err)
		}
		if strings.TrimSpace(string(out)) != "contributor" {
			t.Errorf("expected beads.role=contributor in git config, got %q", out)
		}

		before = proxiedDoltHead(t, db)
		bdProxiedConfig(t, bd, p.dir, "unset", "beads.role")
		after = proxiedDoltHead(t, db)
		if after != before {
			t.Errorf("beads.role unset should not advance HEAD: before=%s after=%s", before, after)
		}
	})
}

func TestProxiedServerConfigSetMany(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("set_many_single_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcsm")
		db := openProxiedDB(t, p)
		before := proxiedDoltHead(t, db)

		bdProxiedConfig(t, bd, p.dir, "set-many",
			"ado.state_map.open=New",
			"ado.state_map.in_progress=Active",
			"ado.state_map.closed=Closed",
		)

		after := proxiedDoltHead(t, db)
		if after == before {
			t.Fatalf("HEAD did not advance for set-many: before=%s after=%s", before, after)
		}
		n := proxiedDoltCommitCountSince(t, db, before)
		if n != 1 {
			t.Errorf("expected exactly 1 Dolt commit for set-many with 3 keys, got %d", n)
		}
	})

	t.Run("set_many_cli_round_trip", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pcsmr")
		bdProxiedConfig(t, bd, p.dir, "set-many",
			"jira.url=https://example.atlassian.net",
			"jira.project=PROJ",
			"linear.state_map.closed=Done",
		)

		want := map[string]string{
			"jira.url":                "https://example.atlassian.net",
			"jira.project":            "PROJ",
			"linear.state_map.closed": "Done",
		}
		for k, v := range want {
			out := bdProxiedConfig(t, bd, p.dir, "get", k)
			if strings.TrimSpace(out) != v {
				t.Errorf("get %s: expected %q, got %q", k, v, out)
			}
		}

		m := bdProxiedConfigListJSON(t, bd, p.dir)
		for k, v := range want {
			if got, ok := m[k]; !ok || got != v {
				t.Errorf("list --json: %s expected %q, got %q (exists=%v)", k, v, got, ok)
			}
		}
	})
}

func TestProxiedServerConfigConcurrent(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "pcc")

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

			for i := 0; i < 5; i++ {
				key := fmt.Sprintf("worker%d.key%d", worker, i)
				value := fmt.Sprintf("value-%d-%d", worker, i)
				if _, err := bdProxiedRun(t, bd, p.dir, "config", "set", key, value); err != nil {
					r.err = fmt.Errorf("set %s: %v", key, err)
					results[worker] = r
					return
				}
			}

			for i := 0; i < 5; i++ {
				key := fmt.Sprintf("worker%d.key%d", worker, i)
				expected := fmt.Sprintf("value-%d-%d", worker, i)
				out, err := bdProxiedRun(t, bd, p.dir, "config", "get", key)
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

			results[worker] = r
		}(w)
	}
	wg.Wait()

	for _, r := range results {
		if r.err != nil &&
			!strings.Contains(r.err.Error(), "serialization failure") &&
			!strings.Contains(r.err.Error(), "Error 1213") {
			t.Errorf("worker %d failed: %v", r.worker, r.err)
		}
	}

	m := bdProxiedConfigListJSON(t, bd, p.dir)
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
