package config

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A config.yaml is read by processes that run beside the writer: every bd
// command loads it at startup and reads events-journal activation from it when
// it opens its store, and `bd serve` resolves that activation once, at
// startup, for the life of the server. Rewriting the file in place (truncate,
// then write) hands any reader that lands in between an empty or half-written
// file, which parses as "no such key" and resolves to the key's DEFAULT. These
// pin that bd's writers replace an existing config.yaml in one step instead:
// a reader sees the old file or the new one, never a fragment of either.

// openedBeforeWrite holds the file a concurrent reader opened before the write,
// and reads it back after: an in-place writer truncates THAT file, a replacing
// writer leaves it whole. It returns nil on Windows, which refuses to rename
// over a file a reader holds open: there internal/atomicfile waits for the
// reader to close it instead (TestWriteFile_TargetHeldOpenByAReader), so the
// test checks the write's result alone.
func openedBeforeWrite(t *testing.T, path string) func() string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return nil
	}
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

// writeConfigFixture writes content to path at exactly mode. WriteFile's mode
// passes through the umask, so it is set again after the write.
func writeConfigFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// assertModeKept checks that a write left path at the mode it had. Windows
// keeps only a read-only bit, so it is not checked there.
func assertModeKept(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != mode {
		t.Errorf("%s mode after the write = %o, want the file's own %o kept", path, got, mode)
	}
}

func TestSetYamlConfigInDirReplacesTheFileInOneStep(t *testing.T) {
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	const before = "events-journal: true\nevents-journal-auto-prune: false\n"
	// 0640 is a mode no writer defaults to, so keeping it is the file's own.
	writeConfigFixture(t, configPath, before, 0o640)
	reader := openedBeforeWrite(t, configPath)

	if err := SetYamlConfigInDir(beadsDir, "json", "true"); err != nil {
		t.Fatalf("SetYamlConfigInDir: %v", err)
	}

	if reader != nil {
		if got := reader(); got != before {
			t.Errorf("a reader that opened config.yaml before the write saw %q, want the whole old file %q: the write truncated it in place", got, before)
		}
	}
	value, present, err := WorkspaceYamlValueStrict(beadsDir, "json")
	if err != nil || !present || value != "true" {
		t.Errorf("after the write json = (%q, %v, %v), want (\"true\", true, nil)", value, present, err)
	}
	assertModeKept(t, configPath, 0o640)
}

func TestUnsetYamlConfigReplacesTheFileInOneStep(t *testing.T) {
	restore := envSnapshot(t)
	defer restore()
	beadsDir := t.TempDir()
	t.Setenv("BEADS_DIR", beadsDir)
	configPath := filepath.Join(beadsDir, "config.yaml")
	const before = "events-journal: true\nother-setting: value\n"
	writeConfigFixture(t, configPath, before, 0o640)
	reader := openedBeforeWrite(t, configPath)

	unset, err := UnsetYamlConfig("other-setting")
	if err != nil || !unset {
		t.Fatalf("UnsetYamlConfig = (%v, %v), want (true, nil)", unset, err)
	}
	if reader != nil {
		if got := reader(); got != before {
			t.Errorf("a reader that opened config.yaml before the unset saw %q, want the whole old file %q", got, before)
		}
	}
	if value, present, err := WorkspaceYamlValueStrict(beadsDir, "other-setting"); err != nil || present {
		t.Errorf("after the unset other-setting = (%q, %v, %v), want it absent", value, present, err)
	}
	assertModeKept(t, configPath, 0o640)
}

// TestSetReposInYAMLReplacesTheFileInOneStep: `bd repo add/remove` and
// `bd init --contributor` rewrite the same config.yaml through SetReposInYAML.
func TestSetReposInYAMLReplacesTheFileInOneStep(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	const before = "issue-prefix: bd\nevents-journal: true\n"
	writeConfigFixture(t, configPath, before, 0o640)
	reader := openedBeforeWrite(t, configPath)

	if err := SetReposInYAML(configPath, &ReposConfig{Primary: ".", Additional: []string{"../other"}}); err != nil {
		t.Fatalf("SetReposInYAML: %v", err)
	}

	if reader != nil {
		if got := reader(); got != before {
			t.Errorf("a reader that opened config.yaml before the write saw %q, want the whole old file %q", got, before)
		}
	}
	repos, err := GetReposFromYAML(configPath)
	if err != nil {
		t.Fatalf("GetReposFromYAML: %v", err)
	}
	if repos.Primary != "." || len(repos.Additional) != 1 || repos.Additional[0] != "../other" {
		t.Errorf("after the write repos = %+v, want primary \".\" and additional [../other]", repos)
	}
	assertModeKept(t, configPath, 0o640)
}

// TestUserConfigYamlStaysOwnerPrivateAcrossRewrites: the user-global
// config.yaml holds node_id, the metrics endpoint and mail.delegate.
// SetUserYamlConfig creates it 0600, and a rewrite that took a default mode
// instead of the file's own would leave it world-readable.
func TestUserConfigYamlStaysOwnerPrivateAcrossRewrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file modes carry only a read-only bit")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := UserConfigYamlPath()
	if err != nil {
		t.Fatalf("UserConfigYamlPath: %v", err)
	}

	if err := SetUserYamlConfig("node_id", "test-node"); err != nil {
		t.Fatalf("SetUserYamlConfig (create): %v", err)
	}
	assertModeKept(t, path, 0o600)
	if err := SetUserYamlConfig("metrics.disabled", "true"); err != nil {
		t.Fatalf("SetUserYamlConfig (rewrite): %v", err)
	}
	assertModeKept(t, path, 0o600)
	unset, err := UnsetUserYamlConfig("node_id")
	if err != nil || !unset {
		t.Fatalf("UnsetUserYamlConfig = (%v, %v), want (true, nil)", unset, err)
	}
	assertModeKept(t, path, 0o600)
}

// TestSetYamlConfigInDirRefusesAReadOnlyConfigYaml: a rename needs write
// permission on the directory, not on the file, so replacing config.yaml
// would rewrite a file its owner made read-only and leave it looking
// read-only. The in-place write refused it; so does the replace.
func TestSetYamlConfigInDirRefusesAReadOnlyConfigYaml(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only file")
	}
	beadsDir := t.TempDir()
	configPath := filepath.Join(beadsDir, "config.yaml")
	const before = "json: false\n"
	writeConfigFixture(t, configPath, before, 0o444)

	err := SetYamlConfigInDir(beadsDir, "json", "true")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("SetYamlConfigInDir on a read-only config.yaml = %v, want a permission error", err)
	}
	data, err := os.ReadFile(configPath) //nolint:gosec // test-owned temp path
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if string(data) != before {
		t.Errorf("read-only config.yaml = %q after the refused write, want it unchanged %q", data, before)
	}
	assertModeKept(t, configPath, 0o444)
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
