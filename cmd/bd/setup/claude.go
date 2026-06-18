package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/templates/agents"
)

var (
	claudeEnvProvider     = defaultClaudeEnv
	errClaudeHooksMissing = errors.New("claude hooks not installed")
)

const claudeInstructionsFile = "CLAUDE.md"

var claudeAgentsIntegration = agentsIntegration{
	name:         "Claude Code",
	setupCommand: "bd setup claude",
	profile:      agents.ProfileMinimal,
}

type claudeEnv struct {
	stdout     io.Writer
	stderr     io.Writer
	homeDir    string
	projectDir string
	ensureDir  func(string, os.FileMode) error
	readFile   func(string) ([]byte, error)
	writeFile  func(string, []byte) error
}

func defaultClaudeEnv() (claudeEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return claudeEnv{}, fmt.Errorf("home directory: %w", err)
	}
	workDir, err := os.Getwd()
	if err != nil {
		return claudeEnv{}, fmt.Errorf("working directory: %w", err)
	}
	return claudeEnv{
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

func projectSettingsPath(base string) string {
	return filepath.Join(base, ".claude", "settings.json")
}

func legacyProjectSettingsPath(base string) string {
	return filepath.Join(base, ".claude", "settings.local.json")
}

func globalSettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

func claudeAgentsEnv(env claudeEnv) agentsEnv {
	return agentsEnv{
		agentsPath: filepath.Join(env.projectDir, claudeInstructionsFile),
		stdout:     env.stdout,
		stderr:     env.stderr,
	}
}

// InstallClaude installs Claude Code hooks
func InstallClaude(global bool, stealth bool) {
	env, err := claudeEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := installClaude(env, global, stealth); err != nil {
		setupExit(1)
	}
}

// InstallClaudeProject installs project-local Claude hooks, returning an error
// instead of exiting. Used by bd init to integrate Claude setup automatically.
func InstallClaudeProject(stealth bool) error {
	env, err := claudeEnvProvider()
	if err != nil {
		return err
	}
	return installClaude(env, false, stealth)
}

func installClaude(env claudeEnv, global bool, stealth bool) error {
	var settingsPath string
	if global {
		settingsPath = globalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Installing Claude hooks globally...")
	} else {
		settingsPath = projectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Installing Claude hooks for this project...")
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
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		settings["hooks"] = hooks
	}

	// GH#955: Clean up any null values left by previous buggy removal
	// Claude Code expects arrays, not null values
	for key, val := range hooks {
		if val == nil {
			delete(hooks, key)
		}
	}

	command := "bd prime --hook-json"
	if stealth {
		command = "bd prime --stealth --hook-json"
	}

	// Migration sweep: remove bare "bd prime" variants registered by older
	// installations. Claude Code's current context-injection contract uses
	// the SessionStart JSON envelope; SessionStart also fires after compaction
	// with source=compact, so PreCompact no longer needs a bd prime hook.
	legacyBareVariants := []string{"bd prime", "bd prime --stealth"}
	for _, legacy := range legacyBareVariants {
		if legacy == command {
			continue
		}
		removeHookCommand(hooks, "SessionStart", legacy)
		removeHookCommand(hooks, "PreCompact", legacy)
	}
	removeHookCommand(hooks, "PreCompact", "bd prime --hook-json")
	removeHookCommand(hooks, "PreCompact", "bd prime --stealth --hook-json")

	// GH#3192: Skip writing hooks if the beads plugin is already providing them,
	// so project-level hooks don't fire bd prime twice per session.
	pluginManaged := hasBeadsPlugin(env)
	if pluginManaged {
		_, _ = fmt.Fprintln(env.stdout, "✓ Beads plugin detected — hooks are plugin-managed, skipping")
	} else {
		if addHookCommand(hooks, "SessionStart", command) {
			_, _ = fmt.Fprintln(env.stdout, "✓ Registered SessionStart hook")
		}
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: marshal settings: %v\n", err)
		return err
	}

	if err := env.writeFile(settingsPath, data); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write settings: %v\n", err)
		return err
	}

	// Migrate legacy hooks: remove beads hooks from settings.local.json if present
	if !global {
		legacyPath := legacyProjectSettingsPath(env.projectDir)
		if hasBeadsHooks(legacyPath) {
			if legacyData, readErr := env.readFile(legacyPath); readErr == nil {
				var legacySettings map[string]interface{}
				if json.Unmarshal(legacyData, &legacySettings) == nil {
					if legacyHooks, ok := legacySettings["hooks"].(map[string]interface{}); ok {
						for _, v := range []string{"bd prime", "bd prime --stealth", "bd prime --hook-json", "bd prime --stealth --hook-json"} {
							removeHookCommand(legacyHooks, "SessionStart", v)
							removeHookCommand(legacyHooks, "PreCompact", v)
						}
						if migrated, marshalErr := json.MarshalIndent(legacySettings, "", "  "); marshalErr == nil {
							if writeErr := env.writeFile(legacyPath, migrated); writeErr == nil {
								_, _ = fmt.Fprintf(env.stdout, "✓ Migrated hooks from %s\n", legacyPath)
							}
						}
					}
				}
			}
		}
	}

	// Install minimal beads section in CLAUDE.md.
	// Hooks handle the heavy lifting via bd prime; CLAUDE.md just needs a pointer.
	agentsEnv := claudeAgentsEnv(env)
	agentsSkipped := false
	agentsEnv.skipped = &agentsSkipped
	if err := installAgents(agentsEnv, claudeAgentsIntegration); err != nil {
		// Non-fatal: hooks are already installed
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", claudeInstructionsFile, err)
	}

	if agentsSkipped {
		_, _ = fmt.Fprintln(env.stdout, "\n✓ Claude Code hooks installed")
		_, _ = fmt.Fprintf(env.stdout, "  Agent instructions skipped: %s is a symlink\n", claudeInstructionsFile)
	} else {
		_, _ = fmt.Fprintln(env.stdout, "\n✓ Claude Code integration installed")
	}
	_, _ = fmt.Fprintf(env.stdout, "  Settings: %s\n", settingsPath)
	_, _ = fmt.Fprintln(env.stdout, "\nRestart Claude Code for changes to take effect.")
	return nil
}

// claudeSettingsUsesRemovedSyncCommand reports whether any hook command references
// bd sync (removed as a real command; GH#3546).
func claudeSettingsUsesRemovedSyncCommand(data []byte) bool {
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}
	for _, raw := range hooks {
		eventHooks, ok := raw.([]interface{})
		if !ok {
			continue
		}
		for _, hook := range eventHooks {
			hookMap, ok := hook.(map[string]interface{})
			if !ok {
				continue
			}
			cmds, ok := hookMap["hooks"].([]interface{})
			if !ok {
				continue
			}
			for _, c := range cmds {
				cmdMap, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				command, _ := cmdMap["command"].(string)
				if strings.Contains(command, "bd sync") {
					return true
				}
			}
		}
	}
	return false
}

func warnIfClaudeHooksUseRemovedSync(env claudeEnv) {
	paths := []string{
		projectSettingsPath(env.projectDir),
		globalSettingsPath(env.homeDir),
		legacyProjectSettingsPath(env.projectDir),
	}
	for _, p := range paths {
		data, err := env.readFile(p)
		if err != nil {
			continue
		}
		if !claudeSettingsUsesRemovedSyncCommand(data) {
			continue
		}
		_, _ = fmt.Fprintf(env.stderr, "Warning: %s contains a hook using removed \"bd sync\". Run bd setup claude to refresh hooks (bd prime / bd dolt push), or edit settings manually.\n", p)
	}
}

// CheckClaude checks if Claude integration is installed
func CheckClaude() {
	env, err := claudeEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := checkClaude(env); err != nil {
		setupExit(1)
	}
}

func checkClaude(env claudeEnv) error {
	warnIfClaudeHooksUseRemovedSync(env)

	projectSettings := projectSettingsPath(env.projectDir)
	globalSettings := globalSettingsPath(env.homeDir)
	legacySettings := legacyProjectSettingsPath(env.projectDir)

	switch {
	case hasBeadsHooks(projectSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Project hooks installed: %s\n", projectSettings)
	case hasBeadsHooks(globalSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Global hooks installed: %s\n", globalSettings)
	case hasBeadsHooks(legacySettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Project hooks installed (legacy): %s\n", legacySettings)
		_, _ = fmt.Fprintf(env.stdout, "  Consider running 'bd setup claude' to migrate to .claude/settings.json\n")
	case hasBeadsPlugin(env):
		// GH#3192: Plugin provides hooks via plugin.json — no project-level hooks needed
		_, _ = fmt.Fprintln(env.stdout, "✓ Hooks provided by beads plugin (plugin-managed)")
	default:
		_, _ = fmt.Fprintln(env.stdout, "✗ No hooks installed")
		_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup claude")
		return errClaudeHooksMissing
	}

	return checkAgents(claudeAgentsEnv(env), claudeAgentsIntegration)
}

// RemoveClaude removes Claude Code hooks
func RemoveClaude(global bool) {
	env, err := claudeEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := removeClaude(env, global); err != nil {
		setupExit(1)
	}
}

func removeClaude(env claudeEnv, global bool) error {
	var settingsPath string
	if global {
		settingsPath = globalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Claude hooks globally...")
	} else {
		settingsPath = projectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Claude hooks from project...")
	}

	data, err := env.readFile(settingsPath)
	if err != nil {
		_, _ = fmt.Fprintln(env.stdout, "No settings file found")
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
			for _, v := range []string{"bd prime", "bd prime --stealth", "bd prime --hook-json", "bd prime --stealth --hook-json"} {
				removeHookCommand(hooks, "SessionStart", v)
				removeHookCommand(hooks, "PreCompact", v)
			}

			data, err = json.MarshalIndent(settings, "", "  ")
			if err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: marshal settings: %v\n", err)
				return err
			}

			if err := env.writeFile(settingsPath, data); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: write settings: %v\n", err)
				return err
			}
		}
	}

	// Also clean legacy settings.local.json when removing project hooks
	if !global {
		legacyPath := legacyProjectSettingsPath(env.projectDir)
		if legacyData, readErr := env.readFile(legacyPath); readErr == nil {
			var legacySettings map[string]interface{}
			if json.Unmarshal(legacyData, &legacySettings) == nil {
				if legacyHooks, ok := legacySettings["hooks"].(map[string]interface{}); ok {
					for _, v := range []string{"bd prime", "bd prime --stealth", "bd prime --hook-json", "bd prime --stealth --hook-json"} {
						removeHookCommand(legacyHooks, "SessionStart", v)
						removeHookCommand(legacyHooks, "PreCompact", v)
					}
					if migrated, marshalErr := json.MarshalIndent(legacySettings, "", "  "); marshalErr == nil {
						_ = env.writeFile(legacyPath, migrated)
					}
				}
			}
		}
	}

	if err := removeAgents(claudeAgentsEnv(env), claudeAgentsIntegration); err != nil {
		// Non-fatal
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", claudeInstructionsFile, err)
	}

	_, _ = fmt.Fprintln(env.stdout, "✓ Claude hooks removed")
	return nil
}

// addHookCommand adds a hook command to an event if not already present
// Returns true if hook was added, false if already exists
func addHookCommand(hooks map[string]interface{}, event, command string) bool {
	// Get or create event array
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		eventHooks = []interface{}{}
	}

	// Check if bd hook already registered
	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			continue
		}
		commands, ok := hookMap["hooks"].([]interface{})
		if !ok {
			continue
		}
		for _, cmd := range commands {
			cmdMap, ok := cmd.(map[string]interface{})
			if !ok {
				continue
			}
			if cmdMap["command"] == command {
				fmt.Printf("✓ Hook already registered: %s\n", event)
				return false
			}
		}
	}

	// Add bd hook to array
	newHook := map[string]interface{}{
		"matcher": "",
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": command,
			},
		},
	}

	eventHooks = append(eventHooks, newHook)
	hooks[event] = eventHooks
	return true
}

// removeHookCommand removes a specific command from an event's hook entries.
// Only the matching command object is removed; sibling commands in the same
// hook entry are preserved. A hook entry is dropped only when its command list
// becomes empty after filtering.
func removeHookCommand(hooks map[string]interface{}, event, command string) {
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		return
	}

	// Initialize as empty slice (not nil) to avoid JSON null serialization.
	filtered := make([]interface{}, 0, len(eventHooks))
	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			filtered = append(filtered, hook)
			continue
		}

		commands, ok := hookMap["hooks"].([]interface{})
		if !ok {
			filtered = append(filtered, hook)
			continue
		}

		// Filter only the matching command; preserve any siblings.
		remaining := make([]interface{}, 0, len(commands))
		removed := false
		for _, cmd := range commands {
			cmdMap, ok := cmd.(map[string]interface{})
			if !ok {
				remaining = append(remaining, cmd)
				continue
			}
			if cmdMap["command"] == command {
				removed = true
				continue
			}
			remaining = append(remaining, cmd)
		}

		if removed {
			fmt.Printf("✓ Removed %s hook\n", event)
		}

		// Drop the hook entry only when it has no commands left.
		if len(remaining) > 0 {
			hookMap["hooks"] = remaining
			filtered = append(filtered, hookMap)
		}
	}

	// GH#955: Delete the key entirely if no hooks remain, rather than
	// leaving an empty array. This is cleaner and avoids potential
	// issues with empty arrays in settings.
	if len(filtered) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = filtered
	}
}

// hasBeadsPlugin checks if the beads Claude Code plugin is enabled in any
// settings file. The plugin declares its own SessionStart hooks in plugin.json,
// so project-level hooks from bd setup claude would duplicate them.
func hasBeadsPlugin(env claudeEnv) bool {
	paths := []string{
		projectSettingsPath(env.projectDir),
		globalSettingsPath(env.homeDir),
		legacyProjectSettingsPath(env.projectDir),
	}
	for _, p := range paths {
		if checkBeadsPluginInFile(env.readFile, p) {
			return true
		}
	}
	return false
}

// checkBeadsPluginInFile checks if the beads plugin is enabled in a single settings file.
func checkBeadsPluginInFile(readFile func(string) ([]byte, error), path string) bool {
	data, err := readFile(path)
	if err != nil {
		return false
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	enabledPlugins, ok := settings["enabledPlugins"].(map[string]interface{})
	if !ok {
		return false
	}
	for key, value := range enabledPlugins {
		// enabledPlugins keys are "<pluginName>@<marketplace>". Match the
		// plugin-name segment exactly: a substring test (GH#4244) mistakes any
		// "*beads*" plugin (e.g. design-to-beads) for the beads hook plugin and
		// wrongly skips the SessionStart hook write.
		name, _, _ := strings.Cut(strings.ToLower(key), "@")
		if name == "beads" {
			if enabled, ok := value.(bool); ok && enabled {
				return true
			}
		}
	}
	return false
}

// hasBeadsHooks checks if a settings file has bd prime hooks
func hasBeadsHooks(settingsPath string) bool {
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- settingsPath is constructed from known safe locations (user home/.claude), not user input
	if err != nil {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false
	}

	// Check SessionStart and PreCompact for "bd prime"
	for _, event := range []string{"SessionStart", "PreCompact"} {
		eventHooks, ok := hooks[event].([]interface{})
		if !ok {
			continue
		}

		for _, hook := range eventHooks {
			hookMap, ok := hook.(map[string]interface{})
			if !ok {
				continue
			}
			commands, ok := hookMap["hooks"].([]interface{})
			if !ok {
				continue
			}
			for _, cmd := range commands {
				cmdMap, ok := cmd.(map[string]interface{})
				if !ok {
					continue
				}
				// Recognize both current (--hook-json) and legacy bare variants.
				switch cmdMap["command"] {
				case "bd prime", "bd prime --stealth",
					"bd prime --hook-json", "bd prime --stealth --hook-json":
					return true
				}
			}
		}
	}

	return false
}
