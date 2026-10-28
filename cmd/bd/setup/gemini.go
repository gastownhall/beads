package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/templates/agents"
)

var (
	geminiEnvProvider     = defaultGeminiEnv
	errGeminiHooksMissing = errors.New("gemini hooks not installed")
	errGeminiHooksLegacy  = errors.New("gemini hooks need upgrade")
)

const geminiInstructionsFile = "GEMINI.md"

// geminiMemoriesHeading opens the section Gemini CLI's save_memory tool
// (through v0.43) keeps in ~/.gemini/GEMINI.md. It adds each memory at the
// end of that section, which it takes to run to the next "## " heading.
const geminiMemoriesHeading = "## Gemini Added Memories"

// geminiAgentsIntegration is the beads section in the user-level
// ~/.gemini/GEMINI.md, which the default, global install maintains.
var geminiAgentsIntegration = agentsIntegration{
	name:         "Gemini CLI",
	setupCommand: "bd setup gemini",
	profile:      agents.ProfileMinimal,
}

// geminiProjectAgentsIntegration is geminiAgentsIntegration for a project's
// GEMINI.md, so its hints name the command that maintains that file.
var geminiProjectAgentsIntegration = agentsIntegration{
	name:         "Gemini CLI",
	setupCommand: "bd setup gemini --project",
	profile:      agents.ProfileMinimal,
}

type geminiEnv struct {
	stdout     io.Writer
	stderr     io.Writer
	homeDir    string
	projectDir string
	ensureDir  func(string, os.FileMode) error
	readFile   func(string) ([]byte, error)
	writeFile  func(string, []byte) error
}

func defaultGeminiEnv() (geminiEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return geminiEnv{}, fmt.Errorf("home directory: %w", err)
	}
	workDir, err := os.Getwd()
	if err != nil {
		return geminiEnv{}, fmt.Errorf("working directory: %w", err)
	}
	return geminiEnv{
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		homeDir:    home,
		projectDir: workDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
	}, nil
}

func geminiProjectSettingsPath(base string) string {
	return filepath.Join(base, ".gemini", "settings.json")
}

func geminiGlobalSettingsPath(home string) string {
	return filepath.Join(home, ".gemini", "settings.json")
}

// geminiGlobalInstructionsPath is Gemini CLI's user-level context file, which
// it loads in every project. A global install keeps its managed beads section
// there, beside the ~/.gemini/settings.json its hook goes in.
func geminiGlobalInstructionsPath(home string) string {
	return filepath.Join(home, ".gemini", geminiInstructionsFile)
}

// geminiInstructionsEnv resolves the file an install or removal manages the
// beads section in, with the integration whose hints name its command. A
// global install, the default, uses the user-level ~/.gemini/GEMINI.md and
// leaves the current directory's files alone. Like bd setup claude --global's
// ~/.claude/CLAUDE.md, it renders that file with fixed options: every project
// shares it, so it must not follow one project's config, and Gemini CLI loads
// it in projects that do not use beads too, so its section applies only in
// those that do. A project install uses the project's GEMINI.md, except where
// it is the global install (geminiProjectIsUserLevel).
func geminiInstructionsEnv(env geminiEnv, project bool) (agentsEnv, agentsIntegration) {
	if project && !geminiProjectIsUserLevel(env) {
		return agentsEnv{
			agentsPath: filepath.Join(env.projectDir, geminiInstructionsFile),
			stdout:     env.stdout,
			stderr:     env.stderr,
		}, geminiProjectAgentsIntegration
	}
	return agentsEnv{
		agentsPath:  geminiGlobalInstructionsPath(env.homeDir),
		stdout:      env.stdout,
		stderr:      env.stderr,
		sectionOnly: true,
		renderOpts:  agents.UserLevelRenderOpts,
		// Below Gemini CLI's memories the section's own "## " heading would
		// end them, and memories added there would land inside the section,
		// where an update or --remove deletes them.
		insertBefore: geminiMemoriesHeading,
	}, geminiAgentsIntegration
}

// geminiCheckAgentsEnv picks the instructions file --check reports on, as
// claudeCheckAgentsEnv does for Claude Code. It is the file a project install
// here manages, which from the home directory and ~/.gemini is the
// user-level one, unless the hooks are global and that file has no beads
// section. Then it is the ~/.gemini/GEMINI.md a global install writes, which
// Gemini CLI loads alongside the project's own context files, if that has a
// section or if the project's file does not exist. A project install is
// judged on the project's file alone.
func geminiCheckAgentsEnv(env geminiEnv, globalHooks bool) (agentsEnv, agentsIntegration) {
	projectEnv, projectIntegration := geminiInstructionsEnv(env, true)
	if !globalHooks || geminiFileHasBeadsSection(env, projectEnv.agentsPath) {
		return projectEnv, projectIntegration
	}
	globalEnv, globalIntegration := geminiInstructionsEnv(env, false)
	if geminiFileHasBeadsSection(env, globalEnv.agentsPath) {
		return globalEnv, globalIntegration
	}
	if _, err := env.readFile(projectEnv.agentsPath); err != nil {
		return globalEnv, globalIntegration
	}
	return projectEnv, projectIntegration
}

// geminiProjectIsUserLevel reports whether a project install from the current
// directory is the global install: from the home directory, whose
// .gemini/settings.json is the global one, so that its GEMINI.md is not the
// ~/GEMINI.md a global install exists to avoid, and from ~/.gemini, whose
// GEMINI.md is the user-level one.
func geminiProjectIsUserLevel(env geminiEnv) bool {
	return sameDir(env.projectDir, env.homeDir) ||
		sameDir(env.projectDir, filepath.Dir(geminiGlobalInstructionsPath(env.homeDir)))
}

func geminiFileHasBeadsSection(env geminiEnv, path string) bool {
	data, err := env.readFile(path)
	return err == nil && containsBeadsMarker(string(data))
}

func InstallGemini(project bool, stealth bool) error {
	env, err := geminiEnvProvider()
	if err != nil {
		return HandleError("%v", err)
	}
	return installGemini(env, project, stealth)
}

func installGemini(env geminiEnv, project bool, stealth bool) error {
	// From the home directory and ~/.gemini a project install is the global
	// install (geminiProjectIsUserLevel): it manages the user-level section,
	// so its hook goes in the global settings file beside that, not in
	// ~/.gemini/.gemini/settings.json.
	project = project && !geminiProjectIsUserLevel(env)

	var settingsPath string
	if project {
		settingsPath = geminiProjectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Installing Gemini CLI hooks for this project...")
	} else {
		settingsPath = geminiGlobalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Installing Gemini CLI hooks globally...")
	}

	if err := env.ensureDir(filepath.Dir(settingsPath), 0o755); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}

	settings := make(map[string]interface{})
	if data, err := env.readFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to parse settings.json: %v\n", err)
			return err
		}
	} else if !os.IsNotExist(err) {
		// As in installClaude: only a missing file starts from empty
		// settings, or an unreadable one is replaced by bd's hook alone.
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", settingsPath, err)
		return err
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		settings["hooks"] = hooks
	}

	// Gemini CLI (and Claude Code, Codex) require hook stdout to be valid JSON;
	// --hook-json wraps bd prime's markdown in the shared SessionStart envelope.
	// PreCompress is intentionally NOT registered: Gemini's PreCompress event is
	// advisory-only and does not support additionalContext injection — context
	// re-injection after compression is architecturally impossible there.
	command := "bd prime --hook-json"
	if stealth {
		command = "bd prime --stealth --hook-json"
	}

	// Migration sweep: remove all known legacy command variants before registering
	// the canonical command. Re-running setup must be a clean upgrade path —
	// stale entries alongside the new one cause Gemini to invoke both, and any
	// pre-JSON variant emits raw markdown that violates the strict hook contract.
	legacyVariants := []string{"bd prime", "bd prime --stealth"}
	for _, legacy := range legacyVariants {
		if legacy == command {
			continue // never remove the variant we're about to add
		}
		removeHookCommand(env.stdout, hooks, "SessionStart", legacy)
		removeHookCommand(env.stdout, hooks, "PreCompress", legacy)
	}
	// Also clear any --hook-json registration from PreCompress. Beads previously
	// registered bd prime on PreCompress before we discovered that Gemini's
	// PreCompress event is advisory-only and cannot inject additionalContext
	// into the model regardless of output format. Re-running setup migrates cleanly.
	removeHookCommand(env.stdout, hooks, "PreCompress", "bd prime --hook-json")
	removeHookCommand(env.stdout, hooks, "PreCompress", "bd prime --stealth --hook-json")

	if addHookCommand(hooks, "SessionStart", command) {
		_, _ = fmt.Fprintln(env.stdout, "✓ Registered SessionStart hook")
	}

	data, err := marshalSettings(settings)
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: marshal settings: %v\n", err)
		return err
	}

	if err := writeSettingsIfChanged(env.readFile, env.writeFile, settingsPath, data); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write settings: %v\n", err)
		return err
	}

	// Install minimal beads section in GEMINI.md: the user-level one for a
	// global install, the project's for --project. Hooks handle the heavy
	// lifting via bd prime; GEMINI.md just needs a pointer.
	agentsEnv, integration := geminiInstructionsEnv(env, project)
	if err := installAgents(agentsEnv, integration); err != nil {
		// Non-fatal: hooks are already installed
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", geminiInstructionsFile, err)
	}

	_, _ = fmt.Fprintln(env.stdout, "\n✓ Gemini CLI integration installed")
	_, _ = fmt.Fprintf(env.stdout, "  Settings: %s\n", settingsPath)
	_, _ = fmt.Fprintln(env.stdout, "\nRestart Gemini CLI for changes to take effect.")
	return nil
}

func CheckGemini() error {
	env, err := geminiEnvProvider()
	if err != nil {
		return HandleError("%v", err)
	}
	return checkGemini(env)
}

func checkGemini(env geminiEnv) error {
	globalSettings := geminiGlobalSettingsPath(env.homeDir)
	projectSettings := geminiProjectSettingsPath(env.projectDir)

	globalHooks := false
	switch {
	case hasCurrentGeminiHooks(globalSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Global hooks installed (current): %s\n", globalSettings)
		globalHooks = true
	case hasCurrentGeminiHooks(projectSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Project hooks installed (current): %s\n", projectSettings)
	case hasGeminiBeadsHooks(globalSettings):
		_, _ = fmt.Fprintf(env.stdout, "⚠ Global hooks installed (legacy format): %s\n", globalSettings)
		_, _ = fmt.Fprintln(env.stdout, "  Legacy 'bd prime' hooks emit raw markdown; Gemini requires JSON stdout.")
		_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup gemini  (upgrades to 'bd prime --hook-json')")
		return errGeminiHooksLegacy
	case hasGeminiBeadsHooks(projectSettings):
		_, _ = fmt.Fprintf(env.stdout, "⚠ Project hooks installed (legacy format): %s\n", projectSettings)
		_, _ = fmt.Fprintln(env.stdout, "  Legacy 'bd prime' hooks emit raw markdown; Gemini requires JSON stdout.")
		_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup gemini --project  (upgrades to 'bd prime --hook-json')")
		return errGeminiHooksLegacy
	default:
		_, _ = fmt.Fprintln(env.stdout, "✗ No hooks installed")
		_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup gemini")
		return errGeminiHooksMissing
	}

	agentsEnv, integration := geminiCheckAgentsEnv(env, globalHooks)
	return checkAgents(agentsEnv, integration)
}

func RemoveGemini(project bool) error {
	env, err := geminiEnvProvider()
	if err != nil {
		return HandleError("%v", err)
	}
	return removeGemini(env, project)
}

func removeGemini(env geminiEnv, project bool) error {
	// From the home directory and ~/.gemini a project removal is the global
	// removal, as installGemini makes the install there: it removes the
	// global hooks and the user-level section paired with them.
	project = project && !geminiProjectIsUserLevel(env)

	var settingsPath string
	if project {
		settingsPath = geminiProjectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Gemini CLI hooks from project...")
	} else {
		settingsPath = geminiGlobalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Gemini CLI hooks globally...")
	}

	data, err := env.readFile(settingsPath)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintln(env.stdout, "No settings file found")
	} else if err != nil {
		// A settings file that cannot be read is not a missing one: stop
		// with the section kept, as bd setup claude --remove does.
		_, _ = fmt.Fprintf(env.stderr, "Error: failed to read %s: %v\n", settingsPath, err)
		return err
	} else {
		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to parse settings.json: %v\n", err)
			return err
		}

		hooks, ok := settings["hooks"].(map[string]interface{})
		if !ok {
			_, _ = fmt.Fprintln(env.stdout, "No hooks found")
		} else {
			// Remove all known variants from both events. PreCompress is
			// included for migration safety: older installations registered
			// bd prime there before we discovered Gemini's PreCompress hook
			// can't inject additionalContext.
			variants := []string{
				"bd prime",
				"bd prime --stealth",
				"bd prime --hook-json",
				"bd prime --stealth --hook-json",
			}
			for _, cmd := range variants {
				removeHookCommand(env.stdout, hooks, "SessionStart", cmd)
				removeHookCommand(env.stdout, hooks, "PreCompress", cmd)
			}

			data, err = marshalSettings(settings)
			if err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: marshal settings: %v\n", err)
				return err
			}

			if err := writeSettingsIfChanged(env.readFile, env.writeFile, settingsPath, data); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write settings: %v\n", err)
				return err
			}
		}
	}

	agentsEnv, integration := geminiInstructionsEnv(env, project)
	if err := removeAgents(agentsEnv, integration); err != nil {
		// Non-fatal
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", geminiInstructionsFile, err)
	}

	_, _ = fmt.Fprintln(env.stdout, "✓ Gemini CLI hooks removed")
	return nil
}

// geminiSessionStartCommands returns all command strings registered under the
// SessionStart hook event in a settings file. Returns nil on any read/parse
// error. Detection scope is SessionStart only — PreCompress is advisory-only
// in Gemini CLI and does not support additionalContext injection.
func geminiSessionStartCommands(settingsPath string) []string {
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- path constructed from known safe locations (user home/.gemini or cwd/.gemini)
	if err != nil {
		return nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return nil
	}
	eventHooks, ok := hooks["SessionStart"].([]interface{})
	if !ok {
		return nil
	}
	var cmds []string
	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			continue
		}
		hookCmds, ok := hookMap["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, cmd := range hookCmds {
			cmdMap, ok := cmd.(map[string]interface{})
			if !ok {
				continue
			}
			if s, ok := cmdMap["command"].(string); ok {
				cmds = append(cmds, s)
			}
		}
	}
	return cmds
}

// hasCurrentGeminiHooks reports whether a settings file has a current-format
// bd prime --hook-json command on SessionStart. Returns false for legacy
// "bd prime" installs, which emit raw markdown that violates Gemini's strict
// stdout-must-be-JSON hook contract.
func hasCurrentGeminiHooks(settingsPath string) bool {
	for _, cmd := range geminiSessionStartCommands(settingsPath) {
		if cmd == "bd prime --hook-json" || cmd == "bd prime --stealth --hook-json" {
			return true
		}
	}
	return false
}

// hasGeminiBeadsHooks reports whether a settings file has any bd prime hook
// on SessionStart — current format or legacy. Used to detect pre-fix
// installations that need upgrading via bd setup gemini.
func hasGeminiBeadsHooks(settingsPath string) bool {
	for _, cmd := range geminiSessionStartCommands(settingsPath) {
		switch cmd {
		case "bd prime", "bd prime --stealth",
			"bd prime --hook-json", "bd prime --stealth --hook-json":
			return true
		}
	}
	return false
}
