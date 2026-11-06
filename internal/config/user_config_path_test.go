package config

import (
	"errors"
	"io/fs"
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
	writeUserConfigFile(t, path, "actor: test\n")
}

func writeUserConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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

// isolateUserConfigEnvironment resets package state, clears BD_/BEADS_
// overrides, and points HOME and the native user-config directory at fresh
// temp directories so the native and documented candidates are distinct,
// with the CWD outside any beads workspace. It returns the resolved
// candidates for the test to populate.
func isolateUserConfigEnvironment(t *testing.T) userConfigYamlCandidates {
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
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
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

// probeUserConfigStats swaps the user-config stat seam for a recorder while
// load runs, returning every candidate path the loader probed. Tests that
// change the seam must not run in parallel.
func probeUserConfigStats(t *testing.T, load func() error) []string {
	t.Helper()
	var probed []string
	saved := userConfigStat
	userConfigStat = func(name string) (fs.FileInfo, error) {
		probed = append(probed, name)
		return saved(name)
	}
	defer func() { userConfigStat = saved }()
	if err := load(); err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	return probed
}

// assertNoNativeProbe checks one loader's recorded probes: no probe of the
// native candidate, at least one probe of the documented candidate, and
// absolute paths only.
func assertNoNativeProbe(t *testing.T, probed []string, candidates userConfigYamlCandidates) {
	t.Helper()
	documentedProbe := false
	for _, path := range probed {
		if !filepath.IsAbs(path) {
			t.Errorf("loader probed non-absolute user config path %q", path)
		}
		switch path {
		case candidates.native:
			t.Errorf("loader probed native candidate %s although documented %s exists", candidates.native, candidates.documented)
		case candidates.documented:
			documentedProbe = true
		}
	}
	if !documentedProbe {
		t.Errorf("loader never probed documented candidate %s; probed %v", candidates.documented, probed)
	}
}

// GH#8113, AC-1: with the documented <home>/.config/bd/config.yaml present,
// loading configuration must make zero existence probes of the native
// os.UserConfigDir()/bd/config.yaml candidate — on macOS 26 that stat raises
// the app-data privacy prompt on every launch — whether or not a file sits
// at the native path, and for both Initialize and WithBeadsDir.
func TestInitializeDoesNotProbeNativeUserConfigWhenDocumentedExists(t *testing.T) {
	for _, tc := range []struct {
		name       string
		nativeFile bool
	}{
		{name: "native file present", nativeFile: true},
		{name: "native file absent", nativeFile: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidates := isolateUserConfigEnvironment(t)
			writeUserConfigFile(t, candidates.documented, "mail.delegate: documented\n")
			if tc.nativeFile {
				writeUserConfigFile(t, candidates.native, "git.author: native-only\n")
			}

			assertNoNativeProbe(t, probeUserConfigStats(t, Initialize), candidates)

			root, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			assertNoNativeProbe(t, probeUserConfigStats(t, func() error {
				return WithBeadsDir(filepath.Join(root, "beads"), func() {})
			}), candidates)
		})
	}
}

// GH#8113, AC-2: with both files present, the documented file is the only
// modern user config merged: its actor wins, a native-only key no longer
// reaches the effective config or counts as a private source, and the legacy
// layer still merges.
func TestInitializeIgnoresNativeUserConfigWhenDocumentedExists(t *testing.T) {
	candidates := isolateUserConfigEnvironment(t)
	writeUserConfigFile(t, candidates.legacy, "git:\n  no-gpg-sign: true\n")
	writeUserConfigFile(t, candidates.native, "actor: native\ngit:\n  author: native-only\n")
	writeUserConfigFile(t, candidates.documented, "actor: documented\n")

	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := GetString("actor"); got != "documented" {
		t.Errorf("actor = %q, want the documented file's actor", got)
	}
	if got := GetString("git.author"); got != "" {
		t.Errorf("git.author = %q, want empty: a native-only key must not reach the effective config", got)
	}
	if got := PrivateConfigValue("git.author"); got != "" {
		t.Errorf("PrivateConfigValue(git.author) = %q, want empty: the native file must not stay a private source", got)
	}
	if !GetBool("git.no-gpg-sign") {
		t.Error("git.no-gpg-sign = false, want true from the legacy layer")
	}
}

// GH#8113 edge cases: a malformed or empty documented file still shadows the
// native candidate — no fallback on parse failure — while a malformed native
// file is neither read nor fatal when the documented file is valid.
func TestInitializeIgnoresInvalidNativeWhenDocumentedExists(t *testing.T) {
	t.Run("malformed native is ignored while documented is valid", func(t *testing.T) {
		candidates := isolateUserConfigEnvironment(t)
		writeUserConfigFile(t, candidates.native, "actor: [unclosed\n")
		writeUserConfigFile(t, candidates.documented, "actor: documented\n")

		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v (the malformed native file must not be read)", err)
		}
		if got := GetString("actor"); got != "documented" {
			t.Errorf("actor = %q, want the documented file's actor", got)
		}
	})

	t.Run("malformed documented still fails without native fallback", func(t *testing.T) {
		candidates := isolateUserConfigEnvironment(t)
		writeUserConfigFile(t, candidates.native, "actor: native\n")
		writeUserConfigFile(t, candidates.documented, "actor: [unclosed\n")

		if err := Initialize(); err == nil {
			t.Fatal("Initialize succeeded with a malformed documented file; want a read error, not a native fallback")
		}
	})

	t.Run("empty documented still shadows native", func(t *testing.T) {
		candidates := isolateUserConfigEnvironment(t)
		writeUserConfigFile(t, candidates.native, "actor: native\n")
		writeUserConfigFile(t, candidates.documented, "")

		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if got := GetString("actor"); got != "" {
			t.Errorf("actor = %q, want empty: the empty documented file must still shadow the native file", got)
		}
	})
}

// GH#8113, AC-3: when the documented file is absent, the native file is
// still the modern user config: it overrides legacy, project config
// overrides it, its private values remain available, the public resolver
// names it, and the loader still probes it.
func TestInitializeReadsNativeUserConfigWhenDocumentedAbsent(t *testing.T) {
	candidates := isolateUserConfigEnvironment(t)
	writeUserConfigFile(t, candidates.legacy, "actor: legacy\n")
	writeUserConfigFile(t, candidates.native, "actor: native\n")

	probed := probeUserConfigStats(t, Initialize)
	if got := GetString("actor"); got != "native" {
		t.Errorf("actor = %q, want the native file's actor over legacy", got)
	}
	if got := PrivateConfigValue("actor"); got != "native" {
		t.Errorf("PrivateConfigValue(actor) = %q, want the native value as a private source", got)
	}
	nativeProbe := false
	for _, path := range probed {
		if path == candidates.native {
			nativeProbe = true
		}
	}
	if !nativeProbe {
		t.Errorf("loader never probed native candidate %s; probed %v", candidates.native, probed)
	}

	projectRoot := t.TempDir()
	writeUserConfigFile(t, filepath.Join(projectRoot, ".beads", "config.yaml"), "actor: project\n")
	t.Chdir(projectRoot)
	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := GetString("actor"); got != "project" {
		t.Errorf("actor = %q, want the project config's actor over native and legacy", got)
	}

	path, err := UserConfigYamlPath()
	if err != nil {
		t.Fatalf("UserConfigYamlPath: %v", err)
	}
	if path != candidates.native {
		t.Errorf("UserConfigYamlPath() = %q, want the native file %q", path, candidates.native)
	}
}

// GH#8113: when the documented <home>/.config/bd/config.yaml exists,
// Initialize must not probe the native os.UserConfigDir()/bd/config.yaml (on
// macOS that stat raises an app-data privacy prompt on every launch). A native
// file is read only after that stat finds it, so a native-only key reaching
// the merged config proves the probe happened.
func TestInitializeSkipsNativeUserConfigWhenDocumentedExists(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	restore := envSnapshot(t)
	defer restore()

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
	if candidates.native == "" || candidates.documented == "" || candidates.native == candidates.documented {
		t.Fatalf("test requires distinct native and documented paths: %#v", candidates)
	}
	for path, body := range map[string]string{
		candidates.documented: "mail.delegate: documented\n",
		candidates.native:     "git.author: native-only\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := GetString("mail.delegate"); got != "documented" {
		t.Fatalf("mail.delegate = %q, want the documented file's value", got)
	}
	if got := GetString("git.author"); got != "" {
		t.Fatalf("git.author = %q: Initialize probed and read native %s although documented %s exists", got, candidates.native, candidates.documented)
	}
}
