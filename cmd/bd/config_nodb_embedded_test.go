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

	t.Run("set_yaml_only_reports_config_yaml_error", func(t *testing.T) {
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "set", "json", "true")
		if err == nil {
			t.Fatalf("expected config set json to fail without a workspace config, stdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		assertNoStoreInitLeak(t, stderr)
		if !strings.Contains(stderr, "no .beads/config.yaml found") {
			t.Fatalf("expected config.yaml error, got stderr:\n%s", stderr)
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

// TestEmbeddedConfigSetNoDBRefused: 'bd config set no-db' (and set-many) is
// refused with an obsolete-setting explanation before it writes config.yaml
// or the database, while 'bd config unset no-db' still clears an old
// config.yaml entry (GH#7387).
func TestEmbeddedConfigSetNoDBRefused(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "nodbset")
	configPath := filepath.Join(beadsDir, "config.yaml")

	readConfig := func() []byte {
		t.Helper()
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	listJSON := func() string {
		t.Helper()
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "list", "--json")
		if err != nil {
			t.Fatalf("config list --json failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		return stdout
	}

	before, beforeList := readConfig(), listJSON()
	for _, args := range [][]string{{"set", "no-db", "true"}, {"set", "no-db", "false"}, {"set-many", "no-db=true"}} {
		stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, args...)
		if err == nil {
			t.Fatalf("bd config %s succeeded:\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), stdout, stderr)
		}
		for _, want := range []string{"no-db", "obsolete", "Dolt"} {
			if !strings.Contains(stderr, want) {
				t.Fatalf("bd config %s refusal does not mention %q:\n%s", strings.Join(args, " "), want, stderr)
			}
		}
		if after := readConfig(); !bytes.Equal(after, before) {
			t.Fatalf("bd config %s changed config.yaml:\n%s", strings.Join(args, " "), after)
		}
		if after := listJSON(); after != beforeList {
			t.Fatalf("bd config %s changed the stored settings:\nbefore:\n%s\nafter:\n%s", strings.Join(args, " "), beforeList, after)
		}
	}

	if err := os.WriteFile(configPath, append(before, []byte("no-db: true\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runConfigCommandWithStreams(t, bd, dir, "unset", "no-db")
	if err != nil {
		t.Fatalf("config unset no-db failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	// unset comments the entry out; only an active one would still be read.
	after := readConfig()
	for _, line := range strings.Split(string(after), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "no-db:") {
			t.Fatalf("config unset no-db left the entry active in config.yaml:\n%s", after)
		}
	}
}

// TestEmbeddedNoDBTrueIgnoredWithWarning: a workspace whose old config still
// turns no-db on (config.yaml or BD_NO_DB) opens its Dolt database as usual,
// and a store-opening command warns exactly once on stderr that the setting
// is obsolete, leaving --json stdout parseable (GH#7387).
func TestEmbeddedNoDBTrueIgnoredWithWarning(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	const warning = "no-db is obsolete"
	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "nodbwarn")
	configPath := filepath.Join(beadsDir, "config.yaml")
	template, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	issue := bdCreate(t, bd, dir, "kept in dolt")

	listsIssue := func(t *testing.T, env []string, wantWarnings int) {
		t.Helper()
		run := runBDMetadataless(t, bd, dir, env, "list", "--json")
		if run.err != nil {
			t.Fatalf("bd list --json failed: %v\nstdout:\n%s\nstderr:\n%s", run.err, run.stdout, run.stderr)
		}
		var issues []map[string]any
		if err := json.Unmarshal([]byte(run.stdout), &issues); err != nil {
			t.Fatalf("bd list --json stdout is not JSON: %v\n%s", err, run.stdout)
		}
		if len(issues) != 1 || issues[0]["id"] != issue.ID {
			t.Fatalf("bd list --json = %s, want only %s from the Dolt database", run.stdout, issue.ID)
		}
		if got := strings.Count(run.stderr, warning); got != wantWarnings {
			t.Fatalf("stderr has %d %q warnings, want %d:\n%s", got, warning, wantWarnings, run.stderr)
		}
	}

	t.Run("config.yaml true", func(t *testing.T) {
		if err := os.WriteFile(configPath, append(append([]byte{}, template...), []byte("no-db: true\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(configPath, template, 0o600) })
		listsIssue(t, nil, 1)

		run := runBDMetadataless(t, bd, dir, nil, "update", issue.ID, "--notes", "written to dolt", "--json")
		if run.err != nil {
			t.Fatalf("bd update --json failed: %v\nstdout:\n%s\nstderr:\n%s", run.err, run.stdout, run.stderr)
		}
		if !json.Valid([]byte(run.stdout)) {
			t.Fatalf("bd update --json stdout is not JSON:\n%s", run.stdout)
		}
		if got := strings.Count(run.stderr, warning); got != 1 {
			t.Fatalf("bd update stderr has %d %q warnings, want 1:\n%s", got, warning, run.stderr)
		}
		if shown := bdShow(t, bd, dir, issue.ID); shown.Notes != "written to dolt" {
			t.Fatalf("notes = %q after bd update, want the write in the Dolt database", shown.Notes)
		}
	})

	t.Run("BD_NO_DB true", func(t *testing.T) {
		listsIssue(t, []string{"BD_NO_DB=true"}, 1)
	})

	t.Run("false or unset is quiet", func(t *testing.T) {
		if err := os.WriteFile(configPath, append(append([]byte{}, template...), []byte("no-db: false\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(configPath, template, 0o600) })
		listsIssue(t, nil, 0)
		listsIssue(t, []string{"BD_NO_DB=false"}, 0)
	})
}
