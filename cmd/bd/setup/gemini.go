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
)

const geminiInstructionsFile = "GEMINI.md"

var geminiAgentsIntegration = agentsIntegration{
	name:         "Gemini CLI",
	setupCommand: "bd setup gemini",
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

func geminiAgentsEnv(env geminiEnv) agentsEnv {
	return agentsEnv{
		agentsPath: filepath.Join(env.projectDir, geminiInstructionsFile),
		stdout:     env.stdout,
		stderr:     env.stderr,
	}
}

// InstallGemini installs Gemini CLI hooks
func InstallGemini(project bool, stealth bool) {
	env, err := geminiEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := installGemini(env, project, stealth); err != nil {
		setupExit(1)
	}
}

func installGemini(env geminiEnv, project bool, stealth bool) error {
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
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		settings["hooks"] = hooks
	}

	command := "bd prime"
	if stealth {
		command = "bd prime --stealth"
	}

	// Gemini CLI uses "PreCompress" instead of Claude's "PreCompact"
	if addHookCommand(hooks, "SessionStart", command) {
		_, _ = fmt.Fprintln(env.stdout, "✓ Registered SessionStart hook")
	}
	if addHookCommand(hooks, "PreCompress", command) {
		_, _ = fmt.Fprintln(env.stdout, "✓ Registered PreCompress hook")
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

	// Install minimal beads section in GEMINI.md.
	// Hooks handle the heavy lifting via bd prime; GEMINI.md just needs a pointer.
	if err := installAgents(geminiAgentsEnv(env), geminiAgentsIntegration); err != nil {
		// Non-fatal: hooks are already installed
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", geminiInstructionsFile, err)
	}

	_, _ = fmt.Fprintln(env.stdout, "\n✓ Gemini CLI integration installed")
	_, _ = fmt.Fprintf(env.stdout, "  Settings: %s\n", settingsPath)
	_, _ = fmt.Fprintln(env.stdout, "\nRestart Gemini CLI for changes to take effect.")
	return nil
}

// CheckGemini checks if Gemini integration is installed
func CheckGemini() {
	env, err := geminiEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := checkGemini(env); err != nil {
		setupExit(1)
	}
}

func checkGemini(env geminiEnv) error {
	globalSettings := geminiGlobalSettingsPath(env.homeDir)
	projectSettings := geminiProjectSettingsPath(env.projectDir)

	switch {
	case hasGeminiBeadsHooks(globalSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Global hooks installed: %s\n", globalSettings)
	case hasGeminiBeadsHooks(projectSettings):
		_, _ = fmt.Fprintf(env.stdout, "✓ Project hooks installed: %s\n", projectSettings)
	default:
		_, _ = fmt.Fprintln(env.stdout, "✗ No hooks installed")
		_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup gemini")
		return errGeminiHooksMissing
	}

	return checkAgents(geminiAgentsEnv(env), geminiAgentsIntegration)
}

// RemoveGemini removes Gemini CLI hooks
func RemoveGemini(project bool) {
	env, err := geminiEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := removeGemini(env, project); err != nil {
		setupExit(1)
	}
}

func removeGemini(env geminiEnv, project bool) error {
	var settingsPath string
	if project {
		settingsPath = geminiProjectSettingsPath(env.projectDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Gemini CLI hooks from project...")
	} else {
		settingsPath = geminiGlobalSettingsPath(env.homeDir)
		_, _ = fmt.Fprintln(env.stdout, "Removing Gemini CLI hooks globally...")
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
			// Remove both variants from both events
			removeHookCommand(hooks, "SessionStart", "bd prime")
			removeHookCommand(hooks, "PreCompress", "bd prime")
			removeHookCommand(hooks, "SessionStart", "bd prime --stealth")
			removeHookCommand(hooks, "PreCompress", "bd prime --stealth")

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

	if err := removeAgents(geminiAgentsEnv(env), geminiAgentsIntegration); err != nil {
		// Non-fatal
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", geminiInstructionsFile, err)
	}

	_, _ = fmt.Fprintln(env.stdout, "✓ Gemini CLI hooks removed")
	return nil
}

// hasGeminiBeadsHooks checks if a settings file has bd prime hooks for Gemini CLI
func hasGeminiBeadsHooks(settingsPath string) bool {
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- settingsPath is constructed from known safe locations (user home/.gemini), not user input
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

	// Check SessionStart and PreCompress for "bd prime"
	for _, event := range []string{"SessionStart", "PreCompress"} {
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
				cmdStr := cmdMap["command"]
				if cmdStr == "bd prime" || cmdStr == "bd prime --stealth" {
					return true
				}
			}
		}
	}

	return false
}
