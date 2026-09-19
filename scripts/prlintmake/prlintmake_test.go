package prlintmake

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFmtCheckClean(t *testing.T) {
	output, err := runFmtCheck(t, "exit 0\n")
	if err != nil {
		t.Fatalf("fmt-check failed: %v\n%s", err, output)
	}
	want := "Checking Go formatting...\nAll Go files are properly formatted\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestFmtCheckReportsUnformattedFiles(t *testing.T) {
	output, err := runFmtCheck(t, "printf '%s\\n' cmd/bd/main.go internal/config/config.go\n")
	if got := processExitCode(err); got != 1 {
		t.Fatalf("exit = %d, want 1; error=%v\n%s", got, err, output)
	}
	want := "Checking Go formatting...\n" +
		"The following files are not properly formatted:\n" +
		"cmd/bd/main.go\n" +
		"internal/config/config.go\n\n" +
		"Run 'make fmt' to fix formatting\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestFmtCheckPreservesGofmtFailure(t *testing.T) {
	output, err := runFmtCheck(t, "printf 'synthetic gofmt failure\\n' >&2\nexit 42\n")
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

func runFmtCheck(t *testing.T, gofmtBody string) (string, error) {
	t.Helper()
	bash := testBash(t)
	testRoot := t.TempDir()
	shimDir := filepath.Join(testRoot, "fmt shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeShellExecutable(t, bash, filepath.Join(shimDir, "gofmt"), "#!/usr/bin/env bash\nset -euo pipefail\n"+gofmtBody)

	path := shimDir + string(os.PathListSeparator) + os.Getenv("PATH")
	if runtime.GOOS == "windows" {
		path = msysPath(shimDir) + ":/usr/bin:/bin"
	}
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
		"LANG":      "C",
		"LC_ALL":    "C",
		"PATH":      path,
		"SHELLOPTS": "",
	})
	output, err := cmd.CombinedOutput()
	return normalizeNewlines(string(output)), err
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
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
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
