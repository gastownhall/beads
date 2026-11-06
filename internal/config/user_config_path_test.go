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

// isolateUserConfigRoots points HOME and the native config root at distinct
// temp directories, clears BD_/BEADS_ overrides, and runs from an empty CWD so
// only the files a test writes can be loaded. It returns the resolved
// candidates and the temp root.
func isolateUserConfigRoots(t *testing.T) (userConfigYamlCandidates, string) {
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
	cwd := filepath.Join(root, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", cwd, err)
	}
	t.Chdir(cwd)

	candidates := currentUserConfigYamlCandidates()
	if candidates.legacy == "" || candidates.native == "" || candidates.documented == "" {
		t.Fatalf("test environment did not resolve all user config candidates: %#v", candidates)
	}
	if candidates.native == candidates.documented {
		t.Fatalf("test requires distinct native and documented paths, both were %q", candidates.native)
	}
	return candidates, root
}

func writeConfigBody(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// recordUserConfigStats replaces the user config existence probe with one
// that records every path it is asked about. Tests using it must not run in
// parallel.
func recordUserConfigStats(t *testing.T) *[]string {
	t.Helper()
	var probed []string
	saved := userConfigStat
	userConfigStat = func(path string) (os.FileInfo, error) {
		probed = append(probed, path)
		return saved(path)
	}
	t.Cleanup(func() { userConfigStat = saved })
	return &probed
}

func countProbes(probed []string, path string) int {
	n := 0
	for _, p := range probed {
		if p == path {
			n++
		}
	}
	return n
}

// GH#8113: on macOS the native os.UserConfigDir()/bd/config.yaml lives under
// ~/Library/Application Support, and stat'ing it raises an app-data privacy
// prompt. When the documented <home>/.config/bd/config.yaml exists the loader
// must not probe the native candidate at all, whether or not a file is there.
func TestInitializeDoesNotProbeNativeUserConfigWhenDocumentedExists(t *testing.T) {
	loaders := map[string]func() error{
		"Initialize": Initialize,
		"WithBeadsDir": func() error {
			return WithBeadsDir("", func() {})
		},
	}
	for _, nativePresent := range []bool{true, false} {
		for name, load := range loaders {
			label := name + "/native absent"
			if nativePresent {
				label = name + "/native present"
			}
			t.Run(label, func(t *testing.T) {
				candidates, _ := isolateUserConfigRoots(t)
				writeConfigActor(t, candidates.documented, "documented")
				if nativePresent {
					writeConfigActor(t, candidates.native, "native")
				}
				probed := recordUserConfigStats(t)

				if err := load(); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if n := countProbes(*probed, candidates.native); n != 0 {
					t.Fatalf("%s probed native %s %d time(s) although documented %s exists; probes: %q",
						name, candidates.native, n, candidates.documented, *probed)
				}
				if countProbes(*probed, candidates.documented) == 0 {
					t.Fatalf("%s never probed documented %s; probes: %q", name, candidates.documented, *probed)
				}
				for _, p := range *probed {
					if !filepath.IsAbs(p) {
						t.Fatalf("%s probed non-absolute path %q", name, p)
					}
				}
			})
		}
	}
}

// A malformed native file must not break loading when the documented file
// shadows it: the loader no longer reads (or stats) native at all. A
// malformed or empty documented file still shadows native rather than
// falling back to it.
func TestInitializeIgnoresInvalidNativeWhenDocumentedExists(t *testing.T) {
	t.Run("valid documented, malformed native", func(t *testing.T) {
		candidates, _ := isolateUserConfigRoots(t)
		writeConfigActor(t, candidates.documented, "documented")
		writeConfigBody(t, candidates.native, "actor: [unterminated\n")
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize with malformed shadowed native: %v", err)
		}
		if got := GetString("actor"); got != "documented" {
			t.Fatalf("actor = %q, want %q", got, "documented")
		}
	})
	t.Run("malformed documented does not fall back to native", func(t *testing.T) {
		candidates, _ := isolateUserConfigRoots(t)
		writeConfigBody(t, candidates.documented, "actor: [unterminated\n")
		writeConfigActor(t, candidates.native, "native")
		probed := recordUserConfigStats(t)
		if err := Initialize(); err == nil {
			t.Fatalf("Initialize with malformed documented succeeded (actor %q); want a read error", GetString("actor"))
		}
		if n := countProbes(*probed, candidates.native); n != 0 {
			t.Fatalf("Initialize probed native %d time(s) after documented failed to parse", n)
		}
	})
	t.Run("empty documented still shadows native", func(t *testing.T) {
		candidates, _ := isolateUserConfigRoots(t)
		writeConfigBody(t, candidates.documented, "")
		writeConfigActor(t, candidates.native, "native")
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if got := GetString("actor"); got == "native" {
			t.Fatalf("actor = %q: empty documented %s did not shadow native", got, candidates.documented)
		}
	})
}

func TestExistingUserConfigYamlPath(t *testing.T) {
	t.Run("equal native and documented paths are probed once", func(t *testing.T) {
		home, _ := userConfigTestRoots(t)
		candidates := buildUserConfigYamlCandidates(home, nil, filepath.Join(home, ".config"), nil)
		if candidates.native != candidates.documented {
			t.Fatalf("test requires equal paths: %#v", candidates)
		}
		probed := recordUserConfigStats(t)
		if got := existingUserConfigYamlPath(candidates); got != "" {
			t.Fatalf("path = %q, want none for a missing file", got)
		}
		if n := countProbes(*probed, candidates.documented); n != 1 {
			t.Fatalf("probed %s %d time(s), want 1", candidates.documented, n)
		}
		writeUserConfigPath(t, candidates.documented)
		if got := existingUserConfigYamlPath(candidates); got != candidates.documented {
			t.Fatalf("path = %q, want %q", got, candidates.documented)
		}
	})

	t.Run("empty candidates are never probed", func(t *testing.T) {
		probed := recordUserConfigStats(t)
		if got := existingUserConfigYamlPath(userConfigYamlCandidates{}); got != "" {
			t.Fatalf("path = %q, want none", got)
		}
		if len(*probed) != 0 {
			t.Fatalf("probed %q for empty candidates", *probed)
		}
	})

	t.Run("documented stat error falls back to native", func(t *testing.T) {
		home, native := userConfigTestRoots(t)
		candidates := buildUserConfigYamlCandidates(home, nil, native, nil)
		writeUserConfigPath(t, candidates.documented)
		writeUserConfigPath(t, candidates.native)
		saved := userConfigStat
		userConfigStat = func(path string) (os.FileInfo, error) {
			if path == candidates.documented {
				return nil, os.ErrPermission
			}
			return saved(path)
		}
		t.Cleanup(func() { userConfigStat = saved })
		if got := existingUserConfigYamlPath(candidates); got != candidates.native {
			t.Fatalf("path = %q, want native %q when documented cannot be stat'ed", got, candidates.native)
		}
	})
}

// GH#8113: with both modern user config files present, only the documented
// file applies. A key set only in the shadowed native file reaches neither
// the effective config nor the private (user-confirmed) values, while the
// legacy file still merges beneath the documented one.
func TestInitializeIgnoresNativeUserConfigWhenDocumentedExists(t *testing.T) {
	candidates, _ := isolateUserConfigRoots(t)
	writeConfigBody(t, candidates.documented, "actor: documented\n")
	writeConfigBody(t, candidates.native, "actor: native\ngit.author: native-only\n")
	writeConfigBody(t, candidates.legacy, "git.no-gpg-sign: true\n")

	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := GetString("actor"); got != "documented" {
		t.Fatalf("actor = %q, want %q", got, "documented")
	}
	if got := GetString("git.author"); got != "" {
		t.Fatalf("git.author = %q: native %s was merged although documented %s exists", got, candidates.native, candidates.documented)
	}
	if got := PrivateConfigValue("git.author"); got != "" {
		t.Fatalf("PrivateConfigValue(git.author) = %q: shadowed native file still confirms private values", got)
	}
	if !GetBool("git.no-gpg-sign") {
		t.Fatalf("git.no-gpg-sign = false, want the legacy %s value to still merge", candidates.legacy)
	}
}
