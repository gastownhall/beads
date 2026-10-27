package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// androidCCArgs is a compiler command line with the argv shapes a wrapper
// can mangle: a space inside an argument and an empty argument.
var androidCCArgs = []string{"-c", "has space.c", "", "-o", "out.o"}

// TestAndroidArm64CompilerWrappers pins scripts/ci/android-arm64-cc and
// -cxx, the CC/CXX of .goreleaser.yml's bd-android-arm64 build (#3538). They
// exec the pinned NDK's API-21 clang/clang++ with the original argv, from
// BEADS_ANDROID_NDK_HOME or else from whatever install-android-ndk.sh prints;
// they refuse non-Linux-x86_64 hosts without running a compiler; and they
// never use ANDROID_NDK_HOME (the runner image's 4 KB-aligned NDK 27).
func TestAndroidArm64CompilerWrappers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the android NDK build is Linux x86_64 only")
	}
	for _, wrapper := range []struct{ name, clang string }{
		{"android-arm64-cc", "aarch64-linux-android21-clang"},
		{"android-arm64-cxx", "aarch64-linux-android21-clang++"},
	} {
		t.Run(wrapper.name, func(t *testing.T) {
			t.Run("BEADS_ANDROID_NDK_HOME has the compiler", func(t *testing.T) {
				h := newAndroidCCHarness(t, wrapper.name)
				run := h.run(t, map[string]string{"BEADS_ANDROID_NDK_HOME": h.ndk(t, "beads-home")})
				run.wantExec(t, "beads-home", wrapper.clang)
				run.wantInstalls(t)
			})
			t.Run("unset runs the installer in RUNNER_TEMP", func(t *testing.T) {
				h := newAndroidCCHarness(t, wrapper.name)
				runnerTemp := t.TempDir()
				run := h.run(t, map[string]string{"RUNNER_TEMP": runnerTemp, "GITHUB_ENV": filepath.Join(t.TempDir(), "github-env")})
				run.wantExec(t, "installed", wrapper.clang)
				// GITHUB_ENV is cleared: one export line per compile is noise.
				run.wantInstalls(t, runnerTemp+" GITHUB_ENV=")
			})
			t.Run("unset without RUNNER_TEMP installs under HOME", func(t *testing.T) {
				h := newAndroidCCHarness(t, wrapper.name)
				home := t.TempDir()
				run := h.run(t, map[string]string{"HOME": home})
				run.wantExec(t, "installed", wrapper.clang)
				run.wantInstalls(t, home+"/.cache/beads-ndk GITHUB_ENV=")
			})
			t.Run("BEADS_ANDROID_NDK_HOME without the compiler runs the installer", func(t *testing.T) {
				h := newAndroidCCHarness(t, wrapper.name)
				runnerTemp := t.TempDir()
				run := h.run(t, map[string]string{"BEADS_ANDROID_NDK_HOME": t.TempDir(), "RUNNER_TEMP": runnerTemp})
				run.wantExec(t, "installed", wrapper.clang)
				run.wantInstalls(t, runnerTemp+" GITHUB_ENV=")
			})
			t.Run("ANDROID_NDK_HOME is never used", func(t *testing.T) {
				h := newAndroidCCHarness(t, wrapper.name)
				runnerTemp := t.TempDir()
				run := h.run(t, map[string]string{"ANDROID_NDK_HOME": h.ndk(t, "image-ndk"), "RUNNER_TEMP": runnerTemp})
				run.wantExec(t, "installed", wrapper.clang)
				run.wantInstalls(t, runnerTemp+" GITHUB_ENV=")
			})
			t.Run("installer failure", func(t *testing.T) {
				h := newAndroidCCHarness(t, wrapper.name)
				run := h.run(t, map[string]string{"RUNNER_TEMP": t.TempDir(), "FAKE_NDK_INSTALL_FAIL": "1"})
				if got := exitCode(run.err); got != 1 {
					t.Fatalf("exit = %d, want 1\n%s", got, run.stderr)
				}
				if !strings.Contains(run.stderr, "fake installer failed") {
					t.Fatalf("stderr does not show the installer's error:\n%s", run.stderr)
				}
				if len(run.compilerCalls) != 0 {
					t.Fatalf("compiler ran after the installer failed: %q", run.compilerCalls)
				}
			})
			for _, host := range [][2]string{{"Darwin", "arm64"}, {"Darwin", "x86_64"}, {"Linux", "aarch64"}} {
				t.Run("host "+host[0]+"/"+host[1], func(t *testing.T) {
					h := newAndroidCCHarness(t, wrapper.name)
					run := h.run(t, map[string]string{
						"BEADS_ANDROID_NDK_HOME": h.ndk(t, "beads-home"),
						"FAKE_UNAME_S":           host[0],
						"FAKE_UNAME_M":           host[1],
					})
					if got := exitCode(run.err); got != 1 {
						t.Fatalf("exit = %d, want 1\n%s", got, run.stderr)
					}
					want := wrapper.name + ": the android CGO build (goreleaser id bd-android-arm64) is Linux x86_64 only"
					if !strings.Contains(run.stderr, want) {
						t.Fatalf("stderr does not contain %q:\n%s", want, run.stderr)
					}
					if len(run.compilerCalls) != 0 {
						t.Fatalf("compiler ran on %s/%s: %q", host[0], host[1], run.compilerCalls)
					}
					run.wantInstalls(t)
				})
			}
			// The repository's own installer, not a fake: with the pinned NDK
			// under $ANDROID_HOME/ndk (as on ubuntu-24.04) the wrapper compiles
			// with that tree and downloads nothing.
			t.Run("real installer uses the pinned NDK under ANDROID_HOME", func(t *testing.T) {
				h := newAndroidCCHarness(t, wrapper.name)
				androidHome := t.TempDir()
				pinned := filepath.Join(androidHome, "ndk", pinnedAndroidNDKRevision)
				writeFakeAndroidNDK(t, pinned, pinnedAndroidNDKRevision)
				h.writeLoggingCompilers(t, pinned, "android-home")
				run := h.runScript(t, filepath.Join(sourceRepoRoot(t), "scripts", "ci", wrapper.name), map[string]string{
					"ANDROID_HOME": androidHome,
					"RUNNER_TEMP":  t.TempDir(),
				})
				run.wantExec(t, "android-home", wrapper.clang)
				if run.curlCalls != "" {
					t.Fatalf("installer downloaded with the pinned NDK preinstalled: %q", run.curlCalls)
				}
			})
		})
	}
}

// TestAndroidArm64CompilerWrappersNeverReadAndroidNDKHome backs the behavioral
// check with the source: no expansion of ANDROID_NDK_HOME in either wrapper or
// their shared body.
func TestAndroidArm64CompilerWrappersNeverReadAndroidNDKHome(t *testing.T) {
	expansion := regexp.MustCompile(`\$\{?ANDROID_NDK_HOME\b`)
	for _, rel := range []string{"android-arm64-cc", "android-arm64-cxx", "lib/android-ndk-clang.sh"} {
		data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "ci", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if loc := expansion.FindIndex(data); loc != nil {
			t.Errorf("scripts/ci/%s expands ANDROID_NDK_HOME: %q", rel, data[loc[0]:loc[1]])
		}
		if rel != "lib/android-ndk-clang.sh" {
			info, err := os.Stat(filepath.Join(sourceRepoRoot(t), "scripts", "ci", rel))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o111 == 0 {
				t.Errorf("scripts/ci/%s is not executable", rel)
			}
		}
	}
}

type androidCCHarness struct {
	wrapper string // the wrapper's path in a copy of scripts/ci
	bin     string // fake uname and curl
	state   string
}

// newAndroidCCHarness copies the wrapper and its shared body into a scratch
// scripts/ci beside a fake install-android-ndk.sh that logs its argument and
// prints a fake NDK whose compilers log how they were run.
func newAndroidCCHarness(t *testing.T, name string) *androidCCHarness {
	t.Helper()
	h := &androidCCHarness{bin: t.TempDir(), state: t.TempDir()}
	ci := filepath.Join(t.TempDir(), "ci")
	if err := os.MkdirAll(filepath.Join(ci, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{name, "lib/android-ndk-clang.sh"} {
		data, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), "scripts", "ci", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		writeExecutable(t, filepath.Join(ci, filepath.FromSlash(rel)), string(data))
	}
	h.wrapper = filepath.Join(ci, name)
	installed := h.ndk(t, "installed")
	writeExecutable(t, filepath.Join(ci, "install-android-ndk.sh"), `#!/bin/sh
printf '%s GITHUB_ENV=%s\n' "$*" "${GITHUB_ENV-<unset>}" >>"$FAKE_STATE/installs"
echo "fake installer progress" >&2
if [ -n "${FAKE_NDK_INSTALL_FAIL:-}" ]; then
  echo "fake installer failed" >&2
  exit 7
fi
printf 'BEADS_ANDROID_NDK_HOME=%s\n' "`+installed+`"
`)
	writeExecutable(t, filepath.Join(h.bin, "uname"), `#!/bin/sh
case "${1:-}" in
  -m) printf '%s\n' "${FAKE_UNAME_M:-x86_64}" ;;
  *) printf '%s\n' "${FAKE_UNAME_S:-Linux}" ;;
esac
`)
	writeExecutable(t, filepath.Join(h.bin, "curl"), `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_STATE/curl"
exit 42
`)
	return h
}

// ndk returns a fake NDK root, labeled so a compiler call names its tree.
func (h *androidCCHarness) ndk(t *testing.T, label string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), label)
	h.writeLoggingCompilers(t, root, label)
	return root
}

func (h *androidCCHarness) writeLoggingCompilers(t *testing.T, root, label string) {
	t.Helper()
	bin := filepath.Join(root, "toolchains", "llvm", "prebuilt", "linux-x86_64", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"aarch64-linux-android21-clang", "aarch64-linux-android21-clang++"} {
		writeExecutable(t, filepath.Join(bin, name), `#!/bin/sh
{
  printf '%s %s\n' "`+label+`" "${0##*/}"
  for arg in "$@"; do
    printf '[%s]\n' "$arg"
  done
} >>"$FAKE_STATE/compiler"
`)
	}
}

type androidCCRun struct {
	stderr        string
	compilerCalls []string
	installCalls  []string
	curlCalls     string
	err           error
}

func (h *androidCCHarness) run(t *testing.T, env map[string]string) androidCCRun {
	t.Helper()
	return h.runScript(t, h.wrapper, env)
}

func (h *androidCCHarness) runScript(t *testing.T, script string, extra map[string]string) androidCCRun {
	t.Helper()
	bash := requireHostTool(t, "bash")
	env := []string{
		"PATH=" + h.bin + ":" + os.Getenv("PATH") + ":/usr/bin:/bin",
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"HOME=" + t.TempDir(),
		"FAKE_STATE=" + h.state,
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	cmd := exec.Command(bash, append([]string{"--noprofile", "--norc", script}, androidCCArgs...)...)
	cmd.Dir = t.TempDir()
	cmd.Env = env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	read := func(name string) string {
		data, readErr := os.ReadFile(filepath.Join(h.state, name))
		if readErr != nil && !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		return strings.TrimSuffix(string(data), "\n")
	}
	run := androidCCRun{stderr: stderr.String(), curlCalls: read("curl"), err: err}
	if c := read("compiler"); c != "" {
		run.compilerCalls = strings.Split(c, "\n")
	}
	if i := read("installs"); i != "" {
		run.installCalls = strings.Split(i, "\n")
	}
	return run
}

// wantExec checks the run exec'd label's compiler once with androidCCArgs.
func (r androidCCRun) wantExec(t *testing.T, label, clang string) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("wrapper failed: %v\n%s", r.err, r.stderr)
	}
	want := []string{label + " " + clang}
	for _, arg := range androidCCArgs {
		want = append(want, "["+arg+"]")
	}
	if !reflect.DeepEqual(r.compilerCalls, want) {
		t.Fatalf("compiler calls = %q, want %q", r.compilerCalls, want)
	}
	if strings.Contains(r.stderr, "fake installer progress") {
		t.Fatalf("a successful install's progress reached the compiler's stderr:\n%s", r.stderr)
	}
}

func (r androidCCRun) wantInstalls(t *testing.T, want ...string) {
	t.Helper()
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(r.installCalls, want) {
		t.Fatalf("installer calls = %q, want %q", r.installCalls, want)
	}
}
