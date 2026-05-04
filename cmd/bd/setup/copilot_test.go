package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newCopilotTestEnv(t *testing.T) (copilotEnv, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	homeDir := filepath.Join(root, "home")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	env := copilotEnv{
		stdout:     stdout,
		stderr:     stderr,
		projectDir: projectDir,
		homeDir:    homeDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
		lookPath: func(string) (string, error) {
			return "/usr/local/bin/copilot", nil
		},
		runCommand: func(string, ...string) ([]byte, error) {
			return []byte("copilot 1.0.5\n"), nil
		},
	}
	return env, stdout, stderr
}

func readCopilotHooks(t *testing.T, path string) copilotHooksConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hooks: %v", err)
	}
	cfg, err := parseCopilotHooks(data)
	if err != nil {
		t.Fatalf("parse hooks: %v", err)
	}
	return cfg
}

func TestResolveCopilotScopes(t *testing.T) {
	tests := []struct {
		name        string
		project     bool
		global      bool
		wantProject bool
		wantGlobal  bool
	}{
		{"default is global", false, false, false, true},
		{"project only", true, false, true, false},
		{"global only", false, true, false, true},
		{"both", true, true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProject, gotGlobal := resolveCopilotScopes(tt.project, tt.global)
			if gotProject != tt.wantProject || gotGlobal != tt.wantGlobal {
				t.Fatalf("resolveCopilotScopes(%v, %v) = (%v, %v), want (%v, %v)", tt.project, tt.global, gotProject, gotGlobal, tt.wantProject, tt.wantGlobal)
			}
		})
	}
}

func TestInstallCopilotDefaultGlobal(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, false, false, false); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	instructionsPath := copilotGlobalInstructionsPath(env.homeDir)
	instructions, err := os.ReadFile(instructionsPath)
	if err != nil {
		t.Fatalf("read instructions: %v", err)
	}
	if !strings.Contains(string(instructions), "# GitHub Copilot Instructions") {
		t.Fatalf("expected copilot instructions header in %s", instructionsPath)
	}
	if !strings.Contains(string(instructions), "profile:minimal") {
		t.Fatalf("expected minimal beads section in %s", instructionsPath)
	}
	cfg := readCopilotHooks(t, copilotGlobalHooksPath(env.homeDir))
	if !hasCopilotHookEvent(cfg, "sessionStart") || !hasCopilotHookEvent(cfg, "preCompact") {
		t.Fatalf("expected global hooks to be installed")
	}
	if _, err := os.Stat(copilotHooksPath(env.projectDir)); !os.IsNotExist(err) {
		t.Fatalf("expected no project hooks for default global install, got err=%v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "global instructions installed") {
		t.Fatalf("expected global success output, got: %s", out)
	}
}

func TestInstallCopilotProject(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, true, false, false); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	instructionsPath := copilotProjectInstructionsPath(env.projectDir)
	instructions, err := os.ReadFile(instructionsPath)
	if err != nil {
		t.Fatalf("read instructions: %v", err)
	}
	if !strings.Contains(string(instructions), "# GitHub Copilot Instructions") {
		t.Fatalf("expected copilot instructions header in %s", instructionsPath)
	}
	if !strings.Contains(string(instructions), "profile:minimal") {
		t.Fatalf("expected minimal beads section in %s", instructionsPath)
	}

	cfg := readCopilotHooks(t, copilotHooksPath(env.projectDir))
	if !hasCopilotHookEvent(cfg, "sessionStart") {
		t.Fatal("expected sessionStart hook")
	}
	if !hasCopilotHookEvent(cfg, "preCompact") {
		t.Fatal("expected preCompact hook")
	}

	out := stdout.String()
	if !strings.Contains(out, "project integration installed") {
		t.Fatalf("expected project success output, got: %s", out)
	}
}

func TestInstallCopilotBothScopes(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, true, true, false); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	if _, err := os.Stat(copilotGlobalInstructionsPath(env.homeDir)); err != nil {
		t.Fatalf("expected global instructions: %v", err)
	}
	if _, err := os.Stat(copilotGlobalHooksPath(env.homeDir)); err != nil {
		t.Fatalf("expected global hooks: %v", err)
	}
	if _, err := os.Stat(copilotProjectInstructionsPath(env.projectDir)); err != nil {
		t.Fatalf("expected project instructions: %v", err)
	}
	if _, err := os.Stat(copilotHooksPath(env.projectDir)); err != nil {
		t.Fatalf("expected project hooks: %v", err)
	}
}

func TestInstallCopilotProjectStealth(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, true, false, true); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}

	cfg := readCopilotHooks(t, copilotHooksPath(env.projectDir))
	for _, event := range []string{"sessionStart", "preCompact"} {
		commands := cfg.Hooks[event]
		if len(commands) != 1 {
			t.Fatalf("expected one %s hook, got %d", event, len(commands))
		}
		if commands[0].Bash != "bd prime --stealth" {
			t.Fatalf("expected stealth command for %s, got %q", event, commands[0].Bash)
		}
	}
}

func TestInstallCopilotProjectIdempotent(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, true, false, false); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := installCopilot(env, true, false, false); err != nil {
		t.Fatalf("second install: %v", err)
	}

	cfg := readCopilotHooks(t, copilotHooksPath(env.projectDir))
	for _, event := range []string{"sessionStart", "preCompact"} {
		if got := len(cfg.Hooks[event]); got != 1 {
			t.Fatalf("expected one %s hook after reinstall, got %d", event, got)
		}
	}
}

func TestInstallCopilotProjectRejectsUnmanagedHookFile(t *testing.T) {
	env, _, stderr := newCopilotTestEnv(t)

	hooksPath := copilotHooksPath(env.projectDir)
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	if err := os.WriteFile(hooksPath, []byte(`{"version":1,"hooks":{"sessionStart":[{"type":"command","bash":"echo custom"}]}}`), 0o644); err != nil {
		t.Fatalf("write hooks file: %v", err)
	}

	err := installCopilot(env, true, false, false)
	if err == nil {
		t.Fatal("expected installCopilot to fail for unmanaged hook file")
	}
	if !strings.Contains(stderr.String(), "install hooks") {
		t.Fatalf("expected hook installation error, got: %s", stderr.String())
	}
}

func TestCheckCopilotGlobal(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, false, true, false); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}
	stdout.Reset()

	if err := checkCopilot(env, false, true); err != nil {
		t.Fatalf("checkCopilot: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Hooks installed") {
		t.Fatalf("expected global hooks check output, got: %s", out)
	}
	if !strings.Contains(out, "GitHub Copilot CLI (global) integration installed") {
		t.Fatalf("expected global check output, got: %s", out)
	}
}

func TestCheckCopilotProject(t *testing.T) {
	env, stdout, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, true, false, false); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}
	stdout.Reset()

	if err := checkCopilot(env, true, false); err != nil {
		t.Fatalf("checkCopilot: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Hooks installed") {
		t.Fatalf("expected hooks check output, got: %s", out)
	}
	if !strings.Contains(out, "GitHub Copilot CLI (project) integration installed") {
		t.Fatalf("expected instructions check output, got: %s", out)
	}
}

func TestRemoveCopilotGlobal(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, false, true, false); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}
	if err := removeCopilot(env, false, true); err != nil {
		t.Fatalf("removeCopilot: %v", err)
	}

	instructions, err := os.ReadFile(copilotGlobalInstructionsPath(env.homeDir))
	if err != nil {
		t.Fatalf("read instructions: %v", err)
	}
	if strings.Contains(string(instructions), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("expected beads section to be removed from global instructions")
	}
	if _, err := os.Stat(copilotGlobalHooksPath(env.homeDir)); !os.IsNotExist(err) {
		t.Fatalf("expected global hooks to be removed, got err=%v", err)
	}
}

func TestRemoveCopilotProject(t *testing.T) {
	env, _, _ := newCopilotTestEnv(t)

	if err := installCopilot(env, true, false, false); err != nil {
		t.Fatalf("installCopilot: %v", err)
	}
	if err := removeCopilot(env, true, false); err != nil {
		t.Fatalf("removeCopilot: %v", err)
	}

	if _, err := os.Stat(copilotHooksPath(env.projectDir)); !os.IsNotExist(err) {
		t.Fatalf("expected hooks file to be removed, got err=%v", err)
	}

	instructions, err := os.ReadFile(copilotProjectInstructionsPath(env.projectDir))
	if err != nil {
		t.Fatalf("read instructions: %v", err)
	}
	if strings.Contains(string(instructions), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("expected beads section to be removed from instructions")
	}
	if !strings.Contains(string(instructions), "# GitHub Copilot Instructions") {
		t.Fatalf("expected scaffold to remain after removing integration")
	}
}

func TestCheckCopilotOldVersion(t *testing.T) {
	env, _, stderr := newCopilotTestEnv(t)
	env.runCommand = func(string, ...string) ([]byte, error) {
		return []byte("copilot 1.0.4\n"), nil
	}

	err := installCopilot(env, true, false, false)
	if !errors.Is(err, errCopilotVersionOld) {
		t.Fatalf("expected errCopilotVersionOld, got %v", err)
	}
	if !strings.Contains(stderr.String(), "1.0.4") {
		t.Fatalf("expected version error in stderr, got: %s", stderr.String())
	}
}
