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
	assertInitializeUserConfigPrecedence(t)
}

// TestInitializeUserConfigPrecedenceDarwinResolver proves the darwin resolver
// keeps the GH#3532 merge order (legacy < native < documented, per key) when
// $XDG_CONFIG_HOME names a native root distinct from ~/.config.
func TestInitializeUserConfigPrecedenceDarwinResolver(t *testing.T) {
	t.Cleanup(SetUserConfigDirForTest(darwinUserConfigDirForTest(t)))
	assertInitializeUserConfigPrecedence(t)
}

func assertInitializeUserConfigPrecedence(t *testing.T) {
	t.Helper()
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

// darwinUserConfigDirForTest returns the darwin resolver for installation
// through SetUserConfigDirForTest. The stdlib fallback must never be reached
// on darwin; reaching it fails the test.
func darwinUserConfigDirForTest(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		return userConfigDirFor("darwin", os.Getenv, func() (string, error) {
			t.Errorf("darwin resolver consulted os.UserConfigDir")
			return "", errors.New("os.UserConfigDir must not be consulted on darwin")
		})
	}
}

// darwinUserConfigDirWithEnvForTest is darwinUserConfigDirForTest reading its
// environment from env instead of the process, so a test can point the real
// XDG_CONFIG_HOME (what os.UserConfigDir reads on Linux) somewhere else.
func darwinUserConfigDirWithEnvForTest(t *testing.T, env map[string]string) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		return userConfigDirFor("darwin", func(key string) string { return env[key] }, func() (string, error) {
			t.Errorf("darwin resolver consulted os.UserConfigDir")
			return "", errors.New("os.UserConfigDir must not be consulted on darwin")
		})
	}
}

// recordUserConfigStats routes every user-config candidate stat through a
// recorder and returns the recorded paths.
func recordUserConfigStats(t *testing.T) *[]string {
	t.Helper()
	var probes []string
	prev := statUserConfigPath
	statUserConfigPath = func(path string) (os.FileInfo, error) {
		probes = append(probes, path)
		return prev(path)
	}
	t.Cleanup(func() { statUserConfigPath = prev })
	return &probes
}

func TestUserConfigDirFor(t *testing.T) {
	abs := t.TempDir()
	home := t.TempDir()
	dotConfig := home + "/.config"
	stdlibErr := errors.New("stdlib resolver failure")

	type env map[string]string
	apple := []struct {
		name    string
		goos    string
		env     env
		want    string
		wantErr string
	}{
		{name: "darwin XDG absolute", goos: "darwin", env: env{"XDG_CONFIG_HOME": abs, "HOME": home}, want: abs},
		{name: "darwin XDG relative", goos: "darwin", env: env{"XDG_CONFIG_HOME": "relative-xdg", "HOME": home}, wantErr: "path in $XDG_CONFIG_HOME is relative"},
		{name: "darwin XDG unset", goos: "darwin", env: env{"HOME": home}, want: dotConfig},
		{name: "darwin XDG equals ~/.config", goos: "darwin", env: env{"XDG_CONFIG_HOME": dotConfig, "HOME": home}, want: dotConfig},
		{name: "darwin HOME empty", goos: "darwin", env: env{}, wantErr: "neither $XDG_CONFIG_HOME nor $HOME are defined"},
		{name: "darwin HOME tilde", goos: "darwin", env: env{"HOME": "~"}, want: "~/.config"},
		{name: "ios XDG unset", goos: "ios", env: env{"HOME": home}, want: dotConfig},
		{name: "ios XDG absolute", goos: "ios", env: env{"XDG_CONFIG_HOME": abs, "HOME": home}, want: abs},
	}
	for _, tt := range apple {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }
			stdlib := func() (string, error) {
				t.Errorf("%s consulted os.UserConfigDir", tt.goos)
				return "", stdlibErr
			}
			got, err := userConfigDirFor(tt.goos, getenv, stdlib)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr || got != "" {
					t.Fatalf("userConfigDirFor = %q, %v; want error %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("userConfigDirFor = %q, %v; want %q", got, err, tt.want)
			}
		})
	}

	for _, goos := range []string{"linux", "windows", "freebsd", "plan9", "android"} {
		t.Run(goos+" passes the stdlib value through", func(t *testing.T) {
			getenv := func(key string) string {
				t.Errorf("%s read %s from the environment", goos, key)
				return ""
			}
			got, err := userConfigDirFor(goos, getenv, func() (string, error) { return abs, nil })
			if err != nil || got != abs {
				t.Fatalf("userConfigDirFor = %q, %v; want %q", got, err, abs)
			}
		})
		t.Run(goos+" passes the stdlib error through", func(t *testing.T) {
			getenv := func(key string) string {
				t.Errorf("%s read %s from the environment", goos, key)
				return ""
			}
			got, err := userConfigDirFor(goos, getenv, func() (string, error) { return "", stdlibErr })
			if !errors.Is(err, stdlibErr) || err != stdlibErr || got != "" {
				t.Fatalf("userConfigDirFor = %q, %v; want stdlib error identity", got, err)
			}
		})
	}
}

// TestUserConfigDirMatchesStdlibOffDarwin runs the production resolver on the
// host. Off darwin and ios it must equal os.UserConfigDir in value and error
// text; on darwin and ios it must follow the Unix rule and differ from the
// stdlib's ~/Library/Application Support answer.
func TestUserConfigDirMatchesStdlibOffDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	for name, xdg := range map[string]string{
		"XDG absolute": filepath.Join(home, "xdg-config"),
		"XDG unset":    "",
		"XDG relative": "relative-xdg",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", xdg)
			got, err := UserConfigDir()
			stdlib, stdlibErr := os.UserConfigDir()
			switch runtime.GOOS {
			case "darwin", "ios":
				switch xdg {
				case "":
					if err != nil || got != home+"/.config" {
						t.Fatalf("UserConfigDir = %q, %v; want %q", got, err, home+"/.config")
					}
				case "relative-xdg":
					if err == nil || err.Error() != "path in $XDG_CONFIG_HOME is relative" {
						t.Fatalf("UserConfigDir = %q, %v; want relative-XDG error", got, err)
					}
					return
				default:
					if err != nil || got != xdg {
						t.Fatalf("UserConfigDir = %q, %v; want %q", got, err, xdg)
					}
				}
				if stdlibErr == nil && got == stdlib {
					t.Fatalf("UserConfigDir = os.UserConfigDir = %q on %s; want the Unix rule", got, runtime.GOOS)
				}
			default:
				if got != stdlib {
					t.Fatalf("UserConfigDir = %q, want os.UserConfigDir %q", got, stdlib)
				}
				if (err == nil) != (stdlibErr == nil) || (err != nil && err.Error() != stdlibErr.Error()) {
					t.Fatalf("UserConfigDir error = %v, want os.UserConfigDir error %v", err, stdlibErr)
				}
			}
		})
	}
}

func TestUserConfigYamlSearchPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Cleanup(SetUserConfigDirForTest(darwinUserConfigDirForTest(t)))
	probes := recordUserConfigStats(t)

	legacy := filepath.Join(home, ".beads", "config.yaml")
	documented := filepath.Join(home, ".config", "bd", "config.yaml")

	t.Run("darwin XDG unset dedups native into documented", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		got := UserConfigYamlSearchPaths()
		want := []string{legacy, documented}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("UserConfigYamlSearchPaths = %q, want %q", got, want)
		}
	})
	t.Run("darwin XDG set keeps three layers", func(t *testing.T) {
		xdg := filepath.Join(home, "xdg-config")
		t.Setenv("XDG_CONFIG_HOME", xdg)
		got := UserConfigYamlSearchPaths()
		want := []string{legacy, filepath.Join(xdg, "bd", "config.yaml"), documented}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("UserConfigYamlSearchPaths = %q, want %q", got, want)
		}
	})
	t.Run("darwin XDG relative skips the native layer", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "relative-xdg")
		got := UserConfigYamlSearchPaths()
		want := []string{legacy, documented}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("UserConfigYamlSearchPaths = %q, want %q", got, want)
		}
	})
	if len(*probes) != 0 {
		t.Fatalf("UserConfigYamlSearchPaths touched the filesystem: %q", *probes)
	}
}

// TestUserConfigNeverReadsMacOSApplicationSupport plants bd config under
// <home>/Library/Application Support and proves no user-config read or write
// probes it (GH#8113). The native subtests use the host resolver, so on macOS
// they exercise the real binary path. The darwin subtests install the darwin
// resolver with its own environment and point the real XDG_CONFIG_HOME and
// APPDATA at Application Support, so os.UserConfigDir answers Application
// Support on every host: a call site reverted to os.UserConfigDir fails here
// on Linux and Windows too, not only on the macOS legs.
func TestUserConfigNeverReadsMacOSApplicationSupport(t *testing.T) {
	const sentinelActor = "appsupport-sentinel"
	const sentinelNode = "appsupport-node"
	sentinel := "actor: " + sentinelActor + "\nnode_id: " + sentinelNode + "\nmetrics:\n  disabled: true\n"

	for _, tc := range []struct {
		name   string
		darwin bool
		xdg    bool
	}{
		{name: "native XDG set", xdg: true},
		{name: "native XDG unset"},
		{name: "darwin XDG set", darwin: true, xdg: true},
		{name: "darwin XDG unset", darwin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, plant := range []string{"sentinel file", "directory"} {
				t.Run(plant, func(t *testing.T) {
					ResetForTesting()
					t.Cleanup(ResetForTesting)
					restore := envSnapshot(t)
					t.Cleanup(restore)

					home := t.TempDir()
					t.Setenv("HOME", home)
					t.Setenv("USERPROFILE", home)
					t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
					xdg := ""
					if tc.xdg {
						xdg = filepath.Join(home, "xdg-config")
					}
					appSupport := filepath.Join(home, "Library", "Application Support")
					t.Setenv("XDG_CONFIG_HOME", xdg)
					if tc.darwin {
						t.Cleanup(SetUserConfigDirForTest(darwinUserConfigDirWithEnvForTest(t,
							map[string]string{"HOME": home, "XDG_CONFIG_HOME": xdg})))
						t.Setenv("XDG_CONFIG_HOME", appSupport)
						t.Setenv("APPDATA", appSupport)
						if dir, err := os.UserConfigDir(); err != nil || dir != appSupport {
							t.Fatalf("precondition: os.UserConfigDir() = %q, %v; want %q", dir, err, appSupport)
						}
						// The darwin resolver is Go's Unix branch verbatim, so it
						// joins with "/" on every host (as TestUserConfigDirFor
						// expects); filepath.Join would use "\" on Windows.
						wantNative := home + "/.config"
						if tc.xdg {
							wantNative = xdg
						}
						if dir, err := UserConfigDir(); err != nil || dir != wantNative {
							t.Fatalf("UserConfigDir() = %q, %v; want %q", dir, err, wantNative)
						}
					}
					t.Chdir(home)

					legacyAppSupport := filepath.Join(appSupport, "bd", "config.yaml")
					if plant == "directory" {
						if err := os.MkdirAll(legacyAppSupport, 0o755); err != nil {
							t.Fatalf("mkdir %s: %v", legacyAppSupport, err)
						}
					} else {
						writeUserConfigPath(t, legacyAppSupport)
						if err := os.WriteFile(legacyAppSupport, []byte(sentinel), 0o600); err != nil {
							t.Fatalf("write %s: %v", legacyAppSupport, err)
						}
					}
					probes := recordUserConfigStats(t)
					touchesAppSupport := func(path string) bool {
						return path == appSupport || strings.HasPrefix(path, appSupport+string(filepath.Separator))
					}

					for _, path := range UserConfigYamlSearchPaths() {
						if touchesAppSupport(path) {
							t.Fatalf("user config candidates include Application Support: %q", path)
						}
					}
					if err := Initialize(); err != nil {
						t.Fatalf("Initialize: %v", err)
					}
					if got := GetString("actor"); got == sentinelActor {
						t.Errorf("GetString(actor) read the Application Support sentinel")
					}
					if got := PrivateConfigValue("actor"); got == sentinelActor {
						t.Errorf("PrivateConfigValue(actor) read the Application Support sentinel")
					}
					if got := NodeID(); got == sentinelNode {
						t.Errorf("NodeID() read the Application Support sentinel")
					}
					if MetricsDisabledByUserConfig() {
						t.Errorf("MetricsDisabledByUserConfig() read the Application Support opt-out")
					}
					if got := GetUserYamlConfig("actor"); got == sentinelActor {
						t.Errorf("GetUserYamlConfig(actor) read the Application Support sentinel")
					}
					if got := UserConfigYamlDisplayPath(); touchesAppSupport(got) {
						t.Errorf("UserConfigYamlDisplayPath() = %q", got)
					}

					documented := filepath.Join(home, ".config", "bd", "config.yaml")
					if err := SetUserYamlConfig("actor", "written-by-test"); err != nil {
						t.Fatalf("SetUserYamlConfig: %v", err)
					}
					path, err := UserConfigYamlPath()
					if err != nil || path != documented {
						t.Fatalf("UserConfigYamlPath = %q, %v; want %q", path, err, documented)
					}
					data, err := os.ReadFile(documented)
					if err != nil || !strings.Contains(string(data), "written-by-test") {
						t.Fatalf("write did not land on %s: %v %q", documented, err, data)
					}

					if len(*probes) == 0 {
						t.Fatal("no user config stat was recorded; the probe seam is not wired")
					}
					for _, probe := range *probes {
						if touchesAppSupport(probe) {
							t.Errorf("user config probed Application Support: %q", probe)
						}
					}
					if plant == "directory" {
						if fi, err := os.Stat(legacyAppSupport); err != nil || !fi.IsDir() {
							t.Errorf("Application Support directory changed: %v", err)
						}
					} else if data, err := os.ReadFile(legacyAppSupport); err != nil || string(data) != sentinel {
						t.Errorf("Application Support file changed: %v %q", err, data)
					}
				})
			}
		})
	}
}

func TestLegacyDarwinUserConfigYamlPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	want := filepath.Join(home, "Library", "Application Support", "bd", "config.yaml")
	probes := recordUserConfigStats(t)
	got, err := LegacyDarwinUserConfigYamlPath()
	if err != nil || got != want {
		t.Fatalf("LegacyDarwinUserConfigYamlPath = %q, %v; want %q", got, err, want)
	}
	if got := LegacyDarwinUserConfigYamlPathFor(home); got != want {
		t.Fatalf("LegacyDarwinUserConfigYamlPathFor = %q, want %q", got, want)
	}
	if len(*probes) != 0 {
		t.Fatalf("computing the legacy path touched the filesystem: %q", *probes)
	}
	if _, err := os.Stat(filepath.Dir(want)); !os.IsNotExist(err) {
		t.Fatalf("computing the legacy path created %s: %v", filepath.Dir(want), err)
	}

	t.Setenv("HOME", "~")
	t.Setenv("USERPROFILE", "~")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	if got, err := LegacyDarwinUserConfigYamlPath(); err == nil || got != "" {
		t.Fatalf("LegacyDarwinUserConfigYamlPath with a relative home = %q, %v; want error", got, err)
	}
}

func TestReadLegacyDarwinUserConfigYaml(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	if _, err := ReadLegacyDarwinUserConfigYaml(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: err = %v, want fs.ErrNotExist", err)
	}
	_, err := ReadLegacyDarwinUserConfigYaml(dir)
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != dir || pathErr.Err.Error() != "not a regular file" {
		t.Fatalf("directory: err = %v, want a *fs.PathError for %s: not a regular file", err, dir)
	}
	big := filepath.Join(dir, "big.yaml")
	if err := os.WriteFile(big, make([]byte, LegacyDarwinUserConfigMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLegacyDarwinUserConfigYaml(big); !errors.As(err, &pathErr) || pathErr.Path != big || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("oversized file: err = %v, want a *fs.PathError size error", err)
	}
	ok := filepath.Join(dir, "ok.yaml")
	if err := os.WriteFile(ok, []byte("node_id: n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadLegacyDarwinUserConfigYaml(ok); err != nil || string(data) != "node_id: n1\n" {
		t.Fatalf("regular file: %q, %v", data, err)
	}
}

func TestYamlValueFromBytes(t *testing.T) {
	for _, tc := range []struct {
		name, data, key, want string
		present, wantErr      bool
	}{
		{name: "nested", data: "metrics:\n  disabled: true\n", key: "metrics.disabled", want: "true", present: true},
		{name: "flat wins", data: "metrics.disabled: false\nmetrics:\n  disabled: true\n", key: "metrics.disabled", want: "false", present: true},
		{name: "absent", data: "actor: a\n", key: "metrics.disabled"},
		{name: "null", data: "metrics:\n  disabled:\n", key: "metrics.disabled"},
		{name: "empty", data: "", key: "metrics.disabled"},
		{name: "malformed", data: "metrics: [unclosed\n", key: "metrics.disabled", wantErr: true},
		{name: "not a mapping", data: "- a\n- b\n", key: "metrics.disabled", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, present, err := YamlValueFromBytes([]byte(tc.data), tc.key)
			if (err != nil) != tc.wantErr || got != tc.want || present != tc.present {
				t.Fatalf("YamlValueFromBytes = %q, %v, %v; want %q, %v, err=%v", got, present, err, tc.want, tc.present, tc.wantErr)
			}
		})
	}
	for raw, want := range map[string][2]bool{"true": {true, true}, " FALSE ": {false, true}, "1": {true, true}, "yes": {false, false}} {
		if v, ok := ParseUserConfigBool(raw); v != want[0] || ok != want[1] {
			t.Errorf("ParseUserConfigBool(%q) = %v, %v; want %v, %v", raw, v, ok, want[0], want[1])
		}
	}
}

func TestConfigYamlReplaceable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ConfigYamlReplaceable(path); err != nil {
		t.Fatalf("writable config: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("ConfigYamlReplaceable left files behind: %v", entries)
	}
	if err := ConfigYamlReplaceable(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing config: want error")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		t.Fatalf("precondition: %s is still writable (running as root?)", path)
	}
	if err := ConfigYamlReplaceable(path); err == nil {
		t.Fatal("read-only config: want error")
	}
}

// TestDarwinXDGUserConfigReadsAndWritesXDGFile: on macOS with XDG_CONFIG_HOME
// set, an existing $XDG_CONFIG_HOME/bd/config.yaml and no
// ~/.config/bd/config.yaml, bd reads that file and writes back to it, as on
// Linux (GH#8113 risk 4).
func TestDarwinXDGUserConfigReadsAndWritesXDGFile(t *testing.T) {
	ResetForTesting()
	t.Cleanup(ResetForTesting)
	t.Cleanup(envSnapshot(t))

	home := t.TempDir()
	xdg := filepath.Join(home, "xdg-config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// The real environment points elsewhere, so only the darwin resolver's
	// own XDG_CONFIG_HOME can find the file.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "Library", "Application Support"))
	t.Setenv("APPDATA", filepath.Join(home, "Library", "Application Support"))
	t.Cleanup(SetUserConfigDirForTest(darwinUserConfigDirWithEnvForTest(t,
		map[string]string{"HOME": home, "XDG_CONFIG_HOME": xdg})))
	t.Chdir(home)

	xdgFile := filepath.Join(xdg, "bd", "config.yaml")
	writeUserConfigPath(t, xdgFile)
	if err := os.WriteFile(xdgFile, []byte("actor: xdg-actor\nnode_id: xdg-node\nmetrics:\n  disabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	documented := filepath.Join(home, ".config", "bd", "config.yaml")

	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := GetString("actor"); got != "xdg-actor" {
		t.Errorf("GetString(actor) = %q, want the XDG file's value", got)
	}
	if got := NodeID(); got != "xdg-node" {
		t.Errorf("NodeID() = %q, want the XDG file's value", got)
	}
	if !MetricsDisabledByUserConfig() {
		t.Error("MetricsDisabledByUserConfig() = false, want the XDG file's opt-out")
	}
	if got := GetUserYamlConfig("actor"); got != "xdg-actor" {
		t.Errorf("GetUserYamlConfig(actor) = %q, want the XDG file's value", got)
	}
	if path, err := UserConfigYamlPath(); err != nil || path != xdgFile {
		t.Fatalf("UserConfigYamlPath() = %q, %v; want %q", path, err, xdgFile)
	}

	if err := SetUserYamlConfig("node_id", "written-node"); err != nil {
		t.Fatalf("SetUserYamlConfig: %v", err)
	}
	if data, err := os.ReadFile(xdgFile); err != nil || !strings.Contains(string(data), "written-node") {
		t.Fatalf("write did not land on %s: %v %q", xdgFile, err, data)
	}
	if _, err := os.Stat(documented); !os.IsNotExist(err) {
		t.Fatalf("write created %s: %v", documented, err)
	}
	if got := GetUserYamlConfig("node_id"); got != "written-node" {
		t.Errorf("GetUserYamlConfig(node_id) after write = %q", got)
	}
}

func TestIsMacOSFamily(t *testing.T) {
	for goos, want := range map[string]bool{"darwin": true, "ios": true, "linux": false, "windows": false, "freebsd": false, "": false} {
		if got := IsMacOSFamily(goos); got != want {
			t.Errorf("IsMacOSFamily(%q) = %v, want %v", goos, got, want)
		}
	}
}

// TestUserConfigSetsBoolKey pins the macOS post-bootstrap metrics gate's
// question: does the user config bd reads hold a valid bool for the key?
func TestUserConfigSetsBoolKey(t *testing.T) {
	for name, tc := range map[string]struct {
		body string // "" writes no file
		want bool
	}{
		"no file":          {},
		"nested true":      {body: "metrics:\n  disabled: true\n", want: true},
		"nested false":     {body: "metrics:\n  disabled: false\n", want: true},
		"flat quoted":      {body: "metrics.disabled: \"FALSE\"\n", want: true},
		"not a bool":       {body: "metrics:\n  disabled: maybe\n"},
		"null":             {body: "metrics:\n  disabled:\n"},
		"absent":           {body: "actor: alice\n"},
		"commented out":    {body: "# metrics:\n#   disabled: true\n"},
		"malformed":        {body: "metrics: [unclosed\n"},
		"not a mapping":    {body: "- metrics\n"},
		"other key is set": {body: "metrics:\n  endpoint: https://x.example\n"},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Cleanup(SetUserConfigDirForTest(func() (string, error) { return filepath.Join(home, ".config"), nil }))
			if tc.body != "" {
				path := filepath.Join(home, ".config", "bd", "config.yaml")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := UserConfigSetsBoolKey("metrics.disabled"); got != tc.want {
				t.Fatalf("UserConfigSetsBoolKey = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckYamlConfigUpdates: the in-memory check refuses exactly the
// documents SetUserYamlConfig's writer refuses, ApplyYamlConfigUpdates
// returns the document that writer leaves after one write per update, and
// neither touches a file.
func TestCheckYamlConfigUpdates(t *testing.T) {
	metricsKeys := []YamlKeyValue{
		{Key: "metrics.disabled", Value: "false"},
		{Key: "metrics.endpoint", Value: "https://example.invalid/x"},
	}
	for _, tc := range []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "empty file", content: ""},
		{name: "comments only", content: "# just a comment\n"},
		{name: "unrelated keys", content: "actor: alice\n"},
		{name: "metrics section without disabled", content: "metrics:\n  notice_shown: true\n"},
		{name: "empty metrics section", content: "metrics:\n"},
		{name: "metrics is false", content: "metrics: false\n", wantErr: "already holds a value"},
		{name: "metrics is off", content: "metrics: off\n", wantErr: "already holds a value"},
		{name: "metrics is a list", content: "metrics:\n  - x\n", wantErr: "already holds a value"},
		{name: "top-level list", content: "- a\n- b\n", wantErr: "not a mapping"},
		{name: "top-level scalar", content: "hello\n", wantErr: "not a mapping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckYamlConfigUpdates([]byte(tc.content), metricsKeys...)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckYamlConfigUpdates: %v", err)
				}
				applied, err := ApplyYamlConfigUpdates([]byte(tc.content), metricsKeys...)
				if err != nil {
					t.Fatalf("ApplyYamlConfigUpdates: %v", err)
				}
				path := filepath.Join(t.TempDir(), "config.yaml")
				if werr := os.WriteFile(path, []byte(tc.content), 0o600); werr != nil {
					t.Fatal(werr)
				}
				for _, u := range metricsKeys {
					if werr := setYamlConfigAtPath(path, u.Key, u.Value); werr != nil {
						t.Fatalf("setYamlConfigAtPath(%s): %v", u.Key, werr)
					}
				}
				if written, rerr := os.ReadFile(path); rerr != nil || string(written) != string(applied) {
					t.Fatalf("ApplyYamlConfigUpdates = %q, want what one write per key leaves, %q (%v)", applied, written, rerr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CheckYamlConfigUpdates err = %v, want %q", err, tc.wantErr)
			}
			// The check agrees with the real write.
			path := filepath.Join(t.TempDir(), "config.yaml")
			if werr := os.WriteFile(path, []byte(tc.content), 0o600); werr != nil {
				t.Fatal(werr)
			}
			if werr := setYamlConfigAtPath(path, metricsKeys[0].Key, metricsKeys[0].Value); werr == nil {
				t.Fatal("setYamlConfigAtPath succeeded where the check failed")
			}
		})
	}
	t.Run("validation runs", func(t *testing.T) {
		if err := CheckYamlConfigUpdates(nil, YamlKeyValue{Key: "hierarchy.max-depth", Value: "0"}); err == nil {
			t.Fatal("want the writer's value validation to reject hierarchy.max-depth=0")
		}
	})
}
