package metrics

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
)

func setupUserConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
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

func TestEnsureUserConfigDefaults_UnsafeRootsFailBeforeMutation(t *testing.T) {
	sentinel := t.TempDir()
	t.Chdir(sentinel)
	t.Setenv("HOME", "~")
	t.Setenv("USERPROFILE", "~")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	t.Setenv("XDG_CONFIG_HOME", "relative-xdg")
	t.Setenv("APPDATA", "relative-appdata")

	err := EnsureUserConfigDefaults()
	if err == nil {
		t.Fatal("EnsureUserConfigDefaults returned nil error for unsafe user roots")
	}
	if !strings.Contains(err.Error(), "resolve user config.yaml") ||
		!strings.Contains(err.Error(), "not an absolute native path") {
		t.Fatalf("EnsureUserConfigDefaults error = %v, want native path resolution context", err)
	}

	for _, relativeRoot := range []string{"~", "relative-xdg", "relative-appdata"} {
		if _, statErr := os.Stat(filepath.Join(sentinel, relativeRoot)); !os.IsNotExist(statErr) {
			t.Errorf("unsafe relative root %q was materialized: %v", relativeRoot, statErr)
		}
	}
}

// TestEnsureUserConfigDefaults_DanglingSymlinkFailsWithoutRecursing: a
// dangling symlink at the user config path reads as missing, yet the
// exclusive create reports it as existing. The bootstrap must return an
// error instead of re-entering itself until the stack is exhausted.
func TestEnsureUserConfigDefaults_DanglingSymlinkFailsWithoutRecursing(t *testing.T) {
	home := setupUserConfigHome(t)
	path := userConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(home, "dotfiles", "bd", "config.yaml") // never created
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := EnsureUserConfigDefaults()
	if err == nil || !strings.Contains(err.Error(), "dangling symlink") {
		t.Fatalf("EnsureUserConfigDefaults() = %v, want a dangling symlink error", err)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Fatalf("bootstrap created the symlink target: %v", statErr)
	}
}

// TestEnsureUserConfigDefaults_UnusablePathConsentUnknownOnlyOnDarwin: on
// macOS, when the user config path can be neither read nor created (a
// symlink to a missing file, a ~/.config that is a file), the bootstrap
// reports config.ErrUserConfigConsentUnknown so the launch keeps metrics off,
// and it never probes the legacy Application Support config for a file it
// cannot publish. writeUserConfigBootstrap reports the sentinel itself rather
// than swallowing it with the existing-file case. On Linux the same shapes
// fail as they always did, without the sentinel.
func TestEnsureUserConfigDefaults_UnusablePathConsentUnknownOnlyOnDarwin(t *testing.T) {
	type shape struct {
		name  string
		plant func(t *testing.T, home string)
	}
	shapes := []shape{
		{"dangling symlink", func(t *testing.T, home string) {
			path := userConfigPath(home)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.Symlink(filepath.Join(home, "dotfiles", "missing.yaml"), path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}},
		{"config dir is a file", func(t *testing.T, home string) {
			if err := os.WriteFile(filepath.Join(home, ".config"), []byte("not a directory"), 0o600); err != nil {
				t.Fatalf("seed blocker: %v", err)
			}
		}},
	}
	for _, goos := range []string{"darwin", "linux"} {
		for _, sh := range shapes {
			t.Run(goos+"/"+sh.name, func(t *testing.T) {
				home := setupUserConfigHome(t)
				t.Setenv("XDG_CONFIG_HOME", "")
				native := filepath.Join(home, ".config") // Linux: the XDG rule
				if goos == "darwin" {
					native = filepath.Join(home, "Library", "Application Support")
				}
				t.Cleanup(config.SetUserConfigPlatformForTest(goos, func() (string, error) { return native, nil }))
				probes := 0
				t.Cleanup(config.SetLegacyUserConfigStatForTest(func(name string) (fs.FileInfo, error) {
					probes++
					return os.Stat(name)
				}))
				sh.plant(t, home)
				wantSentinel := goos == "darwin"

				err := EnsureUserConfigDefaults()
				if err == nil {
					t.Fatal("EnsureUserConfigDefaults returned nil for an unusable user config path")
				}
				if got := errors.Is(err, config.ErrUserConfigConsentUnknown); got != wantSentinel {
					t.Errorf("EnsureUserConfigDefaults = %v; consent-unknown sentinel = %v, want %v", err, got, wantSentinel)
				}
				if sh.name == "dangling symlink" {
					err := writeUserConfigBootstrap(userConfigPath(home))
					if got := errors.Is(err, config.ErrUserConfigConsentUnknown); got != wantSentinel {
						t.Errorf("writeUserConfigBootstrap = %v; consent-unknown sentinel = %v, want %v", err, got, wantSentinel)
					}
					if !wantSentinel && err != nil {
						t.Errorf("writeUserConfigBootstrap = %v; on %s an occupied path is left to the re-read", err, goos)
					}
				}
				if probes != 0 {
					t.Errorf("legacy config probed %d times for a user config bd cannot create", probes)
				}
			})
		}
	}
}
