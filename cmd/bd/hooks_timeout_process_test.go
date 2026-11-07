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
	"syscall"
	"testing"
	"time"
)

// hookProcessBDArgvReport is the start of the scripted bd stand-ins that exit
// at once: it prints the argv bd received, for assertHookProcessBDInvocation.
const hookProcessBDArgvReport = `#!/bin/sh
printf 'bd-argc=%s\n' "$#"
_bd_test_index=0
for _bd_test_arg
do
  printf 'bd-arg-%s=<%s>\n' "$_bd_test_index" "$_bd_test_arg"
  _bd_test_index=$((_bd_test_index + 1))
done
`

const hookProcessBDStub = hookProcessBDArgvReport + `exit "${HOOK_TEST_BD_EXIT:-0}"
`

// hookProcessKilledBDStub dies by SIGKILL after it reports its argv, as an
// OOM-killed bd does. A supervisor must report that death as 128+9, not as an
// exit status that a git hook reads as success.
const hookProcessKilledBDStub = hookProcessBDArgvReport + `kill -KILL $$
`

// hookProcessLongRunningBDStub stands in for a bd stuck on work it runs in a
// child process: runChainedHook runs the .old hook, pre-commit runs bd export,
// and post-merge and post-checkout run bd import. It reacts to signals the way
// the real bd does there, so the expiry subtests prove the timeout helper
// stops bd and not only a stand-in:
//   - It ignores SIGALRM, which the Go runtime catches and takes no action
//     on. A stand-in that died on SIGALRM hid GH#7753.
//   - It catches SIGTERM and keeps waiting, as setupGracefulShutdown does on
//     the first signal. A stand-in that died on SIGTERM hid a supervisor that
//     signaled bd alone.
//   - It waits on a foreground child that only a signal to bd's process group
//     reaches.
//
// When that child is gone the stub exits 97, outside every code the hook
// normalizes, so a helper that passes bd's late status through still fails the
// test. The child prints its pid, so a subtest can check that it did not
// outlive the hook, and runs with its stdio detached, so an orphan cannot hold
// the test's output pipe open past exec.Cmd.WaitDelay. It sleeps 10 seconds:
// when a test run is killed before the helper fires (the harness reaches the
// helper by pid before its child), the orphan exits on its own.
const hookProcessLongRunningBDStub = `#!/bin/sh
trap '' ALRM
trap 'printf "bd-caught-TERM\n"' TERM
printf 'long-running-bd-started\n'
/bin/sh -c 'printf "bd-child-pid=%s\n" "$$"; exec "$1" 10 </dev/null >/dev/null 2>&1' bd-child "${HOOK_TEST_SLEEP:?}"
exit 97
`

// hookProcessTERMResistantBDStub stands in for a bd stuck in its own process
// rather than in a child. It catches SIGTERM and carries on after the group
// signal has ended its current child, so only the Perl supervisor's SIGKILL,
// hookTimeoutKillAfterSeconds after the deadline, stops it. Like
// hookProcessLongRunningBDStub it ignores SIGALRM, and it bounds itself: it
// idles in one-second sleeps, with stdio detached, for at most a minute.
const hookProcessTERMResistantBDStub = `#!/bin/sh
trap '' ALRM
trap 'printf "bd-caught-TERM\n"' TERM
printf 'long-running-bd-started\n'
_bd_test_tick=0
while [ "$_bd_test_tick" -lt 60 ]; do
  "${HOOK_TEST_SLEEP:?}" 1 </dev/null >/dev/null 2>&1
  _bd_test_tick=$((_bd_test_tick + 1))
done
exit 97
`

// hookProcessForwardingBDStub stands in for a bd that waits on a child when a
// signal reaches the hook's process group but not bd's own group, as a Ctrl-C
// does. Its child waits a second, so the supervisor has installed its
// handlers, then sends the signal to the supervisor (the stub's $PPID) and
// sleeps. Only a supervisor that passes the signal on to bd's whole process
// group ends that sleep early. The stub then reports the signal and exits 37,
// and the hook must return that status as bd's own: not a timeout, and not the
// supervisor's death by the signal.
func hookProcessForwardingBDStub(signal string) string {
	return `#!/bin/sh
trap 'printf "bd-caught-` + signal + `\n"' ` + signal + `
printf 'forwarding-bd-started\n'
/bin/sh -c '"$2" 1; kill -` + signal + ` "$1"; exec "$2" 10 </dev/null >/dev/null 2>&1' bd-child "$PPID" "${HOOK_TEST_SLEEP:?}"
exit 37
`
}

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
	sleepPath   string
	args        []string
	hookName    string      // default pre-push
	chainBody   string      // <hook>.old beside the hook; empty for none
	chainMode   os.FileMode // chain file mode when not 0o755
	trailing    bool        // content after the section prints trailing-ran
}

type hookProcessResult struct {
	output    string
	exitCode  int
	elapsed   time.Duration
	chainRuns int // runs hookProcessChainRecorderBody counted
}

func TestGeneratedHookTimeoutProcessBoundary(t *testing.T) {
	t.Run("backend selection", testHookProcessBackendSelection)
	t.Run("positive timeout values", testHookProcessTimeoutValidation)
	t.Run("shell options preserve argv and status", testHookProcessShellOptions)
	t.Run("POSIX sh preserves argv and status", testHookProcessPOSIXShell)
	t.Run("reserved statuses are backend scoped", testHookProcessReservedStatuses)
	t.Run("deadline with a chained gate refuses", testHookProcessDeadlineWithChainedGate)
	t.Run("real coreutils timeout refuses with a chained gate", testHookProcessRealTimeoutChainedGate)
	t.Run("real Perl supervisor refuses with a chained gate", testHookProcessRealPerlChainedGate)
	t.Run("real coreutils timeout stops bd and the child it waits on", testHookProcessRealTimeoutExpiry)
	t.Run("real Perl supervisor stops bd and the child it waits on", testHookProcessRealPerlExpiry)
	t.Run("real Perl supervisor kills a bd that survives TERM", testHookProcessRealPerlKillAfter)
	t.Run("real Perl supervisor forwards signals to bd's process group", testHookProcessRealPerlForwardsSignals)
	t.Run("real Perl supervisor preserves argv and status", testHookProcessRealPerlStatuses)
	t.Run("Windows checkout rejects System32 timeout", testHookProcessWindowsSystemTimeoutCheckout)
}

// TestGeneratedHookBdPresentLeavesChainToBd is the bd-present control: with
// no deadline expiry the section hands the hook to bd with its arguments,
// never runs the <hook>.old beside it itself (bd hooks run owns the chain),
// returns bd's failure and falls through to later content on success.
func TestGeneratedHookBdPresentLeavesChainToBd(t *testing.T) {
	type shellMode struct {
		name        string
		shellOption string
		usePOSIXSh  bool
	}
	shells := []shellMode{
		{name: "bash"}, {name: "bash set -e", shellOption: "e"}, {name: "bash set -u", shellOption: "u"},
		{name: "bash set -eu", shellOption: "eu"}, {name: "sh", usePOSIXSh: true}, {name: "sh set -eu", shellOption: "eu", usePOSIXSh: true},
	}
	backends := []struct {
		name     string
		fixtures []hookProcessFixture
	}{
		{name: "coreutils", fixtures: []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}}},
		{name: "direct", fixtures: []hookProcessFixture{
			{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
			{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
		}},
	}
	args := []string{"remote with space", "https://example.invalid/repo"}
	for _, backend := range backends {
		for _, shell := range shells {
			for _, bdExit := range []int{0, 1, 37} {
				t.Run(fmt.Sprintf("%s/%s/bd exits %d", backend.name, shell.name, bdExit), func(t *testing.T) {
					result := runGeneratedHookProcess(t, hookProcessCase{
						fixtures:    backend.fixtures,
						bdExit:      bdExit,
						shellOption: shell.shellOption,
						usePOSIXSh:  shell.usePOSIXSh,
						args:        args,
						chainBody:   hookProcessChainRecorderBody,
						trailing:    true,
					})
					assertHookProcessBDInvocation(t, result.output, "pre-push", args...)
					if result.chainRuns != 0 {
						t.Errorf("section ran the chain %d times itself, want 0\n%s", result.chainRuns, result.output)
					}
					if result.exitCode != bdExit {
						t.Errorf("generated hook exit = %d, want bd's status %d\n%s", result.exitCode, bdExit, result.output)
					}
					if got := strings.Contains(result.output, "trailing-ran"); got != (bdExit == 0) {
						t.Errorf("trailing content ran = %v, want %v\n%s", got, bdExit == 0, result.output)
					}
				})
			}
		}
	}
	for _, hookName := range managedHookNames {
		t.Run("hook "+hookName, func(t *testing.T) {
			result := runGeneratedHookProcess(t, hookProcessCase{
				fixtures:    []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}},
				shellOption: "eu",
				args:        []string{"arg one"},
				hookName:    hookName,
				chainBody:   hookProcessChainRecorderBody,
				trailing:    true,
			})
			assertHookProcessBDInvocation(t, result.output, hookName, "arg one")
			if result.exitCode != 0 || result.chainRuns != 0 || !strings.Contains(result.output, "trailing-ran") {
				t.Errorf("exit %d, chain runs %d; want 0, 0 and trailing content\n%s", result.exitCode, result.chainRuns, result.output)
			}
		})
	}
}

func testHookProcessBackendSelection(t *testing.T) {
	// Derive the uutils helper from the GNU stub so the two differ only in the
	// banner, but fail loudly if that banner ever moves: strings.Replace returns
	// its input unchanged when the pattern is absent, which would silently turn
	// the uutils case into a second copy of the GNU one.
	const hookProcessGNUTimeoutBanner = "(GNU coreutils) 9.99"
	uutilsTimeoutStub := strings.Replace(hookProcessGNUTimeoutStub, hookProcessGNUTimeoutBanner, "(uutils coreutils) 0.10.0", 1)
	if uutilsTimeoutStub == hookProcessGNUTimeoutStub {
		t.Fatalf("uutils fixture is a copy of the GNU stub: %q not found in hookProcessGNUTimeoutStub", hookProcessGNUTimeoutBanner)
	}

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
			name: "uutils coreutils timeout is selected like GNU",
			fixtures: []hookProcessFixture{
				{name: "timeout", body: uutilsTimeoutStub},
				{name: "gtimeout", body: hookProcessGNUGtimeoutStub},
			},
			wantHelper: "helper=timeout",
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
		{name: "3 is preserved", fixtures: gnu, bdExit: 3, wantExit: 3},
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

// hookProcessChainRefusal is the start of the refusal a gate hook prints when
// its deadline expires with a chained gate it cannot vouch for.
const hookProcessChainRefusal = "is not known to have passed"

// testHookProcessDeadlineWithChainedGate drives a backend deadline (the
// helper's own 124, or the Perl supervisor's 142) on each hook with a chain
// beside it. pre-commit and pre-push are gates: bd was stopped while the chain
// it runs may still have been deciding, so the hook refuses with the backend's
// status rather than report success, and does not run the chain again. Without
// a runnable foreign chain, and on the other hooks, the deadline keeps its
// warn-and-continue behavior.
func testHookProcessDeadlineWithChainedGate(t *testing.T) {
	gnu := []hookProcessFixture{{name: "timeout", body: hookProcessGNUTimeoutStub}}
	perl := []hookProcessFixture{
		{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
		{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
		{name: "perl", body: hookProcessPerlStub},
	}
	direct := perl[:2]
	foreign := hookProcessChainRecorderBody
	callback := "#!/bin/sh\n# bd-shim v1\n" + hookProcessChainRecorderBody
	// The non-executable chain has mode 0644 and no #! line: on Windows
	// os.Chmod cannot clear an execute bit, and Git Bash's [ -x ] reads a
	// regular file that starts with #! as executable.
	nonExecutable := strings.TrimPrefix(foreign, "#!/bin/sh\n")
	if strings.HasPrefix(nonExecutable, "#!") {
		t.Fatalf("non-executable chain fixture starts with #!, which Git Bash runs:\n%s", nonExecutable)
	}
	tests := []struct {
		name      string
		hookName  string
		fixtures  []hookProcessFixture
		bdExit    int
		chainBody string
		chainMode os.FileMode
		wantExit  int
		wantRefus bool
		wantWarn  bool
	}{
		{name: "GNU pre-push refuses", hookName: "pre-push", fixtures: gnu, bdExit: 124, chainBody: foreign, wantExit: 124, wantRefus: true},
		{name: "GNU pre-commit refuses", hookName: "pre-commit", fixtures: gnu, bdExit: 124, chainBody: foreign, wantExit: 124, wantRefus: true},
		{name: "Perl pre-push refuses", hookName: "pre-push", fixtures: perl, bdExit: 142, chainBody: foreign, wantExit: 142, wantRefus: true},
		{name: "Perl pre-commit refuses", hookName: "pre-commit", fixtures: perl, bdExit: 142, chainBody: foreign, wantExit: 142, wantRefus: true},
		{name: "no chain continues", hookName: "pre-commit", fixtures: gnu, bdExit: 124, wantWarn: true},
		{name: "non-executable chain continues", hookName: "pre-commit", fixtures: gnu, bdExit: 124, chainBody: nonExecutable, chainMode: 0o644, wantWarn: true},
		{name: "beads callback chain continues", hookName: "pre-push", fixtures: perl, bdExit: 142, chainBody: callback, wantWarn: true},
		{name: "post-merge continues", hookName: "post-merge", fixtures: gnu, bdExit: 124, chainBody: foreign, wantWarn: true},
		{name: "post-checkout continues", hookName: "post-checkout", fixtures: perl, bdExit: 142, chainBody: foreign, wantWarn: true},
		{name: "prepare-commit-msg continues", hookName: "prepare-commit-msg", fixtures: gnu, bdExit: 124, chainBody: foreign, wantWarn: true},
		{name: "direct natural 124 stays a failure", hookName: "pre-push", fixtures: direct, bdExit: 124, chainBody: foreign, wantExit: 124},
		{name: "direct natural 142 stays a failure", hookName: "pre-commit", fixtures: direct, bdExit: 142, chainBody: foreign, wantExit: 142},
		{name: "GNU natural 142 stays a failure", hookName: "pre-push", fixtures: gnu, bdExit: 142, chainBody: foreign, wantExit: 142},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runGeneratedHookProcess(t, hookProcessCase{
				fixtures:  tt.fixtures,
				bdExit:    tt.bdExit,
				hookName:  tt.hookName,
				chainBody: tt.chainBody,
				chainMode: tt.chainMode,
			})
			if result.exitCode != tt.wantExit {
				t.Errorf("generated hook exit = %d, want %d\n%s", result.exitCode, tt.wantExit, result.output)
			}
			if got := strings.Contains(result.output, hookProcessChainRefusal); got != tt.wantRefus {
				t.Errorf("chained-gate refusal presence = %v, want %v\n%s", got, tt.wantRefus, result.output)
			}
			if got := strings.Contains(result.output, "continuing without beads"); got != tt.wantWarn {
				t.Errorf("continue warning presence = %v, want %v\n%s", got, tt.wantWarn, result.output)
			}
			if tt.wantRefus && !strings.Contains(result.output, "hook '"+tt.hookName+"' timed out after 300s") {
				t.Errorf("refusal does not name the hook and its timeout\n%s", result.output)
			}
			if tt.wantRefus && !strings.Contains(result.output, tt.hookName+".old") {
				t.Errorf("refusal does not name the chained hook\n%s", result.output)
			}
			if result.chainRuns != 0 {
				t.Errorf("the section ran the chain %d times after bd returned; it must not rerun it\n%s", result.chainRuns, result.output)
			}
		})
	}
}

// testHookProcessRealTimeoutChainedGate is testHookProcessRealTimeoutExpiry
// with a foreign <hook>.old beside each gate hook: the real coreutils deadline
// now refuses with 124 instead of continuing.
func testHookProcessRealTimeoutChainedGate(t *testing.T) {
	_, helperDir := findHookProcessCoreutilsTimeout(t)
	for _, hookName := range []string{"pre-push", "pre-commit"} {
		t.Run(hookName, func(t *testing.T) {
			timeout := "1"
			result := runGeneratedHookProcess(t, hookProcessCase{
				bdBody:    hookProcessLongRunningBDStub,
				timeout:   &timeout,
				pathTail:  helperDir,
				sleepPath: findHookProcessSleep(t),
				hookName:  hookName,
				chainBody: hookProcessChainRecorderBody,
			})
			if result.exitCode != 124 {
				t.Fatalf("generated hook exit = %d, want the coreutils deadline status 124\n%s", result.exitCode, result.output)
			}
			assertHookProcessChainedGateRefusal(t, hookName, result)
		})
	}
}

// testHookProcessRealPerlChainedGate is the same for the real Perl
// supervisor's 142.
func testHookProcessRealPerlChainedGate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(hookProcessPerlSignalSkip)
	}
	for _, hookName := range []string{"pre-commit", "pre-push"} {
		t.Run(hookName, func(t *testing.T) {
			timeout := "1"
			result := runGeneratedHookProcess(t, hookProcessCase{
				fixtures: []hookProcessFixture{
					{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
					{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
				},
				bdBody:    hookProcessLongRunningBDStub,
				timeout:   &timeout,
				pathTail:  findHookProcessPerl(t),
				sleepPath: findHookProcessSleep(t),
				hookName:  hookName,
				chainBody: hookProcessChainRecorderBody,
			})
			if result.exitCode != 142 {
				t.Fatalf("generated hook exit = %d, want the Perl supervisor's deadline status 142\n%s", result.exitCode, result.output)
			}
			assertHookProcessChainedGateRefusal(t, hookName, result)
			assertHookProcessChildStopped(t, result.output)
		})
	}
}

// assertHookProcessChainedGateRefusal checks a real deadline's refusal: it
// names the hook, its timeout and the chain, says the chain's result is
// unknown, points at the ways out, and never reruns the chain.
func assertHookProcessChainedGateRefusal(t *testing.T, hookName string, result hookProcessResult) {
	t.Helper()
	for _, want := range []string{"hook '" + hookName + "' timed out after 1s", hookName + ".old", hookProcessChainRefusal, "BEADS_HOOK_TIMEOUT", "--no-verify"} {
		if !strings.Contains(result.output, want) {
			t.Errorf("deadline refusal missing %q\n%s", want, result.output)
		}
	}
	if result.chainRuns != 0 {
		t.Errorf("the section ran the chain %d times after the deadline\n%s", result.chainRuns, result.output)
	}
}

func testHookProcessRealTimeoutExpiry(t *testing.T) {
	helperName, helperDir := findHookProcessCoreutilsTimeout(t)
	timeout := "1"
	result := runGeneratedHookProcess(t, hookProcessCase{
		bdBody:    hookProcessLongRunningBDStub,
		timeout:   &timeout,
		pathTail:  helperDir,
		sleepPath: findHookProcessSleep(t),
	})
	if result.exitCode != 0 {
		t.Fatalf("generated hook exit = %d, want normalized timeout success\n%s", result.exitCode, result.output)
	}
	if !strings.Contains(result.output, "long-running-bd-started") || !strings.Contains(result.output, "timed out after 1s") {
		t.Fatalf("%s did not stop and normalize the TERM-catching bd\n%s", helperName, result.output)
	}
	maxElapsed := 9 * time.Second
	if result.elapsed > maxElapsed {
		t.Errorf("%s expiry took %s, want at most %s", helperName, result.elapsed, maxElapsed)
	}
}

// hookProcessPerlSignalSkip is why the subtests that signal bd through the real
// Perl supervisor do not run on Windows.
const hookProcessPerlSignalSkip = "Git Bash normally selects its bundled GNU timeout; whether MSYS Perl can signal a native bd.exe is unverified"

func testHookProcessRealPerlExpiry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(hookProcessPerlSignalSkip)
	}
	result := runHookProcessRealPerl(t, hookProcessLongRunningBDStub, "1")
	if result.exitCode != 0 {
		t.Fatalf("generated hook exit = %d, want normalized Perl supervisor timeout success\n%s", result.exitCode, result.output)
	}
	for _, want := range []string{"long-running-bd-started", "bd-caught-TERM", "timed out after 1s"} {
		if !strings.Contains(result.output, want) {
			t.Fatalf("real Perl did not stop and normalize the TERM-catching bd: output missing %q\n%s", want, result.output)
		}
	}
	if result.elapsed > 9*time.Second {
		t.Errorf("Perl expiry took %s, want at most 9s", result.elapsed)
	}
	assertHookProcessChildStopped(t, result.output)
}

// testHookProcessRealPerlKillAfter drives the real Perl supervisor with a bd
// that the deadline SIGTERM does not stop: the supervisor must follow up with
// SIGKILL and still report the timeout.
func testHookProcessRealPerlKillAfter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(hookProcessPerlSignalSkip)
	}
	result := runHookProcessRealPerl(t, hookProcessTERMResistantBDStub, "1")
	if result.exitCode != 0 {
		t.Fatalf("generated hook exit = %d, want normalized Perl supervisor timeout success\n%s", result.exitCode, result.output)
	}
	for _, want := range []string{"long-running-bd-started", "bd-caught-TERM", "timed out after 1s"} {
		if !strings.Contains(result.output, want) {
			t.Fatalf("real Perl did not kill and normalize the TERM-resistant bd: output missing %q\n%s", want, result.output)
		}
	}
	// SIGTERM at 1s, then SIGKILL hookTimeoutKillAfterSeconds later.
	if result.elapsed > 9*time.Second {
		t.Errorf("Perl SIGKILL follow-up took %s, want at most 9s", result.elapsed)
	}
}

// testHookProcessRealPerlForwardsSignals sends each signal bd shuts down on to
// the real Perl supervisor only, as a Ctrl-C or a hangup reaches the hook's
// process group and not bd's. The supervisor must pass it on to bd's group and
// then return bd's own status.
func testHookProcessRealPerlForwardsSignals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(hookProcessPerlSignalSkip)
	}
	for _, signal := range []string{"HUP", "INT", "TERM"} {
		t.Run(signal, func(t *testing.T) {
			result := runHookProcessRealPerl(t, hookProcessForwardingBDStub(signal), "60")
			if result.exitCode != 37 {
				t.Fatalf("generated hook exit = %d, want bd's own status 37 after the forwarded %s\n%s", result.exitCode, signal, result.output)
			}
			if !strings.Contains(result.output, "bd-caught-"+signal) {
				t.Errorf("bd never received the forwarded %s\n%s", signal, result.output)
			}
			if strings.Contains(result.output, "timed out after") {
				t.Errorf("a forwarded %s was reported as a timeout\n%s", signal, result.output)
			}
			// The child sleeps 10 seconds unless the signal reached bd's group.
			if result.elapsed > 9*time.Second {
				t.Errorf("forwarded %s took %s to stop bd's child, want at most 9s", signal, result.elapsed)
			}
		})
	}
}

// testHookProcessRealPerlStatuses drives the generated Perl supervisor with a
// bd that exits before the deadline: the supervisor's waitpid/exit mapping must
// hand bd's argv through unchanged and forward its status to the same
// normalization the other backends use.
func testHookProcessRealPerlStatuses(t *testing.T) {
	perlDir := findHookProcessPerl(t)
	args := []string{"argument with space", "second-argument"}
	tests := []struct {
		name          string
		bdBody        string
		bdExit        int
		wantExit      int
		wantWarning   bool
		wantDBWarning bool
	}{
		{name: "success continues", bdExit: 0},
		{name: "3 is preserved", bdExit: 3, wantExit: 3},
		{name: "other status is preserved", bdExit: 37, wantExit: 37},
		{name: "124 is preserved", bdExit: 124, wantExit: 124},
		{name: "142 is reported as a timeout", bdExit: 142, wantWarning: true},
		{name: "death by SIGKILL is reported as 137", bdBody: hookProcessKilledBDStub, wantExit: 137},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.bdBody != "" && runtime.GOOS == "windows" {
				t.Skip("whether MSYS Perl sees an MSYS process's signal death is unverified")
			}
			result := runGeneratedHookProcess(t, hookProcessCase{
				fixtures: []hookProcessFixture{
					{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
					{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
				},
				bdBody:   tt.bdBody,
				bdExit:   tt.bdExit,
				pathTail: perlDir,
				args:     args,
			})
			if result.exitCode != tt.wantExit {
				t.Errorf("generated hook exit = %d, want %d\n%s", result.exitCode, tt.wantExit, result.output)
			}
			if got := strings.Contains(result.output, "timed out after"); got != tt.wantWarning {
				t.Errorf("timeout warning presence = %v, want %v\n%s", got, tt.wantWarning, result.output)
			}
			if got := strings.Contains(result.output, "database not initialized"); got != tt.wantDBWarning {
				t.Errorf("database warning presence = %v, want %v\n%s", got, tt.wantDBWarning, result.output)
			}
			assertHookProcessBDInvocation(t, result.output, "pre-push", args...)
		})
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

	hookName := tc.hookName
	if hookName == "" {
		hookName = "pre-push"
	}
	hookPath := filepath.Join(t.TempDir(), hookName)
	hookBody := "#!/bin/sh\n" + generateHookSection(hookName)
	if tc.trailing {
		hookBody += "printf 'trailing-ran\\n'\n"
	}
	writeHookProcessFixture(t, filepath.Dir(hookPath), filepath.Base(hookPath), hookBody)
	if tc.chainBody != "" {
		writeHookProcessFixture(t, filepath.Dir(hookPath), hookName+".old", tc.chainBody)
		if tc.chainMode != 0 {
			if err := os.Chmod(hookPath+".old", tc.chainMode); err != nil {
				t.Fatalf("chmod chain: %v", err)
			}
		}
	}
	countPath := filepath.Join(t.TempDir(), "chain-count")
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
	// $0 is the hook's own path, as when git runs it: the section finds
	// <hook>.old beside the hook through $0.
	shellHookPath := hookProcessShellPath(t, hookPath)
	shellPath := hookProcessShell(t)
	commandArgs := []string{"--noprofile", "--norc", "-c", controlScript, shellHookPath, controlledPath, tc.shellOption, shellHookPath}
	if tc.usePOSIXSh {
		shellPath = hookProcessPOSIXShell(t)
		commandArgs = []string{"-c", controlScript, shellHookPath, controlledPath, tc.shellOption, shellHookPath}
	}
	commandArgs = append(commandArgs, tc.args...)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, shellPath, commandArgs...)
	cmd.WaitDelay = time.Second
	cmd.Env = append(hookProcessEnv(), "HOOK_TEST_BD_EXIT="+strconv.Itoa(tc.bdExit), "HOOK_TEST_CHAIN_COUNT="+countPath)
	if tc.timeout != nil {
		cmd.Env = append(cmd.Env, "BEADS_HOOK_TIMEOUT="+*tc.timeout)
	}
	if tc.sleepPath != "" {
		cmd.Env = append(cmd.Env, "HOOK_TEST_SLEEP="+tc.sleepPath)
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
	runs := 0
	if counted, err := os.ReadFile(countPath); err == nil {
		runs = strings.Count(string(counted), "run\n")
	}
	return hookProcessResult{output: string(output), exitCode: exitCode, elapsed: elapsed, chainRuns: runs}
}

// runHookProcessRealPerl runs the generated hook with bdBody standing in for
// bd, on the host's real Perl backend: both timeout candidates are
// incompatible, so the shim falls through to the perl on the PATH tail.
func runHookProcessRealPerl(t *testing.T, bdBody, timeout string) hookProcessResult {
	t.Helper()
	return runGeneratedHookProcess(t, hookProcessCase{
		fixtures: []hookProcessFixture{
			{name: "timeout", body: hookProcessIncompatibleTimeoutStub},
			{name: "gtimeout", body: hookProcessIncompatibleGtimeoutStub},
		},
		bdBody:    bdBody,
		timeout:   &timeout,
		pathTail:  findHookProcessPerl(t),
		sleepPath: findHookProcessSleep(t),
	})
}

// assertHookProcessChildStopped fails when the child that
// hookProcessLongRunningBDStub waits on is still running after the hook
// returned: the timeout helper stopped bd but left bd's work running. It kills
// a survivor so the failure does not leak it.
func assertHookProcessChildStopped(t *testing.T, output string) {
	t.Helper()
	_, rest, ok := strings.Cut(output, "bd-child-pid=")
	if !ok {
		t.Fatalf("output missing the bd child's pid\n%s", output)
	}
	line, _, _ := strings.Cut(rest, "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parse the bd child's pid %q: %v", line, err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if child.Signal(syscall.Signal(0)) == nil {
		_ = child.Kill()
		t.Errorf("bd's child %d was still running after the hook returned\n%s", pid, output)
	}
}

// findHookProcessCoreutilsTimeout mirrors the generated shim's identity
// allowlist (cmd/bd/hooks.go generateHookSection). Keep the `case` here in step
// with the one the generator emits: this is the second copy of that contract,
// and a narrower copy silently skips the only test that drives a real timeout
// binary on exactly the hosts the wider allowlist exists for.
func findHookProcessCoreutilsTimeout(t *testing.T) (string, string) {
	t.Helper()
	probe := `for _bd_test_candidate in timeout gtimeout; do
  if command -v "$_bd_test_candidate" >/dev/null 2>&1 &&
     _bd_test_version="$("$_bd_test_candidate" --version 2>/dev/null)"; then
    case "$_bd_test_version" in
      "timeout (GNU coreutils) "*|"timeout (uutils coreutils) "*)
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
		t.Skipf("no compatible coreutils timeout is available: %s", strings.TrimSpace(string(output)))
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		t.Fatalf("unexpected coreutils timeout probe output: %q", string(output))
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

// findHookProcessSleep resolves the sleep the long-running bd stub paces itself
// with. The generated hook runs under a controlled PATH that carries only the
// fixtures and the helper under test, so the stub is handed sleep by absolute
// path (in the shell's own spelling, which on Git Bash is the MSYS form).
func findHookProcessSleep(t *testing.T) string {
	t.Helper()
	probe := `
_bd_test_path=$(command -v sleep) || exit 1
"$_bd_test_path" 0 || exit 1
printf '%s\n' "$_bd_test_path"
`
	cmd := exec.Command(hookProcessShell(t), "--noprofile", "--norc", "-c", probe)
	cmd.Env = hookProcessEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sleep is required to pace the long-running bd stub: %s", strings.TrimSpace(string(output)))
	}
	path := strings.TrimSpace(string(output))
	if path == "" {
		t.Fatalf("unexpected sleep probe output: %q", string(output))
	}
	return path
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
			upper == "BEADS_HOOK_TIMEOUT" || upper == "HOOK_TEST_BD_EXIT" || upper == "HOOK_TEST_SLEEP" {
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

// TestGeneratedHookChainsOldHookWhenBDMissing pins GH#6751: when bd is not on
// PATH, a refusing <hook>.old gate must still refuse the git operation.
func TestGeneratedHookChainsOldHookWhenBDMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell process test")
	}
	shellPath := hookProcessPOSIXShell(t)
	hooksDir := t.TempDir()
	writeHookProcessFixture(t, hooksDir, "pre-commit", "#!/bin/sh\n"+generateHookSection("pre-commit"))
	writeHookProcessFixture(t, hooksDir, "pre-commit.old", "#!/bin/sh\necho 'chained gate refused' >&2\nexit 1\n")

	cmd := exec.Command(shellPath, filepath.Join(hooksDir, "pre-commit"))
	cmd.Env = append(hookProcessEnv(), "PATH="+t.TempDir())
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("hook exited 0 with bd missing from PATH; chained pre-commit.old (exit 1) never ran\n%s", output)
	}
	if !strings.Contains(string(output), "chained gate refused") {
		t.Fatalf("hook failed without running pre-commit.old: %v\n%s", err, output)
	}
}

// TestGeneratedHookChainWhenBdMissingRealGit reproduces GH#6751 through git
// itself: a GUI client, cron or a minimal shell runs the hook with a PATH that
// has no bd, and the chained pre-commit.old gate must still decide the commit.
// The hook pins PATH to an empty directory, so the chain runs on shell builtins
// alone under an absolute /bin/sh shebang.
func TestGeneratedHookChainWhenBdMissingRealGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell process test")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is required: %v", err)
	}
	setup := func(t *testing.T, chainBody string, sentinelDir string) (string, []string, string) {
		t.Helper()
		repoDir := filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatalf("create temporary repository: %v", err)
		}
		gitEnv := hookProcessGitEnv(t.TempDir())
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "init", "--initial-branch=main", ".")
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "config", "core.hooksPath", ".git/hooks")
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "config", "user.name", "Hook Test")
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "config", "user.email", "hook-test@example.invalid")
		if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
			t.Fatalf("write tracked fixture: %v", err)
		}
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "add", "--", "tracked.txt")
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "commit", "--no-verify", "-m", "initial")
		baseline := strings.TrimSpace(runHookProcessGit(t, gitPath, repoDir, gitEnv, "rev-parse", "HEAD"))
		if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
			t.Fatalf("change tracked fixture: %v", err)
		}
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "add", "--", "tracked.txt")

		hooksDir := filepath.Join(repoDir, ".git", "hooks")
		hookBody := "#!/bin/sh\nPATH=" + hookProcessShellQuote(t.TempDir()) + "\nexport PATH\n" +
			generateHookSection("pre-commit") +
			"printf 'trailing ran\\n' > " + hookProcessShellQuote(filepath.Join(sentinelDir, "trailing")) + "\n"
		writeHookProcessFixture(t, hooksDir, "pre-commit", hookBody)
		if chainBody != "" {
			writeHookProcessFixture(t, hooksDir, "pre-commit.old", chainBody)
		}
		return repoDir, gitEnv, baseline
	}

	t.Run("refusing chain blocks the commit", func(t *testing.T) {
		sentinelDir := t.TempDir()
		repoDir, gitEnv, baseline := setup(t, "#!/bin/sh\necho 'chained gate refused' >&2\nexit 1\n", sentinelDir)
		commit := exec.Command(gitPath, "commit", "-m", "gated")
		commit.Dir = repoDir
		commit.Env = gitEnv
		output, err := commit.CombinedOutput()
		if err == nil {
			t.Fatalf("git commit succeeded with bd missing; the refusing pre-commit.old never ran\n%s", output)
		}
		if !strings.Contains(string(output), "chained gate refused") {
			t.Errorf("commit output missing the chained gate's message\n%s", output)
		}
		if head := strings.TrimSpace(runHookProcessGit(t, gitPath, repoDir, gitEnv, "rev-parse", "HEAD")); head != baseline {
			t.Errorf("HEAD = %s after the refused commit, want unchanged %s", head, baseline)
		}
		if _, err := os.Stat(filepath.Join(sentinelDir, "trailing")); err == nil {
			t.Errorf("trailing hook content ran after the chained gate refused")
		}
	})

	t.Run("passing chain lets the commit and trailing content run", func(t *testing.T) {
		sentinelDir := t.TempDir()
		chainSentinel := filepath.Join(sentinelDir, "chain")
		repoDir, gitEnv, baseline := setup(t, "#!/bin/sh\nprintf 'chain ran\\n' > "+hookProcessShellQuote(chainSentinel)+"\n", sentinelDir)
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "commit", "-m", "gated")
		if _, err := os.Stat(chainSentinel); err != nil {
			t.Errorf("pre-commit.old never ran with bd missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(sentinelDir, "trailing")); err != nil {
			t.Errorf("trailing hook content did not run after the chained gate passed: %v", err)
		}
		if head := strings.TrimSpace(runHookProcessGit(t, gitPath, repoDir, gitEnv, "rev-parse", "HEAD")); head == baseline {
			t.Errorf("HEAD did not advance after the passing commit")
		}
	})

	t.Run("no chain lets the commit proceed", func(t *testing.T) {
		sentinelDir := t.TempDir()
		repoDir, gitEnv, baseline := setup(t, "", sentinelDir)
		runHookProcessGit(t, gitPath, repoDir, gitEnv, "commit", "-m", "no-chain")
		if _, err := os.Stat(filepath.Join(sentinelDir, "trailing")); err != nil {
			t.Errorf("trailing hook content did not run with no chain: %v", err)
		}
		if head := strings.TrimSpace(runHookProcessGit(t, gitPath, repoDir, gitEnv, "rev-parse", "HEAD")); head == baseline {
			t.Errorf("HEAD did not advance with no chain")
		}
	})

	// Scenarios from PR #6840: a foreign hook that only looks like a beads
	// marker must still gate the commit, and a legacy inline bd hook
	// (GH#1120: only the "# bd (beads)" comment) must never be chained into.
	for _, tc := range []struct {
		name    string
		marker  string
		foreign bool
	}{
		{name: "foreign hook merely mentioning BEADS INTEGRATION still chains", marker: "# unrelated hook, see BEADS INTEGRATION docs for context", foreign: true},
		{name: "foreign hook starting with '# --- ' but not the BEGIN marker still chains", marker: "# --- NOTE: not a BEADS INTEGRATION marker", foreign: true},
		{name: "foreign begin-marker-shaped line with extra words still chains", marker: "# --- BEGIN foo BEADS INTEGRATION", foreign: true},
		{name: "legacy inline bd hook is not re-chained", marker: "# bd (beads)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sentinelDir := t.TempDir()
			chainSentinel := filepath.Join(sentinelDir, "chain")
			chainBody := "#!/bin/sh\n" + tc.marker + "\nprintf 'chain ran\\n' > " + hookProcessShellQuote(chainSentinel) +
				"\necho 'chained gate refused' >&2\nexit 9\n"
			repoDir, gitEnv, baseline := setup(t, chainBody, sentinelDir)
			commit := exec.Command(gitPath, "commit", "-m", "gated")
			commit.Dir = repoDir
			commit.Env = gitEnv
			output, err := commit.CombinedOutput()
			head := strings.TrimSpace(runHookProcessGit(t, gitPath, repoDir, gitEnv, "rev-parse", "HEAD"))
			_, chainErr := os.Stat(chainSentinel)
			if tc.foreign {
				if err == nil {
					t.Fatalf("git commit succeeded; the foreign pre-commit.old was misclassified as bd-owned and skipped\n%s", output)
				}
				if chainErr != nil || !strings.Contains(string(output), "chained gate refused") {
					t.Errorf("the foreign pre-commit.old did not run\n%s", output)
				}
				if head != baseline {
					t.Errorf("HEAD = %s after the refused commit, want unchanged %s", head, baseline)
				}
				return
			}
			if err != nil {
				t.Fatalf("git commit failed; the legacy inline bd hook was chained into\n%s", output)
			}
			if chainErr == nil {
				t.Errorf("the legacy inline bd hook ran as a chain\n%s", output)
			}
			if head == baseline {
				t.Errorf("HEAD did not advance after skipping the bd-owned chain")
			}
		})
	}
}

// hookProcessChainRecorderBody is a builtin-only <hook>.old that reports what
// it was given: its argv, BD_GIT_HOOK and stdin. It appends one line to
// HOOK_TEST_CHAIN_COUNT per run and exits HOOK_TEST_CHAIN_EXIT.
const hookProcessChainRecorderBody = `#!/bin/sh
printf 'chain-argc=%s\n' "$#"
_chain_test_index=0
for _chain_test_arg
do
  printf 'chain-arg-%s=<%s>\n' "$_chain_test_index" "$_chain_test_arg"
  _chain_test_index=$((_chain_test_index + 1))
done
printf 'chain-bd-git-hook=<%s>\n' "${BD_GIT_HOOK-unset}"
while IFS= read -r _chain_test_line; do
  printf 'chain-stdin=<%s>\n' "$_chain_test_line"
done
printf 'run\n' >> "${HOOK_TEST_CHAIN_COUNT:?}"
exit "${HOOK_TEST_CHAIN_EXIT:-0}"
`

type hookChainProcessCase struct {
	hookName    string
	chainBody   string // empty: no <hook>.old
	chainMode   os.FileMode
	chainIsDir  bool
	shellOption string
	usePOSIXSh  bool
	slashless   bool   // run the hook as ./<hook> from its own directory, so $0 has no slash
	dirName     string // hooks directory name, to exercise paths with spaces
	args        []string
	stdin       string
	chainExit   int
}

type hookChainProcessResult struct {
	output      string
	exitCode    int
	chainRuns   int
	trailingRan bool
}

// runGeneratedHookChainProcess runs the generated section for tc.hookName with
// bd absent from PATH, so the section's own fallback must decide whether to run
// <hook>.old. Unlike runGeneratedHookProcess it hands the shell the hook's real
// path as $0, which is how the section finds the chain beside it, and the
// PATH it runs under is an empty directory. Trailing user content after the
// section prints "trailing-ran".
func runGeneratedHookChainProcess(t *testing.T, tc hookChainProcessCase) hookChainProcessResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell process test")
	}
	hookName := tc.hookName
	if hookName == "" {
		hookName = "pre-push"
	}
	dirName := tc.dirName
	if dirName == "" {
		dirName = "hooks"
	}
	hooksDir := filepath.Join(t.TempDir(), dirName)
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("create hooks directory: %v", err)
	}
	writeHookProcessFixture(t, hooksDir, hookName, "#!/bin/sh\n"+generateHookSection(hookName)+"printf 'trailing-ran\\n'\n")
	chainPath := filepath.Join(hooksDir, hookName+".old")
	switch {
	case tc.chainIsDir:
		if err := os.Mkdir(chainPath, 0o755); err != nil {
			t.Fatalf("create directory chain: %v", err)
		}
	case tc.chainBody != "":
		writeHookProcessFixture(t, hooksDir, hookName+".old", tc.chainBody)
		if tc.chainMode != 0 {
			if err := os.Chmod(chainPath, tc.chainMode); err != nil {
				t.Fatalf("chmod chain: %v", err)
			}
		}
	}
	countPath := filepath.Join(t.TempDir(), "chain-count")

	hookArg := filepath.Join(hooksDir, hookName)
	source := `. "$0"`
	if tc.slashless {
		hookArg = hookName
		source = `. "./$0"`
	}
	script := source + "\n"
	if tc.shellOption != "" {
		script = "set -" + tc.shellOption + "\n" + script
	}
	shellPath := hookProcessShell(t)
	commandArgs := []string{"--noprofile", "--norc", "-c", script, hookArg}
	if tc.usePOSIXSh {
		shellPath = hookProcessPOSIXShell(t)
		commandArgs = []string{"-c", script, hookArg}
	}
	commandArgs = append(commandArgs, tc.args...)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, shellPath, commandArgs...)
	cmd.WaitDelay = time.Second
	cmd.Dir = hooksDir
	cmd.Stdin = strings.NewReader(tc.stdin)
	env := make([]string, 0, len(hookProcessEnv())+3)
	for _, entry := range hookProcessEnv() {
		if key, _, _ := strings.Cut(entry, "="); key == "PATH" || key == "BD_GIT_HOOK" {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env,
		"PATH="+t.TempDir(),
		"HOOK_TEST_CHAIN_COUNT="+countPath,
		"HOOK_TEST_CHAIN_EXIT="+strconv.Itoa(tc.chainExit),
	)
	output, err := cmd.CombinedOutput()
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
	runs := 0
	if counted, err := os.ReadFile(countPath); err == nil {
		runs = strings.Count(string(counted), "run\n")
	}
	return hookChainProcessResult{
		output:      string(output),
		exitCode:    exitCode,
		chainRuns:   runs,
		trailingRan: strings.Contains(string(output), "trailing-ran"),
	}
}

func assertHookChainInvocation(t *testing.T, output string, args ...string) {
	t.Helper()
	if marker := fmt.Sprintf("chain-argc=%d", len(args)); !strings.Contains(output, marker) {
		t.Errorf("output missing %q\n%s", marker, output)
	}
	for index, arg := range args {
		if marker := fmt.Sprintf("chain-arg-%d=<%s>", index, arg); !strings.Contains(output, marker) {
			t.Errorf("output missing chain argument marker %q\n%s", marker, output)
		}
	}
}

// TestGeneratedHookChainFallbackProcess drives the bd-missing fallback: the
// section runs <hook>.old exactly once with the hook's own argv, stdin and
// BD_GIT_HOOK=1 (what runChainedHook would have handed it), falls through to
// trailing content on success, and fails with the chain's status otherwise.
func TestGeneratedHookChainFallbackProcess(t *testing.T) {
	args := []string{"argument with space", "", "second-argument"}
	stdin := "refs/heads/main 1111 refs/heads/main 0000\nsecond line\n"
	shells := []struct {
		name       string
		option     string
		usePOSIXSh bool
	}{
		{name: "bash"},
		{name: "bash set -eu", option: "eu"},
		{name: "sh", usePOSIXSh: true},
		{name: "sh set -eu", option: "eu", usePOSIXSh: true},
	}
	for _, shell := range shells {
		for _, status := range []int{0, 1, 3, 37} {
			t.Run(fmt.Sprintf("%s/exit %d", shell.name, status), func(t *testing.T) {
				result := runGeneratedHookChainProcess(t, hookChainProcessCase{
					chainBody:   hookProcessChainRecorderBody,
					shellOption: shell.option,
					usePOSIXSh:  shell.usePOSIXSh,
					args:        args,
					stdin:       stdin,
					chainExit:   status,
				})
				if result.exitCode != status {
					t.Errorf("hook exit = %d, want the chain's %d\n%s", result.exitCode, status, result.output)
				}
				if result.chainRuns != 1 {
					t.Errorf("chain ran %d times, want exactly once\n%s", result.chainRuns, result.output)
				}
				if result.trailingRan != (status == 0) {
					t.Errorf("trailing content ran = %v, want %v\n%s", result.trailingRan, status == 0, result.output)
				}
				assertHookChainInvocation(t, result.output, args...)
				for _, want := range []string{
					"chain-bd-git-hook=<1>",
					"chain-stdin=<refs/heads/main 1111 refs/heads/main 0000>",
					"chain-stdin=<second line>",
				} {
					if !strings.Contains(result.output, want) {
						t.Errorf("output missing %q\n%s", want, result.output)
					}
				}
			})
		}
	}

	t.Run("hook path with spaces", func(t *testing.T) {
		result := runGeneratedHookChainProcess(t, hookChainProcessCase{
			chainBody: hookProcessChainRecorderBody,
			dirName:   "hooks dir with spaces",
			args:      args,
			chainExit: 5,
		})
		if result.exitCode != 5 || result.chainRuns != 1 {
			t.Fatalf("hook exit = %d with %d chain runs, want 5 and one run\n%s", result.exitCode, result.chainRuns, result.output)
		}
		assertHookChainInvocation(t, result.output, args...)
	})

	t.Run("slash-less $0 finds the chain beside the hook", func(t *testing.T) {
		result := runGeneratedHookChainProcess(t, hookChainProcessCase{
			chainBody:  hookProcessChainRecorderBody,
			usePOSIXSh: true,
			slashless:  true,
			chainExit:  6,
		})
		if result.exitCode != 6 || result.chainRuns != 1 {
			t.Fatalf("hook exit = %d with %d chain runs, want 6 and one run\n%s", result.exitCode, result.chainRuns, result.output)
		}
	})

	t.Run("each managed hook chains its own .old", func(t *testing.T) {
		for _, hookName := range managedHookNames {
			result := runGeneratedHookChainProcess(t, hookChainProcessCase{
				hookName:  hookName,
				chainBody: hookProcessChainRecorderBody,
				args:      []string{"only-arg"},
				chainExit: 9,
			})
			if result.exitCode != 9 || result.chainRuns != 1 {
				t.Errorf("%s: hook exit = %d with %d chain runs, want 9 and one run\n%s", hookName, result.exitCode, result.chainRuns, result.output)
			}
			assertHookChainInvocation(t, result.output, "only-arg")
		}
	})
}

// TestGeneratedHookChainFallbackClassification pins which <hook>.old files the
// bd-missing fallback runs. A beads callback is skipped: it would re-enter bd,
// which is what runChainedHook's getHookVersion check refuses (GH#843,
// GH#1120). A foreign hook that only resembles one runs. Each fixture records
// a run and exits 1, so a skipped chain leaves the hook at 0 with no run, and
// each decision is checked against getHookVersion as well.
func TestGeneratedHookChainFallbackClassification(t *testing.T) {
	const record = "printf 'run\\n' >> \"$HOOK_TEST_CHAIN_COUNT\"\nexit 1\n"
	tests := []struct {
		name     string
		body     string
		wantSkip bool
		// goSkip is getHookVersion's decision when it differs from the shell's
		// on purpose; nil means the two agree.
		goSkip *bool
	}{
		{name: "section marker", body: "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.40.0 ---\n# --- END BEADS INTEGRATION v0.40.0 ---\n" + record, wantSkip: true},
		{name: "section marker mid-line", body: "#!/bin/sh\necho '# --- BEGIN BEADS INTEGRATION v0.40.0 ---'\n" + record, wantSkip: true},
		{name: "thin shim", body: "#!/bin/sh\n# bd-shim v1\n" + record, wantSkip: true},
		{name: "indented shim marker", body: "#!/bin/sh\n  # bd-shim v2\n" + record, wantSkip: true},
		{name: "legacy version marker", body: "#!/bin/sh\n# bd-hooks-version: 0.55.0\n" + record, wantSkip: true},
		{name: "inline hook marker", body: "#!/bin/sh\n# bd (beads) pre-push hook\n" + record, wantSkip: true},
		{name: "delegator", body: "#!/bin/sh\nbd hooks run pre-push \"$@\"\n" + record, wantSkip: true},
		{name: "exec delegator", body: "#!/bin/sh\nexec bd hooks run pre-push \"$@\"\n" + record, wantSkip: true},
		{name: "commented delegator", body: "#!/bin/sh\n# bd hooks run pre-push\n" + record, wantSkip: true},
		{name: "substituted delegator", body: "#!/bin/sh\n_out=\"$(bd hooks run pre-push)\"\n" + record, wantSkip: true},
		{name: "bare delegator at end of file", body: "#!/bin/sh\n" + record + "bd hooks run", wantSkip: true},
		{name: "CRLF shim", body: "#!/bin/sh\r\n# bd-shim v1\r\n" + record, wantSkip: true},
		{name: "CRLF delegator", body: "#!/bin/sh\r\nbd hooks run\r\n" + record, wantSkip: true},
		{name: "marker on a last line without newline", body: "#!/bin/sh\n" + record + "# bd-shim v1", wantSkip: true},

		{name: "foreign NOTE comment", body: "#!/bin/sh\n# --- NOTE: not a BEADS INTEGRATION marker\n" + record},
		{name: "foreign BEGIN with a word between", body: "#!/bin/sh\n# --- BEGIN foo BEADS INTEGRATION\n" + record},
		{name: "foreign BEGIN with doubled space", body: "#!/bin/sh\n# --- BEGIN BEADS  INTEGRATION\n" + record},
		{name: "mid-line legacy version text", body: "#!/bin/sh\necho '# bd-hooks-version: 1'\n" + record},
		{name: "inline marker without space", body: "#!/bin/sh\n# bd(beads)\n" + record},
		{name: "longer command name", body: "#!/bin/sh\n# mybd hooks run\n" + record},
		{name: "longer subcommand", body: "#!/bin/sh\n# bd hooks runner\n" + record},
		{name: "plain foreign gate", body: "#!/bin/sh\n" + record},
		// Go's \s+ also accepts runs of whitespace; the shell matches single
		// spaces only and runs the rest, which cannot recurse with bd missing.
		{name: "whitespace-run delegator", body: "#!/bin/sh\n# bd  hooks\trun\n" + record, goSkip: hookProcessBool(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runGeneratedHookChainProcess(t, hookChainProcessCase{chainBody: tt.body, usePOSIXSh: true, shellOption: "eu"})
			if tt.wantSkip {
				if result.exitCode != 0 || result.chainRuns != 0 || !result.trailingRan {
					t.Errorf("beads callback was not skipped: exit %d, %d chain runs, trailing ran %v\n%s", result.exitCode, result.chainRuns, result.trailingRan, result.output)
				}
			} else if result.exitCode != 1 || result.chainRuns != 1 || result.trailingRan {
				t.Errorf("foreign gate did not decide the hook: exit %d, %d chain runs, trailing ran %v\n%s", result.exitCode, result.chainRuns, result.trailingRan, result.output)
			}

			goWant := tt.wantSkip
			if tt.goSkip != nil {
				goWant = *tt.goSkip
			}
			path := filepath.Join(t.TempDir(), "pre-push.old")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			info, err := getHookVersion(path)
			if err != nil {
				t.Fatalf("getHookVersion: %v", err)
			}
			if got := info.IsBdHook || info.IsExternalIntegration; got != goWant {
				t.Errorf("getHookVersion skip = %v, want %v (%+v)", got, goWant, info)
			}
		})
	}

	t.Run("absent chain falls through", func(t *testing.T) {
		result := runGeneratedHookChainProcess(t, hookChainProcessCase{usePOSIXSh: true, shellOption: "eu"})
		if result.exitCode != 0 || !result.trailingRan || strings.TrimSpace(strings.ReplaceAll(result.output, "trailing-ran", "")) != "" {
			t.Errorf("want a silent fall-through, got exit %d\n%s", result.exitCode, result.output)
		}
	})
	t.Run("non-executable chain falls through", func(t *testing.T) {
		result := runGeneratedHookChainProcess(t, hookChainProcessCase{chainBody: "#!/bin/sh\n" + record, chainMode: 0o644, usePOSIXSh: true, shellOption: "eu"})
		if result.exitCode != 0 || result.chainRuns != 0 || !result.trailingRan {
			t.Errorf("want a silent fall-through, got exit %d with %d runs\n%s", result.exitCode, result.chainRuns, result.output)
		}
	})
	t.Run("directory chain falls through", func(t *testing.T) {
		result := runGeneratedHookChainProcess(t, hookChainProcessCase{chainIsDir: true, usePOSIXSh: true, shellOption: "eu"})
		if result.exitCode != 0 || !result.trailingRan {
			t.Errorf("want a silent fall-through, got exit %d\n%s", result.exitCode, result.output)
		}
	})
	t.Run("unreadable executable chain is attempted", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode 0111 file")
		}
		result := runGeneratedHookChainProcess(t, hookChainProcessCase{chainBody: "#!/bin/sh\n" + record, chainMode: 0o111, usePOSIXSh: true, shellOption: "eu"})
		if result.exitCode == 0 || result.trailingRan {
			t.Errorf("an unreadable executable chain was treated as a beads callback: exit %d\n%s", result.exitCode, result.output)
		}
	})
}

func hookProcessBool(value bool) *bool {
	return &value
}
