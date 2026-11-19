package config

import (
	"errors"
	"path/filepath"
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
