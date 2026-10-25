//go:build !windows

package credentialcmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func prepareFixture() {
	fixture.shellName = "sh"
	shellSource, err := exec.LookPath(fixture.shellName)
	if err != nil {
		fixture.err = fmt.Errorf("resolve production credential shell %q: %w", fixture.shellName, err)
		return
	}
	fixture.root, err = os.MkdirTemp("", "beads-credential-command-*")
	if err != nil {
		fixture.err = fmt.Errorf("create fixture root: %w", err)
		return
	}
	fixture.shellDir = filepath.Join(fixture.root, "credential shell only")
	if err := os.MkdirAll(fixture.shellDir, 0o755); err != nil {
		fixture.err = fmt.Errorf("create isolated credential shell directory: %w", err)
		return
	}
	fixture.shellPath = filepath.Join(fixture.shellDir, fixture.shellName)
	if err := linkOrCopyExecutable(shellSource, fixture.shellPath); err != nil {
		fixture.err = fmt.Errorf("install credential shell: %w", err)
		return
	}

	helperDir := filepath.Join(fixture.root, "credential helper with spaces")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		fixture.err = fmt.Errorf("create credential helper directory: %w", err)
		return
	}
	fixture.helperPath = filepath.Join(helperDir, "credential-helper")
	currentExecutable, err := os.Executable()
	if err != nil {
		fixture.err = fmt.Errorf("resolve current test executable: %w", err)
		return
	}
	if err := linkOrCopyExecutable(currentExecutable, fixture.helperPath); err != nil {
		fixture.err = fmt.Errorf("install current test executable: %w", err)
	}
}

func configurePlatformCommand(t *testing.T, executableEnv string) string {
	t.Helper()

	t.Setenv("PATH", fixture.shellDir)
	resolvedShell, err := exec.LookPath(fixture.shellName)
	if err != nil {
		t.Fatalf("resolve credential shell from isolated PATH: %v", err)
	}
	resolvedInfo, err := os.Stat(resolvedShell)
	if err != nil {
		t.Fatalf("stat resolved credential shell: %v", err)
	}
	wantInfo, err := os.Stat(fixture.shellPath)
	if err != nil {
		t.Fatalf("stat installed credential shell: %v", err)
	}
	if !os.SameFile(resolvedInfo, wantInfo) {
		t.Fatalf("credential shell resolved outside isolated PATH: got %q, want %q", resolvedShell, fixture.shellPath)
	}
	t.Setenv(executableEnv, fixture.helperPath)
	return `"$` + executableEnv + `"`
}

func linkOrCopyExecutable(source, destination string) error {
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", source, err)
	}
	if err := os.Symlink(resolvedSource, destination); err == nil {
		return nil
	}

	input, err := os.Open(resolvedSource) //nolint:gosec // G304: source is the resolved shell or current executable in this test-only fixture.
	if err != nil {
		return fmt.Errorf("open %q: %w", resolvedSource, err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755) //nolint:gosec // G302: the copied shell or test helper must be executable.
	if err != nil {
		return fmt.Errorf("create %q: %w", destination, err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("copy to %q: %w", destination, err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close %q: %w", destination, err)
	}
	if err := os.Chmod(destination, 0o755); err != nil { //nolint:gosec // G302: the copied shell or test helper must be executable.
		return fmt.Errorf("make %q runnable: %w", destination, err)
	}
	return nil
}
