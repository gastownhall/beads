//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runConfigCommandWithStreams(t *testing.T, bd, dir string, args ...string) (stdout string, stderr string, err error) {
	t.Helper()

	fullArgs := append([]string{"config"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

func assertNoStoreInitLeak(t *testing.T, stderr string) {
	t.Helper()
	for _, needle := range []string{
		"database name may default incorrectly",
		"failed to open database",
		"no database selected",
	} {
		if strings.Contains(stderr, needle) {
			t.Fatalf("unexpected low-level store init output %q in stderr:\n%s", needle, stderr)
		}
	}
}

func TestEmbeddedConfigNoWorkspaceStoreFreeCommands(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()

	t.Run("get_yaml_only", func(t *testing.T) {
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "get", "no-db")
		if err != nil {
			t.Fatalf("config get no-db failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		assertNoStoreInitLeak(t, stderr)
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("expected no stderr for config get no-db, got:\n%s", stderr)
		}
		got := strings.TrimSpace(stdout)
		if got != "false" && !strings.Contains(got, "not set in config.yaml") {
			t.Fatalf("expected yaml-only config lookup result, got stdout:\n%s", stdout)
		}
	})

	t.Run("set_refused_as_obsolete_setting", func(t *testing.T) {
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "set", "no-db", "true")
		if err == nil {
			t.Fatalf("expected config set no-db to be refused as an obsolete setting, stdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		assertNoStoreInitLeak(t, stderr)
		if !strings.Contains(stderr, "obsolete") || !strings.Contains(stderr, "bd config unset no-db") {
			t.Fatalf("expected obsolete-setting refusal, got stderr:\n%s", stderr)
		}
	})

	t.Run("show_json", func(t *testing.T) {
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "show", "--json")
		if err != nil {
			t.Fatalf("config show --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		assertNoStoreInitLeak(t, stderr)
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("expected no stderr for config show --json, got:\n%s", stderr)
		}

		var payload []map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("parse config show --json output: %v\nstdout:\n%s", err, stdout)
		}
		if len(payload) == 0 {
			t.Fatalf("expected config show --json to return entries, got empty payload")
		}
	})

	t.Run("drift_json", func(t *testing.T) {
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "drift", "--json")
		if err != nil {
			t.Fatalf("config drift --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		assertNoStoreInitLeak(t, stderr)
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("expected no stderr for config drift --json, got:\n%s", stderr)
		}

		var payload []map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("parse config drift --json output: %v\nstdout:\n%s", err, stdout)
		}
		if len(payload) == 0 {
			t.Fatalf("expected config drift --json to return checks, got empty payload")
		}
	})

	t.Run("apply_json", func(t *testing.T) {
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "apply", "--json")
		if err != nil {
			t.Fatalf("config apply --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		assertNoStoreInitLeak(t, stderr)
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("expected no stderr for config apply --json, got:\n%s", stderr)
		}

		var payload []map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("parse config apply --json output: %v\nstdout:\n%s", err, stdout)
		}
		if len(payload) == 0 {
			t.Fatalf("expected config apply --json to return results, got empty payload")
		}
	})
}

func TestEmbeddedConfigValidateJSONNoWorkspaceWritesStdout(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()

	stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "validate", "--json")
	if err == nil {
		t.Fatalf("expected config validate --json to fail without a workspace, stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	assertNoStoreInitLeak(t, stderr)
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected JSON error on stdout only, got stderr:\n%s", stderr)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse config validate --json output: %v\nstdout:\n%s", err, stdout)
	}
	if errField, _ := payload["error"].(string); errField != activeWorkspaceNotFoundError() {
		t.Fatalf("error = %q, want %q", errField, activeWorkspaceNotFoundError())
	}
	if hint, _ := payload["hint"].(string); !strings.Contains(hint, "bd where") {
		t.Fatalf("expected hint to mention bd where, got %q", hint)
	}
}

// TestEmbeddedConfigSetNoDBRefused pins GH#4135 AC-6: both config-set entry
// points refuse no-db as an obsolete setting — it never selected a storage
// backend; issues live in the Dolt database and the JSONL export is an
// export — before writing to config.yaml or the database, while
// `bd config unset no-db` still removes the entry an older workspace may
// carry.
func TestEmbeddedConfigSetNoDBRefused(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "nd")
	configPath := filepath.Join(beadsDir, "config.yaml")

	stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "set", "no-db", "true")
	if err == nil {
		t.Fatalf("bd config set no-db succeeded on an initialized workspace, stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	assertNoStoreInitLeak(t, stderr)
	if !strings.Contains(stderr, "obsolete") || !strings.Contains(stderr, "bd config unset no-db") {
		t.Fatalf("bd config set no-db refusal is not the obsolete-setting explanation, stderr:\n%s", stderr)
	}

	stdout, stderr, err = runConfigCommandWithStreams(t, bd, dir, "set-many", "no-db=true")
	if err == nil {
		t.Fatalf("bd config set-many no-db=true succeeded on an initialized workspace, stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	assertNoStoreInitLeak(t, stderr)
	if !strings.Contains(stderr, "obsolete") || !strings.Contains(stderr, "bd config unset no-db") {
		t.Fatalf("bd config set-many no-db=true refusal is not the obsolete-setting explanation, stderr:\n%s", stderr)
	}

	// The refusals wrote nothing: the init-created config.yaml carries no
	// no-db entry.
	configYaml, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read %s: %v", configPath, readErr)
	}
	if strings.Contains(string(configYaml), "no-db") {
		t.Fatalf("config.yaml holds a no-db entry after the refused sets:\n%s", configYaml)
	}

	// An older workspace's entry is still removable: unset removes it from
	// config.yaml without touching the database.
	if writeErr := os.WriteFile(configPath, append(configYaml, []byte("no-db: true\n")...), 0o600); writeErr != nil {
		t.Fatalf("seed no-db entry in %s: %v", configPath, writeErr)
	}
	stdout, stderr, err = runConfigCommandWithStreams(t, bd, dir, "unset", "no-db")
	if err != nil {
		t.Fatalf("bd config unset no-db failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	assertNoStoreInitLeak(t, stderr)
	configYaml, readErr = os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("reread %s: %v", configPath, readErr)
	}
	for _, line := range strings.Split(string(configYaml), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "no-db:") {
			t.Fatalf("bd config unset no-db left an active no-db entry in config.yaml:\n%s", configYaml)
		}
	}
}
