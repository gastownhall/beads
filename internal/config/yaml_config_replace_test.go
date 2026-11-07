package config

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A config.yaml is read by processes that run beside the writer: a running
// `bd serve` re-reads events-journal activation about once a second, and
// every CLI reads the file at startup. Rewriting it in place (truncate, then
// write) hands any reader that lands in between an empty or half-written file,
// which parses as "no such key" and resolves to the key's DEFAULT. These pin
// that bd's own writers replace the file in one step instead: a reader sees
// the old file or the new one, never a fragment of either.

// openedBeforeWrite holds the file a concurrent reader opened before the write,
// and reads it back after: an in-place writer truncates THAT file, a replacing
// writer leaves it whole.
func openedBeforeWrite(t *testing.T, path string) func() string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return func() string {
		t.Helper()
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			t.Fatalf("seek: %v", err)
		}
		data, err := io.ReadAll(f)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(data)
	}
}

func TestSetYamlConfigInDirReplacesTheFileInOneStep(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	const before = "events-journal: true\nevents-journal-auto-prune: false\n"
	if err := os.WriteFile(configPath, []byte(before), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	reader := openedBeforeWrite(t, configPath)

	if err := SetYamlConfigInDir(beadsDir, "json", "true"); err != nil {
		t.Fatalf("SetYamlConfigInDir: %v", err)
	}

	if got := reader(); got != before {
		t.Errorf("a reader that opened config.yaml before the write saw %q, want the whole old file %q: the write truncated it in place", got, before)
	}
	value, present, err := WorkspaceYamlValueStrict(beadsDir, "json")
	if err != nil || !present || value != "true" {
		t.Errorf("after the write json = (%q, %v, %v), want (\"true\", true, nil)", value, present, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("config.yaml mode after the write = %o, want the file's own 0644 kept", got)
		}
	}
}

func TestUnsetYamlConfigReplacesTheFileInOneStep(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()
	beadsDir := t.TempDir()
	t.Setenv("BEADS_DIR", beadsDir)
	configPath := filepath.Join(beadsDir, "config.yaml")
	const before = "events-journal: true\nother-setting: value\n"
	if err := os.WriteFile(configPath, []byte(before), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	reader := openedBeforeWrite(t, configPath)

	unset, err := UnsetYamlConfig("other-setting")
	if err != nil || !unset {
		t.Fatalf("UnsetYamlConfig = (%v, %v), want (true, nil)", unset, err)
	}
	if got := reader(); got != before {
		t.Errorf("a reader that opened config.yaml before the unset saw %q, want the whole old file %q", got, before)
	}
}

// TestSetYamlConfigInDirWritesThroughASymlink: replacing by rename must not
// turn a symlinked config.yaml (a dotfiles checkout, a shared config) into a
// regular file that silently stops tracking its target.
func TestSetYamlConfigInDirWritesThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevated privileges on Windows")
	}
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(dir, "shared-config.yaml")
	if err := os.WriteFile(target, []byte("json: false\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(beadsDir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := SetYamlConfigInDir(beadsDir, "json", "true"); err != nil {
		t.Fatalf("SetYamlConfigInDir: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config.yaml is no longer a symlink after the write")
	}
	data, err := os.ReadFile(target) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !strings.Contains(string(data), "json: true") {
		t.Errorf("symlink target = %q, want the written value", data)
	}
}
