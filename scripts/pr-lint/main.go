// pr-lint runs the repository's canonical Go lint passes without requiring
// Make or Bash. The supported scripts/ci/pr-lint.sh entrypoint delegates here,
// and bd preflight runs this checkout-owned command for the Beads source tree.
// The standalone workflow lint action and pre-commit hook do not call this
// driver; their scope and behavior still differ.
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
	"path/filepath"
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
	GOROOT     string `json:"GOROOT"`
	GOVERSION  string `json:"GOVERSION"`
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
	probeCtx, cancel := context.WithTimeout(context.Background(), goEnvTimeout)
	defer cancel()
	native, code := readNativeGoEnvironment(probeCtx, dir, effectiveEnv, goPath, stderr, runner)
	if code != 0 {
		return code
	}

	mergeBase, _ := environmentValue(effectiveEnv, "BD_LINT_NEW_FROM_MERGE_BASE", runtime.GOOS == "windows")
	argsForLint := lintArgs(mergeBase)
	skipWindows := native.GOOS == "windows" && native.CGOEnabled == "0"
	windowsEnv := setEnvironment(effectiveEnv, map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      "amd64",
		"GOOS":        "windows",
		"GOWORK":      "off",
	}, runtime.GOOS == "windows")
	// Files guarded by //go:build darwin (and the !windows && !linux fallbacks)
	// are invisible to the Linux runner too, so a darwin-only finding must fail
	// the PR here instead of on the next maintainer's laptop.
	skipDarwin := native.GOOS == "darwin" && native.CGOEnabled == "0"
	darwinEnv := setEnvironment(effectiveEnv, map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      "arm64",
		"GOOS":        "darwin",
		"GOWORK":      "off",
	}, runtime.GOOS == "windows")
	nativeEnv := effectiveEnv
	if runtime.GOOS == "windows" {
		nativeEnv = preferSelectedGo(probeCtx, dir, effectiveEnv, native, stderr, runner)
		if !skipWindows {
			// GOWORK=off can select a different toolchain. Probe the original
			// Go executable and PATH again, before either lint pass starts.
			selected, code := readNativeGoEnvironment(probeCtx, dir, windowsEnv, goPath, stderr, runner)
			if code == 0 {
				windowsEnv = preferSelectedGo(probeCtx, dir, windowsEnv, selected, stderr, runner)
			}
		}
		if !skipDarwin {
			selected, code := readNativeGoEnvironment(probeCtx, dir, darwinEnv, goPath, stderr, runner)
			if code == 0 {
				darwinEnv = preferSelectedGo(probeCtx, dir, darwinEnv, selected, stderr, runner)
			}
		}
	}
	cancel() // All discovery shares the existing 30-second probe budget.
	if code := runLintPass(
		"golangci-lint (native)",
		commandSpec{name: lintPath, args: argsForLint, dir: dir, env: nativeEnv},
		stdout,
		stderr,
		runner,
	); code != 0 {
		return code
	}

	if skipWindows {
		fmt.Fprintln(stdout, "==> golangci-lint (windows/amd64, non-CGO) already covered by native pass")
	} else if code := runLintPass(
		"golangci-lint (windows/amd64, non-CGO)",
		commandSpec{name: lintPath, args: argsForLint, dir: dir, env: windowsEnv},
		stdout,
		stderr,
		runner,
	); code != 0 {
		return code
	}

	if skipDarwin {
		fmt.Fprintln(stdout, "==> golangci-lint (darwin/arm64, non-CGO) already covered by native pass")
		return 0
	}

	return runLintPass(
		"golangci-lint (darwin/arm64, non-CGO)",
		commandSpec{name: lintPath, args: argsForLint, dir: dir, env: darwinEnv},
		stdout,
		stderr,
		runner,
	)
}

func readNativeGoEnvironment(
	ctx context.Context,
	dir string,
	environ []string,
	goPath string,
	stderr io.Writer,
	runner processRunner,
) (nativeGoEnvironment, int) {
	spec := commandSpec{
		name: goPath,
		args: []string{"env", "-json", "GOOS", "CGO_ENABLED", "GOROOT", "GOVERSION"},
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

// On Windows, Go auto-selection starts another process instead of replacing
// itself. Prefer the selected SDK for lint so cancellation reaches that Go
// process directly. This is not general descendant-process containment.
func preferSelectedGo(ctx context.Context, dir string, environ []string, selected nativeGoEnvironment, stderr io.Writer, runner processRunner) []string {
	if !filepath.IsAbs(selected.GOROOT) || selected.GOVERSION == "" {
		return environ
	}
	bin := filepath.Join(selected.GOROOT, "bin")
	if strings.ContainsRune(bin, os.PathListSeparator) {
		fmt.Fprintln(stderr, "Windows lint: retaining original Go PATH; selected SDK directory contains a PATH separator")
		return environ
	}
	// GOROOT can describe a source-only or custom installation. Verify the
	// candidate without allowing it to switch toolchains during this probe.
	output, diagnostic, err := runner.output(ctx, commandSpec{
		name: filepath.Join(bin, "go.exe"),
		args: []string{"env", "-json", "GOROOT", "GOVERSION"},
		dir:  dir,
		env:  setEnvironment(environ, map[string]string{"GOTOOLCHAIN": "local"}, true),
	})
	writeDiagnostic(stderr, diagnostic)
	var candidate nativeGoEnvironment
	if err != nil || json.Unmarshal(output, &candidate) != nil ||
		candidate.GOVERSION != selected.GOVERSION ||
		!strings.EqualFold(filepath.Clean(candidate.GOROOT), filepath.Clean(selected.GOROOT)) {
		fmt.Fprintln(stderr, "Windows lint: retaining original Go PATH; selected SDK could not be verified")
		return environ
	}
	path, _ := environmentValue(environ, "PATH", true)
	return setEnvironment(environ, map[string]string{"PATH": bin + string(os.PathListSeparator) + path}, true)
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
	// Match .buildflags: an appended -tags value overrides an inherited bare-Go
	// -tags value rather than merging tag lists.
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
