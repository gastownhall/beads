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

// newDistinctUserConfigFixture isolates HOME, the native user config root and
// the working directory, and returns user config candidates whose native and
// documented paths differ.
func newDistinctUserConfigFixture(t *testing.T) (userConfigYamlCandidates, string) {
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
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)

	candidates := currentUserConfigYamlCandidates()
	if candidates.legacy == "" || candidates.native == "" || candidates.documented == "" {
		t.Fatalf("test environment did not resolve all user config candidates: %#v", candidates)
	}
	if candidates.native == candidates.documented {
		t.Fatalf("test requires distinct native and documented paths, both were %q", candidates.native)
	}
	return candidates, root
}

// recordUserConfigStats counts every user config existence probe by path while
// still answering from the real filesystem.
func recordUserConfigStats(t *testing.T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	saved := statUserConfigPath
	statUserConfigPath = func(path string) (os.FileInfo, error) {
		counts[filepath.Clean(path)]++
		return os.Stat(path)
	}
	t.Cleanup(func() { statUserConfigPath = saved })
	return counts
}

func writeConfigYAML(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// GH#8113: on macOS touching os.UserConfigDir() raises a privacy prompt, so no
// user config read may probe the native file while the documented one exists.
func TestUserConfigReadsNeverProbeNativeWhenDocumentedExists(t *testing.T) {
	candidates, _ := newDistinctUserConfigFixture(t)
	writeConfigYAML(t, candidates.native, "actor: native\nmail:\n  delegate: native-delegate\n")
	writeConfigYAML(t, candidates.documented, "actor: documented\nmail:\n  delegate: documented-delegate\n")
	counts := recordUserConfigStats(t)

	reads := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"Initialize", func(t *testing.T) {
			if err := Initialize(); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			if got := GetString("actor"); got != "documented" {
				t.Fatalf("actor = %q, want documented", got)
			}
		}},
		{"UserConfigYamlPath", func(t *testing.T) {
			got, err := UserConfigYamlPath()
			if err != nil || got != candidates.documented {
				t.Fatalf("UserConfigYamlPath() = %q, %v; want %q", got, err, candidates.documented)
			}
		}},
		{"GetUserYamlConfig", func(t *testing.T) {
			if got := GetUserYamlConfig("mail.delegate"); got != "documented-delegate" {
				t.Fatalf("GetUserYamlConfig(mail.delegate) = %q, want documented-delegate", got)
			}
		}},
	}
	for _, read := range reads {
		t.Run(read.name, func(t *testing.T) {
			clear(counts)
			read.run(t)
			if counts[candidates.documented] == 0 {
				t.Errorf("documented %s was never probed: %v", candidates.documented, counts)
			}
			if n := counts[candidates.native]; n != 0 {
				t.Errorf("native %s probed %d time(s) although documented exists", candidates.native, n)
			}
		})
	}

	t.Run("coincident native and documented paths are probed once", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", filepath.Dir(filepath.Dir(candidates.documented)))
		coincident := currentUserConfigYamlCandidates()
		if coincident.native != coincident.documented {
			t.Skipf("os.UserConfigDir() does not follow XDG_CONFIG_HOME here: native %q, documented %q",
				coincident.native, coincident.documented)
		}
		clear(counts)
		ResetForTesting()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if n := counts[coincident.documented]; n != 1 {
			t.Errorf("coincident user config %s probed %d time(s), want exactly 1", coincident.documented, n)
		}
		if got := GetString("actor"); got != "documented" {
			t.Errorf("actor = %q, want documented", got)
		}
	})

	t.Run("empty documented file still shadows native", func(t *testing.T) {
		writeConfigYAML(t, candidates.documented, "")
		clear(counts)
		ResetForTesting()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		if n := counts[candidates.native]; n != 0 {
			t.Errorf("native %s probed %d time(s) behind an empty documented file", candidates.native, n)
		}
		if got := GetString("actor"); got != "" {
			t.Errorf("actor = %q, want empty (an empty documented file shadows native)", got)
		}
	})
}

// TestNativeConfigPath_NotProbedWhenDocumentedExists is a regression test for
// GH#8113. The native os.UserConfigDir()/bd/config.yaml location is replaced
// with a directory as a trap: os.Stat on a directory succeeds, so if Initialize
// still merged it, viper would fail to read it as YAML and Initialize would
// return an error. A passing Initialize proves the native path was never read
// or merged; TestUserConfigReadsNeverProbeNativeWhenDocumentedExists proves it
// was never stat'd either.
func TestNativeConfigPath_NotProbedWhenDocumentedExists(t *testing.T) {
	candidates, _ := newDistinctUserConfigFixture(t)
	writeConfigYAML(t, candidates.documented, "actor: documented-user\n")
	if err := os.MkdirAll(candidates.native, 0o755); err != nil {
		t.Fatalf("failed to create trap directory at native config path: %v", err)
	}

	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() returned error: %v (native config.yaml path was merged even though the documented path exists)", err)
	}

	if got := GetString("actor"); got != "documented-user" {
		t.Errorf("GetString(actor) = %q, want %q (from documented ~/.config/bd/config.yaml)", got, "documented-user")
	}
}

// Native is a fallback, not a layer: while the documented file exists, keys
// only the native file defines stop applying, effective or private, while the
// legacy file still merges beneath documented.
func TestInitializeIgnoresNativeUserConfigWhenDocumentedExists(t *testing.T) {
	candidates, _ := newDistinctUserConfigFixture(t)
	writeConfigYAML(t, candidates.legacy, "actor: legacy\n")
	writeConfigYAML(t, candidates.native,
		"actor: native\ngithub:\n  url: https://native.example\nnode_id: native-node\n")
	writeConfigYAML(t, candidates.documented, "jira:\n  url: https://documented.example\n")

	initialize := func() {
		t.Helper()
		ResetForTesting()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}
	assertNativeIgnored := func(wantActor string) {
		t.Helper()
		if got := GetString("actor"); got != wantActor {
			t.Errorf("actor = %q, want %q", got, wantActor)
		}
		if got := PrivateConfigValue("actor"); got != wantActor {
			t.Errorf("private actor = %q, want %q", got, wantActor)
		}
		for _, key := range []string{"github.url", "node_id"} {
			if got := GetString(key); got != "" {
				t.Errorf("native-only %s = %q, want unset", key, got)
			}
			if got := PrivateConfigValue(key); got != "" {
				t.Errorf("private native-only %s = %q, want unset", key, got)
			}
		}
		if got := NodeID(); got != "" {
			t.Errorf("NodeID() = %q, want unset", got)
		}
	}

	initialize()
	assertNativeIgnored("legacy")
	if got := GetString("jira.url"); got != "https://documented.example" {
		t.Errorf("jira.url = %q, want the documented value", got)
	}
	if got := PrivateConfigValue("jira.url"); got != "https://documented.example" {
		t.Errorf("private jira.url = %q, want the documented value", got)
	}

	writeConfigYAML(t, candidates.documented, "actor: documented\n")
	initialize()
	assertNativeIgnored("documented")

	// An empty documented file still exists, so it still shadows native.
	writeConfigYAML(t, candidates.documented, "")
	initialize()
	assertNativeIgnored("legacy")
}

// Without the documented file, native still applies above legacy and below
// project and BEADS_DIR config, and direct user reads resolve to it.
func TestInitializeFallsBackToNativeUserConfigWhenDocumentedMissing(t *testing.T) {
	candidates, root := newDistinctUserConfigFixture(t)
	writeConfigYAML(t, candidates.legacy, "actor: legacy\njira:\n  url: https://legacy.example\n")
	writeConfigYAML(t, candidates.native,
		"actor: native\ngithub:\n  url: https://native.example\nnode_id: native-node\n")
	counts := recordUserConfigStats(t)

	initialize := func() {
		t.Helper()
		ResetForTesting()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	}
	assertNativeMerged := func(wantActor string) {
		t.Helper()
		if got := GetString("actor"); got != wantActor {
			t.Errorf("actor = %q, want %q", got, wantActor)
		}
		if got := PrivateConfigValue("actor"); got != "native" {
			t.Errorf("private actor = %q, want native", got)
		}
		for key, want := range map[string]string{
			"github.url": "https://native.example",
			"node_id":    "native-node",
			"jira.url":   "https://legacy.example",
		} {
			if got := GetString(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
			if got := PrivateConfigValue(key); got != want {
				t.Errorf("private %s = %q, want %q", key, got, want)
			}
		}
		if got := NodeID(); got != "native-node" {
			t.Errorf("NodeID() = %q, want native-node", got)
		}
	}

	initialize()
	assertNativeMerged("native")
	if counts[candidates.native] == 0 {
		t.Errorf("native %s was never probed although documented is absent", candidates.native)
	}
	if got, err := UserConfigYamlPath(); err != nil || got != candidates.native {
		t.Errorf("UserConfigYamlPath() = %q, %v; want native %q", got, err, candidates.native)
	}

	projectRoot := filepath.Join(root, "project")
	writeConfigActor(t, filepath.Join(projectRoot, ".beads", "config.yaml"), "project")
	t.Chdir(projectRoot)
	initialize()
	assertNativeMerged("project")

	beadsConfig := filepath.Join(root, "explicit", ".beads", "config.yaml")
	writeConfigActor(t, beadsConfig, "beads-dir")
	t.Setenv("BEADS_DIR", filepath.Dir(beadsConfig))
	initialize()
	assertNativeMerged("beads-dir")

	// With neither user file present, both are probed and nothing applies.
	t.Setenv("BEADS_DIR", "")
	t.Chdir(filepath.Join(root, "work"))
	removeConfigPath(t, candidates.legacy)
	removeConfigPath(t, candidates.native)
	clear(counts)
	initialize()
	if counts[candidates.documented] == 0 || counts[candidates.native] == 0 {
		t.Errorf("absent user files were not both probed: %v", counts)
	}
	if got := GetString("github.url"); got != "" {
		t.Errorf("github.url = %q with no user config, want unset", got)
	}
}

// TestNativeConfigPath_FallbackWhenDocumentedMissing verifies the native
// os.UserConfigDir()/bd/config.yaml location still loads as a fallback when
// the documented ~/.config/bd/config.yaml does not exist. This is the
// existing-behavior half of the GH#8113 regression test: the fix must only
// skip the native probe when the documented file is present, never remove the
// fallback outright.
func TestNativeConfigPath_FallbackWhenDocumentedMissing(t *testing.T) {
	candidates, _ := newDistinctUserConfigFixture(t)
	// No documented config is created: the documented path is absent.
	writeConfigYAML(t, candidates.native, "actor: native-fallback-user\n")

	ResetForTesting()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize() returned error: %v", err)
	}

	if got := GetString("actor"); got != "native-fallback-user" {
		t.Errorf("GetString(actor) = %q, want %q (from native os.UserConfigDir() fallback)", got, "native-fallback-user")
	}
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
