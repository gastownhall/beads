// pr-lint runs the repository's lint gate without requiring Make or Bash:
// nogo (//tools/nogo: go test's vet checks plus the golangci-lint linters
// .golangci.yml enables) under Bazel. The supported scripts/ci/pr-lint.sh
// entrypoint delegates here, and bd preflight runs this checkout-owned
// command for the Beads source tree.
//
// BD_LINT_TARGETS (a comma list of native, windows and darwin; default all)
// selects what is analyzed:
//
//   - native: `bazel build --config=nogo //...`, every package and test, in
//     the configuration of bazel.yml's unit lane.
//   - windows, darwin: //tools/bazel:release_cross, every go_library and
//     go_binary split-transitioned to windows/amd64 and darwin/arm64 without
//     cgo, so the files only those platforms compile (//go:build windows,
//     darwin, !linux, ...) are analyzed on any host, as golangci-lint's
//     former GOOS=windows/darwin legs did. bazel.yml's pure lane builds the
//     same target for every release platform.
//
// Bazel executes remotely when the developer's or CI's rc configures it.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const passTimeout = 30 * time.Minute

// defaultLintTargets is BD_LINT_TARGETS' value when unset or empty: the
// no-argument usage contract (`make ci-pr-lint`, `bd preflight`) runs all
// three.
var defaultLintTargets = []string{"native", "windows", "darwin"}

// crossPlatforms maps the cross targets to rules_go's non-cgo platforms.
var crossPlatforms = []struct{ target, platform string }{
	{"windows", "windows_amd64"},
	{"darwin", "darwin_arm64"},
}

type commandSpec struct {
	name string
	args []string
	dir  string
}

type processRunner interface {
	lookPath(string) (string, error)
	run(context.Context, commandSpec, io.Writer, io.Writer) error
}

type osProcessRunner struct{}

func (osProcessRunner) lookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (osProcessRunner) run(ctx context.Context, spec commandSpec, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, spec.name, spec.args...) //nolint:gosec // G204: the Bazel launcher with internally assembled arguments
	cmd.Dir = spec.dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
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
	caseInsensitive := runtime.GOOS == "windows"
	targets, err := parseLintTargets(environmentValue(environ, "BD_LINT_TARGETS", caseInsensitive))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	bazel, err := findBazel(environmentValue(environ, "BAZEL", caseInsensitive), runner)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if containsTarget(targets, "native") {
		spec := commandSpec{name: bazel, args: []string{"build", "--config=nogo", "//..."}, dir: dir}
		if code := runPass("nogo (native)", spec, stdout, stderr, runner); code != 0 {
			return code
		}
	}

	var platforms []string
	for _, p := range crossPlatforms {
		if containsTarget(targets, p.target) {
			platforms = append(platforms, p.platform)
		}
	}
	if len(platforms) > 0 {
		label := "nogo (" + strings.Join(platforms, ", ") + "; non-cgo)"
		spec := commandSpec{
			name: bazel,
			args: []string{"build", "--config=nogo-cross", "--//tools/bazel:release_platforms=" + strings.Join(platforms, ","), "//tools/bazel:release_cross"},
			dir:  dir,
		}
		if code := runPass(label, spec, stdout, stderr, runner); code != 0 {
			return code
		}
	}
	return 0
}

// findBazel resolves the Bazel launcher: $BAZEL (the Makefile's variable),
// else bazel, else bazelisk.
func findBazel(override string, runner processRunner) (string, error) {
	candidates := []string{"bazel", "bazelisk"}
	if override != "" {
		candidates = []string{override}
	}
	for _, name := range candidates {
		if path, err := runner.lookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("bazel not found in PATH (tried %s); install bazelisk (https://github.com/bazelbuild/bazelisk) or set BAZEL", strings.Join(candidates, ", "))
}

func runPass(label string, spec commandSpec, stdout, stderr io.Writer, runner processRunner) int {
	fmt.Fprintf(stdout, "==> %s\n", label)
	ctx, cancel := context.WithTimeout(context.Background(), passTimeout)
	defer cancel()
	err := runner.run(ctx, spec, stdout, stderr)
	if err == nil {
		fmt.Fprintf(stdout, "<== %s succeeded\n", label)
		return 0
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(stderr, "<== %s exceeded %s\n", label, passTimeout)
		return 1
	}
	fmt.Fprintf(stderr, "<== %s failed: %v\n", label, err)
	return processExitCode(err)
}

// parseLintTargets parses BD_LINT_TARGETS: a comma-separated list of
// "native", "windows" and "darwin", in any order and with any repetition.
// An empty value (unset, or only whitespace and commas) selects all three.
// Any other token is an error: callers exit 2, the usage-error exit code.
func parseLintTargets(raw string) ([]string, error) {
	var targets []string
	for _, field := range strings.Split(raw, ",") {
		target := strings.TrimSpace(field)
		if target == "" {
			continue
		}
		if !containsTarget(defaultLintTargets, target) {
			return nil, fmt.Errorf("BD_LINT_TARGETS: unknown target %q (want a comma list of native, windows, darwin)", target)
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return defaultLintTargets, nil
	}
	return targets, nil
}

func containsTarget(targets []string, target string) bool {
	for _, candidate := range targets {
		if candidate == target {
			return true
		}
	}
	return false
}

// environmentValue returns the last value of wanted in environ; Windows
// environment names are case-insensitive.
func environmentValue(environ []string, wanted string, caseInsensitive bool) string {
	for index := len(environ) - 1; index >= 0; index-- {
		key, value, ok := strings.Cut(environ[index], "=")
		if ok && (key == wanted || caseInsensitive && strings.EqualFold(key, wanted)) {
			return value
		}
	}
	return ""
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
