package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// platformSeamForTest points the resolver at goos with native as the
// os.UserConfigDir stand-in, and cleans the ambient environment behind it.
// The in-package twin of SetUserConfigPlatformForTest.
func platformSeamForTest(t *testing.T, goos string, nativeDir string, nativeErr error) {
	t.Helper()
	prevGOOS, prevNative := userConfigGOOS, osUserConfigDir
	userConfigGOOS = goos
	osUserConfigDir = func() (string, error) { return nativeDir, nativeErr }
	t.Cleanup(func() { userConfigGOOS, osUserConfigDir = prevGOOS, prevNative })
}

// darwinSeamForTest simulates a macOS host from any OS: HOME is the real
// source of ~/.config, and nativeDir stands in for the
// ~/Library/Application Support directory os.UserConfigDir would return on
// darwin. XDG_CONFIG_HOME is cleared so the darwin rule resolves HOME/.config.
func darwinSeamForTest(t *testing.T) (home, nativeDir string) {
	t.Helper()
	home = t.TempDir()
	nativeDir = filepath.Join(home, "Library", "Application Support")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatalf("mkdir application support stand-in: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	platformSeamForTest(t, "darwin", nativeDir, nil)
	return home, nativeDir
}

// TestUserConfigDirDarwinLaunchPathsNeverReadApplicationSupport is the GH#8113
// regression: on darwin, nothing on the launch path — the config loader
// (Initialize), UserConfigYamlPath, the metrics consent readers, or
// GetUserYamlConfig — may stat or read ~/Library/Application Support. A config
// file planted there (with a distinctive actor and metrics.disabled: true)
// proves the negative: any read of it would surface through the selected
// path, the actor, or the consent decision. This fails if the resolver call
// site is reverted to os.UserConfigDir().
func TestUserConfigDirDarwinLaunchPathsNeverReadApplicationSupport(t *testing.T) {
	home, nativeDir := darwinSeamForTest(t)

	// The pre-GH#8113 native file: only in Application Support.
	legacyFile := filepath.Join(nativeDir, "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(legacyFile), 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	legacy := "actor: appsupport\nmetrics:\n  disabled: true\n"
	if err := os.WriteFile(legacyFile, []byte(legacy), 0o600); err != nil {
		t.Fatalf("plant legacy config: %v", err)
	}

	// The config loader runs from init() on every launch; Initialize is the
	// same code path.
	ResetForTesting()
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Chdir(home)
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if got := GetString("actor"); got == "appsupport" {
		t.Errorf("Initialize merged the Application Support config: actor = %q", got)
	}

	documented := filepath.Join(home, ".config", "bd", "config.yaml")
	path, err := UserConfigYamlPath()
	if err != nil {
		t.Fatalf("UserConfigYamlPath: %v", err)
	}
	if path != documented {
		t.Errorf("UserConfigYamlPath() = %q, want the documented path %q (Application Support must never be selected)", path, documented)
	}

	if got := GetUserYamlConfig("actor"); got == "appsupport" {
		t.Errorf("GetUserYamlConfig(actor) = %q; the Application Support file was read", got)
	}
	// The planted file opted out. Only reading it can report true here: with
	// the fix nothing does, so consent stays at the default (no file at all).
	if MetricsDisabledByUserConfig() {
		t.Error("MetricsDisabledByUserConfig() = true; the Application Support consent was read")
	}
	if got := UserMetricsEndpoint(); got != "" {
		t.Errorf("UserMetricsEndpoint() = %q; the Application Support file was read", got)
	}
	if MetricsNoticeShownByUserConfig() {
		t.Error("MetricsNoticeShownByUserConfig() = true; the Application Support file was read")
	}
}

// TestUserConfigDirDarwinCandidatesIgnoreApplicationSupport pins the candidate
// set itself: on darwin with XDG unset, native == documented (one deduped
// read), and with XDG set to an absolute directory, native is that XDG file —
// never Application Support, even though os.UserConfigDir() would return it.
func TestUserConfigDirDarwinCandidatesIgnoreApplicationSupport(t *testing.T) {
	t.Run("XDG unset: native equals documented", func(t *testing.T) {
		home, nativeDir := darwinSeamForTest(t)
		candidates := currentUserConfigYamlCandidates()
		want := filepath.Join(home, ".config", "bd", "config.yaml")
		if candidates.native != want || candidates.documented != want {
			t.Errorf("native = %q, documented = %q, want both %q", candidates.native, candidates.documented, want)
		}
		if strings.Contains(candidates.native, nativeDir) {
			t.Errorf("native candidate %q resolves Application Support", candidates.native)
		}
	})

	t.Run("XDG absolute: native is the XDG file, reads and writes go there", func(t *testing.T) {
		home, _ := darwinSeamForTest(t)
		xdg := filepath.Join(home, "xdg")
		if err := os.MkdirAll(filepath.Join(xdg, "bd"), 0o755); err != nil {
			t.Fatalf("mkdir xdg dir: %v", err)
		}
		t.Setenv("XDG_CONFIG_HOME", xdg)
		xdgFile := filepath.Join(xdg, "bd", "config.yaml")
		if err := os.WriteFile(xdgFile, []byte("actor: xdg-user\n"), 0o600); err != nil {
			t.Fatalf("plant xdg config: %v", err)
		}
		candidates := currentUserConfigYamlCandidates()
		if candidates.native != xdgFile {
			t.Fatalf("native = %q, want the XDG file %q", candidates.native, xdgFile)
		}
		// With the documented file absent, the existing XDG file is selected
		// for reads and writes — the merge order is unchanged.
		got, err := UserConfigYamlPath()
		if err != nil {
			t.Fatalf("UserConfigYamlPath: %v", err)
		}
		if got != xdgFile {
			t.Errorf("UserConfigYamlPath() = %q, want the existing XDG file %q", got, xdgFile)
		}
		if got := GetUserYamlConfig("actor"); got != "xdg-user" {
			t.Errorf("GetUserYamlConfig(actor) = %q, want %q", got, "xdg-user")
		}
	})
}

// TestUserConfigDirDarwinDedupWithDocumentedFile guards the merge-precedence
// invariant on darwin: when the documented ~/.config/bd/config.yaml exists it
// wins over the native candidate, and the legacy <home>/.beads file still
// merges below native.
func TestUserConfigDirDarwinDedupWithDocumentedFile(t *testing.T) {
	home, _ := darwinSeamForTest(t)
	legacyDir := filepath.Join(home, ".beads")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "config.yaml"), []byte("actor: legacy-user\n"), 0o600); err != nil {
		t.Fatalf("plant legacy config: %v", err)
	}
	documentedDir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(documentedDir, 0o755); err != nil {
		t.Fatalf("mkdir documented dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(documentedDir, "config.yaml"), []byte("actor: documented-user\n"), 0o600); err != nil {
		t.Fatalf("plant documented config: %v", err)
	}

	ResetForTesting()
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	// cwd must be a tree that does not contain home/.beads on any parent:
	// the project-config walk would otherwise load the legacy file as a
	// project config (highest priority) and swamp the user-tier merge.
	t.Chdir(t.TempDir())
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := GetString("actor"); got != "documented-user" {
		t.Errorf("GetString(actor) = %q, want the documented file's %q", got, "documented-user")
	}
	if got := GetUserYamlConfig("actor"); got != "documented-user" {
		t.Errorf("GetUserYamlConfig(actor) = %q, want %q (selected file, not the merged view)", got, "documented-user")
	}
}

// TestUserConfigDir covers the resolver contract platform by platform
// (GH#8113): darwin/ios apply the Unix XDG rule with os.UserConfigDir's error
// text; every other GOOS returns os.UserConfigDir() exactly, value and error.
// The platform under test is the seam, so this runs on every host.
func TestUserConfigDir(t *testing.T) {
	for _, goos := range []string{"darwin", "ios"} {
		t.Run(goos, func(t *testing.T) {
			t.Run("XDG absolute: returns that directory", func(t *testing.T) {
				home, _ := darwinSeamForTest(t)
				xdg := filepath.Join(home, "xdg-root")
				t.Setenv("XDG_CONFIG_HOME", xdg)
				got, err := UserConfigDir()
				if err != nil || got != xdg {
					t.Fatalf("UserConfigDir() = %q, %v; want %q, nil", got, err, xdg)
				}
			})
			t.Run("XDG unset: returns HOME/.config", func(t *testing.T) {
				home, _ := darwinSeamForTest(t)
				got, err := UserConfigDir()
				want := filepath.Join(home, ".config")
				if err != nil || got != want {
					t.Fatalf("UserConfigDir() = %q, %v; want %q, nil", got, err, want)
				}
			})
			t.Run("XDG relative: errors naming XDG_CONFIG_HOME, documented file still used", func(t *testing.T) {
				home, _ := darwinSeamForTest(t)
				t.Setenv("XDG_CONFIG_HOME", "relative-xdg")
				got, err := UserConfigDir()
				if err == nil || got != "" {
					t.Fatalf("UserConfigDir() = %q, %v; want \"\", error", got, err)
				}
				if !strings.Contains(err.Error(), "XDG_CONFIG_HOME") {
					t.Errorf("error %q does not name XDG_CONFIG_HOME", err)
				}
				// The failed native candidate falls back to the documented
				// path, exactly as on Linux.
				path, err := UserConfigYamlPath()
				if err != nil {
					t.Fatalf("UserConfigYamlPath: %v", err)
				}
				if want := filepath.Join(home, ".config", "bd", "config.yaml"); path != want {
					t.Errorf("UserConfigYamlPath() = %q, want the documented %q", path, want)
				}
			})
			t.Run("HOME empty and XDG unset: errors naming both", func(t *testing.T) {
				platformSeamForTest(t, goos, "", nil)
				t.Setenv("HOME", "")
				t.Setenv("USERPROFILE", "")
				t.Setenv("XDG_CONFIG_HOME", "")
				got, err := UserConfigDir()
				if err == nil || got != "" {
					t.Fatalf("UserConfigDir() = %q, %v; want \"\", error", got, err)
				}
				if !strings.Contains(err.Error(), "neither $XDG_CONFIG_HOME nor $HOME are defined") {
					t.Errorf("error %q does not match the os.UserConfigDir Unix text", err)
				}
			})
		})
	}

	t.Run("other GOOS returns os.UserConfigDir exactly", func(t *testing.T) {
		// Value passthrough, including a value the candidate builder would
		// otherwise reject (a relative one): the resolver must not clean,
		// validate or rewrite it for non-darwin/ios.
		platformSeamForTest(t, "windows", `C:\Users\u\AppData\Roaming`, nil)
		t.Setenv("APPDATA", "")
		got, err := UserConfigDir()
		if err != nil || got != `C:\Users\u\AppData\Roaming` {
			t.Fatalf("UserConfigDir() = %q, %v; want the native value unchanged", got, err)
		}
		sentinel := errors.New("native resolver diagnostic")
		platformSeamForTest(t, "linux", "", sentinel)
		got, err = UserConfigDir()
		if !errors.Is(err, sentinel) || got != "" {
			t.Fatalf("UserConfigDir() = %q, %v; want the native error unchanged", got, err)
		}
		// The exported seam agrees with the in-package one.
		restore := SetUserConfigPlatformForTest("plan9", func() (string, error) { return "/plan9", nil })
		defer restore()
		got, err = UserConfigDir()
		if err != nil || got != "/plan9" {
			t.Fatalf("UserConfigDir() after SetUserConfigPlatformForTest = %q, %v", got, err)
		}
		restore()
		got, err = UserConfigDir()
		// restore() must put back what SetUserConfigPlatformForTest found:
		// the linux/sentinel seam from platformSeamForTest above.
		if !errors.Is(err, sentinel) || got != "" {
			t.Fatalf("after restore: UserConfigDir() = %q, %v; want the pre-seam sentinel error", got, err)
		}
	})
}
