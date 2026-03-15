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
func InstallClaude(project bool, stealth bool) {
	env, err := claudeEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := installClaude(env, project, stealth); err != nil {
		setupExit(1)
	}
}

func installClaude(env claudeEnv, project bool, stealth bool) error {
	var settingsPath string
	if project {
		settingsPath = projectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Installing Claude hooks for this project...")
	} else {
		settingsPath = globalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Installing Claude hooks globally...")
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

	command := "bd prime"
	if stealth {
		command = "bd prime --stealth"
	}

	if addHookCommand(hooks, "SessionStart", command) {
		_, _ = fmt.Fprintln(env.stdout, "✓ Registered SessionStart hook")
	}
	if addHookCommand(hooks, "PreCompact", command) {
		_, _ = fmt.Fprintln(env.stdout, "✓ Registered PreCompact hook")
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

	// Install minimal beads section in CLAUDE.md.
	// Hooks handle the heavy lifting via bd prime; CLAUDE.md just needs a pointer.
	if err := installAgents(claudeAgentsEnv(env), claudeAgentsIntegration); err != nil {
		// Non-fatal: hooks are already installed
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", claudeInstructionsFile, err)
	}

	_, _ = fmt.Fprintln(env.stdout, "\n✓ Claude Code integration installed")
	_, _ = fmt.Fprintf(env.stdout, "  Settings: %s\n", settingsPath)
	_, _ = fmt.Fprintln(env.stdout, "\nRestart Claude Code for changes to take effect.")
	return nil
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
	globalSettings := globalSettingsPath(env.homeDir)
	projectSettings := projectSettingsPath(env.projectDir)

	switch {
	case hasBeadsHooks(globalSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Global hooks installed: %s\n", globalSettings)
	case hasBeadsHooks(projectSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Project hooks installed: %s\n", projectSettings)
	default:
		_, _ = fmt.Fprintln(env.stdout, "✗ No hooks installed")
		_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup claude")
		return errClaudeHooksMissing
	}

	return checkAgents(claudeAgentsEnv(env), claudeAgentsIntegration)
}

// RemoveClaude removes Claude Code hooks
func RemoveClaude(project bool) {
	env, err := claudeEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := removeClaude(env, project); err != nil {
		setupExit(1)
	}
}

func removeClaude(env claudeEnv, project bool) error {
	var settingsPath string
	if project {
		settingsPath = projectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Claude hooks from project...")
	} else {
		settingsPath = globalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Claude hooks globally...")
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
			removeHookCommand(hooks, "SessionStart", "bd prime")
			removeHookCommand(hooks, "PreCompact", "bd prime")
			removeHookCommand(hooks, "SessionStart", "bd prime --stealth")
			removeHookCommand(hooks, "PreCompact", "bd prime --stealth")

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

// removeHookCommand removes a hook command from an event
func removeHookCommand(hooks map[string]interface{}, event, command string) {
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		return
	}

	// Filter out bd prime hooks
	// Initialize as empty slice (not nil) to avoid JSON null serialization
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

		keepHook := true
		for _, cmd := range commands {
			cmdMap, ok := cmd.(map[string]interface{})
			if !ok {
				continue
			}
			if cmdMap["command"] == command {
				keepHook = false
				fmt.Printf("✓ Removed %s hook\n", event)
				break
			}
		}

		if keepHook {
			filtered = append(filtered, hook)
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
				// Check for either variant
				cmd := cmdMap["command"]
				if cmd == "bd prime" || cmd == "bd prime --stealth" {
					return true
				}
			}
		}
	}

	return false
}
