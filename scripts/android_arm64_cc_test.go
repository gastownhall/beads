package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAndroidArm64CCWrappersResolveNDKClang pins the CC/CXX contract of the
// bd-android-arm64 goreleaser build (#3538): the wrappers exec the pinned
// NDK's API-21 clang/clang++ with the caller's arguments, resolve it from
// BEADS_ANDROID_NDK_HOME or by running scripts/ci/install-android-ndk.sh,
// refuse to run anywhere but Linux x86_64, and never read ANDROID_NDK_HOME
// (the runner image's default there is NDK 27, whose linker emits 4 KB ELF
// alignment -- a binary Android 15 devices cannot load).
func TestAndroidArm64CCWrappersResolveNDKClang(t *testing.T) {
	const clangName = "aarch64-linux-android21-clang"
	const clangxxName = "aarch64-linux-android21-clang++"
	// The fake clang logs "$0 $*" so the test can tell which tree's compiler
	// ran, not just that one ran.
	fakeClang := `#!/bin/sh
set -eu
printf '%s %s\n' "$0" "$*" >>"$BEADS_TEST_CC_LOG"
exit "${BEADS_TEST_CC_EXIT:-0}"
`
	fakeInstaller := `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$BEADS_TEST_INSTALLER_ARGS"
printf 'BEADS_ANDROID_NDK_HOME=%s\n' "$BEADS_TEST_FAKE_INSTALL_ROOT"
`
	fakeUname := `#!/bin/sh
set -eu
case "${1:-}" in
  -m) printf '%s\n' "${BEADS_TEST_UNAME_ARCH:-x86_64}" ;;
  *) printf '%s\n' "${BEADS_TEST_UNAME_OS:-Linux}" ;;
esac
`

	// Each case runs once per wrapper (cc execs clang, cxx execs clang++).
	tests := []struct {
		name string
		// envRoot sets BEADS_ANDROID_NDK_HOME for the case; nil leaves the
		// variable unset.
		envRoot      func(t *testing.T, bash, envTree string, pathEnv, env []string) []string
		androidHome  bool // populate ANDROID_NDK_HOME with a usable tree
		installer    bool // place a fake install-android-ndk.sh next to the wrapper
		unameOS      string
		wantExecRoot string // empty: nothing may be exec'd
		wantExit     int
		ccExit       string
	}{
		{
			name:         "env root clang execs with original arguments",
			envRoot:      envRootInstalled,
			wantExecRoot: "env",
			wantExit:     0,
		},
		{
			name:         "env root clang exit code propagates",
			envRoot:      envRootInstalled,
			wantExecRoot: "env",
			wantExit:     7,
			ccExit:       "7",
		},
		{
			name:         "unset env root installs the pinned NDK",
			envRoot:      nil,
			installer:    true,
			wantExecRoot: "install",
			wantExit:     0,
		},
		{
			// BEADS_ANDROID_NDK_HOME set but its clang missing: fall through
			// to the installer rather than exec-ing something else.
			name:         "env root without clang falls back to installer",
			envRoot:      envRootEmpty,
			installer:    true,
			wantExecRoot: "install",
			wantExit:     0,
		},
		{
			name:     "non-Linux host refuses without exec",
			envRoot:  envRootInstalled,
			unameOS:  "Darwin",
			wantExit: 1,
		},
		{
			// ANDROID_NDK_HOME holds a perfectly usable clang; the wrapper
			// must ignore it and use the installer's root instead.
			name:         "android ndk home is never read",
			envRoot:      nil,
			androidHome:  true,
			installer:    true,
			wantExecRoot: "install",
			wantExit:     0,
		},
	}

	for _, wrapper := range []struct {
		file     string
		toolName string
	}{
		{"android-arm64-cc", clangName},
		{"android-arm64-cxx", clangxxName},
	} {
		for _, test := range tests {
			t.Run(wrapper.file+"/"+test.name, func(t *testing.T) {
				bash := requireHostTool(t, "bash")
				root := sourceRepoRoot(t)

				bin := t.TempDir()
				stateDir := t.TempDir()
				ccLog := filepath.Join(stateDir, "cc-calls")
				installerArgs := filepath.Join(stateDir, "installer-args")
				for _, path := range []string{ccLog, installerArgs} {
					if err := os.WriteFile(path, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}

				// Trees: the one named by BEADS_ANDROID_NDK_HOME ("env"),
				// the one the fake installer prints ("install"), and the
				// ANDROID_NDK_HOME decoy (same shape as "env").
				writeNDKTree := func(rootPath string) string {
					dir := filepath.Join(rootPath, "toolchains", "llvm", "prebuilt", "linux-x86_64", "bin")
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
					writeExecutable(t, filepath.Join(dir, clangName), fakeClang)
					writeExecutable(t, filepath.Join(dir, clangxxName), fakeClang)
					return rootPath
				}
				envTree := writeNDKTree(filepath.Join(t.TempDir(), "ndk-env"))
				installTree := writeNDKTree(filepath.Join(t.TempDir(), "ndk-install"))
				androidHomeTree := writeNDKTree(filepath.Join(t.TempDir(), "ndk-android-home"))

				// The wrapper under test is a copy, so the fake installer
				// can sit beside it without touching the repo.
				wrapperDir := t.TempDir()
				wrapperSource, err := os.ReadFile(filepath.Join(root, "scripts", "ci", wrapper.file))
				if err != nil {
					t.Fatalf("read scripts/ci/%s: %v", wrapper.file, err)
				}
				wrapperPath := filepath.Join(wrapperDir, wrapper.file)
				writeExecutable(t, wrapperPath, string(wrapperSource))
				if wrapper.file == "android-arm64-cxx" {
					// android-arm64-cxx delegates to its cc sibling.
					ccSource, err := os.ReadFile(filepath.Join(root, "scripts", "ci", "android-arm64-cc"))
					if err != nil {
						t.Fatalf("read scripts/ci/android-arm64-cc: %v", err)
					}
					writeExecutable(t, filepath.Join(wrapperDir, "android-arm64-cc"), string(ccSource))
				}
				if test.installer {
					writeExecutable(t, filepath.Join(wrapperDir, "install-android-ndk.sh"), fakeInstaller)
				}

				writeExecutable(t, filepath.Join(bin, "uname"), fakeUname)

				pathEnv := shellPathEnv()
				binPath := shellPathUnderEnv(t, bash, bin, pathEnv)
				statePath := shellPathUnderEnv(t, bash, stateDir, pathEnv)
				commandPath := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
				if runtime.GOOS == "windows" {
					commandPath = binPath + ":/usr/bin:/bin"
				}
				env := []string{
					"PATH=" + os.Getenv("PATH"),
					"BEADS_TEST_COMMAND_PATH=" + commandPath,
					"LC_ALL=C",
					"LANG=C",
					"BASH_ENV=",
					"ENV=",
					"BEADS_TEST_CC_LOG=" + statePath + "/cc-calls",
					"BEADS_TEST_INSTALLER_ARGS=" + statePath + "/installer-args",
					"BEADS_TEST_FAKE_INSTALL_ROOT=" + shellPathUnderEnv(t, bash, installTree, pathEnv),
					"BEADS_TEST_UNAME_OS=" + test.unameOS,
					"BEADS_TEST_CC_EXIT=" + test.ccExit,
				}
				if test.envRoot != nil {
					env = test.envRoot(t, bash, envTree, pathEnv, env)
				}
				if test.androidHome {
					env = append(env, "ANDROID_NDK_HOME="+shellPathUnderEnv(t, bash, androidHomeTree, pathEnv))
				}
				requireShellCommandPath(t, bash, root, env, "uname", binPath+"/uname")

				// bashScriptCommand execs only "$1"; this wrapper must also
				// receive the compiler arguments it has to forward.
				cmd := exec.Command(bash, "--noprofile", "--norc", "-c",
					`PATH="$BEADS_TEST_COMMAND_PATH"; export PATH; exec "$BASH" --noprofile --norc "$@"`,
					"script-test", shellPathUnderEnv(t, bash, wrapperPath, pathEnv),
					"-c", "foo.c", "-o", "foo.o")
				cmd.Dir = root
				cmd.Env = env
				output, runErr := cmd.CombinedOutput()
				if got := exitCode(runErr); got != test.wantExit {
					t.Fatalf("exit = %d, want %d; err=%v\n%s", got, test.wantExit, runErr, output)
				}

				calls := readCallLines(t, ccLog)
				installCalls := readCallLines(t, installerArgs)
				if test.wantExecRoot == "" {
					if len(calls) != 0 {
						t.Fatalf("clang was exec'd on a refusing host: %q\n%s", calls, output)
					}
					if len(installCalls) != 0 {
						t.Fatalf("installer ran on a refusing host: %q\n%s", installCalls, output)
					}
					if !strings.Contains(string(output), "Linux x86_64") {
						t.Fatalf("refusal does not name the Linux x86_64 restriction:\n%s", output)
					}
					return
				}
				wantTree := envTree
				if test.wantExecRoot == "install" {
					wantTree = installTree
					if len(installCalls) == 0 {
						t.Fatalf("installer was not invoked\n%s", output)
					}
				} else if len(installCalls) != 0 {
					t.Fatalf("installer ran although BEADS_ANDROID_NDK_HOME was usable: %q\n%s", installCalls, output)
				}
				wantTool := filepath.Join(wantTree, "toolchains", "llvm", "prebuilt", "linux-x86_64", "bin", wrapper.toolName)
				want := wantTool + " -c foo.c -o foo.o"
				if len(calls) != 1 || calls[0] != want {
					t.Fatalf("clang calls = %q, want %q (ANDROID_NDK_HOME decoy: %q)\n%s", calls, want, androidHomeTree, output)
				}
			})
		}
	}
}

// envRootInstalled points BEADS_ANDROID_NDK_HOME at a usable tree.
func envRootInstalled(t *testing.T, bash, envTree string, pathEnv, env []string) []string {
	t.Helper()
	return append(env, "BEADS_ANDROID_NDK_HOME="+shellPathUnderEnv(t, bash, envTree, pathEnv))
}

// envRootEmpty sets BEADS_ANDROID_NDK_HOME to a directory with no clang in
// it, so only the installer path can succeed.
func envRootEmpty(t *testing.T, bash, envTree string, pathEnv, env []string) []string {
	t.Helper()
	empty := t.TempDir()
	return append(env, "BEADS_ANDROID_NDK_HOME="+shellPathUnderEnv(t, bash, empty, pathEnv))
}

func TestAndroidArm64CCWrappersAreExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve Unix executable bits")
	}
	for _, name := range []string{"android-arm64-cc", "android-arm64-cxx"} {
		path := filepath.Join(sourceRepoRoot(t), "scripts", "ci", name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable", path)
		}
	}
}
