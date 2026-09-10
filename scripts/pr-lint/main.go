// pr-lint runs the repository's canonical Go lint passes without requiring
// Make or Bash. The supported scripts/ci/pr-lint.sh entrypoint delegates here,
// and bd preflight runs this checkout-owned command for the Beads source tree.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	beadsBuildTags      = "gms_pure_go"
	goEnvTimeout        = 30 * time.Second
	lintProcessTimeout  = 6 * time.Minute
	lintReportedTimeout = "5m"
)

type commandSpec struct {
	name string
	args []string
	dir  string
	env  []string
}

type processRunner interface {
	lookPath(string) (string, error)
	output(context.Context, commandSpec) ([]byte, []byte, error)
	run(context.Context, commandSpec, io.Writer, io.Writer) error
}

type osProcessRunner struct{}

func (osProcessRunner) lookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (osProcessRunner) output(ctx context.Context, spec commandSpec) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, spec.name, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func (osProcessRunner) run(ctx context.Context, spec commandSpec, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, spec.name, spec.args...) //nolint:gosec // G702: executable is PATH-resolved; argv positions are assembled internally, and the env merge-base remains one unshelled argv value.
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

type nativeGoEnvironment struct {
	GOOS       string `json:"GOOS"`
	CGOEnabled string `json:"CGO_ENABLED"`
}

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "determine repository root: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(os.Args[1:], dir, os.Environ(), os.Stdout, os.Stderr, osProcessRunner{}))
}

func run(args []string, dir string, environ []string, stdout, stderr io.Writer, runner processRunner) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: go run -mod=readonly -tags=gms_pure_go ./scripts/pr-lint")
		return 2
	}

	goPath, err := runner.lookPath("go")
	if err != nil {
		fmt.Fprintf(stderr, "Go toolchain not found in PATH: %v\n", err)
		return 1
	}
	lintPath, err := runner.lookPath("golangci-lint")
	if err != nil {
		fmt.Fprintf(stderr, "golangci-lint not found in PATH: %v\n", err)
		return 1
	}

	effectiveEnv := buildEnvironment(environ, runtime.GOOS == "windows")
	native, code := readNativeGoEnvironment(dir, effectiveEnv, goPath, stderr, runner)
	if code != 0 {
		return code
	}

	mergeBase, _ := environmentValue(effectiveEnv, "BD_LINT_NEW_FROM_MERGE_BASE", runtime.GOOS == "windows")
	argsForLint := lintArgs(mergeBase)
	if code := runLintPass(
		"golangci-lint (native)",
		commandSpec{name: lintPath, args: argsForLint, dir: dir, env: effectiveEnv},
		stdout,
		stderr,
		runner,
	); code != 0 {
		return code
	}

	if native.GOOS == "windows" && native.CGOEnabled == "0" {
		fmt.Fprintln(stdout, "==> golangci-lint (windows/amd64, non-CGO) already covered by native pass")
		return 0
	}

	windowsEnv := setEnvironment(effectiveEnv, map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      "amd64",
		"GOOS":        "windows",
		"GOWORK":      "off",
	}, runtime.GOOS == "windows")
	return runLintPass(
		"golangci-lint (windows/amd64, non-CGO)",
		commandSpec{name: lintPath, args: argsForLint, dir: dir, env: windowsEnv},
		stdout,
		stderr,
		runner,
	)
}

func readNativeGoEnvironment(
	dir string,
	environ []string,
	goPath string,
	stderr io.Writer,
	runner processRunner,
) (nativeGoEnvironment, int) {
	ctx, cancel := context.WithTimeout(context.Background(), goEnvTimeout)
	defer cancel()

	spec := commandSpec{
		name: goPath,
		args: []string{"env", "-json", "GOOS", "CGO_ENABLED"},
		dir:  dir,
		env:  environ,
	}
	output, commandStderr, err := runner.output(ctx, spec)
	writeDiagnostic(stderr, commandStderr)
	if err != nil {
		writeDiagnostic(stderr, output)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fmt.Fprintf(stderr, "go env exceeded %s\n", goEnvTimeout)
			return nativeGoEnvironment{}, 1
		}
		fmt.Fprintf(stderr, "inspect native Go target: %v\n", err)
		return nativeGoEnvironment{}, processExitCode(err)
	}

	var native nativeGoEnvironment
	if err := json.Unmarshal(output, &native); err != nil {
		fmt.Fprintf(stderr, "parse native Go target: %v\n", err)
		return nativeGoEnvironment{}, 1
	}
	if native.GOOS == "" || native.CGOEnabled == "" {
		fmt.Fprintf(stderr, "go env returned an incomplete target: GOOS=%q CGO_ENABLED=%q\n", native.GOOS, native.CGOEnabled)
		return nativeGoEnvironment{}, 1
	}
	return native, 0
}

func writeDiagnostic(destination io.Writer, output []byte) {
	if len(output) == 0 {
		return
	}
	_, _ = destination.Write(output)
	if output[len(output)-1] != '\n' {
		fmt.Fprintln(destination)
	}
}

func runLintPass(label string, spec commandSpec, stdout, stderr io.Writer, runner processRunner) int {
	fmt.Fprintf(stdout, "==> %s\n", label)
	ctx, cancel := context.WithTimeout(context.Background(), lintProcessTimeout)
	defer cancel()

	err := runner.run(ctx, spec, stdout, stderr)
	if err == nil {
		fmt.Fprintf(stdout, "<== %s succeeded\n", label)
		return 0
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(stderr, "<== %s exceeded %s\n", label, lintProcessTimeout)
		return 1
	}
	fmt.Fprintf(stderr, "<== %s failed: %v\n", label, err)
	return processExitCode(err)
}

func lintArgs(mergeBase string) []string {
	args := []string{
		"run",
		"--config=.golangci.yml",
		"--modules-download-mode=readonly",
		"--timeout=" + lintReportedTimeout,
		"--build-tags=" + beadsBuildTags,
	}
	if mergeBase != "" {
		args = append(args, "--new-from-merge-base="+mergeBase)
	}
	return append(args, "./...")
}

func buildEnvironment(environ []string, caseInsensitive bool) []string {
	cgoEnabled, found := environmentValue(environ, "CGO_ENABLED", caseInsensitive)
	if !found || cgoEnabled == "" {
		cgoEnabled = "1"
	}

	goFlags, _ := environmentValue(environ, "GOFLAGS", caseInsensitive)
	if !strings.Contains(goFlags, beadsBuildTags) {
		if goFlags != "" {
			goFlags += " "
		}
		goFlags += "-tags=" + beadsBuildTags
	}

	return setEnvironment(environ, map[string]string{
		"BEADS_BUILD_TAGS": beadsBuildTags,
		"CGO_ENABLED":      cgoEnabled,
		"GOFLAGS":          goFlags,
	}, caseInsensitive)
}

func setEnvironment(environ []string, overrides map[string]string, caseInsensitive bool) []string {
	result := make([]string, 0, len(environ)+len(overrides))
	for _, entry := range environ {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			result = append(result, entry)
			continue
		}
		if containsEnvironmentKey(overrides, key, caseInsensitive) {
			continue
		}
		result = append(result, entry)
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func environmentValue(environ []string, wanted string, caseInsensitive bool) (string, bool) {
	for index := len(environ) - 1; index >= 0; index-- {
		key, value, ok := strings.Cut(environ[index], "=")
		if ok && environmentKeysEqual(key, wanted, caseInsensitive) {
			return value, true
		}
	}
	return "", false
}

func containsEnvironmentKey(values map[string]string, wanted string, caseInsensitive bool) bool {
	for key := range values {
		if environmentKeysEqual(key, wanted, caseInsensitive) {
			return true
		}
	}
	return false
}

func environmentKeysEqual(left, right string, caseInsensitive bool) bool {
	if caseInsensitive {
		return strings.ToLower(left) == strings.ToLower(right)
	}
	return left == right
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode()
	}
	return 1
}
