package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUserConfigYamlPathNamesNativeEnvironmentSource(t *testing.T) {
	t.Setenv("HOME", "~")
	t.Setenv("USERPROFILE", "~")
	t.Setenv("home", "~")
	t.Setenv("APPDATA", "relative-appdata")
	for _, xdg := range []string{"relative-xdg", ""} {
		name := "XDG configured"
		wantSource := "XDG_CONFIG_HOME"
		if xdg == "" {
			name, wantSource = "XDG absent", "HOME"
		}
		switch runtime.GOOS {
		case "windows":
			wantSource = "APPDATA"
		case "darwin", "ios":
			wantSource = "HOME"
		case "plan9":
			wantSource = "home"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", xdg)
			path, err := UserConfigYamlPath()
			if err == nil || path != "" {
				t.Fatalf("unsafe roots produced path %q, err=%v", path, err)
			}
			wantLabel := "native user config directory (" + wantSource + ")"
			if wantSource == "XDG_CONFIG_HOME" {
				// Go rejects relative XDG roots before returning a directory;
				// preserve its source-bearing error under the generic label.
				wantLabel = "native user config directory: "
			}
			if !strings.Contains(err.Error(), wantLabel) || !strings.Contains(err.Error(), wantSource) {
				t.Errorf("native config error %q does not identify %s", err, wantSource)
			}
		})
	}
}

func TestUserConfigYamlCandidatesOwnNativeDiagnostics(t *testing.T) {
	wantSource := "HOME"
	if runtime.GOOS == "windows" {
		wantSource = "APPDATA"
	} else if runtime.GOOS == "plan9" {
		wantSource = "home"
	}
	t.Run("validation identifies its source", func(t *testing.T) {
		candidates := buildUserConfigYamlCandidates(t.TempDir(), nil, "relative-native", nil)
		want := "native user config directory (" + wantSource + ")"
		if candidates.nativeErr == nil || !strings.Contains(candidates.nativeErr.Error(), want) {
			t.Fatalf("builder native error = %v, want label %q", candidates.nativeErr, want)
		}
	})
	t.Run("resolver diagnostic and identity are preserved", func(t *testing.T) {
		resolverErr := errors.New("XDG_CONFIG_HOME resolver diagnostic")
		candidates := buildUserConfigYamlCandidates(t.TempDir(), nil, "", resolverErr)
		if !errors.Is(candidates.nativeErr, resolverErr) {
			t.Fatalf("builder native error = %v, want original resolver error", candidates.nativeErr)
		}
		if got, want := candidates.nativeErr.Error(), "native user config directory: "+resolverErr.Error(); got != want {
			t.Fatalf("builder native error = %q, want %q", got, want)
		}
	})
}

func TestSelectUserConfigYamlPathPrecedence(t *testing.T) {
	t.Run("existing documented path wins", func(t *testing.T) {
		home, native := userConfigTestRoots(t)
		candidates := buildUserConfigYamlCandidates(home, nil, native, nil)
		writeUserConfigPath(t, candidates.native)
		writeUserConfigPath(t, candidates.documented)

		got, err := selectUserConfigYamlPath(candidates)
		if err != nil {
			t.Fatalf("selectUserConfigYamlPath: %v", err)
		}
		if got != candidates.documented {
			t.Fatalf("path = %q, want documented path %q", got, candidates.documented)
		}
	})

	t.Run("existing native path wins over a missing documented path", func(t *testing.T) {
		home, native := userConfigTestRoots(t)
		candidates := buildUserConfigYamlCandidates(home, nil, native, nil)
		writeUserConfigPath(t, candidates.native)

		got, err := selectUserConfigYamlPath(candidates)
		if err != nil {
			t.Fatalf("selectUserConfigYamlPath: %v", err)
		}
		if got != candidates.native {
			t.Fatalf("path = %q, want existing native path %q", got, candidates.native)
		}
	})

	t.Run("documented path is the preferred creation target", func(t *testing.T) {
		home, native := userConfigTestRoots(t)
		candidates := buildUserConfigYamlCandidates(home, nil, native, nil)

		got, err := selectUserConfigYamlPath(candidates)
		if err != nil {
			t.Fatalf("selectUserConfigYamlPath: %v", err)
		}
		if got != candidates.documented {
			t.Fatalf("path = %q, want documented creation target %q", got, candidates.documented)
		}
	})

	t.Run("native path is the creation fallback when home is unsafe", func(t *testing.T) {
		_, native := userConfigTestRoots(t)
		candidates := buildUserConfigYamlCandidates("~", nil, native, nil)

		got, err := selectUserConfigYamlPath(candidates)
		if err != nil {
			t.Fatalf("selectUserConfigYamlPath: %v", err)
		}
		if got != candidates.native {
			t.Fatalf("path = %q, want native creation target %q", got, candidates.native)
		}
	})
}

func TestUserConfigYamlCandidatesRequireAbsoluteNativeRoots(t *testing.T) {
	resolutionFailure := errors.New("not available")
	tests := []struct {
		name      string
		home      string
		homeErr   error
		native    string
		nativeErr error
	}{
		{name: "literal tilde", home: "~", native: "relative-config"},
		{name: "empty roots", home: "", native: ""},
		{name: "resolver errors", homeErr: resolutionFailure, nativeErr: resolutionFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := buildUserConfigYamlCandidates(tt.home, tt.homeErr, tt.native, tt.nativeErr)
			if candidates.legacy != "" || candidates.documented != "" || candidates.native != "" {
				t.Fatalf("unsafe roots produced filesystem candidates: %#v", candidates)
			}
			if _, err := selectUserConfigYamlPath(candidates); err == nil {
				t.Fatal("selectUserConfigYamlPath returned nil error for unsafe roots")
			}
			if got := userConfigYamlDisplayPath(candidates); got != userConfigYamlDisplayFallback {
				t.Fatalf("display path = %q, want compatibility fallback %q", got, userConfigYamlDisplayFallback)
			}
		})
	}
}

func TestUserConfigYamlCandidatesAreCleanAndAbsolute(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "profile", "..", "home")
	native := filepath.Join(base, "native", ".", "config")
	candidates := buildUserConfigYamlCandidates(home, nil, native, nil)

	for name, path := range map[string]string{
		"legacy":     candidates.legacy,
		"native":     candidates.native,
		"documented": candidates.documented,
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%s path %q is not absolute", name, path)
		}
		if filepath.Clean(path) != path {
			t.Errorf("%s path %q is not clean", name, path)
		}
	}
}

func TestRelativeUserRootsNeverReachImplicitOrExplicitFilesystemPaths(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	sentinel := t.TempDir()
	t.Chdir(sentinel)
	t.Setenv("HOME", "~")
	t.Setenv("USERPROFILE", "~")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	t.Setenv("XDG_CONFIG_HOME", "relative-xdg")
	t.Setenv("APPDATA", "relative-appdata")

	path, err := UserConfigYamlPath()
	if err == nil {
		t.Fatalf("UserConfigYamlPath() = %q, nil; want resolution error", path)
	}
	if path != "" {
		t.Fatalf("UserConfigYamlPath() returned unsafe path %q with error %v", path, err)
	}

	if got := GetUserYamlConfig("metrics.disabled"); got != "" {
		t.Fatalf("implicit read = %q, want absent value on resolution failure", got)
	}
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize should skip unsafe user roots, got: %v", err)
	}
	setErr := SetUserYamlConfig("metrics.disabled", "true")
	if setErr == nil {
		t.Fatal("SetUserYamlConfig returned nil error for unsafe roots")
	}
	for _, variable := range []string{"HOME", "USERPROFILE"} {
		if !strings.Contains(setErr.Error(), variable) {
			t.Errorf("SetUserYamlConfig error %q does not name %s", setErr, variable)
		}
	}
	if _, err := UnsetUserYamlConfig("metrics.disabled"); err == nil {
		t.Fatal("UnsetUserYamlConfig returned nil error for unsafe roots")
	}

	for _, relativeRoot := range []string{"~", "relative-xdg", "relative-appdata"} {
		if _, statErr := os.Stat(filepath.Join(sentinel, relativeRoot)); !os.IsNotExist(statErr) {
			t.Errorf("unsafe relative root %q was materialized: %v", relativeRoot, statErr)
		}
	}
}

func TestInitializeUserConfigPrecedence(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	restore := envSnapshot(t)
	defer restore()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	nativeRoot := filepath.Join(root, "native-config")
	projectRoot := filepath.Join(root, "project")
	projectConfig := filepath.Join(projectRoot, ".beads", "config.yaml")
	beadsConfig := filepath.Join(root, "explicit", ".beads", "config.yaml")

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", nativeRoot)
	t.Setenv("XDG_CONFIG_HOME", nativeRoot)
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "")

	candidates := currentUserConfigYamlCandidates()
	if candidates.legacy == "" || candidates.native == "" || candidates.documented == "" {
		t.Fatalf("test environment did not resolve all user config candidates: %#v", candidates)
	}
	if candidates.native == candidates.documented {
		t.Fatalf("test requires distinct native and documented paths, both were %q", candidates.native)
	}

	writeConfigActor(t, candidates.legacy, "legacy")
	writeConfigActor(t, candidates.native, "native")
	writeConfigActor(t, candidates.documented, "documented")
	writeConfigActor(t, projectConfig, "project")
	writeConfigActor(t, beadsConfig, "beads-dir")
	t.Chdir(projectRoot)
	t.Setenv("BEADS_DIR", filepath.Dir(beadsConfig))

	assertInitializedActor(t, "beads-dir")
	t.Setenv("BEADS_DIR", "")
	assertInitializedActor(t, "project")
	removeConfigPath(t, projectConfig)
	assertInitializedActor(t, "documented")
	removeConfigPath(t, candidates.documented)
	assertInitializedActor(t, "native")
	removeConfigPath(t, candidates.native)
	assertInitializedActor(t, "legacy")
}

func userConfigTestRoots(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	return filepath.Join(base, "home"), filepath.Join(base, "native")
}

func writeUserConfigPath(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("actor: test\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeConfigActor(t *testing.T, path, actor string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("actor: "+actor+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func removeConfigPath(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove %s: %v", path, err)
	}
}

func assertInitializedActor(t *testing.T, want string) {
	t.Helper()
	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := GetString("actor"); got != want {
		t.Fatalf("actor = %q, want %q", got, want)
	}
}

// isolateUserConfigCandidates isolates the user-config environment the way
// TestInitializeUserConfigPrecedence does: a fresh home, a native user-config
// root distinct from the documented location, cleared BD_*/BEADS_* variables,
// and a cwd with no .beads. It returns the resolved candidate paths.
func isolateUserConfigCandidates(t *testing.T) userConfigYamlCandidates {
	t.Helper()
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	restore := envSnapshot(t)
	t.Cleanup(restore)

	root := t.TempDir()
	home := filepath.Join(root, "home")
	nativeRoot := filepath.Join(root, "native-config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", nativeRoot)
	t.Setenv("XDG_CONFIG_HOME", nativeRoot)
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "")
	t.Chdir(root)

	candidates := currentUserConfigYamlCandidates()
	if candidates.legacy == "" || candidates.native == "" || candidates.documented == "" {
		t.Fatalf("test environment did not resolve all user config candidates: %#v", candidates)
	}
	if candidates.native == candidates.documented {
		t.Fatalf("test requires distinct native and documented paths, both were %q", candidates.native)
	}
	return candidates
}

func writeUserYamlConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// recordUserConfigPathStats replaces statUserConfigPath with a recorder that
// delegates to the real probe and counts calls per path. The original probe is
// restored via t.Cleanup.
func recordUserConfigPathStats(t *testing.T) map[string]int {
	t.Helper()
	probes := map[string]int{}
	saved := statUserConfigPath
	statUserConfigPath = func(name string) (os.FileInfo, error) {
		probes[name]++
		return saved(name)
	}
	t.Cleanup(func() { statUserConfigPath = saved })
	return probes
}

// GH#8113: with the documented <home>/.config/bd/config.yaml present, no
// user-config read path may probe the native os.UserConfigDir()/bd/config.yaml
// location (on macOS, touching that directory raises a per-launch
// "access data from other apps" permission prompt).
func TestUserConfigReadsNeverProbeNativeWhenDocumentedExists(t *testing.T) {
	candidates := isolateUserConfigCandidates(t)
	writeUserConfigPath(t, candidates.documented)
	writeUserConfigPath(t, candidates.native)

	t.Run("Initialize never probes native", func(t *testing.T) {
		probes := recordUserConfigPathStats(t)
		ResetForTesting()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if got := probes[candidates.native]; got != 0 {
			t.Errorf("Initialize probed native config %q %d times, want 0", candidates.native, got)
		}
		if got := probes[candidates.documented]; got == 0 {
			t.Errorf("Initialize never probed documented config %q", candidates.documented)
		}
	})

	t.Run("UserConfigYamlPath never probes native", func(t *testing.T) {
		probes := recordUserConfigPathStats(t)
		path, err := UserConfigYamlPath()
		if err != nil {
			t.Fatalf("UserConfigYamlPath: %v", err)
		}
		if path != candidates.documented {
			t.Errorf("UserConfigYamlPath() = %q, want documented %q", path, candidates.documented)
		}
		if got := probes[candidates.native]; got != 0 {
			t.Errorf("UserConfigYamlPath probed native config %q %d times, want 0", candidates.native, got)
		}
		if got := probes[candidates.documented]; got == 0 {
			t.Errorf("UserConfigYamlPath never probed documented config %q", candidates.documented)
		}
	})

	t.Run("GetUserYamlConfig never probes native", func(t *testing.T) {
		probes := recordUserConfigPathStats(t)
		if got := GetUserYamlConfig("actor"); got != "test" {
			t.Errorf("GetUserYamlConfig(actor) = %q, want %q from documented config", got, "test")
		}
		if got := probes[candidates.native]; got != 0 {
			t.Errorf("GetUserYamlConfig probed native config %q %d times, want 0", candidates.native, got)
		}
		if got := probes[candidates.documented]; got == 0 {
			t.Errorf("GetUserYamlConfig never probed documented config %q", candidates.documented)
		}
	})

	t.Run("empty documented file still shadows native", func(t *testing.T) {
		if err := os.WriteFile(candidates.documented, []byte{}, 0o600); err != nil {
			t.Fatalf("write %s: %v", candidates.documented, err)
		}
		probes := recordUserConfigPathStats(t)
		ResetForTesting()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if got := probes[candidates.native]; got != 0 {
			t.Errorf("Initialize probed native config %q %d times behind an empty documented file", candidates.native, got)
		}
		if got := GetString("actor"); got != "" {
			t.Errorf("actor = %q, want empty (empty documented file shadows native)", got)
		}
	})

	t.Run("coincident native and documented paths are probed once", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", filepath.Dir(filepath.Dir(candidates.documented)))
		coincident := currentUserConfigYamlCandidates()
		if coincident.native != coincident.documented {
			t.Skipf("os.UserConfigDir() does not follow XDG_CONFIG_HOME here: native %q, documented %q",
				coincident.native, coincident.documented)
		}
		writeUserConfigPath(t, coincident.documented)
		probes := recordUserConfigPathStats(t)
		ResetForTesting()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if got := probes[coincident.native]; got != 1 {
			t.Errorf("coincident user config probed %d times, want exactly 1", got)
		}
		if got := GetString("actor"); got != "test" {
			t.Errorf("actor = %q, want %q", got, "test")
		}
	})
}
