package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestConformanceScriptUsesExplicitTimeoutBudgets(t *testing.T) {
	run := runConformanceScript(t, 0, 0)
	if run.err != nil {
		t.Fatalf("conformance.sh failed: %v\n%s", run.err, run.output)
	}

	want := [][]string{
		{"test", "-tags", "gms_pure_go", "-v", "-timeout", "30m", "./internal/storage/embeddeddolt/", "-run", "TestConformance"},
		{"test", "-tags", "gms_pure_go e2e", "-timeout", "10m", "./test/conformance/"},
	}
	if !reflect.DeepEqual(run.calls, want) {
		t.Fatalf("go calls = %#v, want %#v", run.calls, want)
	}
}

func TestConformanceScriptPropagatesGoTestFailures(t *testing.T) {
	tests := []struct {
		name      string
		failCall  int
		exitCode  int
		wantCalls int
	}{
		{name: "tier 1", failCall: 1, exitCode: 41, wantCalls: 1},
		{name: "tier 2", failCall: 2, exitCode: 42, wantCalls: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := runConformanceScript(t, test.failCall, test.exitCode)
			if got := conformanceExitCode(run.err); got != test.exitCode {
				t.Fatalf("exit = %d, want %d; error=%v\n%s", got, test.exitCode, run.err, run.output)
			}
			if len(run.calls) != test.wantCalls {
				t.Fatalf("go call count = %d, want %d; calls=%#v", len(run.calls), test.wantCalls, run.calls)
			}
		})
	}
}

func conformanceExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func TestConformanceWorkflowHasOuterTimeoutBudget(t *testing.T) {
	path := filepath.Join(sourceRepoRoot(t), ".github", "workflows", "conformance.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	want := "  conformance:\n" +
		"    name: Storage backend conformance (embedded Dolt oracle)\n" +
		"    timeout-minutes: 45\n" +
		"    runs-on: ubuntu-latest\n"
	if !strings.Contains(text, want) {
		t.Fatalf("conformance job does not declare the maintained 45-minute outer budget:\n%s", text)
	}
}

type conformanceRun struct {
	output string
	calls  [][]string
	err    error
}

func runConformanceScript(t *testing.T, failCall, failExit int) conformanceRun {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is required to test conformance.sh: %v", err)
	}

	bin := t.TempDir()
	stateDir := t.TempDir()
	callLog := filepath.Join(stateDir, "go-calls")
	if err := os.WriteFile(callLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "go"), `#!/bin/sh
set -eu
count=0
if [ -s "$GO_CALL_COUNT" ]; then
  count="$(cat "$GO_CALL_COUNT")"
fi
count=$((count + 1))
printf '%s\n' "$count" >"$GO_CALL_COUNT"
printf '%s\0' "$#" >>"$GO_CALL_LOG"
printf '%s\0' "$@" >>"$GO_CALL_LOG"
if [ "$count" = "$GO_FAIL_CALL" ]; then
  exit "$GO_FAIL_EXIT"
fi
if [ "$count" = 1 ]; then
  printf '%s\n' '--- PASS: TestConformance (0.01s)'
fi
`)

	root := sourceRepoRoot(t)
	binPath := shellPath(t, bin)
	statePath := shellPath(t, stateDir)
	path := binPath + ":" + os.Getenv("PATH") + ":/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		path = binPath + ":/usr/bin:/bin"
	}
	cmd := exec.Command(bash, "scripts/conformance.sh")
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + path,
		"HOME=" + statePath,
		"LC_ALL=C",
		"LANG=C",
		"BASH_ENV=",
		"ENV=",
		"GO_CALL_LOG=" + statePath + "/go-calls",
		"GO_CALL_COUNT=" + statePath + "/go-call-count",
		"GO_FAIL_CALL=" + strconv.Itoa(failCall),
		"GO_FAIL_EXIT=" + strconv.Itoa(failExit),
	}
	output, runErr := cmd.CombinedOutput()
	log, readErr := os.ReadFile(callLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return conformanceRun{output: string(output), calls: parseArgvCalls(t, log), err: runErr}
}

func parseArgvCalls(t *testing.T, log []byte) [][]string {
	t.Helper()
	tokens := strings.Split(string(log), "\x00")
	if len(tokens) == 0 || tokens[len(tokens)-1] != "" {
		t.Fatalf("invalid NUL-delimited argv log: %q", log)
	}
	tokens = tokens[:len(tokens)-1]

	var calls [][]string
	for len(tokens) > 0 {
		count, err := strconv.Atoi(tokens[0])
		if err != nil || count < 0 || len(tokens) < count+1 {
			t.Fatalf("invalid NUL-delimited argv log: %q", log)
		}
		calls = append(calls, append([]string(nil), tokens[1:count+1]...))
		tokens = tokens[count+1:]
	}
	return calls
}
