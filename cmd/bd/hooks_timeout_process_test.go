package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const hookProcessBDStub = `#!/bin/sh
printf 'bd-argc=%s\n' "$#"
_bd_test_index=0
for _bd_test_arg
do
  printf 'bd-arg-%s=<%s>\n' "$_bd_test_index" "$_bd_test_arg"
  _bd_test_index=$((_bd_test_index + 1))
done
exit "${HOOK_TEST_BD_EXIT:-0}"
`

const hookProcessLongRunningBDStub = `#!/bin/sh
printf 'long-running-bd-started\n'
while :; do :; done
`

const hookProcessGNUTimeoutStub = `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  printf 'timeout (GNU coreutils) 9.99\n'
  exit 0
fi
if [ "${1-}" != "--" ] || [ "$#" -lt 6 ]; then
  printf 'invalid-timeout-argv\n' >&2
  exit 96
fi
shift
printf 'helper=timeout\n'
printf 'duration=<%s>\n' "$1"
shift
exec "$@"
`

const hookProcessGNUGtimeoutStub = `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  printf 'timeout (GNU coreutils) 9.99\n'
  exit 0
fi
if [ "${1-}" != "--" ] || [ "$#" -lt 6 ]; then
  printf 'invalid-gtimeout-argv\n' >&2
  exit 96
fi
shift
printf 'helper=gtimeout\n'
printf 'duration=<%s>\n' "$1"
shift
exec "$@"
`

const hookProcessIncompatibleTimeoutStub = `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  printf 'Microsoft Windows timeout\n'
  exit 1
fi
printf 'hostile-timeout-invoked\n' >&2
exit 97
`

const hookProcessFailedGNUProbeStub = `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  printf 'timeout (GNU coreutils) 9.99\n'
  exit 23
fi
printf 'failed-gnu-probe-invoked\n' >&2
exit 97
`

const hookProcessIncompatibleGtimeoutStub = `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  printf 'not GNU coreutils\n'
  exit 0
fi
printf 'hostile-gtimeout-invoked\n' >&2
exit 98
`

const hookProcessPerlStub = `#!/bin/sh
if [ "${1-}" != "-e" ] || [ "${3-}" != "--" ] || [ "$#" -lt 8 ]; then
  printf 'invalid-perl-argv\n' >&2
  exit 96
fi
shift 3
printf 'helper=perl\n'
printf 'duration=<%s>\n' "$1"
shift
exec "$@"
`

type hookProcessFixture struct {
	name string
	body string
}

type hookProcessCase struct {
	fixtures    []hookProcessFixture
	bdBody      string
	bdExit      int
	shellOption string
	usePOSIXSh  bool
	timeout     *string
	pathTail    string
	args        []string
}

type hookProcessResult struct {
	output   string
	exitCode int
	elapsed  time.Duration
}

func TestGeneratedHookTimeoutProcessBoundary(t *testing.T) {
	t.Run("backend selection", testHookProcessBackendSelection)
	t.Run("positive timeout values", testHookProcessTimeoutValidation)
	t.Run("shell options preserve argv and status", testHookProcessShellOptions)
	t.Run("POSIX sh preserves argv and status", testHookProcessPOSIXShell)
	t.Run("reserved statuses are backend scoped", testHookProcessReservedStatuses)
	t.Run("real GNU timeout expires a responsive child", testHookProcessRealTimeoutExpiry)
	t.Run("real Perl alarm expires a responsive child", testHookProcessRealPerlExpiry)
	t.Run("Windows checkout rejects System32 timeout", testHookProcessWindowsSystemTimeoutCheckout)
}

func testHookProcessBackendSelection(t *testing.T) {
	tests := []struct {
		name        string
		fixtures    []hookProcessFixture
		wantHelper  string
		wantWarning bool
	}{
		{
			name: "incompatible timeout yields to gtimeout",
			fixtures: []hookProcessFixture{
				{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
				{name: "gtimeout", body: hookProcessGNUGtimeoutStub},
			},
			wantHelper: "helper=gtimeout",
		},
		{
			name: "nonzero GNU-looking probe yields to gtimeout",
			fixtures: []hookProcessFixture{
				{name: "timeout", body: hookProcessFailedGNUProbeStub},
				{name: "gtimeout", body: hookProcessGNUGtimeoutStub},
			},
			wantHelper: "helper=gtimeout",
		},
		{
			name: "Perl follows incompatible timeout commands",
			fixtures: []hookProcessFixture{
				{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
				{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
				{name: "perl", body: hookProcessPerlStub},
			},
			wantHelper: "helper=perl",
		},
		{
			name: "direct fallback is explicit",
			fixtures: []hookProcessFixture{
				{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
				{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
			},
			wantWarning: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timeout := "17"
			args := []string{"remote with space", "https://example.invalid/repo"}
			result := runGeneratedHookProcess(t, hookProcessCase{
				fixtures: tt.fixtures,
				timeout:  &timeout,
				args:     args,
			})
			if result.exitCode != 0 {
				t.Fatalf("generated hook exit = %d, want 0\n%s", result.exitCode, result.output)
			}
			if tt.wantHelper != "" && !strings.Contains(result.output, tt.wantHelper) {
				t.Errorf("output missing selected helper %q\n%s", tt.wantHelper, result.output)
			}
			if got := strings.Contains(result.output, "running without timeout"); got != tt.wantWarning {
				t.Errorf("direct-fallback warning presence = %v, want %v\n%s", got, tt.wantWarning, result.output)
			}
			for _, forbidden := range []string{
				"hostile-timeout-invoked", "failed-gnu-probe-invoked", "hostile-gtimeout-invoked",
			} {
				if strings.Contains(result.output, forbidden) {
					t.Errorf("incompatible helper was invoked: %s\n%s", forbidden, result.output)
				}
			}
			assertHookProcessBDInvocation(t, result.output, "pre-push", args...)
		})
	}
}

func testHookProcessTimeoutValidation(t *testing.T) {
	tests := []struct {
		name        string
		value       *string
		wantSeconds string
		wantWarning bool
	}{
		{name: "unset uses default", wantSeconds: strconv.Itoa(hookTimeoutSeconds)},
		{name: "option-looking value", value: hookProcessString("--help"), wantSeconds: strconv.Itoa(hookTimeoutSeconds), wantWarning: true},
		{name: "zero", value: hookProcessString("0"), wantSeconds: strconv.Itoa(hookTimeoutSeconds), wantWarning: true},
		{name: "all-zero digits", value: hookProcessString("000"), wantSeconds: strconv.Itoa(hookTimeoutSeconds), wantWarning: true},
		{name: "mixed value", value: hookProcessString("12s"), wantSeconds: strconv.Itoa(hookTimeoutSeconds), wantWarning: true},
		{name: "positive whole seconds", value: hookProcessString("17"), wantSeconds: "17"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runGeneratedHookProcess(t, hookProcessCase{
				fixtures: []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}},
				timeout:  tt.value,
			})
			if result.exitCode != 0 {
				t.Fatalf("generated hook exit = %d, want 0\n%s", result.exitCode, result.output)
			}
			if marker := "duration=<" + tt.wantSeconds + ">"; !strings.Contains(result.output, marker) {
				t.Errorf("output missing effective timeout %q\n%s", marker, result.output)
			}
			if got := strings.Contains(result.output, "invalid BEADS_HOOK_TIMEOUT"); got != tt.wantWarning {
				t.Errorf("invalid-timeout warning presence = %v, want %v\n%s", got, tt.wantWarning, result.output)
			}
		})
	}
}

func testHookProcessShellOptions(t *testing.T) {
	const wantExit = 37
	args := []string{"argument with space", "second-argument"}
	for _, option := range []string{"", "e", "u", "eu"} {
		name := option
		if name == "" {
			name = "default"
		}
		t.Run("set-"+name, func(t *testing.T) {
			result := runGeneratedHookProcess(t, hookProcessCase{
				fixtures:    []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}},
				bdExit:      wantExit,
				shellOption: option,
				args:        args,
			})
			if result.exitCode != wantExit {
				t.Fatalf("generated hook exit = %d, want bd exit %d\n%s", result.exitCode, wantExit, result.output)
			}
			assertHookProcessBDInvocation(t, result.output, "pre-push", args...)
		})
	}
}

func testHookProcessPOSIXShell(t *testing.T) {
	const wantExit = 37
	args := []string{"argument with space", "second-argument"}
	result := runGeneratedHookProcess(t, hookProcessCase{
		fixtures:    []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}},
		bdExit:      wantExit,
		shellOption: "eu",
		usePOSIXSh:  true,
		args:        args,
	})
	if result.exitCode != wantExit {
		t.Fatalf("generated hook exit = %d, want bd exit %d\n%s", result.exitCode, wantExit, result.output)
	}
	assertHookProcessBDInvocation(t, result.output, "pre-push", args...)
}

func testHookProcessReservedStatuses(t *testing.T) {
	gnu := []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}}
	perl := []hookProcessFixture{
		{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
		{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
		{name: "perl", body: hookProcessPerlStub},
	}
	direct := perl[:2]
	tests := []struct {
		name          string
		fixtures      []hookProcessFixture
		bdExit        int
		wantExit      int
		wantWarning   bool
		wantDBWarning bool
	}{
		{name: "database-not-initialized is skipped", fixtures: gnu, bdExit: 3, wantDBWarning: true},
		{name: "GNU owns 124", fixtures: gnu, bdExit: 124, wantWarning: true},
		{name: "GNU preserves 137", fixtures: gnu, bdExit: 137, wantExit: 137},
		{name: "GNU preserves 142", fixtures: gnu, bdExit: 142, wantExit: 142},
		{name: "Perl preserves 124", fixtures: perl, bdExit: 124, wantExit: 124},
		{name: "Perl owns 142", fixtures: perl, bdExit: 142, wantWarning: true},
		{name: "direct preserves 124", fixtures: direct, bdExit: 124, wantExit: 124},
		{name: "direct preserves 137", fixtures: direct, bdExit: 137, wantExit: 137},
		{name: "direct preserves 142", fixtures: direct, bdExit: 142, wantExit: 142},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runGeneratedHookProcess(t, hookProcessCase{fixtures: tt.fixtures, bdExit: tt.bdExit})
			if result.exitCode != tt.wantExit {
				t.Errorf("generated hook exit = %d, want %d\n%s", result.exitCode, tt.wantExit, result.output)
			}
			if got := strings.Contains(result.output, "timed out after"); got != tt.wantWarning {
				t.Errorf("timeout warning presence = %v, want %v\n%s", got, tt.wantWarning, result.output)
			}
			if got := strings.Contains(result.output, "database not initialized"); got != tt.wantDBWarning {
				t.Errorf("database warning presence = %v, want %v\n%s", got, tt.wantDBWarning, result.output)
			}
		})
	}
}

func testHookProcessRealTimeoutExpiry(t *testing.T) {
	helperName, helperDir := findHookProcessGNUTimeout(t)
	timeout := "1"
	result := runGeneratedHookProcess(t, hookProcessCase{
		bdBody:   hookProcessLongRunningBDStub,
		timeout:  &timeout,
		pathTail: helperDir,
	})
	if result.exitCode != 0 {
		t.Fatalf("generated hook exit = %d, want normalized timeout success\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "long-running-bd-started") || !strings.Contains(result.output, "timed out after 1s") {
		t.Fatalf("%s did not expire and normalize the responsive child\n%s", helperName, result.output)
	}
	maxElapsed := 9 * time.Second
	if result.elapsed > maxElapsed {
		t.Errorf("%s expiry took %s, want at most %s", helperName, result.elapsed, maxElapsed)
	}
}

func testHookProcessRealPerlExpiry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git for Windows Perl does not reliably preserve alarm across exec; GNU timeout is the normal Git Bash backend")
	}
	perlDir := findHookProcessPerl(t)
	timeout := "1"
	result := runGeneratedHookProcess(t, hookProcessCase{
		fixtures: []hookProcessFixture{
			{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
			{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
		},
		bdBody:   hookProcessLongRunningBDStub,
		timeout:  &timeout,
		pathTail: perlDir,
	})
	if result.exitCode != 0 {
		t.Fatalf("generated hook exit = %d, want normalized Perl alarm success\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "long-running-bd-started") || !strings.Contains(result.output, "timed out after 1s") {
		t.Fatalf("real Perl did not expire and normalize the responsive child\n%s", result.output)
	}
	if result.elapsed > 9*time.Second {
		t.Errorf("Perl expiry took %s, want at most 9s", result.elapsed)
	}
}

func testHookProcessWindowsSystemTimeoutCheckout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the System32 timeout boundary is Windows-specific")
	}

	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		t.Fatal("SystemRoot is required for the System32 timeout boundary")
	}
	system32 := filepath.Join(systemRoot, "System32")
	systemTimeout := filepath.Join(system32, "timeout.exe")
	if _, err := os.Stat(systemTimeout); err != nil {
		t.Fatalf("System32 timeout.exe is required: %v", err)
	}

	binDir := t.TempDir()
	writeHookProcessFixture(t, binDir, "bd", hookProcessBDStub)
	controlledPath := hookProcessShellPath(t, binDir) + ":" + hookProcessShellPath(t, system32)
	probe := exec.Command(hookProcessShell(t), "--noprofile", "--norc", "-c", `
PATH=$1
export PATH
_bd_test_timeout=$(command -v timeout) || exit 1
printf '%s\n' "$_bd_test_timeout"
if timeout --version >/dev/null 2>&1; then exit 2; fi
`, "system32-timeout-probe", controlledPath)
	probe.Env = hookProcessEnv()
	probeOutput, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("exercise System32 timeout through the hook PATH: %v\n%s", err, string(probeOutput))
	}
	gotTimeout := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(string(probeOutput))), ".exe")
	wantTimeout := strings.TrimSuffix(strings.ToLower(hookProcessShellPath(t, systemTimeout)), ".exe")
	if gotTimeout != wantTimeout {
		t.Fatalf("hook PATH resolved timeout to %q, want System32 %q", gotTimeout, wantTimeout)
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("create temporary repository: %v", err)
	}
	gitPath, err := exec.LookPath("git.exe")
	if err != nil {
		t.Fatalf("Git for Windows is required: %v", err)
	}
	gitEnv := hookProcessGitEnv(t.TempDir(), "HOOK_TEST_BD_EXIT=0")
	runHookProcessGit(t, gitPath, repoDir, gitEnv, "init", "--initial-branch=main", ".")
	runHookProcessGit(t, gitPath, repoDir, gitEnv, "config", "core.hooksPath", ".git/hooks")
	runHookProcessGit(t, gitPath, repoDir, gitEnv, "config", "user.name", "Hook Test")
	runHookProcessGit(t, gitPath, repoDir, gitEnv, "config", "user.email", "hook-test@example.invalid")
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatalf("write tracked fixture: %v", err)
	}
	runHookProcessGit(t, gitPath, repoDir, gitEnv, "add", "--", "tracked.txt")
	runHookProcessGit(t, gitPath, repoDir, gitEnv, "commit", "--no-verify", "-m", "initial")
	commit := strings.TrimSpace(runHookProcessGit(t, gitPath, repoDir, gitEnv, "rev-parse", "HEAD"))
	runHookProcessGit(t, gitPath, repoDir, gitEnv, "branch", "topic")
	if head := strings.TrimSpace(runHookProcessGit(t, gitPath, repoDir, gitEnv, "symbolic-ref", "--short", "HEAD")); head != "main" {
		t.Fatalf("HEAD = %q before checkout, want main", head)
	}

	hookBody := "#!/bin/sh\nPATH=" + hookProcessShellQuote(controlledPath) + "\nexport PATH\n" + generateHookSection("post-checkout")
	writeHookProcessFixture(t, filepath.Join(repoDir, ".git", "hooks"), "post-checkout", hookBody)
	checkout := exec.Command(gitPath, "checkout", "topic")
	checkout.Dir = repoDir
	checkout.Env = gitEnv
	checkoutOutput, err := checkout.CombinedOutput()
	if err != nil {
		t.Fatalf("checkout reported generated-hook failure: %v\n%s", err, string(checkoutOutput))
	}
	output := string(checkoutOutput)
	if !strings.Contains(output, "running without timeout") {
		t.Errorf("checkout output missing incompatible-timeout fallback warning\n%s", output)
	}
	assertHookProcessBDInvocation(t, output, "post-checkout", commit, commit, "1")
	if head := strings.TrimSpace(runHookProcessGit(t, gitPath, repoDir, gitEnv, "symbolic-ref", "--short", "HEAD")); head != "topic" {
		t.Fatalf("HEAD = %q after successful checkout, want topic", head)
	}
}

func runGeneratedHookProcess(t *testing.T, tc hookProcessCase) hookProcessResult {
	t.Helper()

	binDir := t.TempDir()
	bdBody := tc.bdBody
	if bdBody == "" {
		bdBody = hookProcessBDStub
	}
	writeHookProcessFixture(t, binDir, "bd", bdBody)
	for _, fixture := range tc.fixtures {
		writeHookProcessFixture(t, binDir, fixture.name, fixture.body)
	}

	hookPath := filepath.Join(t.TempDir(), "pre-push")
	writeHookProcessFixture(t, filepath.Dir(hookPath), filepath.Base(hookPath), "#!/bin/sh\n"+generateHookSection("pre-push"))
	controlledPath := hookProcessShellPath(t, binDir)
	if tc.pathTail != "" {
		controlledPath += ":" + tc.pathTail
	}

	controlScript := `PATH=$1
export PATH
shift
case "$1" in
  "") ;;
  e) set -e ;;
  u) set -u ;;
  eu) set -eu ;;
  *) exit 95 ;;
esac
shift
_bd_test_hook=$1
shift
. "$_bd_test_hook"
`
	shellPath := hookProcessShell(t)
	commandArgs := []string{"--noprofile", "--norc", "-c", controlScript, "generated-hook-test", controlledPath, tc.shellOption, hookProcessShellPath(t, hookPath)}
	if tc.usePOSIXSh {
		shellPath = hookProcessPOSIXShell(t)
		commandArgs = []string{"-c", controlScript, "generated-hook-test", controlledPath, tc.shellOption, hookProcessShellPath(t, hookPath)}
	}
	commandArgs = append(commandArgs, tc.args...)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, shellPath, commandArgs...)
	cmd.WaitDelay = time.Second
	cmd.Env = append(hookProcessEnv(), "HOOK_TEST_BD_EXIT="+strconv.Itoa(tc.bdExit))
	if tc.timeout != nil {
		cmd.Env = append(cmd.Env, "BEADS_HOOK_TIMEOUT="+*tc.timeout)
	}
	started := time.Now()
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(started)
	if ctx.Err() != nil {
		t.Fatalf("generated hook exceeded process-test deadline: %v\n%s", ctx.Err(), string(output))
	}
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run generated hook: %v\n%s", err, string(output))
		}
		exitCode = exitErr.ExitCode()
	}
	return hookProcessResult{output: string(output), exitCode: exitCode, elapsed: elapsed}
}

func findHookProcessGNUTimeout(t *testing.T) (string, string) {
	t.Helper()
	probe := `for _bd_test_candidate in timeout gtimeout; do
  if command -v "$_bd_test_candidate" >/dev/null 2>&1 &&
     _bd_test_version="$("$_bd_test_candidate" --version 2>/dev/null)"; then
    case "$_bd_test_version" in
      "timeout (GNU coreutils) "*)
        _bd_test_path=$(command -v "$_bd_test_candidate")
        printf '%s\n%s\n' "$_bd_test_candidate" "${_bd_test_path%/*}"
        exit 0
        ;;
    esac
  fi
done
exit 1
`
	cmd := exec.Command(hookProcessShell(t), "--noprofile", "--norc", "-c", probe)
	cmd.Env = hookProcessEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("compatible GNU timeout is unavailable: %s", strings.TrimSpace(string(output)))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		t.Fatalf("unexpected GNU timeout probe output: %q", string(output))
	}
	return lines[0], lines[1]
}

func findHookProcessPerl(t *testing.T) string {
	t.Helper()
	probe := `
_bd_test_path=$(command -v perl) || exit 1
perl -e 'exit 0' || exit 1
printf '%s\n' "${_bd_test_path%/*}"
`
	cmd := exec.Command(hookProcessShell(t), "--noprofile", "--norc", "-c", probe)
	cmd.Env = hookProcessEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("Perl is unavailable: %s", strings.TrimSpace(string(output)))
	}
	dir := strings.TrimSpace(string(output))
	if dir == "" {
		t.Fatalf("unexpected Perl probe output: %q", string(output))
	}
	return dir
}

func writeHookProcessFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s fixture: %v", name, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("make %s fixture executable: %v", name, err)
	}
}

func assertHookProcessBDInvocation(t *testing.T, output, hookName string, hookArgs ...string) {
	t.Helper()
	wantArgs := append([]string{"hooks", "run", hookName}, hookArgs...)
	if marker := fmt.Sprintf("bd-argc=%d", len(wantArgs)); !strings.Contains(output, marker) {
		t.Errorf("output missing %q\n%s", marker, output)
	}
	for index, arg := range wantArgs {
		marker := fmt.Sprintf("bd-arg-%d=<%s>", index, arg)
		if !strings.Contains(output, marker) {
			t.Errorf("output missing argument marker %q\n%s", marker, output)
		}
	}
}

func hookProcessShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		gitPath, err := exec.LookPath("git.exe")
		if err != nil {
			t.Fatalf("Git for Windows is required to locate Git Bash: %v", err)
		}
		dir := filepath.Dir(gitPath)
		for range 5 {
			for _, candidate := range []string{filepath.Join(dir, "bash.exe"), filepath.Join(dir, "bin", "bash.exe")} {
				if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
					return candidate
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		t.Fatalf("could not locate Git Bash beside %s", gitPath)
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("Bash is required to exercise generated hooks: %v", err)
	}
	return shell
}

func hookProcessPOSIXShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		bashPath := hookProcessShell(t)
		gitRoot := filepath.Dir(filepath.Dir(bashPath))
		candidate := filepath.Join(gitRoot, "usr", "bin", "sh.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		t.Fatalf("could not locate Git for Windows POSIX sh beside %s", bashPath)
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("POSIX sh is required to exercise generated hooks: %v", err)
	}
	return shell
}

func hookProcessShellPath(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve shell path %s: %v", path, err)
	}
	if runtime.GOOS != "windows" {
		return filepath.ToSlash(absolute)
	}
	volume := filepath.VolumeName(absolute)
	if len(volume) != 2 || volume[1] != ':' {
		t.Fatalf("Git Bash fixture path must use a drive-letter volume: %q", absolute)
	}
	return "/" + strings.ToLower(volume[:1]) + filepath.ToSlash(absolute[len(volume):])
}

func hookProcessShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func hookProcessEnv() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		if upper == "BASH_ENV" || upper == "BASHOPTS" || upper == "ENV" || upper == "SHELLOPTS" ||
			upper == "BEADS_HOOK_TIMEOUT" || upper == "HOOK_TEST_BD_EXIT" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "BASH_ENV=", "ENV=")
}

func hookProcessGitEnv(home string, extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+8+len(extra))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		if upper == "HOME" || upper == "USERPROFILE" || upper == "BASH_ENV" || upper == "BASHOPTS" || upper == "ENV" || upper == "SHELLOPTS" ||
			strings.HasPrefix(upper, "GIT_") || upper == "HOOK_TEST_BD_EXIT" {
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"HOME="+home,
		"USERPROFILE="+home,
		"BASH_ENV=",
		"ENV=",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	return append(env, extra...)
}

func runHookProcessGit(t *testing.T, gitPath, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
	}
	return string(output)
}

func hookProcessString(value string) *string {
	return &value
}
