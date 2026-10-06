package prlintmake

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/bazeltest"
)

func TestFmtCheckClean(t *testing.T) {
	gofmt, output, err := runFmtCheck(t, "exit 0\n")
	if err != nil {
		t.Fatalf("fmt-check failed: %v\n%s", err, output)
	}
	want := "Checking Go formatting...\nUsing gofmt " + gofmt + "\nAll Go files are properly formatted\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestFmtCheckReportsUnformattedFiles(t *testing.T) {
	gofmt, output, err := runFmtCheck(t, "printf '%s\\n' cmd/bd/main.go internal/config/config.go\n")
	if got := processExitCode(err); got != 1 {
		t.Fatalf("exit = %d, want 1; error=%v\n%s", got, err, output)
	}
	want := "Checking Go formatting...\n" +
		"Using gofmt " + gofmt + "\n" +
		"The following files are not properly formatted:\n" +
		"cmd/bd/main.go\n" +
		"internal/config/config.go\n\n" +
		"Run 'make fmt' to fix formatting\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

// TestFmtCheckReportsGofmtVersion pins describe_gofmt's version-bearing arm,
// the "<version> (<path>)" form.
//
// The cases above inject a bash script, for which `go version` errors and the
// version is always empty, so only the bare-path else arm is exercised there --
// deleting the whole version block leaves all three of them green. The version
// is the half that makes a toolchain skew legible as a skew, which is the whole
// justification for the reporting change, so it needs a real Go-built binary.
func TestFmtCheckReportsGofmtVersion(t *testing.T) {
	goBin := testGo(t)
	shim := buildNoopGoBinary(t, goBin, "gofmt")
	want := goBuildVersion(t, goBin, shim)

	output, err := runFmtCheckWithGofmt(t, shim)
	if err != nil {
		t.Fatalf("fmt-check failed: %v\n%s", err, output)
	}
	line := "Using gofmt " + want + " (" + shim + ")\n"
	if !strings.Contains(output, line) {
		t.Fatalf("missing %q in output:\n%s", line, output)
	}
}

func TestFmtCheckPreservesGofmtFailure(t *testing.T) {
	_, output, err := runFmtCheck(t, "printf 'synthetic gofmt failure\\n' >&2\nexit 42\n")
	if got := processExitCode(err); got != 42 {
		t.Fatalf("exit = %d, want 42; error=%v\n%s", got, err, output)
	}
	for _, want := range []string{
		"Checking Go formatting...",
		"synthetic gofmt failure",
		"gofmt failed while checking formatting",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in output:\n%s", want, output)
		}
	}
}

func TestPRLintWrapperDelegatesPolicyToCheckoutGoDriver(t *testing.T) {
	run := runPRLintWrapper(t, "exit 0\n")
	if run.err != nil {
		t.Fatalf("pr-lint wrapper failed: %v\n%s", run.err, run.output)
	}
	if run.goArgs != "run -mod=readonly -tags=gms_pure_go ./scripts/pr-lint" {
		t.Fatalf("go args = %q, want checkout driver invocation", run.goArgs)
	}
	for _, want := range []string{
		"CGO_ENABLED=1",
		"BEADS_BUILD_TAGS=gms_pure_go",
		"GOFLAGS=-mod=readonly -tags=gms_pure_go",
		"BD_LINT_NEW_FROM_MERGE_BASE=origin/main",
	} {
		if !strings.Contains(run.goEnvironment, want+"\n") {
			t.Fatalf("driver environment missing %q:\n%s", want, run.goEnvironment)
		}
	}
	if !strings.Contains(run.output, "==> golangci-lint (native + windows/darwin non-CGO)") ||
		!strings.Contains(run.output, "<== golangci-lint (native + windows/darwin non-CGO) succeeded") {
		t.Fatalf("aggregate lint timing is not attributable:\n%s", run.output)
	}
}

func TestPRLintWrapperReportsGoRunFailure(t *testing.T) {
	run := runPRLintWrapper(t, "printf 'exit status 42\\n' >&2\nexit 1\n")
	if got := processExitCode(run.err); got == 0 {
		t.Fatalf("exit = %d, want nonzero; error=%v\n%s", got, run.err, run.output)
	}
	if !strings.Contains(run.output, "exit status 42") || !strings.Contains(run.output, "failed after") {
		t.Fatalf("missing aggregate failure diagnostic:\n%s", run.output)
	}
}

type prLintWrapperRun struct {
	output        string
	err           error
	goArgs        string
	goEnvironment string
}

func runPRLintWrapper(t *testing.T, goBody string) prLintWrapperRun {
	t.Helper()
	bash := testBash(t)
	testRoot := t.TempDir()
	shimDir := filepath.Join(testRoot, "pr-lint shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// fmt-check.sh ignores a PATH gofmt (be-gx8), so the gofmt shim goes in
	// through GOFMT, as in runFmtCheck. Only the go shim needs to be on PATH.
	gofmt := filepath.Join(testRoot, "gofmt")
	writeShellExecutable(t, bash, gofmt, "#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n")
	writeShellExecutable(t, bash, filepath.Join(shimDir, "go"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"$GO_ARGS_MARKER"
printf 'CGO_ENABLED=%s\n' "${CGO_ENABLED-}" >"$GO_ENV_MARKER"
printf 'BEADS_BUILD_TAGS=%s\n' "${BEADS_BUILD_TAGS-}" >>"$GO_ENV_MARKER"
printf 'GOFLAGS=%s\n' "${GOFLAGS-}" >>"$GO_ENV_MARKER"
printf 'BD_LINT_NEW_FROM_MERGE_BASE=%s\n' "${BD_LINT_NEW_FROM_MERGE_BASE-}" >>"$GO_ENV_MARKER"
`+goBody)

	argsMarker := filepath.Join(testRoot, "go-args")
	envMarker := filepath.Join(testRoot, "go-environment")
	path := shimDir + string(os.PathListSeparator) + os.Getenv("PATH")
	if runtime.GOOS == "windows" {
		path = msysPath(shimDir) + ":/usr/bin:/bin"
	}
	cmd := exec.Command(
		bash,
		"--noprofile",
		"--norc",
		"--",
		shellVisiblePath(filepath.Join(sourceRepoRoot(), "scripts", "ci", "pr-lint.sh")),
	)
	cmd.Dir = sourceRepoRoot()
	cmd.Env = environment(map[string]string{
		"BASH_ENV":                    "",
		"BASHOPTS":                    "",
		"BD_LINT_NEW_FROM_MERGE_BASE": "origin/main",
		"BEADS_BUILD_TAGS":            "stale",
		"CGO_ENABLED":                 "",
		"ENV":                         "",
		"GOFLAGS":                     "-mod=readonly",
		"GOFMT":                       shellVisiblePath(gofmt),
		"GO_ARGS_MARKER":              shellVisiblePath(argsMarker),
		"GO_ENV_MARKER":               shellVisiblePath(envMarker),
		"LANG":                        "C",
		"LC_ALL":                      "C",
		"PATH":                        path,
		"SHELLOPTS":                   "",
	})
	output, runErr := cmd.CombinedOutput()
	args, argsErr := os.ReadFile(argsMarker)
	if argsErr != nil {
		t.Fatalf("read go argument marker: %v\n%s", argsErr, output)
	}
	environment, envErr := os.ReadFile(envMarker)
	if envErr != nil {
		t.Fatalf("read go environment marker: %v\n%s", envErr, output)
	}
	return prLintWrapperRun{
		output:        normalizeNewlines(string(output)),
		err:           runErr,
		goArgs:        strings.TrimSpace(normalizeNewlines(string(args))),
		goEnvironment: normalizeNewlines(string(environment)),
	}
}

// runFmtCheck runs scripts/ci/fmt-check.sh against a gofmt shim with the given
// body, and returns the shim path fmt-check.sh is expected to report.
//
// The shim is injected through GOFMT rather than PATH. fmt-check.sh deliberately
// ignores a PATH gofmt -- that is the whole of be-gx8 -- so a PATH shim would be
// silently bypassed and these tests would grade the real toolchain instead of
// the case they name. TestGofmtBinIgnoresPathGofmt covers the resolution itself.
func runFmtCheck(t *testing.T, gofmtBody string) (string, string, error) {
	t.Helper()
	bash := testBash(t)
	testRoot := t.TempDir()
	shimDir := filepath.Join(testRoot, "fmt shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(shimDir, "gofmt")
	writeShellExecutable(t, bash, shim, "#!/usr/bin/env bash\nset -euo pipefail\n"+gofmtBody)
	gofmt := shellVisiblePath(shim)

	output, err := runFmtCheckWithGofmt(t, gofmt)
	return gofmt, output, err
}

// runFmtCheckWithGofmt runs scripts/ci/fmt-check.sh with GOFMT pointing at an
// already-built binary, and returns its combined output.
func runFmtCheckWithGofmt(t *testing.T, gofmt string) (string, error) {
	t.Helper()
	bash := testBash(t)
	cmd := exec.Command(
		bash,
		"--noprofile",
		"--norc",
		"--",
		shellVisiblePath(filepath.Join(sourceRepoRoot(), "scripts", "ci", "fmt-check.sh")),
	)
	cmd.Dir = sourceRepoRoot()
	cmd.Env = environment(map[string]string{
		"BASH_ENV":  "",
		"BASHOPTS":  "",
		"ENV":       "",
		"GOFMT":     gofmt,
		"LANG":      "C",
		"LC_ALL":    "C",
		"SHELLOPTS": "",
	})
	output, err := cmd.CombinedOutput()
	return normalizeNewlines(string(output)), err
}

// buildNoopGoBinary compiles a do-nothing program under the given name and
// returns its shell-visible path. `go version` reports the toolchain that built
// it, which is what the caller needs and what a shell shim can never provide.
func buildNoopGoBinary(t *testing.T, goBin, name string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(base, content string) {
		if err := os.WriteFile(filepath.Join(dir, base), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module noopgobinary\n")
	write("main.go", "package main\n\nfunc main() {}\n")

	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	cmd := exec.Command(goBin, "build", "-o", binary, ".")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build a no-op %s: %v\n%s", name, err, output)
	}
	return shellVisiblePath(binary)
}

// goBuildVersion returns the "goX.Y.Z" that `go version <binary>` reports.
func goBuildVersion(t *testing.T, goBin, binary string) string {
	t.Helper()
	output, err := exec.Command(goBin, "version", binary).CombinedOutput()
	if err != nil {
		t.Fatalf("go version %s: %v\n%s", binary, err, output)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		t.Fatalf("go version %s printed no fields", binary)
	}
	return fields[len(fields)-1]
}

func writeShellExecutable(t *testing.T, bash, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, "\r\n", "\n")), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	cmd := exec.Command(bash, "--noprofile", "--norc", "-c", `/usr/bin/chmod +x "$1"`, "--", msysPath(path))
	cmd.Env = environment(map[string]string{
		"BASH_ENV":  "",
		"BASHOPTS":  "",
		"ENV":       "",
		"SHELLOPTS": "",
	})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make %s executable: %v\n%s", path, err, output)
	}
}

func testBash(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is required: %v", err)
	}
	return path
}

func environment(overrides map[string]string) []string {
	overridden := make(map[string]struct{}, len(overrides))
	for key := range overrides {
		overridden[strings.ToUpper(key)] = struct{}{}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := overridden[strings.ToUpper(key)]; !ok {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func sourceRepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	// Under Bazel the caller path is workspace-relative; CallerDir rebuilds it
	// under the runfiles root, which holds the declared fmt-check.sh.
	return filepath.Dir(filepath.Dir(bazeltest.CallerDir(file, "scripts/prlintmake")))
}

func shellVisiblePath(path string) string {
	if runtime.GOOS == "windows" {
		return msysPath(path)
	}
	return path
}

func msysPath(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	if len(path) >= 3 && path[1] == ':' && path[2] == '/' {
		return "/" + strings.ToLower(path[:1]) + path[2:]
	}
	return path
}

func normalizeNewlines(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}
