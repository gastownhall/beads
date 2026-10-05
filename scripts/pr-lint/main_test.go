package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

type recordingRunner struct {
	paths          map[string]string
	goEnvOutput    string
	goEnvStderr    string
	goEnvErr       error
	runErrors      []error
	outputCommands []commandSpec
	runCommands    []commandSpec
	outputFunc     func(context.Context, commandSpec) ([]byte, []byte, error)
}

func (runner *recordingRunner) lookPath(name string) (string, error) {
	if path, ok := runner.paths[name]; ok {
		return path, nil
	}
	return "", errors.New("synthetic missing command")
}

func (runner *recordingRunner) output(ctx context.Context, spec commandSpec) ([]byte, []byte, error) {
	runner.outputCommands = append(runner.outputCommands, spec)
	if runner.outputFunc != nil {
		return runner.outputFunc(ctx, spec)
	}
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

func TestRunUsesCanonicalNativeAndCrossTargetPasses(t *testing.T) {
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
	wantProbes := 1
	if runtime.GOOS == "windows" {
		// One native probe plus one per cross-target pass.
		wantProbes = 3
	}
	if len(runner.outputCommands) != wantProbes {
		t.Fatalf("go env calls = %d, want %d", len(runner.outputCommands), wantProbes)
	}
	wantGoArgs := []string{"env", "-json", "GOOS", "CGO_ENABLED", "GOROOT", "GOVERSION"}
	if got := runner.outputCommands[0].args; !reflect.DeepEqual(got, wantGoArgs) {
		t.Fatalf("go env args = %#v, want %#v", got, wantGoArgs)
	}
	if len(runner.runCommands) != 3 {
		t.Fatalf("lint calls = %d, want 3", len(runner.runCommands))
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
	assertEnvironmentValue(t, runner.runCommands[2].env, "GOOS", "darwin", false)
	assertEnvironmentValue(t, runner.runCommands[2].env, "GOARCH", "arm64", false)
	assertEnvironmentValue(t, runner.runCommands[2].env, "CGO_ENABLED", "0", false)
	assertEnvironmentValue(t, runner.runCommands[2].env, "GOWORK", "off", false)
	for _, heading := range []string{
		"==> golangci-lint (native)",
		"==> golangci-lint (windows/amd64, non-CGO)",
		"==> golangci-lint (darwin/arm64, non-CGO)",
	} {
		if !strings.Contains(stdout.String(), heading) {
			t.Fatalf("missing lane heading %q in output:\n%s", heading, stdout.String())
		}
	}
}

// TestRunHonorsBDLintTargetsSelection covers the pr.yml PR Lint matrix
// (F5.2): each leg sets BD_LINT_TARGETS to exactly one of native, windows or
// darwin, and only that pass must run.
func TestRunHonorsBDLintTargetsSelection(t *testing.T) {
	for _, tc := range []struct {
		targets  string
		wantRuns []string // GOOS set on the one command's env, in order
	}{
		{"native", []string{"linux"}},
		{"windows", []string{"windows"}},
		{"darwin", []string{"darwin"}},
		{"native,windows", []string{"linux", "windows"}},
		{" windows , darwin ", []string{"windows", "darwin"}},
		{"windows,windows", []string{"windows"}},
	} {
		t.Run(tc.targets, func(t *testing.T) {
			runner := &recordingRunner{
				paths: map[string]string{
					"go":            "/tools/go",
					"golangci-lint": "/tools/golangci-lint",
				},
				goEnvOutput: `{"GOOS":"linux","CGO_ENABLED":"1"}`,
			}
			var stdout, stderr bytes.Buffer
			environ := []string{"PATH=/tools", "BD_LINT_TARGETS=" + tc.targets}

			code := run(nil, "/repo", environ, &stdout, &stderr, runner)
			if code != 0 {
				t.Fatalf("run exit = %d, want 0; stderr=%s", code, stderr.String())
			}
			if len(runner.runCommands) != len(tc.wantRuns) {
				t.Fatalf("lint calls = %d, want %d (%v); commands=%#v", len(runner.runCommands), len(tc.wantRuns), tc.wantRuns, runner.runCommands)
			}
			for index, wantGOOS := range tc.wantRuns {
				if wantGOOS == "linux" {
					// The native pass does not force a GOOS override.
					if got, found := environmentValue(runner.runCommands[index].env, "GOOS", false); found && got != "" && got != "linux" {
						t.Fatalf("native pass GOOS override = %q", got)
					}
					continue
				}
				assertEnvironmentValue(t, runner.runCommands[index].env, "GOOS", wantGOOS, false)
			}
		})
	}
}

func TestRunDefaultsBDLintTargetsToAllThree(t *testing.T) {
	runner := &recordingRunner{
		paths: map[string]string{
			"go":            "/tools/go",
			"golangci-lint": "/tools/golangci-lint",
		},
		goEnvOutput: `{"GOOS":"linux","CGO_ENABLED":"1"}`,
	}
	var stdout, stderr bytes.Buffer
	// No BD_LINT_TARGETS at all, and a second case with it set but empty:
	// both must run the full native+windows+darwin contract (the no-argument
	// usage contract `make ci-pr-lint` and `bd preflight` depend on).
	for _, environ := range [][]string{
		{"PATH=/tools"},
		{"PATH=/tools", "BD_LINT_TARGETS="},
		{"PATH=/tools", "BD_LINT_TARGETS= , ,"},
	} {
		runner.runCommands = nil
		code := run(nil, "/repo", environ, &stdout, &stderr, runner)
		if code != 0 {
			t.Fatalf("run exit = %d, want 0; stderr=%s", code, stderr.String())
		}
		if len(runner.runCommands) != 3 {
			t.Fatalf("env=%v: lint calls = %d, want 3", environ, len(runner.runCommands))
		}
	}
}

func TestRunRejectsUnknownBDLintTarget(t *testing.T) {
	runner := &recordingRunner{
		paths: map[string]string{
			"go":            "/tools/go",
			"golangci-lint": "/tools/golangci-lint",
		},
		goEnvOutput: `{"GOOS":"linux","CGO_ENABLED":"1"}`,
	}
	var stdout, stderr bytes.Buffer
	environ := []string{"PATH=/tools", "BD_LINT_TARGETS=native,solaris"}

	code := run(nil, "/repo", environ, &stdout, &stderr, runner)
	if code != 2 {
		t.Fatalf("run exit = %d, want 2; stderr=%s", code, stderr.String())
	}
	if len(runner.runCommands) != 0 {
		t.Fatalf("an unknown target must run nothing, got %d lint calls", len(runner.runCommands))
	}
	if !strings.Contains(stderr.String(), "solaris") {
		t.Fatalf("missing the unknown target in the diagnostic: %q", stderr.String())
	}
}

func TestParseLintTargets(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    []string
		wantErr bool
	}{
		{"", []string{"native", "windows", "darwin"}, false},
		{" , ", []string{"native", "windows", "darwin"}, false},
		{"native", []string{"native"}, false},
		{"native,darwin", []string{"native", "darwin"}, false},
		{" native , darwin ", []string{"native", "darwin"}, false},
		{"bogus", nil, true},
		{"native,bogus", nil, true},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseLintTargets(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseLintTargets(%q) = %v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLintTargets(%q) unexpected error: %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseLintTargets(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
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
	// The native pass already covers windows/non-CGO, so only the native and
	// darwin cross-target passes remain.
	if len(runner.runCommands) != 2 {
		t.Fatalf("lint calls = %d, want 2", len(runner.runCommands))
	}
	assertEnvironmentValue(t, runner.runCommands[1].env, "GOOS", "darwin", false)
	if !strings.Contains(stdout.String(), "==> golangci-lint (windows/amd64, non-CGO) already covered by native pass") {
		t.Fatalf("missing duplicate-pass diagnostic:\n%s", stdout.String())
	}
}

func TestRunSkipsDuplicateNativeDarwinNonCGOPass(t *testing.T) {
	runner := &recordingRunner{
		paths: map[string]string{
			"go":            "go",
			"golangci-lint": "golangci-lint",
		},
		goEnvOutput: `{"GOOS":"darwin","CGO_ENABLED":"0"}`,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run(nil, "/repo", []string{"CGO_ENABLED=0"}, &stdout, &stderr, runner)
	if code != 0 {
		t.Fatalf("run exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	// The native pass already covers darwin/non-CGO, so only the native and
	// windows cross-target passes remain.
	if len(runner.runCommands) != 2 {
		t.Fatalf("lint calls = %d, want 2", len(runner.runCommands))
	}
	assertEnvironmentValue(t, runner.runCommands[1].env, "GOOS", "windows", false)
	if !strings.Contains(stdout.String(), "==> golangci-lint (darwin/arm64, non-CGO) already covered by native pass") {
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
	if len(runner.runCommands) != 2 {
		t.Fatalf("lint calls = %d, want the native Windows/non-CGO pass plus the darwin cross-lint", len(runner.runCommands))
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

func TestRunUsesEachWindowsPassSelectedToolchain(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows auto-selection creates the extra process")
	}
	nativeRoot, crossRoot := filepath.Join(t.TempDir(), "workspace SDK"), filepath.Join(t.TempDir(), "module SDK")
	environ := []string{"Path=original", "GOTOOLCHAIN=auto", "GOROOT=custom", "GOWORK=workspace"}
	runner := &recordingRunner{paths: map[string]string{"go": "launcher", "golangci-lint": "lint"}}
	var probeContext context.Context
	runner.outputFunc = func(ctx context.Context, spec commandSpec) ([]byte, []byte, error) {
		if probeContext != nil && ctx != probeContext {
			t.Fatal("toolchain discovery escaped the shared probe deadline")
		}
		probeContext = ctx
		selected := nativeGoEnvironment{GOOS: "windows", CGOEnabled: "1", GOROOT: nativeRoot, GOVERSION: "go1.26.7"}
		if work, _ := environmentValue(spec.env, "GOWORK", true); work == "off" {
			selected.GOROOT, selected.GOVERSION, selected.CGOEnabled = crossRoot, "go1.26.5", "0"
		}
		assertEnvironmentValue(t, spec.env, "PATH", "original", true)
		if spec.name != "launcher" {
			if spec.name != filepath.Join(selected.GOROOT, "bin", "go.exe") {
				t.Fatalf("candidate = %q, root = %q", spec.name, selected.GOROOT)
			}
			assertEnvironmentValue(t, spec.env, "GOTOOLCHAIN", "local", true)
		}
		data, err := json.Marshal(selected)
		return data, nil, err
	}
	var stderr bytes.Buffer
	if code := run(nil, ".", environ, io.Discard, &stderr, runner); code != 0 || len(runner.runCommands) != 2 {
		t.Fatalf("run = %d, passes = %d, stderr=%s", code, len(runner.runCommands), &stderr)
	}
	for index, root := range []string{nativeRoot, crossRoot} {
		env := runner.runCommands[index].env
		assertSingleLogicalEnvironmentKey(t, env, "PATH")
		assertEnvironmentValue(t, env, "PATH", filepath.Join(root, "bin")+";original", true)
		assertEnvironmentValue(t, env, "GOTOOLCHAIN", "auto", true)
		assertEnvironmentValue(t, env, "GOROOT", "custom", true)
	}
	if environ[0] != "Path=original" {
		t.Fatal("lint changed the caller's PATH")
	}
}

func TestSelectedGoFallsBackForNonstandardSDK(t *testing.T) {
	root := t.TempDir()
	selected := nativeGoEnvironment{GOROOT: root, GOVERSION: "go1.26.7"}
	environ := []string{"Path=original", "GOTOOLCHAIN=auto", "GOROOT=custom"}
	t.Run("PATH separator", func(t *testing.T) {
		unsupported := selected
		unsupported.GOROOT += string(os.PathListSeparator) + "SDK"
		data, err := json.Marshal(unsupported)
		if err != nil {
			t.Fatal(err)
		}
		var diagnostic bytes.Buffer
		got := preferSelectedGo(context.Background(), ".", environ, unsupported, &diagnostic, &recordingRunner{goEnvOutput: string(data)})
		if !reflect.DeepEqual(got, environ) || !strings.Contains(diagnostic.String(), "retaining original Go PATH") {
			t.Fatalf("fallback env=%v, diagnostic=%s", got, &diagnostic)
		}
	})
	for _, tc := range []struct {
		name, root, version string
		err                 error
	}{
		{"missing executable", root, "go1.26.7", os.ErrNotExist},
		{"wrong version", root, "go1.26.5", nil},
		{"different root", filepath.Join(root, "other"), "go1.26.7", nil},
		{"deadline", root, "go1.26.7", context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(nativeGoEnvironment{GOROOT: tc.root, GOVERSION: tc.version})
			if err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{goEnvOutput: string(data), goEnvErr: tc.err}
			var diagnostic bytes.Buffer
			got := preferSelectedGo(context.Background(), ".", environ, selected, &diagnostic, runner)
			if !reflect.DeepEqual(got, environ) || !strings.Contains(diagnostic.String(), "retaining original Go PATH") {
				t.Fatalf("fallback env=%v, diagnostic=%s", got, &diagnostic)
			}
		})
	}
}

func TestSelectedGoIsResolvedByLintChild(t *testing.T) {
	const marker = "BEADS_SELECTED_GO_PROBE"
	if expected := os.Getenv(marker); expected != "" {
		resolved, err := exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
		got, gotErr := os.Stat(resolved)
		want, wantErr := os.Stat(expected)
		if gotErr != nil || wantErr != nil || !os.SameFile(got, want) {
			t.Fatalf("child resolved %q, want identity of %q", resolved, expected)
		}
		return
	}
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable discovery")
	}
	root := t.TempDir()
	selectedBin, oldBin := filepath.Join(root, "selected SDK", "bin"), filepath.Join(root, "old SDK", "bin")
	for _, bin := range []string{selectedBin, oldBin} {
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "go.exe"), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	selected := nativeGoEnvironment{GOROOT: filepath.Dir(selectedBin), GOVERSION: "go1.26.7"}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	environ := setEnvironment(os.Environ(), map[string]string{"PATH": oldBin, marker: filepath.Join(selectedBin, "go.exe")}, true)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSelectedGoIsResolvedByLintChild$")
	cmd.Env = preferSelectedGo(context.Background(), root, environ, selected, io.Discard, &recordingRunner{goEnvOutput: string(data)})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lint child Go discovery: %v\n%s", err, output)
	}
}

// TestBuildFlagsContractMatchesShellSource reads .buildflags itself and pins
// the driver constants to it. TestBuildEnvironmentMatchesBuildFlagsContract
// pins today's values from the Go side only, so the two files could drift
// apart silently: buildEnvironment appends its own -tags value and overrides
// rather than merges, which means a tag added to .buildflags alone would be
// dropped by the driver with every existing test still green.
func TestBuildFlagsContractMatchesShellSource(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(bazeltest.CallerDir(thisFile, "scripts/pr-lint")))
	path := filepath.Join(repoRoot, ".buildflags")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(data)

	// export BEADS_BUILD_TAGS="gms_pure_go"
	tags := regexp.MustCompile(`(?m)^export BEADS_BUILD_TAGS="([^"]*)"$`).FindStringSubmatch(source)
	if tags == nil {
		t.Fatalf(".buildflags no longer exports BEADS_BUILD_TAGS in the expected form:\n%s", source)
	}
	if tags[1] != beadsBuildTags {
		t.Errorf("BEADS_BUILD_TAGS drift: .buildflags has %q, driver constant beadsBuildTags is %q; "+
			"buildEnvironment overrides rather than merges -tags, so the shell value would be silently dropped",
			tags[1], beadsBuildTags)
	}

	// : "${CGO_ENABLED:=1}"
	cgo := regexp.MustCompile(`(?m)^: "\$\{CGO_ENABLED:=([^}]*)\}"$`).FindStringSubmatch(source)
	if cgo == nil {
		t.Fatalf(".buildflags no longer defaults CGO_ENABLED in the expected form:\n%s", source)
	}
	if got, _ := environmentValue(buildEnvironment([]string{"CGO_ENABLED="}, false), "CGO_ENABLED", false); got != cgo[1] {
		t.Errorf("CGO_ENABLED default drift: .buildflags defaults to %q, buildEnvironment defaults to %q", cgo[1], got)
	}

	// if [[ "${GOFLAGS:-}" != *gms_pure_go* ]]; then
	guard := regexp.MustCompile(`(?m)^if \[\[ "\$\{GOFLAGS:-\}" != \*([^*]*)\* \]\]; then$`).FindStringSubmatch(source)
	if guard == nil {
		t.Fatalf(".buildflags no longer guards GOFLAGS in the expected form:\n%s", source)
	}
	if guard[1] != beadsBuildTags {
		t.Errorf("GOFLAGS guard drift: .buildflags skips its append when GOFLAGS contains %q, "+
			"driver skips on %q; the two must test the same token or one appends when the other does not",
			guard[1], beadsBuildTags)
	}
	// The shell guard is a substring match (*tag*), and buildEnvironment uses
	// strings.Contains for the same reason. Pin that agreement: making only the
	// Go side stricter would append a tag the wrapper had already decided was
	// present.
	preset := []string{"GOFLAGS=-tags=" + guard[1] + "_x"}
	if got, _ := environmentValue(buildEnvironment(preset, false), "GOFLAGS", false); got != preset[0][len("GOFLAGS="):] {
		t.Errorf("GOFLAGS guard is no longer the substring test .buildflags performs: got %q, want %q",
			got, preset[0][len("GOFLAGS="):])
	}
}
