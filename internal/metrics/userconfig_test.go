package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func setupUserConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func userConfigPath(home string) string {
	return filepath.Join(home, ".config", "bd", "config.yaml")
}

func parseUserConfig(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var got map[string]interface{}
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", path, err, data)
	}
	return got
}

func metricsLeaf(t *testing.T, parsed map[string]interface{}, leaf string) interface{} {
	t.Helper()
	m, ok := parsed["metrics"].(map[string]interface{})
	if !ok {
		t.Fatalf("metrics key missing or not a map: %#v", parsed["metrics"])
	}
	v, ok := m[leaf]
	if !ok {
		t.Fatalf("metrics.%s missing: %#v", leaf, m)
	}
	return v
}

func TestEnsureUserConfigDefaults_CreatesMissingFile(t *testing.T) {
	home := setupUserConfigHome(t)
	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	parsed := parseUserConfig(t, userConfigPath(home))
	if got := metricsLeaf(t, parsed, "disabled"); got != false {
		t.Errorf("metrics.disabled = %v, want false", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != DefaultEndpoint {
		t.Errorf("metrics.endpoint = %v, want %q", got, DefaultEndpoint)
	}
}

func TestEnsureUserConfigDefaults_AddsMissingMetricsBlock(t *testing.T) {
	home := setupUserConfigHome(t)
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("other: value\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	parsed := parseUserConfig(t, path)
	if parsed["other"] != "value" {
		t.Errorf("existing key clobbered: %#v", parsed)
	}
	if got := metricsLeaf(t, parsed, "disabled"); got != false {
		t.Errorf("metrics.disabled = %v, want false", got)
	}
	if got := metricsLeaf(t, parsed, "endpoint"); got != DefaultEndpoint {
		t.Errorf("metrics.endpoint = %v, want %q", got, DefaultEndpoint)
	}
}

func TestEnsureUserConfigDefaults_FillsMissingLeafKeepsExisting(t *testing.T) {
	home := setupUserConfigHome(t)
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := "metrics:\n  endpoint: https://existing.example.com\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	parsed := parseUserConfig(t, path)
	if got := metricsLeaf(t, parsed, "endpoint"); got != "https://existing.example.com" {
		t.Errorf("metrics.endpoint clobbered = %v", got)
	}
	if got := metricsLeaf(t, parsed, "disabled"); got != false {
		t.Errorf("metrics.disabled = %v, want false", got)
	}
}

func TestEnsureUserConfigDefaults_BothLeavesPresent_NoWrite(t *testing.T) {
	home := setupUserConfigHome(t)
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := []byte("metrics:\n  disabled: true\n  endpoint: https://kept.example.com\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	preStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	postStat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !postStat.ModTime().Equal(preStat.ModTime()) {
		t.Errorf("file was rewritten; mtime changed from %v to %v", preStat.ModTime(), postStat.ModTime())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("file content changed.\nwant: %q\ngot:  %q", original, got)
	}
}

func TestEnsureUserConfigDefaults_CommentedBlock_SkippedSilently(t *testing.T) {
	home := setupUserConfigHome(t)
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	original := []byte("# metrics:\n#   disabled: true\nother: value\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := EnsureUserConfigDefaults(); err != nil {
		t.Fatalf("EnsureUserConfigDefaults: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("commented-out metrics block triggered a write.\nwant: %q\ngot:  %q", original, got)
	}
}

func TestEnsureUserConfigDefaults_MalformedYaml_FailsLoudly(t *testing.T) {
	home := setupUserConfigHome(t)
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("metrics:\n  disabled: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := EnsureUserConfigDefaults()
	if err == nil {
		t.Fatal("expected an error for malformed yaml, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse failure, got: %v", err)
	}
}

func TestEnsureUserConfigDefaults_ConfigDirIsFile_FailsLoudly(t *testing.T) {
	home := setupUserConfigHome(t)
	blocker := filepath.Join(home, ".config")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}

	err := EnsureUserConfigDefaults()
	if err == nil {
		t.Fatal("expected an error when ~/.config is a file, got nil")
	}
}
