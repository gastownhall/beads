package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/templates/agents"
)

const (
	muxHookMarkerBegin = "# BEGIN BEADS MUX HOOK"
	muxHookMarkerEnd   = "# END BEADS MUX HOOK"
)

const muxInitHookTemplate = `#!/usr/bin/env bash
set -euo pipefail

` + muxHookMarkerBegin + `
# Claude SessionStart equivalent for Mux: prime beads context when workspace initializes.
if command -v bd >/dev/null 2>&1; then
  bd prime --stealth >/dev/null 2>&1 || true
elif [ -x "$HOME/bin/bd" ]; then
  "$HOME/bin/bd" prime --stealth >/dev/null 2>&1 || true
fi
` + muxHookMarkerEnd + `
`

const muxToolPostHookTemplate = `#!/usr/bin/env bash
set -euo pipefail

` + muxHookMarkerBegin + `
# Claude PreCompact approximation for Mux: keep beads metadata synced after file edits.
if [ "${MUX_TOOL:-}" = "file_edit_replace_string" ] || [ "${MUX_TOOL:-}" = "file_edit_insert" ]; then
  if command -v bd >/dev/null 2>&1; then
    bd dolt push >/dev/null 2>&1 || true
  elif [ -x "$HOME/bin/bd" ]; then
    "$HOME/bin/bd" dolt push >/dev/null 2>&1 || true
  fi
fi
` + muxHookMarkerEnd + `
`

const muxToolEnvHookTemplate = `# Mux tool_env (sourced before bash tool calls)
` + muxHookMarkerBegin + `
# Ensure bd installed in ~/bin is discoverable.
export PATH="$HOME/bin:$PATH"
` + muxHookMarkerEnd + `
`

var (
	muxIntegration = agentsIntegration{
		name:         "Mux",
		setupCommand: "bd setup mux",
		readHint:     "Mux reads AGENTS.md in workspace and global contexts. Restart the workspace session if it is already running.",
		docsURL:      muxAgentInstructionsURL,
		profile:      agents.ProfileFull,
	}

	muxProjectIntegration = agentsIntegration{
		name:         "Mux (workspace layer)",
		setupCommand: "bd setup mux --project",
		readHint:     "Mux also supports layered workspace instructions via .mux/AGENTS.md.",
		docsURL:      muxAgentInstructionsURL,
		profile:      agents.ProfileFull,
	}

	muxGlobalIntegration = agentsIntegration{
		name:         "Mux (global layer)",
		setupCommand: "bd setup mux --global",
		readHint:     "Mux global defaults can be stored in ~/.mux/AGENTS.md.",
		docsURL:      muxAgentInstructionsURL,
		profile:      agents.ProfileFull,
	}

	muxEnvProvider     = defaultAgentsEnv
	muxUserHomeDir     = os.UserHomeDir
	errMuxHooksMissing = errors.New("mux hooks missing")
)

func muxProjectDir(baseAgentsPath string) string {
	baseDir := filepath.Dir(baseAgentsPath)
	if baseDir == "." || baseDir == "" {
		return ".mux"
	}
	return filepath.Join(baseDir, ".mux")
}

func muxProjectAgentsPath(baseAgentsPath string) string {
	return filepath.Join(muxProjectDir(baseAgentsPath), "AGENTS.md")
}

func muxProjectHookPaths(baseAgentsPath string) (initPath, toolPostPath, toolEnvPath string) {
	dir := muxProjectDir(baseAgentsPath)
	return filepath.Join(dir, "init"), filepath.Join(dir, "tool_post"), filepath.Join(dir, "tool_env")
}

func muxGlobalAgentsPath() (string, error) {
	home, err := muxUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mux", "AGENTS.md"), nil
}

func writeMuxHook(path, content string, mode os.FileMode) error {
	if err := atomicWriteFile(path, []byte(content)); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func installMuxHook(env agentsEnv, path, content string, mode os.FileMode) error {
	data, err := os.ReadFile(path) // #nosec G304 -- generated internal paths only
	switch {
	case err == nil:
		if !strings.Contains(string(data), muxHookMarkerBegin) {
			_, _ = fmt.Fprintf(env.stdout, "ℹ Existing hook kept (not managed by bd setup mux): %s\n", path)
			return nil
		}
		if err := writeMuxHook(path, content, mode); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(env.stdout, "✓ Updated Mux hook: %s\n", path)
		return nil
	case os.IsNotExist(err):
		if err := writeMuxHook(path, content, mode); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(env.stdout, "✓ Installed Mux hook: %s\n", path)
		return nil
	default:
		return err
	}
}

func installMuxProjectHooks(env agentsEnv) error {
	dir := muxProjectDir(env.agentsPath)
	if err := EnsureDir(dir, 0o755); err != nil {
		return err
	}
	initPath, toolPostPath, toolEnvPath := muxProjectHookPaths(env.agentsPath)
	if err := installMuxHook(env, initPath, muxInitHookTemplate, 0o755); err != nil {
		return err
	}
	if err := installMuxHook(env, toolPostPath, muxToolPostHookTemplate, 0o755); err != nil {
		return err
	}
	if err := installMuxHook(env, toolEnvPath, muxToolEnvHookTemplate, 0o644); err != nil {
		return err
	}
	return nil
}

func checkMuxProjectHooks(env agentsEnv) error {
	initPath, toolPostPath, toolEnvPath := muxProjectHookPaths(env.agentsPath)
	missing := make([]string, 0, 3)
	for _, path := range []string{initPath, toolPostPath, toolEnvPath} {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, path)
		}
	}
	if len(missing) == 0 {
		_, _ = fmt.Fprintln(env.stdout, "✓ Mux hooks installed: .mux/init, .mux/tool_post, .mux/tool_env")
		return nil
	}
	_, _ = fmt.Fprintf(env.stdout, "✗ Missing Mux hooks: %s\n", strings.Join(missing, ", "))
	_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup mux")
	return errMuxHooksMissing
}

func removeManagedMuxHook(env agentsEnv, path string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- generated internal paths only
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(data), muxHookMarkerBegin) {
		_, _ = fmt.Fprintf(env.stdout, "ℹ Kept existing custom hook: %s\n", path)
		return nil
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Removed Mux hook: %s\n", path)
	return nil
}

func removeMuxProjectHooks(env agentsEnv) error {
	initPath, toolPostPath, toolEnvPath := muxProjectHookPaths(env.agentsPath)
	for _, path := range []string{initPath, toolPostPath, toolEnvPath} {
		if err := removeManagedMuxHook(env, path); err != nil {
			return err
		}
	}
	_ = os.Remove(muxProjectDir(env.agentsPath))
	return nil
}

// InstallMux installs Mux integration.
// When project=true, it also installs .mux/AGENTS.md.
// When global=true, it also installs ~/.mux/AGENTS.md.
func InstallMux(project bool, global bool) {
	env := muxEnvProvider()
	if err := installMux(env, project, global); err != nil {
		setupExit(1)
	}
}

func installMux(env agentsEnv, project bool, global bool) error {
	if err := installAgents(env, muxIntegration); err != nil {
		return err
	}
	if err := installMuxProjectHooks(env); err != nil {
		return err
	}

	if project {
		projectPath := muxProjectAgentsPath(env.agentsPath)
		if err := EnsureDir(filepath.Dir(projectPath), 0o755); err != nil {
			return err
		}

		projectEnv := env
		projectEnv.agentsPath = projectPath
		if err := installAgents(projectEnv, muxProjectIntegration); err != nil {
			return err
		}
	}

	if !global {
		return nil
	}

	globalPath, err := muxGlobalAgentsPath()
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(globalPath), 0o755); err != nil {
		return err
	}

	globalEnv := env
	globalEnv.agentsPath = globalPath
	return installAgents(globalEnv, muxGlobalIntegration)
}

// CheckMux checks if Mux integration is installed.
// When project=true, it also verifies .mux/AGENTS.md.
// When global=true, it also verifies ~/.mux/AGENTS.md.
func CheckMux(project bool, global bool) {
	env := muxEnvProvider()
	if err := checkMux(env, project, global); err != nil {
		setupExit(1)
	}
}

func checkMux(env agentsEnv, project bool, global bool) error {
	if err := checkAgents(env, muxIntegration); err != nil {
		return err
	}
	if err := checkMuxProjectHooks(env); err != nil {
		return err
	}

	if project {
		projectEnv := env
		projectEnv.agentsPath = muxProjectAgentsPath(env.agentsPath)
		if err := checkAgents(projectEnv, muxProjectIntegration); err != nil {
			return err
		}
	}

	if !global {
		return nil
	}

	globalPath, err := muxGlobalAgentsPath()
	if err != nil {
		return err
	}
	globalEnv := env
	globalEnv.agentsPath = globalPath
	return checkAgents(globalEnv, muxGlobalIntegration)
}

// RemoveMux removes Mux integration.
// When project=true, it also removes section from .mux/AGENTS.md.
// When global=true, it also removes section from ~/.mux/AGENTS.md.
func RemoveMux(project bool, global bool) {
	env := muxEnvProvider()
	if err := removeMux(env, project, global); err != nil {
		setupExit(1)
	}
}

func removeMux(env agentsEnv, project bool, global bool) error {
	if err := removeAgents(env, muxIntegration); err != nil {
		return err
	}
	if err := removeMuxProjectHooks(env); err != nil {
		return err
	}

	if project {
		projectEnv := env
		projectEnv.agentsPath = muxProjectAgentsPath(env.agentsPath)
		if err := removeAgents(projectEnv, muxProjectIntegration); err != nil {
			return err
		}
	}

	if !global {
		return nil
	}

	globalPath, err := muxGlobalAgentsPath()
	if err != nil {
		return err
	}
	globalEnv := env
	globalEnv.agentsPath = globalPath
	return removeAgents(globalEnv, muxGlobalIntegration)
}
