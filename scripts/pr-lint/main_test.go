package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type recordingRunner struct {
	paths       map[string]string
	runErrors   []error
	runCommands []commandSpec
}

func (runner *recordingRunner) lookPath(name string) (string, error) {
	if path, ok := runner.paths[name]; ok {
		return path, nil
	}
	return "", errors.New("synthetic missing command")
}

func (runner *recordingRunner) run(_ context.Context, spec commandSpec, stdout, _ io.Writer) error {
	runner.runCommands = append(runner.runCommands, spec)
	_, _ = io.WriteString(stdout, "synthetic bazel output\n")
	index := len(runner.runCommands) - 1
	if index < len(runner.runErrors) {
		return runner.runErrors[index]
	}
	return nil
}

type syntheticExitError struct {
	code int
}

func (err syntheticExitError) Error() string {
	return "synthetic command failure"
}

func (err syntheticExitError) ExitCode() int {
	return err.code
}

func newRunner() *recordingRunner {
	return &recordingRunner{paths: map[string]string{"bazel": "/bin/bazel"}}
}

var nativeArgs = []string{"build", "--config=nogo", "//..."}

func crossArgs(platforms string) []string {
	return []string{"build", "--config=nogo-cross", "--//tools/bazel:release_platforms=" + platforms, "//tools/bazel:release_cross"}
}

func commandArgs(runner *recordingRunner) [][]string {
	var got [][]string
	for _, spec := range runner.runCommands {
		got = append(got, spec.args)
	}
	return got
}

func TestRunsNativeThenTheWindowsAndDarwinCrossPass(t *testing.T) {
	runner := newRunner()
	var stdout, stderr bytes.Buffer
	if code := run(nil, "/repo", []string{"PATH=/bin"}, &stdout, &stderr, runner); code != 0 {
		t.Fatalf("run() = %d, stderr:\n%s", code, stderr.String())
	}
	for _, spec := range runner.runCommands {
		if spec.name != "/bin/bazel" || spec.dir != "/repo" {
			t.Errorf("command %q in %q, want /bin/bazel in /repo", spec.name, spec.dir)
		}
	}
	want := [][]string{nativeArgs, crossArgs("windows_amd64,darwin_arm64")}
	if got := commandArgs(runner); !reflect.DeepEqual(got, want) {
		t.Errorf("bazel commands = %q, want %q", got, want)
	}
	for _, label := range []string{"nogo (native)", "nogo (windows_amd64, darwin_arm64; non-cgo)"} {
		if !strings.Contains(stdout.String(), "==> "+label+"\n") || !strings.Contains(stdout.String(), "<== "+label+" succeeded\n") {
			t.Errorf("stdout lacks the %q pass markers:\n%s", label, stdout.String())
		}
	}
}

func TestBDLintTargetsSelectsPasses(t *testing.T) {
	for _, c := range []struct {
		targets string
		want    [][]string
	}{
		{"native", [][]string{nativeArgs}},
		{" darwin , darwin", [][]string{crossArgs("darwin_arm64")}},
		{"windows", [][]string{crossArgs("windows_amd64")}},
		// Order and repetition do not matter.
		{"darwin,native,windows", [][]string{nativeArgs, crossArgs("windows_amd64,darwin_arm64")}},
		{" , ", [][]string{nativeArgs, crossArgs("windows_amd64,darwin_arm64")}},
	} {
		runner := newRunner()
		var stdout, stderr bytes.Buffer
		if code := run(nil, "/repo", []string{"BD_LINT_TARGETS=" + c.targets}, &stdout, &stderr, runner); code != 0 {
			t.Fatalf("%q: run() = %d, stderr:\n%s", c.targets, code, stderr.String())
		}
		if got := commandArgs(runner); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: bazel commands = %q, want %q", c.targets, got, c.want)
		}
	}
}

func TestRejectsUnknownTargetAndArguments(t *testing.T) {
	runner := newRunner()
	var stdout, stderr bytes.Buffer
	if code := run(nil, "/repo", []string{"BD_LINT_TARGETS=native,linux"}, &stdout, &stderr, runner); code != 2 {
		t.Errorf("unknown target: run() = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown target "linux"`) {
		t.Errorf("stderr = %q, want the unknown target named", stderr.String())
	}
	if code := run([]string{"./..."}, "/repo", nil, &stdout, &stderr, runner); code != 2 {
		t.Errorf("arguments: run() = %d, want 2", code)
	}
	if len(runner.runCommands) != 0 {
		t.Errorf("ran %+v after a usage error", runner.runCommands)
	}
}

func TestStopsAtTheFirstFailingPassWithItsExitCode(t *testing.T) {
	runner := newRunner()
	runner.runErrors = []error{syntheticExitError{code: 3}}
	var stdout, stderr bytes.Buffer
	if code := run(nil, "/repo", nil, &stdout, &stderr, runner); code != 3 {
		t.Fatalf("run() = %d, want the native pass's 3", code)
	}
	if len(runner.runCommands) != 1 {
		t.Errorf("ran %d passes after the native pass failed", len(runner.runCommands))
	}
	if !strings.Contains(stderr.String(), "<== nogo (native) failed") {
		t.Errorf("stderr = %q, want the failed pass named", stderr.String())
	}
	runner = newRunner()
	runner.runErrors = []error{nil, errors.New("no exit code")}
	stderr.Reset()
	if code := run(nil, "/repo", nil, &stdout, &stderr, runner); code != 1 {
		t.Fatalf("run() = %d, want 1 for a failure without an exit code", code)
	}
}

func TestBazelLookup(t *testing.T) {
	runner := &recordingRunner{paths: map[string]string{"bazelisk": "/bin/bazelisk", "custom": "/opt/custom"}}
	var stdout, stderr bytes.Buffer
	// No bazel on PATH: bazelisk is the same launcher.
	if code := run(nil, "/repo", []string{"BD_LINT_TARGETS=native"}, &stdout, &stderr, runner); code != 0 || runner.runCommands[0].name != "/bin/bazelisk" {
		t.Fatalf("run() = %d with %+v, want bazelisk", code, runner.runCommands)
	}
	// $BAZEL names the launcher, as the Makefile's BAZEL does.
	if code := run(nil, "/repo", []string{"BD_LINT_TARGETS=native", "BAZEL=custom"}, &stdout, &stderr, runner); code != 0 || runner.runCommands[1].name != "/opt/custom" {
		t.Fatalf("run() = %d with %+v, want $BAZEL", code, runner.runCommands)
	}
	missing := &recordingRunner{}
	stderr.Reset()
	if code := run(nil, "/repo", nil, &stdout, &stderr, missing); code != 1 || !strings.Contains(stderr.String(), "bazel not found") {
		t.Fatalf("run() = %d, stderr %q; want a missing-bazel error", code, stderr.String())
	}
}

func TestEnvironmentLookupIsCaseInsensitiveOnlyOnWindows(t *testing.T) {
	runner := newRunner()
	var stdout, stderr bytes.Buffer
	if code := run(nil, "/repo", []string{"bd_lint_targets=native"}, &stdout, &stderr, runner); code != 0 {
		t.Fatalf("run() = %d", code)
	}
	wantPasses := 2 // the lower-case name is a different variable: every pass
	if runtime.GOOS == "windows" {
		wantPasses = 1
	}
	if len(runner.runCommands) != wantPasses {
		t.Errorf("%d passes, want %d", len(runner.runCommands), wantPasses)
	}
}
