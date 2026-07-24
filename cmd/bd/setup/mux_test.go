package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubMuxEnvProvider(t *testing.T, env agentsEnv) {
	t.Helper()
	orig := muxEnvProvider
	muxEnvProvider = func() agentsEnv {
		return env
	}
	t.Cleanup(func() { muxEnvProvider = orig })
}

func TestInstallMuxCreatesNewFile(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Mux integration installed") {
		t.Error("expected Mux install success message")
	}
	if !strings.Contains(stdout.String(), muxAgentInstructionsURL) {
		t.Error("expected Mux docs URL in install output")
	}
}

func TestInstallMuxReportsSkippedSymlinkRootInstructions(t *testing.T) {
	env, stdout, stderr := newFactoryTestEnv(t)
	target := filepath.Join(filepath.Dir(env.agentsPath), "SHARED_AGENTS.md")
	if err := os.WriteFile(target, []byte("# Shared instructions\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, env.agentsPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux returned error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Mux hooks installed; managed section skipped") {
		t.Fatalf("expected partial Mux success message, got:\n%s", out)
	}
	if strings.Contains(out, "Mux integration installed") {
		t.Fatalf("should not report full Mux integration success when root instructions are skipped:\n%s", out)
	}
	if !strings.Contains(stderr.String(), "AGENTS.md is a symlink") {
		t.Fatalf("expected symlink warning on stderr, got:\n%s", stderr.String())
	}
	if !FileExists(muxProjectHookPathsFirst(env.agentsPath)) {
		t.Fatalf("expected Mux init hook to be installed")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if strings.Contains(string(data), "BEGIN BEADS INTEGRATION") {
		t.Fatalf("symlink target should remain untouched:\n%s", data)
	}
}

func muxProjectHookPathsFirst(baseAgentsPath string) string {
	initPath, _, _ := muxProjectHookPaths(baseAgentsPath)
	return initPath
}

func TestCheckMuxMissingFile(t *testing.T) {
	env, stdout, _ := newFactoryTestEnv(t)
	err := checkMux(env, false, false)
	if err == nil {
		t.Fatal("expected error for missing AGENTS.md")
	}
	if !strings.Contains(stdout.String(), "bd setup mux") {
		t.Error("expected setup guidance for mux")
	}
}

func TestMuxProjectAgentsPath(t *testing.T) {
	if got, want := muxProjectAgentsPath("AGENTS.md"), filepath.Join(".mux", "AGENTS.md"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	agentsPath := filepath.Join(t.TempDir(), "AGENTS.md")
	if got, want := muxProjectAgentsPath(agentsPath), filepath.Join(filepath.Dir(agentsPath), ".mux", "AGENTS.md"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMuxProjectHookPaths(t *testing.T) {
	initPath, toolPostPath, toolEnvPath := muxProjectHookPaths("AGENTS.md")
	if want := filepath.Join(".mux", "init"); initPath != want {
		t.Fatalf("init path = %q, want %q", initPath, want)
	}
	if want := filepath.Join(".mux", "tool_post"); toolPostPath != want {
		t.Fatalf("tool_post path = %q, want %q", toolPostPath, want)
	}
	if want := filepath.Join(".mux", "tool_env"); toolEnvPath != want {
		t.Fatalf("tool_env path = %q, want %q", toolEnvPath, want)
	}
}

func TestInstallMuxProjectInstallsBothLayers(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	if err := installMux(env, true, false); err != nil {
		t.Fatalf("installMux(project=true) returned error: %v", err)
	}
	if !FileExists(env.agentsPath) {
		t.Fatalf("expected root AGENTS.md at %s", env.agentsPath)
	}
	projectPath := muxProjectAgentsPath(env.agentsPath)
	if !FileExists(projectPath) {
		t.Fatalf("expected project AGENTS.md at %s", projectPath)
	}
}

func TestCheckMuxProjectRequiresBothLayers(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	if err := installMux(env, false, false); err != nil {
		t.Fatalf("installMux(project=false) returned error: %v", err)
	}
	if err := checkMux(env, true, false); err == nil {
		t.Fatal("expected project check to fail when .mux/AGENTS.md is missing")
	}
}

func TestRemoveMuxProjectRemovesBothLayers(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	if err := installMux(env, true, false); err != nil {
		t.Fatalf("installMux(project=true) returned error: %v", err)
	}
	if err := removeMux(env, true, false); err != nil {
		t.Fatalf("removeMux(project=true) returned error: %v", err)
	}

	for _, path := range []string{env.agentsPath, muxProjectAgentsPath(env.agentsPath)} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected %s to remain readable after remove: %v", path, err)
		}
		content := string(data)
		if strings.Contains(content, agentsBeginMarker) || strings.Contains(content, agentsEndMarker) {
			t.Fatalf("expected beads markers removed from %s", path)
		}
	}

	if err := checkMux(env, true, false); err == nil {
		t.Fatal("expected project check to fail after remove")
	}
}

func TestMuxGlobalAgentsPath(t *testing.T) {
	t.Cleanup(func() {
		muxUserHomeDir = os.UserHomeDir
	})
	home := t.TempDir()
	muxUserHomeDir = func() (string, error) {
		return home, nil
	}

	got, err := muxGlobalAgentsPath()
	if err != nil {
		t.Fatalf("muxGlobalAgentsPath returned error: %v", err)
	}
	if want := filepath.Join(home, ".mux", "AGENTS.md"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInstallMuxGlobalInstallsGlobalLayer(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	home := t.TempDir()
	t.Cleanup(func() {
		muxUserHomeDir = os.UserHomeDir
	})
	muxUserHomeDir = func() (string, error) {
		return home, nil
	}

	if err := installMux(env, false, true); err != nil {
		t.Fatalf("installMux(global=true) returned error: %v", err)
	}

	globalPath, err := muxGlobalAgentsPath()
	if err != nil {
		t.Fatalf("muxGlobalAgentsPath returned error: %v", err)
	}
	if !FileExists(globalPath) {
		t.Fatalf("expected global AGENTS.md at %s", globalPath)
	}
	if err := checkMux(env, false, true); err != nil {
		t.Fatalf("checkMux(global=true) returned error: %v", err)
	}
}

func TestRemoveMuxGlobalRemovesGlobalLayerSection(t *testing.T) {
	env, _, _ := newFactoryTestEnv(t)
	home := t.TempDir()
	t.Cleanup(func() {
		muxUserHomeDir = os.UserHomeDir
	})
	muxUserHomeDir = func() (string, error) {
		return home, nil
	}

	if err := installMux(env, false, true); err != nil {
		t.Fatalf("installMux(global=true) returned error: %v", err)
	}
	if err := removeMux(env, false, true); err != nil {
		t.Fatalf("removeMux(global=true) returned error: %v", err)
	}

	globalPath, err := muxGlobalAgentsPath()
	if err != nil {
		t.Fatalf("muxGlobalAgentsPath returned error: %v", err)
	}
	data, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatalf("expected %s to remain readable after remove: %v", globalPath, err)
	}
	if strings.Contains(string(data), agentsBeginMarker) {
		t.Fatalf("expected beads markers removed from %s", globalPath)
	}
}
