package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// countingNativeDir stands in for os.UserConfigDir. On darwin/ios every call is
// a lookup of the legacy ~/Library/Application Support root, so the counter is
// the number of times bd named that path.
type countingNativeDir struct {
	dir   string
	err   error
	calls int
}

func (c *countingNativeDir) fn() (string, error) {
	c.calls++
	return c.dir, c.err
}

func TestUserConfigDirDarwinUsesXDGRule(t *testing.T) {
	for _, goos := range []string{"darwin", "ios"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			native := &countingNativeDir{dir: filepath.Join(home, "Library", "Application Support")}
			t.Cleanup(SetUserConfigPlatformForTest(goos, native.fn))
			t.Setenv("HOME", home)

			t.Run("XDG absolute", func(t *testing.T) {
				xdg := filepath.Join(t.TempDir(), "xdg")
				t.Setenv("XDG_CONFIG_HOME", xdg)
				got, err := UserConfigDir()
				if err != nil || got != xdg {
					t.Fatalf("UserConfigDir() = %q, %v; want %q", got, err, xdg)
				}
			})
			t.Run("XDG unset", func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", "")
				got, err := UserConfigDir()
				if want := filepath.Join(home, ".config"); err != nil || got != want {
					t.Fatalf("UserConfigDir() = %q, %v; want %q", got, err, want)
				}
				path, err := UserConfigYamlPath()
				if want := filepath.Join(home, ".config", "bd", "config.yaml"); err != nil || path != want {
					t.Fatalf("UserConfigYamlPath() = %q, %v; want %q", path, err, want)
				}
			})
			t.Run("XDG relative", func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", "relative-xdg")
				got, err := UserConfigDir()
				if err == nil || got != "" || !strings.Contains(err.Error(), "XDG_CONFIG_HOME") {
					t.Fatalf("UserConfigDir() = %q, %v; want an XDG_CONFIG_HOME error", got, err)
				}
				// The documented file is still used, as on Linux.
				path, err := UserConfigYamlPath()
				if want := filepath.Join(home, ".config", "bd", "config.yaml"); err != nil || path != want {
					t.Fatalf("UserConfigYamlPath() = %q, %v; want %q", path, err, want)
				}
			})
			t.Run("HOME empty", func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", "")
				t.Setenv("HOME", "")
				if got, err := UserConfigDir(); err == nil || got != "" {
					t.Fatalf("UserConfigDir() = %q, %v; want an error", got, err)
				}
			})
			if native.calls != 0 {
				t.Fatalf("os.UserConfigDir (Application Support) consulted %d times on %s", native.calls, goos)
			}
		})
	}
}

func TestUserConfigDirOtherGOOSIsOSUserConfigDir(t *testing.T) {
	sentinel := errors.New("native resolver failed")
	for _, goos := range []string{"linux", "windows", "freebsd", "plan9"} {
		t.Run(goos, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
			native := &countingNativeDir{dir: "/native/root"}
			t.Cleanup(SetUserConfigPlatformForTest(goos, native.fn))
			if got, err := UserConfigDir(); err != nil || got != "/native/root" {
				t.Fatalf("UserConfigDir() = %q, %v; want os.UserConfigDir's value", got, err)
			}
			native.dir, native.err = "", sentinel
			if got, err := UserConfigDir(); !errors.Is(err, sentinel) || got != "" {
				t.Fatalf("UserConfigDir() = %q, %v; want os.UserConfigDir's error exactly", got, err)
			}
			if native.calls != 2 {
				t.Fatalf("os.UserConfigDir called %d times, want 2", native.calls)
			}
		})
	}
}

// TestDarwinUserConfigReadsNeverTouchApplicationSupport plants a config under
// ~/Library/Application Support/bd on darwin and drives every launch-path
// user-config read. None may name that path: os.UserConfigDir (the only route
// to it) must never be called, and nothing it holds may surface.
func TestDarwinUserConfigReadsNeverTouchApplicationSupport(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	restore := envSnapshot(t)
	defer restore()

	home := filepath.Join(t.TempDir(), "home")
	appSupport := filepath.Join(home, "Library", "Application Support")
	legacyConfig := filepath.Join(appSupport, "bd", "config.yaml")
	writeLegacyAppSupportConfig(t, legacyConfig)

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("BEADS_DIR", "")
	t.Chdir(t.TempDir())

	readAll := func(t *testing.T) (actor, path string, disabled bool, endpoint string) {
		t.Helper()
		ResetForTesting()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		actor = GetString("actor")
		var err error
		path, err = UserConfigYamlPath()
		if err != nil {
			t.Fatalf("UserConfigYamlPath: %v", err)
		}
		_ = UserConfigYamlDisplayPath()
		_ = MetricsNoticeShownByUserConfig()
		_ = GetUserYamlConfig("actor")
		return actor, path, MetricsDisabledByUserConfig(), UserMetricsEndpoint()
	}

	t.Run("darwin", func(t *testing.T) {
		native := &countingNativeDir{dir: appSupport}
		t.Cleanup(SetUserConfigPlatformForTest("darwin", native.fn))
		actor, path, disabled, endpoint := readAll(t)
		if native.calls != 0 {
			t.Fatalf("Application Support named %d times by launch-path reads", native.calls)
		}
		if actor == "appsupport" || disabled || endpoint != "" {
			t.Fatalf("Application Support config surfaced: actor=%q disabled=%v endpoint=%q", actor, disabled, endpoint)
		}
		if want := filepath.Join(home, ".config", "bd", "config.yaml"); path != want {
			t.Fatalf("UserConfigYamlPath() = %q, want %q", path, want)
		}
	})

	// Control: on Linux the same native root is still merged, which proves
	// the reads above go through UserConfigDir and the darwin result is the
	// resolver's doing, not a path the reads never consulted.
	t.Run("linux control", func(t *testing.T) {
		native := &countingNativeDir{dir: appSupport}
		t.Cleanup(SetUserConfigPlatformForTest("linux", native.fn))
		actor, path, disabled, _ := readAll(t)
		if native.calls == 0 || actor != "appsupport" || !disabled || path != legacyConfig {
			t.Fatalf("linux native root not consulted: calls=%d actor=%q disabled=%v path=%q",
				native.calls, actor, disabled, path)
		}
	})
}

func writeLegacyAppSupportConfig(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	body := "actor: appsupport\nmetrics:\n  disabled: true\n  endpoint: https://legacy.invalid/\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestDarwinUserConfigIgnoresApplicationSupport is the native macOS check of
// the seam-driven test above: with no seam, a config planted where Go's
// os.UserConfigDir points on darwin (~/Library/Application Support) is never
// merged, and the user config path is ~/.config/bd/config.yaml (GH#8113).
// scripts/ci/test-user-config-diagnostic.sh runs it on the macOS PR leg.
func TestDarwinUserConfigIgnoresApplicationSupport(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("native darwin check; %s covers it through the seam in TestDarwinUserConfigReadsNeverTouchApplicationSupport", runtime.GOOS)
	}
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	restore := envSnapshot(t)
	defer restore()

	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("BEADS_DIR", "")
	t.Chdir(t.TempDir())

	appSupport, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("os.UserConfigDir: %v", err)
	}
	if want := filepath.Join(home, "Library", "Application Support"); appSupport != want {
		t.Fatalf("os.UserConfigDir() = %q, want %q", appSupport, want)
	}
	writeLegacyAppSupportConfig(t, filepath.Join(appSupport, "bd", "config.yaml"))

	if got, err := UserConfigDir(); err != nil || got != filepath.Join(home, ".config") {
		t.Fatalf("UserConfigDir() = %q, %v; want %q", got, err, filepath.Join(home, ".config"))
	}
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if actor := GetString("actor"); actor == "appsupport" {
		t.Fatal("actor came from ~/Library/Application Support/bd/config.yaml")
	}
	if MetricsDisabledByUserConfig() {
		t.Fatal("metrics consent came from ~/Library/Application Support/bd/config.yaml")
	}
	path, err := UserConfigYamlPath()
	if want := filepath.Join(home, ".config", "bd", "config.yaml"); err != nil || path != want {
		t.Fatalf("UserConfigYamlPath() = %q, %v; want %q", path, err, want)
	}
}
