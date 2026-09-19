package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
	if err := SetUserYamlConfig("metrics.disabled", "true"); err == nil {
		t.Fatal("SetUserYamlConfig returned nil error for unsafe roots")
	}
	if err := UnsetUserYamlConfig("metrics.disabled"); err == nil {
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
