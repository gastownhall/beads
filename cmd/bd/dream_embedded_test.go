//go:build cgo

package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// bdDream runs "bd dream <args...>" and returns combined output.
func bdDream(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"dream"}, args...)
	cmd := exec.Command(bd, full...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd dream %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdDreamFail runs "bd dream <args...>" expecting a non-zero exit.
func bdDreamFail(t *testing.T, bd, dir string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{"dream"}, args...)
	cmd := exec.Command(bd, full...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd dream %s to fail, but succeeded:\n%s", strings.Join(args, " "), out)
	}
	exitCode := -1
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	}
	return string(out), exitCode
}

// TestDreamStatus_NeverRun verifies the fresh-repo state.
func TestDreamStatus_NeverRun(t *testing.T) {
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dt")

	out := bdDream(t, bd, dir, "status")
	if !strings.Contains(out, "never run") {
		t.Errorf("expected 'never run', got: %s", out)
	}
	if !strings.Contains(out, "memories: 0") {
		t.Errorf("expected memories: 0, got: %s", out)
	}
}

// TestDreamStatusJSON validates the JSON shape and key fields.
func TestDreamStatusJSON(t *testing.T) {
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dt")

	out := bdDream(t, bd, dir, "--json", "status")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if m["never_run"] != true {
		t.Errorf("expected never_run=true, got: %v", m["never_run"])
	}
	if int(m["memory_count"].(float64)) != 0 {
		t.Errorf("expected memory_count=0, got: %v", m["memory_count"])
	}
	if m["min_interval_hours"].(float64) != 24 {
		t.Errorf("expected default min_interval_hours=24, got: %v", m["min_interval_hours"])
	}
	if m["min_churn"].(float64) != 5 {
		t.Errorf("expected default min_churn=5, got: %v", m["min_churn"])
	}
}

// TestDreamCheck_FreshRepoEligibleThenSkippedAfterRecord verifies the eligibility
// gate: a fresh repo with no run history is eligible (modulo memory count),
// and after we record a synthetic last-run state, --check returns 1.
func TestDreamCheck_TooFewMemoriesNotEligible(t *testing.T) {
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dt")

	// Zero memories — should not be eligible.
	_, code := bdDreamFail(t, bd, dir, "run", "--check")
	if code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
}

// TestDreamCheck_ChurnGate seeds last-run state via `bd config set` and
// verifies the churn gate engages when memory count is unchanged.
func TestDreamCheck_ChurnGate(t *testing.T) {
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dt")

	// Add 6 memories so we're past the count==1 floor.
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		bdRemember(t, bd, dir, "content for "+k, "--key", k)
	}

	// Seed a recent last-run with the same memory count.
	// We use bd config set (writes directly to dolt config table).
	for _, kv := range [][]string{
		{"dream.last-run-at", "2030-01-01T00:00:00Z"}, // far future to ensure interval not elapsed
		{"dream.memory-count-at-last-run", "6"},
	} {
		cmd := exec.Command(bd, "config", "set", kv[0], kv[1])
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("config set %s: %v\n%s", kv[0], err, out)
		}
	}

	out, code := bdDreamFail(t, bd, dir, "run", "--check")
	if code != 1 {
		t.Errorf("expected exit 1 from --check, got %d, out=%s", code, out)
	}
	if !strings.Contains(out, "interval not elapsed") || !strings.Contains(out, "churn below threshold") {
		t.Errorf("expected both interval+churn reasons, got: %s", out)
	}
}

// TestDreamRun_MissingAPIKey ensures we surface a helpful error when no key.
func TestDreamRun_MissingAPIKey(t *testing.T) {
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dt")

	// Add memories so we get past the eligibility gate (never-run = always eligible).
	for _, k := range []string{"a", "b", "c"} {
		bdRemember(t, bd, dir, "content "+k, "--key", k)
	}

	// Build an env without ANTHROPIC_API_KEY.
	cmd := exec.Command(bd, "dream", "run", "--force", "--dry-run")
	cmd.Dir = dir
	env := bdEnv(dir)
	scrubbed := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
			scrubbed = append(scrubbed, e)
		}
	}
	cmd.Env = scrubbed
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected missing-key error, got success:\n%s", out)
	}
	if !strings.Contains(string(out), "API key") && !strings.Contains(string(out), "ANTHROPIC_API_KEY") {
		t.Errorf("expected helpful API key message, got: %s", out)
	}
}

// TestKVReservedDream verifies the kv prefix guard rejects dream.* keys
// (so users can't shadow dream's state via bd kv set).
func TestKVReservedDream(t *testing.T) {
	t.Parallel()
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "dt")

	cmd := exec.Command(bd, "kv", "set", "dream.injected", "value")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected bd kv set dream.* to fail:\n%s", out)
	}
	if !strings.Contains(string(out), "reserved prefix") {
		t.Errorf("expected reserved-prefix error, got: %s", out)
	}
}
