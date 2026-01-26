package setup

import (
	"strings"
	"testing"
)

func stubCodexEnvProvider(t *testing.T, env agentsEnv) {
	t.Helper()
	orig := codexEnvProvider
	codexEnvProvider = func() agentsEnv {
		return env
	}
	t.Cleanup(func() { codexEnvProvider = orig })
}

func TestInstallCodexCreatesNewFile(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	if err := installCodex(env); err != nil {
		t.Fatalf("installCodex returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Codex CLI integration installed") {
		t.Error("expected Codex install success message")
	}
}

func TestCheckCodexMissingFile(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	err := checkCodex(env)
	if err == nil {
		t.Fatal("expected error for missing AGENTS.md")
	}
	if !strings.Contains(stdout.String(), "bd setup codex") {
		t.Error("expected setup guidance for codex")
	}
}
