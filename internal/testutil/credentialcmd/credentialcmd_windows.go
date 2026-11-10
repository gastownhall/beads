package credentialcmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func prepareFixture() {
	fixture.shellName = "cmd.exe"
	fixture.shellPath, fixture.err = exec.LookPath(fixture.shellName)
	if fixture.err != nil {
		fixture.err = fmt.Errorf("resolve production credential shell %q: %w", fixture.shellName, fixture.err)
		return
	}
	fixture.shellDir = filepath.Dir(fixture.shellPath)
	fixture.root, fixture.err = os.MkdirTemp("", "beads-credential-command-*")
	if fixture.err != nil {
		fixture.err = fmt.Errorf("create fixture root: %w", fixture.err)
		return
	}
	helperDir := filepath.Join(fixture.root, "credential helper with spaces")
	if err := os.MkdirAll(helperDir, 0o755); err != nil {
		fixture.err = fmt.Errorf("create credential helper directory: %w", err)
		return
	}
	fixture.helperPath = filepath.Join(helperDir, "credential-helper.cmd")
	fixture.targetPath, fixture.err = os.Executable()
	if fixture.err != nil {
		fixture.err = fmt.Errorf("resolve current test executable: %w", fixture.err)
		return
	}
	fixture.targetEnv = fmt.Sprintf("BEADS_TEST_CREDENTIAL_COMMAND_TARGET_%d", os.Getpid())
	trampoline := "@echo off\r\n\"%" + fixture.targetEnv + "%\" %*\r\nexit /b %errorlevel%\r\n"
	if err := os.WriteFile(fixture.helperPath, []byte(trampoline), 0o600); err != nil {
		fixture.err = fmt.Errorf("write credential helper trampoline: %w", err)
	}
}

func configurePlatformCommand(t *testing.T, executableEnv string) string {
	t.Helper()

	// Restrict lookup to the directory containing the mandatory production
	// shell. Unlike the Unix fixture, Windows cannot isolate cmd.exe in a copied
	// directory without creating a suspicious system-binary copy chain, so
	// explicitly prove the ambient fixture utilities remain unavailable.
	t.Setenv("PATH", fixture.shellDir)
	resolvedShell, err := exec.LookPath(fixture.shellName)
	if err != nil {
		t.Fatalf("resolve credential shell from restricted PATH: %v", err)
	}
	resolvedInfo, err := os.Stat(resolvedShell)
	if err != nil {
		t.Fatalf("stat resolved credential shell: %v", err)
	}
	wantInfo, err := os.Stat(fixture.shellPath)
	if err != nil {
		t.Fatalf("stat production credential shell: %v", err)
	}
	if !os.SameFile(resolvedInfo, wantInfo) {
		t.Fatalf("credential shell resolved outside restricted PATH: got %q, want %q", resolvedShell, fixture.shellPath)
	}
	for _, forbidden := range []string{"printf", "false"} {
		if path, err := exec.LookPath(forbidden); err == nil {
			t.Fatalf("ambient fixture utility %q remains available at %q", forbidden, path)
		}
	}

	t.Setenv(fixture.targetEnv, fixture.targetPath)
	// Let cmd.exe expand quotes stored in the variable value. Literal quotes in
	// exec.Command's single /C argument are otherwise escaped by Go's Windows
	// argv encoder before cmd.exe parses them.
	t.Setenv(executableEnv, `"`+fixture.helperPath+`"`)
	return `%` + executableEnv + `%`
}
