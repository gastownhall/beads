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

// muxGlobalAgentsEnv returns env set up for the global layer's file,
// ~/.mux/AGENTS.md, which Mux loads in every workspace. It is rendered with
// fixed options: every project shares it, so it must not follow one
// project's beads config, and Mux loads it in workspaces that do not use
// beads too, so its section applies only in those that do. A new file holds
// just the section, without the project scaffolding.
func muxGlobalAgentsEnv(env agentsEnv, globalPath string) agentsEnv {
	env.agentsPath = globalPath
	env.sectionOnly = true
	env.renderOpts = agents.UserLevelRenderOpts
	return env
}

// muxInGlobalDir reports whether path is in ~/.mux, the global layer's
// directory. Run from there, bd runs in no workspace: its hooks would land in
// ~/.mux/.mux, which Mux does not load.
func muxInGlobalDir(path string) bool {
	globalPath, err := muxGlobalAgentsPath()
	return err == nil && sameDir(filepath.Dir(path), filepath.Dir(globalPath))
}

// muxNoWorkspaceLayerNote is what setup and --check print for the workspace
// layer when bd runs from ~/.mux (installMuxProject).
const muxNoWorkspaceLayerNote = "ℹ Mux workspace layer skipped: ~/.mux is not a workspace, and Mux does not load ~/.mux/.mux/AGENTS.md"

// muxNestedHooksNote is how setup and --check run from ~/.mux name the
// hooks an older bd installed in ~/.mux/.mux (noteMuxNestedHooks).
const muxNestedHooksNote = "ℹ Mux does not load the hooks an earlier bd installed in ~/.mux/.mux: "

// noteMuxNestedHooks names the hooks carrying bd's marker in the .mux
// directory of ~/.mux, for setup and --check run from there, which install
// and check none (installMuxRoot): an older bd installed them, and Mux does
// not load them. bd setup mux --remove run there removes them, and with
// them the global section, since there the root AGENTS.md is the global
// layer's file (removeMux).
func noteMuxNestedHooks(env agentsEnv) {
	initPath, toolPostPath, toolEnvPath := muxProjectHookPaths(env.agentsPath)
	var hooks []string
	for _, path := range []string{initPath, toolPostPath, toolEnvPath} {
		data, err := os.ReadFile(path) // #nosec G304 -- generated internal paths only
		if err == nil && strings.Contains(string(data), muxHookMarkerBegin) {
			hooks = append(hooks, path)
		}
	}
	if len(hooks) == 0 {
		return
	}
	_, _ = fmt.Fprintln(env.stdout, muxNestedHooksNote+strings.Join(hooks, ", "))
	_, _ = fmt.Fprintln(env.stdout, "  Delete them, or run bd setup mux --remove here and then bd setup mux --global (--remove here also removes the global section)")
}

// muxGlobalLayerFile reports whether path is the global layer's file,
// ~/.mux/AGENTS.md, and returns that file's path: run from the home
// directory, the workspace layer's .mux/AGENTS.md is, and run from ~/.mux,
// so is the root AGENTS.md.
func muxGlobalLayerFile(path string) (string, bool) {
	globalPath, err := muxGlobalAgentsPath()
	if err != nil || filepath.Base(path) != filepath.Base(globalPath) || !muxInGlobalDir(path) {
		return "", false
	}
	return globalPath, true
}

// muxLayerEnv returns the env and integration that manage a layer's file.
// The global layer's file is managed as the global layer whichever layer
// names it (muxGlobalLayerFile). Otherwise one run would write that file one
// way and then the other, and --check would always find one of the two
// stale.
func muxLayerEnv(env agentsEnv, integration agentsIntegration) (agentsEnv, agentsIntegration) {
	globalPath, ok := muxGlobalLayerFile(env.agentsPath)
	if !ok {
		return env, integration
	}
	return muxGlobalAgentsEnv(env, globalPath), muxGlobalIntegration
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

// removeManagedMuxHook removes the hook at path if it carries bd's marker,
// and reports whether it did.
func removeManagedMuxHook(env agentsEnv, path string) (bool, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- generated internal paths only
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !strings.Contains(string(data), muxHookMarkerBegin) {
		_, _ = fmt.Fprintf(env.stdout, "ℹ Kept existing custom hook: %s\n", path)
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Removed Mux hook: %s\n", path)
	return true, nil
}

// removeMuxProjectHooks removes the hooks bd installed in the .mux directory
// of the directory bd runs in, and then that directory if they were all it
// held. It removes no directory it took no hook from, and never a .mux that
// is a symlink, or on Windows a junction: removing one unlinks it, whatever
// its target holds. Run from the home directory, that .mux is ~/.mux, which
// Xum's migration replaces with a link to ~/.xum.
func removeMuxProjectHooks(env agentsEnv) error {
	initPath, toolPostPath, toolEnvPath := muxProjectHookPaths(env.agentsPath)
	removed := false
	for _, path := range []string{initPath, toolPostPath, toolEnvPath} {
		ok, err := removeManagedMuxHook(env, path)
		if err != nil {
			return err
		}
		removed = removed || ok
	}
	if removed {
		_ = RemoveEmptyDir(muxProjectDir(env.agentsPath))
	}
	return nil
}

// InstallMux installs Mux integration.
// When project=true, it also installs .mux/AGENTS.md.
// When global=true, it also installs ~/.mux/AGENTS.md.
func InstallMux(project bool, global bool) error {
	return installMux(muxEnvProvider(), project, global)
}

// installMuxRoot installs the root layer: its agents file and the hooks of
// the workspace bd runs in. Run from ~/.mux, bd runs in no workspace
// (muxInGlobalDir), so it installs no hooks, whatever the agents file is
// named, and names those an older bd installed there (noteMuxNestedHooks);
// a root AGENTS.md there is the global layer's file (muxLayerEnv).
func installMuxRoot(env agentsEnv) error {
	skipped := false
	env.skipped = &skipped
	if err := installAgents(muxLayerEnv(env, muxIntegration)); err != nil {
		return err
	}
	if muxInGlobalDir(env.agentsPath) {
		noteMuxNestedHooks(env)
		return nil
	}
	if err := installMuxProjectHooks(env); err != nil {
		return err
	}
	if skipped {
		_, _ = fmt.Fprintf(env.stdout, "✓ Mux hooks installed; managed section skipped for symlinked %s\n", env.agentsPath)
	}
	return nil
}

// installMuxProject installs the workspace layer's .mux/AGENTS.md. Run from
// ~/.mux, bd runs in no workspace (installMuxRoot), so it installs none: the
// file would be ~/.mux/.mux/AGENTS.md, which Mux does not load.
func installMuxProject(env agentsEnv) error {
	if muxInGlobalDir(env.agentsPath) {
		_, _ = fmt.Fprintln(env.stdout, muxNoWorkspaceLayerNote)
		return nil
	}
	projectPath := muxProjectAgentsPath(env.agentsPath)
	if err := EnsureDir(filepath.Dir(projectPath), 0o755); err != nil {
		return err
	}

	projectEnv := env
	projectEnv.agentsPath = projectPath
	projectSkipped := false
	projectEnv.skipped = &projectSkipped
	if err := installAgents(muxLayerEnv(projectEnv, muxProjectIntegration)); err != nil {
		return err
	}
	if projectSkipped {
		_, _ = fmt.Fprintf(env.stdout, "✓ Mux project hooks installed; managed section skipped for symlinked %s\n", projectPath)
	}
	return nil
}

func installMux(env agentsEnv, project bool, global bool) error {
	if err := installMuxRoot(env); err != nil {
		return err
	}

	if project {
		if err := installMuxProject(env); err != nil {
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

	globalEnv := muxGlobalAgentsEnv(env, globalPath)
	globalSkipped := false
	globalEnv.skipped = &globalSkipped
	if err := installAgents(globalEnv, muxGlobalIntegration); err != nil {
		return err
	}
	if globalSkipped {
		_, _ = fmt.Fprintf(env.stdout, "✓ Mux global setup completed; managed section skipped for symlinked %s\n", globalPath)
	}
	return nil
}

// CheckMux checks if Mux integration is installed.
// When project=true, it also verifies .mux/AGENTS.md.
// When global=true, it also verifies ~/.mux/AGENTS.md.
func CheckMux(project bool, global bool) error {
	return checkMux(muxEnvProvider(), project, global)
}

// checkMuxRoot checks the root layer: its agents file and, unless bd runs
// in ~/.mux (installMuxRoot), the workspace's hooks.
func checkMuxRoot(env agentsEnv) error {
	if err := checkAgents(muxLayerEnv(env, muxIntegration)); err != nil {
		return err
	}
	if muxInGlobalDir(env.agentsPath) {
		noteMuxNestedHooks(env)
		return nil
	}
	return checkMuxProjectHooks(env)
}

// checkMuxProject checks the workspace layer's .mux/AGENTS.md, unless bd
// runs in ~/.mux (installMuxProject).
func checkMuxProject(env agentsEnv) error {
	if muxInGlobalDir(env.agentsPath) {
		_, _ = fmt.Fprintln(env.stdout, muxNoWorkspaceLayerNote)
		return nil
	}
	projectEnv := env
	projectEnv.agentsPath = muxProjectAgentsPath(env.agentsPath)
	return checkAgents(muxLayerEnv(projectEnv, muxProjectIntegration))
}

func checkMux(env agentsEnv, project bool, global bool) error {
	if err := checkMuxRoot(env); err != nil {
		return err
	}

	if project {
		if err := checkMuxProject(env); err != nil {
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
	return checkAgents(muxGlobalAgentsEnv(env, globalPath), muxGlobalIntegration)
}

// RemoveMux removes Mux integration.
// When project=true, it also removes section from .mux/AGENTS.md.
// When global=true, it also removes section from ~/.mux/AGENTS.md.
func RemoveMux(project bool, global bool) error {
	return removeMux(muxEnvProvider(), project, global)
}

func removeMux(env agentsEnv, project bool, global bool) error {
	// Run from ~/.mux, the root AGENTS.md is the global layer's file, so this
	// removes the global layer's section, as setup there installs it, and
	// reports it as the global layer (muxLayerEnv).
	if err := removeAgents(muxLayerEnv(env, muxIntegration)); err != nil {
		return err
	}
	// Run from ~/.mux this clears the hooks an older bd installed in
	// ~/.mux/.mux; installMuxRoot installs none there.
	if err := removeMuxProjectHooks(env); err != nil {
		return err
	}

	// Run from ~/.mux this removes the section an older bd installed in
	// ~/.mux/.mux/AGENTS.md; installMuxProject installs none there.
	if project {
		projectEnv := env
		projectEnv.agentsPath = muxProjectAgentsPath(env.agentsPath)
		if err := removeAgents(muxLayerEnv(projectEnv, muxProjectIntegration)); err != nil {
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
