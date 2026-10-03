package scripts_test

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	testScriptFakeGoLogEnv      = "BEADS_TEST_SCRIPT_FAKE_GO_LOG"
	testScriptExpectedBinaryEnv = "BEADS_TEST_SCRIPT_EXPECTED_BINARY"
	testScriptExpectedBaseEnv   = "BEADS_TEST_SCRIPT_EXPECTED_BASENAME"
	testScriptDriverEnv         = "BEADS_TEST_SCRIPT_DRIVER"
	testScriptNativeSuffixEnv   = "BEADS_TEST_SCRIPT_NATIVE_SUFFIX"
	testScriptLaunchProbeEnv    = "BEADS_TEST_SCRIPT_LAUNCH_PROBE"
)

const testScriptFakeGo = `#!/usr/bin/env bash
set -euo pipefail

record() {
    printf '%s\n' "$1" >>"$BEADS_TEST_SCRIPT_FAKE_GO_LOG"
}

case "${1:-}" in
    env)
        record env
        if [[ $# -ne 2 || "$2" != "GOEXE" ]]; then
            printf 'fake go: unsupported env arguments: %s\n' "$*" >&2
            exit 90
        fi
        printf '%s\n' "$BEADS_TEST_SCRIPT_NATIVE_SUFFIX"
        ;;
    build)
        record build
        shift
        output=""
        while [[ $# -gt 0 ]]; do
            if [[ "$1" == "-o" ]]; then
                if [[ $# -lt 2 ]]; then
                    printf 'fake go: -o is missing its output\n' >&2
                    exit 90
                fi
                output="$2"
                shift 2
            else
                shift
            fi
        done
        if [[ -z "$output" || "$output" != "$BEADS_TEST_SCRIPT_EXPECTED_BINARY" ]]; then
            printf 'fake go: build output %q, want %q\n' "$output" "$BEADS_TEST_SCRIPT_EXPECTED_BINARY" >&2
            exit 90
        fi
        cp -f -- "$BEADS_TEST_SCRIPT_DRIVER" "$output"
        chmod +x "$output"
        ;;
    test)
        record test
        "$BEADS_TEST_SCRIPT_DRIVER" \
            -test.run '^TestTestScriptPrebuiltBinaryLaunchProbe$' \
            -test.count=1
        ;;
    *)
        printf 'fake go: unsupported command: %s\n' "$*" >&2
        exit 90
        ;;
esac
`

func TestTestScriptPrebuiltBinaryContract(t *testing.T) {
	t.Run("generated path uses the native executable suffix and launches", func(t *testing.T) {
		commands := runTestScriptWithFakeGo(t, "")
		assertFakeGoCommands(t, commands, "env", "build", "test")
	})

	t.Run("caller supplied binary wins without a build", func(t *testing.T) {
		fixtureRoot := filepath.Join(t.TempDir(), "caller override with spaces")
		if err := os.MkdirAll(fixtureRoot, 0o755); err != nil {
			t.Fatalf("create caller fixture root: %v", err)
		}
		callerBinary := filepath.Join(fixtureRoot, "caller supplied bd"+nativeExecutableSuffix())
		copyCurrentTestExecutable(t, callerBinary)

		commands := runTestScriptWithFakeGo(t, callerBinary)
		assertFakeGoCommands(t, commands, "test")
	})
}

// TestTestScriptPrebuiltBinaryLaunchProbe is selected only by the fake go test
// process above. Keeping the os/exec probe in a normal test avoids claiming the
// package-wide TestMain authority needed by other script-selection contracts.
func TestTestScriptPrebuiltBinaryLaunchProbe(t *testing.T) {
	if os.Getenv(testScriptLaunchProbeEnv) != "1" {
		t.Skip("re-exec probe runs only under the test.sh fake-go driver")
	}

	prebuilt := os.Getenv("BEADS_TEST_BD_BINARY")
	expected := os.Getenv(testScriptExpectedBinaryEnv)
	if prebuilt == "" || expected == "" || !sameTestScriptFile(prebuilt, expected) {
		t.Fatalf("exported prebuilt binary %q is not expected file %q", prebuilt, expected)
	}
	if want := os.Getenv(testScriptExpectedBaseEnv); filepath.Base(prebuilt) != want {
		t.Fatalf("exported prebuilt basename = %q, want %q", filepath.Base(prebuilt), want)
	}

	command := exec.Command(prebuilt, "-test.run=^$")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("launch exported prebuilt binary through os/exec: %v\n%s", err, output)
	}
}

func runTestScriptWithFakeGo(t *testing.T, callerBinary string) []string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "test script root with spaces")
	fakeBin := filepath.Join(root, "fake go bin")
	testEnvRoot := filepath.Join(root, "isolated test environment")
	tempRoot := filepath.Join(root, "temporary files")
	for _, path := range []string{fakeBin, testEnvRoot, tempRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create fixture directory %s: %v", path, err)
		}
	}

	fakeGo := filepath.Join(fakeBin, "go")
	if err := os.WriteFile(fakeGo, []byte(testScriptFakeGo), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}
	callLog := filepath.Join(root, "fake go calls")
	if err := os.WriteFile(callLog, nil, 0o600); err != nil {
		t.Fatalf("initialize fake-go call log: %v", err)
	}

	expected := callerBinary
	if expected == "" {
		expected = filepath.Join(testEnvRoot, "prebuilt-bd", "bd"+nativeExecutableSuffix())
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is required to exercise scripts/test.sh: %v", err)
	}
	repoRoot := sourceRepoRoot(t)
	env := testScriptEnvironment(testEnvRoot, tempRoot, expected, callerBinary)
	fakeBinShellPath := shellPathUnderEnv(t, bash, fakeBin, env)
	fakeGoShellPath := shellPathUnderEnv(t, bash, fakeGo, env)
	driverShellPath := shellPathUnderEnv(t, bash, currentTestExecutable(t), env)
	callLogShellPath := shellPathUnderEnv(t, bash, callLog, env)
	env = append(env,
		"BEADS_TEST_COMMAND_PATH="+fakeBinShellPath+":/usr/bin:/bin",
		testScriptDriverEnv+"="+driverShellPath,
		testScriptFakeGoLogEnv+"="+callLogShellPath,
	)

	cmd := exec.Command(
		bash,
		"--noprofile",
		"--norc",
		"-c",
		`PATH="$BEADS_TEST_COMMAND_PATH"; export PATH; exec "$BASH" --noprofile --norc "$1" "$2"`,
		"test-script",
		shellPathUnderEnv(t, bash, filepath.Join(repoRoot, "scripts", "test.sh"), env),
		"./cmd/bd",
	)
	cmd.Dir = repoRoot
	cmd.Env = env
	requireShellCommandPath(t, bash, repoRoot, env, "go", fakeGoShellPath)
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("scripts/test.sh failed: %v\n%s", runErr, output)
	}

	content, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read fake-go call log: %v", err)
	}
	return strings.Fields(string(content))
}

func testScriptEnvironment(testEnvRoot string, tempRoot string, expected string, callerBinary string) []string {
	home := filepath.Join(testEnvRoot, "home")
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + portableTestScriptPath(home),
		"USERPROFILE=" + portableTestScriptPath(home),
		"TMPDIR=" + portableTestScriptPath(tempRoot),
		"TEMP=" + portableTestScriptPath(tempRoot),
		"TMP=" + portableTestScriptPath(tempRoot),
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"CGO_ENABLED=1",
		"GOFLAGS=",
		"BEADS_TEST_ENV_ACTIVE=1",
		"BEADS_TEST_ENV_ROOT=" + portableTestScriptPath(testEnvRoot),
		testScriptExpectedBinaryEnv + "=" + portableTestScriptPath(expected),
		testScriptExpectedBaseEnv + "=" + filepath.Base(expected),
		testScriptNativeSuffixEnv + "=" + nativeExecutableSuffix(),
		testScriptLaunchProbeEnv + "=1",
	}
	if callerBinary != "" {
		env = append(env, "BEADS_TEST_BD_BINARY="+portableTestScriptPath(callerBinary))
	}
	for _, key := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func assertFakeGoCommands(t *testing.T, commands []string, want ...string) {
	t.Helper()
	if strings.Join(commands, " ") != strings.Join(want, " ") {
		t.Fatalf("fake-go commands = %q, want %q", commands, want)
	}
}

func copyCurrentTestExecutable(t *testing.T, destination string) {
	t.Helper()
	input, err := os.Open(currentTestExecutable(t))
	if err != nil {
		t.Fatalf("open current test executable: %v", err)
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatalf("create native test executable: %v", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatalf("copy native test executable: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close native test executable: %v", err)
	}
}

func currentTestExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve current test executable: %v", err)
	}
	return path
}

func sameTestScriptFile(first string, second string) bool {
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

func nativeExecutableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func portableTestScriptPath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

// fakePrebuiltTestBinary is a stand-in for a cross-compiled `go test -c`
// binary: it records its own cwd, its BEADS_TEST_REPO_ROOT, and every
// argument it was launched with, then exits 0. The flag-mapping contract it
// exercises (scripts/test.sh -> scripts/ci/run-go-test-binary.sh -> this
// binary) is host-independent, so a bash stand-in is representative even
// though the real artifact is a Windows PE .exe: that artifact's own
// Windows-launch behavior is exercised by the CI job itself, not by this
// policy test.
const fakePrebuiltTestBinary = `#!/usr/bin/env bash
set -euo pipefail
{
    printf 'cwd=%s\n' "$PWD"
    printf 'repo_root=%s\n' "${BEADS_TEST_REPO_ROOT:-}"
    printf 'env_root=%s\n' "${BEADS_TEST_ENV_ROOT:-}"
    for a in "$@"; do
        printf 'arg=%s\n' "$a"
    done
} >>"$BEADS_TEST_SCRIPT_PREBUILT_LOG"
`

const testScriptPrebuiltLogEnv = "BEADS_TEST_SCRIPT_PREBUILT_LOG"

type prebuiltTestBinaryRun struct {
	cwd      string
	repoRoot string
	envRoot  string
	args     []string
}

// TestTestScriptPrebuiltTestBinaryContract pins scripts/test.sh's
// BEADS_TEST_PREBUILT_TEST_BINARY mode (F4.4's run-go-test-binary.sh path):
// it must map go-test-style flags to -test.* flags, launch the binary from
// the package directory (the same convention `go test` itself uses), export
// BEADS_TEST_REPO_ROOT, and refuse a request naming more than one package
// (a prebuilt binary is compiled from exactly one package, so there is no
// single binary that could run a multi-package request).
func TestTestScriptPrebuiltTestBinaryContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The bash stand-in above is a Linux/macOS-only harness; see its doc
		// comment. The real cross-built .exe is launched by the Windows job,
		// not this test.
		t.Skip("prebuilt-binary contract is exercised with a bash stand-in on Linux/macOS")
	}

	t.Run("maps go-test flags to -test.* flags, cwd, and BEADS_TEST_REPO_ROOT", func(t *testing.T) {
		repoRoot := sourceRepoRoot(t)
		run := runTestScriptWithPrebuiltBinary(t, repoRoot, []string{
			"-v", "-timeout", "5m", "-skip", "TestBar", "-run", "TestFoo", "-count", "1", "./scripts/ci",
		})

		wantArgs := []string{
			"-test.timeout", "5m",
			"-test.parallel", "4",
			"-test.v",
			"-test.skip", "TestBar",
			"-test.run", "TestFoo",
			"-test.count", "1",
			"-test.paniconexit0",
		}
		if strings.Join(run.args, " ") != strings.Join(wantArgs, " ") {
			t.Fatalf("args = %v, want %v", run.args, wantArgs)
		}

		wantCwd := filepath.Join(repoRoot, "scripts", "ci")
		if !sameTestScriptDir(t, run.cwd, wantCwd) {
			t.Fatalf("cwd = %q, want %q", run.cwd, wantCwd)
		}
		if !sameTestScriptDir(t, run.repoRoot, repoRoot) {
			t.Fatalf("BEADS_TEST_REPO_ROOT = %q, want %q", run.repoRoot, repoRoot)
		}
	})

	t.Run("supports -count=N form alongside -count N", func(t *testing.T) {
		repoRoot := sourceRepoRoot(t)
		run := runTestScriptWithPrebuiltBinary(t, repoRoot, []string{"-count=1", "./scripts/ci"})
		wantArgs := []string{"-test.timeout", "25m", "-test.parallel", "4", "-test.count", "1", "-test.paniconexit0"}
		if strings.Join(run.args, " ") != strings.Join(wantArgs, " ") {
			t.Fatalf("args = %v, want %v", run.args, wantArgs)
		}
	})

	t.Run("refuses more than one package", func(t *testing.T) {
		repoRoot := sourceRepoRoot(t)
		output, err := runTestScriptWithPrebuiltBinaryExpectFailure(t, repoRoot, []string{"./scripts/ci", "./scripts"})
		if err == nil {
			t.Fatalf("expected scripts/test.sh to fail for two packages, output:\n%s", output)
		}
		if !strings.Contains(string(output), "requires exactly one package") {
			t.Fatalf("expected a single-package error, got:\n%s", output)
		}
	})

	t.Run("cleans up BEADS_TEST_ENV_ROOT after the binary exits (no exec short-circuit)", func(t *testing.T) {
		// Regression test for F4 review SF-4: scripts/test.sh's prebuilt-binary
		// branch used to `exec` run-go-test-binary.sh, which replaces the
		// shell process image before the EXIT trap armed by
		// beads_test_env_enter (beads_test_env_cleanup) ever runs - leaking
		// the per-run BEADS_TEST_ENV_ROOT temp directory (and, with
		// BEADS_TEST_SHARED_SERVER=1, potentially orphaning a shared dolt
		// sql-server process). Running the binary as a child and exiting with
		// its captured status instead lets the trap fire normally.
		repoRoot := sourceRepoRoot(t)
		run := runTestScriptWithPrebuiltBinary(t, repoRoot, []string{"-count=1", "./scripts/ci"})
		if run.envRoot == "" {
			t.Fatal("fake prebuilt binary never observed BEADS_TEST_ENV_ROOT; cannot assert cleanup")
		}
		if _, err := os.Stat(run.envRoot); !os.IsNotExist(err) {
			t.Fatalf("BEADS_TEST_ENV_ROOT %q still exists after scripts/test.sh exited (cleanup trap did not run); stat err = %v", run.envRoot, err)
		}
	})
}

func runTestScriptWithPrebuiltBinary(t *testing.T, repoRoot string, args []string) prebuiltTestBinaryRun {
	t.Helper()
	output, logContent, err := runTestScriptPrebuiltBinary(t, repoRoot, args)
	if err != nil {
		t.Fatalf("scripts/test.sh failed: %v\n%s", err, output)
	}

	run := prebuiltTestBinaryRun{}
	for _, line := range strings.Split(strings.TrimRight(logContent, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "cwd="):
			run.cwd = strings.TrimPrefix(line, "cwd=")
		case strings.HasPrefix(line, "repo_root="):
			run.repoRoot = strings.TrimPrefix(line, "repo_root=")
		case strings.HasPrefix(line, "env_root="):
			run.envRoot = strings.TrimPrefix(line, "env_root=")
		case strings.HasPrefix(line, "arg="):
			run.args = append(run.args, strings.TrimPrefix(line, "arg="))
		}
	}
	if run.cwd == "" {
		t.Fatalf("fake prebuilt binary never ran; scripts/test.sh output:\n%s\nlog:\n%s", output, logContent)
	}
	return run
}

func runTestScriptWithPrebuiltBinaryExpectFailure(t *testing.T, repoRoot string, args []string) ([]byte, error) {
	t.Helper()
	output, _, err := runTestScriptPrebuiltBinary(t, repoRoot, args)
	return output, err
}

// filterEnv returns environ with any "key=..." entry for the given keys
// removed, so a caller can force a subprocess to fall back to its own
// hardcoded default instead of inheriting the host/sandbox's value.
func filterEnv(environ []string, keys ...string) []string {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	filtered := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if ok && drop[name] {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}

func runTestScriptPrebuiltBinary(t *testing.T, repoRoot string, args []string) (output []byte, logContent string, err error) {
	t.Helper()

	root := t.TempDir()
	fakeBin := filepath.Join(root, "fake-prebuilt-bd-cgo.test")
	if err := os.WriteFile(fakeBin, []byte(fakePrebuiltTestBinary), 0o755); err != nil {
		t.Fatalf("write fake prebuilt test binary: %v", err)
	}
	logPath := filepath.Join(root, "fake-prebuilt.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatalf("initialize fake prebuilt binary log: %v", err)
	}

	bash, lookErr := exec.LookPath("bash")
	if lookErr != nil {
		t.Fatalf("bash is required to exercise scripts/test.sh: %v", lookErr)
	}

	// Bazel's test runner sets its own TEST_TIMEOUT in the sandbox env
	// (seconds, e.g. "300" for the default moderate size) - the same name
	// scripts/test.sh reads to override its "25m" default. Strip it so the
	// "-count=N form" subtest's assertion on that hardcoded default is not
	// at the mercy of whatever test size this target happens to run under;
	// go test does not set this variable, so only Bazel needs the filter.
	//
	// scripts/ci/scripts-go-test.sh (the CI driver for this very package's
	// `go test ./scripts/...` run) calls beads_test_env_enter in its own
	// shell before invoking go test, so this process's own os.Environ() can
	// already carry an inherited BEADS_TEST_ENV_ACTIVE=1 / BEADS_TEST_ENV_ROOT
	// / BEADS_TEST_ENV_DISABLE / BEADS_TEST_ENV_KEEP from that outer,
	// longer-lived shell. If left in the child env below, scripts/test.sh's
	// own beads_test_env_enter call (scripts/test.sh:17) sees the
	// already-active guard and no-ops: no new root, no new EXIT trap - so the
	// directory observed by the fake binary is the outer root, which is
	// still alive (and rightly so: it's owned by the outer process) when
	// scripts/test.sh exits. That makes the "cleans up BEADS_TEST_ENV_ROOT"
	// subtest below fail only when this test binary happens to run nested
	// under scripts-go-test.sh, even though the SF-4 fix it is guarding is
	// correct. Every real production caller of scripts/test.sh's prebuilt
	// path (pr.yml's Windows liveness/worktree-remove prebuilt steps) runs it
	// as the first command of a fresh GitHub Actions `run:` step, never
	// nested under an already-entered hermetic env - so strip the hermetic
	// env markers here to deterministically exercise that same top-level,
	// non-nested invocation regardless of the ambient env this test binary
	// itself happens to be running under.
	env := append(filterEnv(os.Environ(),
		"TEST_TIMEOUT",
		"BEADS_TEST_ENV_ACTIVE",
		"BEADS_TEST_ENV_ROOT",
		"BEADS_TEST_ENV_DISABLE",
		"BEADS_TEST_ENV_KEEP",
	),
		"BEADS_TEST_BD_BINARY=/nonexistent-bd-not-needed-in-prebuilt-test-binary-mode",
		"BEADS_TEST_PREBUILT_TEST_BINARY="+fakeBin,
		"GITHUB_WORKSPACE="+repoRoot,
		testScriptPrebuiltLogEnv+"="+logPath,
	)

	cmdArgs := append([]string{filepath.Join(repoRoot, "scripts", "test.sh")}, args...)
	cmd := exec.Command(bash, cmdArgs...)
	cmd.Dir = repoRoot
	cmd.Env = env
	output, err = cmd.CombinedOutput()

	logBytes, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read fake prebuilt binary log: %v", readErr)
	}
	return output, string(logBytes), err
}

func sameTestScriptDir(t *testing.T, first, second string) bool {
	t.Helper()
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)
	if firstErr != nil || secondErr != nil {
		return false
	}
	return os.SameFile(firstInfo, secondInfo)
}
