package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

type recordingRunner struct {
	paths          map[string]string
	goEnvOutput    string
	goEnvStderr    string
	goEnvErr       error
	runErrors      []error
	outputCommands []commandSpec
	runCommands    []commandSpec
}

func (runner *recordingRunner) lookPath(name string) (string, error) {
	if path, ok := runner.paths[name]; ok {
		return path, nil
	}
	return "", errors.New("synthetic missing command")
}

func (runner *recordingRunner) output(_ context.Context, spec commandSpec) ([]byte, []byte, error) {
	runner.outputCommands = append(runner.outputCommands, spec)
	return []byte(runner.goEnvOutput), []byte(runner.goEnvStderr), runner.goEnvErr
}

func (runner *recordingRunner) run(_ context.Context, spec commandSpec, stdout, _ io.Writer) error {
	runner.runCommands = append(runner.runCommands, spec)
	_, _ = io.WriteString(stdout, "synthetic lint output\n")
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

func TestRunUsesCanonicalNativeAndWindowsPasses(t *testing.T) {
	runner := &recordingRunner{
		paths: map[string]string{
			"go":            "/tools/go",
			"golangci-lint": "/tools/golangci-lint",
		},
		goEnvOutput: `{"GOOS":"linux","CGO_ENABLED":"1"}`,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	environ := []string{
		"PATH=/tools",
		"BD_LINT_NEW_FROM_MERGE_BASE=origin/main",
	}

	code := run(nil, "/repo", environ, &stdout, &stderr, runner)
	if code != 0 {
		t.Fatalf("run exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if len(runner.outputCommands) != 1 {
		t.Fatalf("go env calls = %d, want 1", len(runner.outputCommands))
	}
	wantGoArgs := []string{"env", "-json", "GOOS", "CGO_ENABLED"}
	if got := runner.outputCommands[0].args; !reflect.DeepEqual(got, wantGoArgs) {
		t.Fatalf("go env args = %#v, want %#v", got, wantGoArgs)
	}
	if len(runner.runCommands) != 2 {
		t.Fatalf("lint calls = %d, want 2", len(runner.runCommands))
	}
	wantLintArgs := []string{
		"run",
		"--config=.golangci.yml",
		"--modules-download-mode=readonly",
		"--timeout=5m",
		"--build-tags=gms_pure_go",
		"--new-from-merge-base=origin/main",
		"./...",
	}
	for index, command := range runner.runCommands {
		if command.name != "/tools/golangci-lint" {
			t.Fatalf("lint call %d executable = %q", index, command.name)
		}
		if command.dir != "/repo" {
			t.Fatalf("lint call %d dir = %q, want /repo", index, command.dir)
		}
		if !reflect.DeepEqual(command.args, wantLintArgs) {
			t.Fatalf("lint call %d args = %#v, want %#v", index, command.args, wantLintArgs)
		}
	}
	assertEnvironmentValue(t, runner.runCommands[0].env, "CGO_ENABLED", "1", false)
	assertEnvironmentValue(t, runner.runCommands[0].env, "BEADS_BUILD_TAGS", "gms_pure_go", false)
	assertEnvironmentValue(t, runner.runCommands[0].env, "GOFLAGS", "-tags=gms_pure_go", false)
	assertEnvironmentValue(t, runner.runCommands[1].env, "GOOS", "windows", false)
	assertEnvironmentValue(t, runner.runCommands[1].env, "GOARCH", "amd64", false)
	assertEnvironmentValue(t, runner.runCommands[1].env, "CGO_ENABLED", "0", false)
	assertEnvironmentValue(t, runner.runCommands[1].env, "GOWORK", "off", false)
	for _, heading := range []string{
		"==> golangci-lint (native)",
		"==> golangci-lint (windows/amd64, non-CGO)",
	} {
		if !strings.Contains(stdout.String(), heading) {
			t.Fatalf("missing lane heading %q in output:\n%s", heading, stdout.String())
		}
	}
}

func TestLintArgsKeepsHostileMergeBaseInOneArgument(t *testing.T) {
	mergeBase := `origin/main; printf injected >&2`
	args := lintArgs(mergeBase)
	want := "--new-from-merge-base=" + mergeBase
	count := 0
	for _, arg := range args {
		if arg == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("hostile merge base occurrence count = %d, want 1; args=%#v", count, args)
	}
	if len(args) != 7 {
		t.Fatalf("hostile merge base changed argv cardinality: %#v", args)
	}
}

func TestRunSkipsDuplicateNativeWindowsNonCGOPass(t *testing.T) {
	runner := &recordingRunner{
		paths: map[string]string{
			"go":            "go",
			"golangci-lint": "golangci-lint",
		},
		goEnvOutput: `{"GOOS":"windows","CGO_ENABLED":"0"}`,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run(nil, "/repo", []string{"CGO_ENABLED=0"}, &stdout, &stderr, runner)
	if code != 0 {
		t.Fatalf("run exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if len(runner.runCommands) != 1 {
		t.Fatalf("lint calls = %d, want 1", len(runner.runCommands))
	}
	if !strings.Contains(stdout.String(), "already covered by native pass") {
		t.Fatalf("missing duplicate-pass diagnostic:\n%s", stdout.String())
	}
}

func TestOSProcessRunnerSeparatesStdoutAndStderr(t *testing.T) {
	const helperEnvironment = "BEADS_PR_LINT_OUTPUT_HELPER"
	if os.Getenv(helperEnvironment) == "1" {
		_, _ = io.WriteString(os.Stdout, `{"GOOS":"windows","CGO_ENABLED":"0"}`)
		_, _ = io.WriteString(os.Stderr, "synthetic benign go env warning\n")
		os.Exit(0)
	}

	stdout, stderr, err := (osProcessRunner{}).output(context.Background(), commandSpec{
		name: os.Args[0],
		args: []string{"-test.run=^TestOSProcessRunnerSeparatesStdoutAndStderr$"},
		env:  append(os.Environ(), helperEnvironment+"=1"),
	})
	if err != nil {
		t.Fatalf("output helper failed: %v; stderr=%s", err, stderr)
	}
	if got, want := string(stdout), `{"GOOS":"windows","CGO_ENABLED":"0"}`; got != want {
		t.Fatalf("stdout = %q, want JSON-only %q", got, want)
	}
	if got, want := string(stderr), "synthetic benign go env warning\n"; got != want {
		t.Fatalf("stderr = %q, want warning-only %q", got, want)
	}
}

func TestRunParsesGoEnvStdoutWhenSuccessfulCommandWarnsOnStderr(t *testing.T) {
	runner := &recordingRunner{
		paths: map[string]string{
			"go":            "go",
			"golangci-lint": "golangci-lint",
		},
		goEnvOutput: `{"GOOS":"windows","CGO_ENABLED":"0"}`,
		goEnvStderr: "synthetic benign go env warning\n",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run(nil, "/repo", []string{"CGO_ENABLED=0"}, &stdout, &stderr, runner)
	if code != 0 {
		t.Fatalf("run exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "synthetic benign go env warning") {
		t.Fatalf("go env stderr warning was not surfaced: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "parse native Go target") {
		t.Fatalf("go env stderr corrupted stdout JSON parsing: %q", stderr.String())
	}
	if len(runner.runCommands) != 1 {
		t.Fatalf("lint calls = %d, want one native Windows/non-CGO pass", len(runner.runCommands))
	}
}

func TestRunPreservesNativeLintExitCodeAndStops(t *testing.T) {
	runner := &recordingRunner{
		paths: map[string]string{
			"go":            "go",
			"golangci-lint": "golangci-lint",
		},
		goEnvOutput: `{"GOOS":"linux","CGO_ENABLED":"1"}`,
		runErrors:   []error{syntheticExitError{code: 23}},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run(nil, "/repo", nil, &stdout, &stderr, runner)
	if code != 23 {
		t.Fatalf("run exit = %d, want 23", code)
	}
	if len(runner.runCommands) != 1 {
		t.Fatalf("lint calls = %d, want only failed native pass", len(runner.runCommands))
	}
	if !strings.Contains(stderr.String(), "golangci-lint (native) failed") {
		t.Fatalf("missing native failure diagnostic:\n%s", stderr.String())
	}
}

func TestRunFailsClearlyWhenCapabilityIsMissing(t *testing.T) {
	for _, missing := range []string{"go", "golangci-lint"} {
		t.Run(missing, func(t *testing.T) {
			paths := map[string]string{
				"go":            "go",
				"golangci-lint": "golangci-lint",
			}
			delete(paths, missing)
			runner := &recordingRunner{paths: paths}
			var stderr bytes.Buffer

			if code := run(nil, "/repo", nil, io.Discard, &stderr, runner); code == 0 {
				t.Fatal("missing capability unexpectedly passed")
			}
			if !strings.Contains(strings.ToLower(stderr.String()), missing) || !strings.Contains(stderr.String(), "PATH") {
				t.Fatalf("missing %s diagnostic is unclear: %q", missing, stderr.String())
			}
		})
	}
}

func TestBuildEnvironmentMatchesBuildFlagsContract(t *testing.T) {
	environ := []string{
		"PATH=/tools",
		"CGO_ENABLED=0",
		"GOFLAGS=-mod=readonly",
		"BEADS_BUILD_TAGS=stale",
	}
	got := buildEnvironment(environ, false)
	assertEnvironmentValue(t, got, "CGO_ENABLED", "0", false)
	assertEnvironmentValue(t, got, "GOFLAGS", "-mod=readonly -tags=gms_pure_go", false)
	assertEnvironmentValue(t, got, "BEADS_BUILD_TAGS", "gms_pure_go", false)

	defaulted := buildEnvironment([]string{"CGO_ENABLED="}, false)
	assertEnvironmentValue(t, defaulted, "CGO_ENABLED", "1", false)
	assertEnvironmentValue(t, defaulted, "GOFLAGS", "-tags=gms_pure_go", false)
}

func TestBuildEnvironmentNormalizesWindowsKeyIdentity(t *testing.T) {
	environ := []string{
		"cgo_enabled=0",
		"CGO_ENABLED=1",
		"GoFlags=-mod=readonly",
		"GOFLAGS=-trimpath",
		"beads_build_tags=stale",
		"BEADſ_BUILD_TAGS=near-collision",
		"MALFORMED",
		"=C:=C:\\source\\beads",
	}
	got := buildEnvironment(environ, true)

	assertSingleLogicalEnvironmentKey(t, got, "CGO_ENABLED")
	assertSingleLogicalEnvironmentKey(t, got, "GOFLAGS")
	assertSingleLogicalEnvironmentKey(t, got, "BEADS_BUILD_TAGS")
	assertEnvironmentValue(t, got, "CGO_ENABLED", "1", true)
	assertEnvironmentValue(t, got, "GOFLAGS", "-trimpath -tags=gms_pure_go", true)
	assertEnvironmentValue(t, got, "BEADS_BUILD_TAGS", "gms_pure_go", true)
	for _, preserved := range []string{"BEADſ_BUILD_TAGS=near-collision", "MALFORMED", "=C:=C:\\source\\beads"} {
		if !containsExact(got, preserved) {
			t.Fatalf("environment entry %q was not preserved in %#v", preserved, got)
		}
	}
}

func TestRunRejectsArguments(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"--unexpected"}, "/repo", nil, io.Discard, &stderr, &recordingRunner{})
	if code != 2 {
		t.Fatalf("run exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("missing usage diagnostic: %q", stderr.String())
	}
}

func assertEnvironmentValue(t *testing.T, environ []string, key, want string, caseInsensitive bool) {
	t.Helper()
	got, found := environmentValue(environ, key, caseInsensitive)
	if !found || got != want {
		t.Fatalf("%s = %q, found=%v, want %q; env=%#v", key, got, found, want, environ)
	}
}

func assertSingleLogicalEnvironmentKey(t *testing.T, environ []string, wanted string) {
	t.Helper()
	count := 0
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.ToLower(key) == strings.ToLower(wanted) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("logical environment key %s occurs %d times in %#v", wanted, count, environ)
	}
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
