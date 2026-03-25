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
	copilotEnvProvider     = defaultCopilotEnv
	errCopilotHooksMissing = errors.New("copilot hooks not installed")
)

const copilotInstructionsFile = ".github/copilot-instructions.md"

var copilotAgentsIntegration = agentsIntegration{
	name:         "GitHub Copilot",
	setupCommand: "bd setup copilot",
	profile:      agents.ProfileMinimal,
}

type copilotEnv struct {
	stdout     io.Writer
	stderr     io.Writer
	projectDir string
	ensureDir  func(string, os.FileMode) error
	readFile   func(string) ([]byte, error)
	writeFile  func(string, []byte) error
}

func defaultCopilotEnv() (copilotEnv, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return copilotEnv{}, fmt.Errorf("working directory: %w", err)
	}
	return copilotEnv{
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		projectDir: workDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
	}, nil
}

func copilotHooksPath(base string) string {
	return filepath.Join(base, ".github", "hooks", "beads.json")
}

func copilotAgentsEnv(env copilotEnv) agentsEnv {
	return agentsEnv{
		agentsPath: filepath.Join(env.projectDir, copilotInstructionsFile),
		stdout:     env.stdout,
		stderr:     env.stderr,
	}
}

// InstallCopilot installs GitHub Copilot CLI hooks and instructions.
func InstallCopilot(stealth bool) {
	env, err := copilotEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := installCopilot(env, stealth); err != nil {
		setupExit(1)
	}
}

func installCopilot(env copilotEnv, stealth bool) error {
	hooksPath := copilotHooksPath(env.projectDir)
	_, _ = fmt.Fprintln(env.stdout, "Installing GitHub Copilot CLI hooks...")

	if err := env.ensureDir(filepath.Dir(hooksPath), 0o755); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}

	// Read existing hooks file or start fresh
	hooksConfig := make(map[string]interface{})
	if data, err := env.readFile(hooksPath); err == nil {
		if err := json.Unmarshal(data, &hooksConfig); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to parse %s: %v\n", hooksPath, err)
			return err
		}
	}

	// Ensure version field
	hooksConfig["version"] = float64(1)

	// Get or create hooks object
	hooks, ok := hooksConfig["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		hooksConfig["hooks"] = hooks
	}

	command := "bd prime"
	if stealth {
		command = "bd prime --stealth"
	}

	if addCopilotHook(hooks, "sessionStart", command) {
		_, _ = fmt.Fprintln(env.stdout, "✓ Registered sessionStart hook")
	}

	data, err := json.MarshalIndent(hooksConfig, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: marshal hooks: %v\n", err)
		return err
	}

	if err := env.writeFile(hooksPath, data); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write hooks: %v\n", err)
		return err
	}

	// Install minimal beads section in copilot-instructions.md
	if err := installAgents(copilotAgentsEnv(env), copilotAgentsIntegration); err != nil {
		// Non-fatal: hooks are already installed
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", copilotInstructionsFile, err)
	}

	_, _ = fmt.Fprintln(env.stdout, "\n✓ GitHub Copilot integration installed")
	_, _ = fmt.Fprintf(env.stdout, "  Hooks: %s\n", hooksPath)
	_, _ = fmt.Fprintf(env.stdout, "  Instructions: %s\n", copilotInstructionsFile)
	_, _ = fmt.Fprintln(env.stdout, "\nCommit .github/hooks/beads.json to share hooks with your team.")
	return nil
}

// CheckCopilot checks if GitHub Copilot integration is installed.
func CheckCopilot() {
	env, err := copilotEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := checkCopilot(env); err != nil {
		setupExit(1)
	}
}

func checkCopilot(env copilotEnv) error {
	hooksPath := copilotHooksPath(env.projectDir)

	if hasCopilotBeadsHooks(hooksPath) {
		_, _ = fmt.Fprintf(env.stdout, "✓ Hooks installed: %s\n", hooksPath)
	} else {
		_, _ = fmt.Fprintln(env.stdout, "✗ No hooks installed")
		_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup copilot")
		return errCopilotHooksMissing
	}

	return checkAgents(copilotAgentsEnv(env), copilotAgentsIntegration)
}

// RemoveCopilot removes GitHub Copilot CLI hooks and instructions.
func RemoveCopilot() {
	env, err := copilotEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := removeCopilot(env); err != nil {
		setupExit(1)
	}
}

func removeCopilot(env copilotEnv) error {
	hooksPath := copilotHooksPath(env.projectDir)
	_, _ = fmt.Fprintln(env.stdout, "Removing GitHub Copilot hooks...")

	data, err := env.readFile(hooksPath)
	if err != nil {
		_, _ = fmt.Fprintln(env.stdout, "No hooks file found")
	} else {
		var hooksConfig map[string]interface{}
		if err := json.Unmarshal(data, &hooksConfig); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to parse %s: %v\n", hooksPath, err)
			return err
		}

		hooks, ok := hooksConfig["hooks"].(map[string]interface{})
		if !ok {
			_, _ = fmt.Fprintln(env.stdout, "No hooks found")
		} else {
			removeCopilotHook(hooks, "sessionStart", "bd prime")
			removeCopilotHook(hooks, "sessionStart", "bd prime --stealth")

			// If no hooks remain, remove the file entirely
			if len(hooks) == 0 {
				if err := os.Remove(hooksPath); err != nil && !os.IsNotExist(err) {
					_, _ = fmt.Fprintf(env.stderr, "Error: remove hooks file: %v\n", err)
					return err
				}
			} else {
				data, err = json.MarshalIndent(hooksConfig, "", "  ")
				if err != nil {
					_, _ = fmt.Fprintf(env.stderr, "Error: marshal hooks: %v\n", err)
					return err
				}
				if err := env.writeFile(hooksPath, data); err != nil {
					_, _ = fmt.Fprintf(env.stderr, "Error: write hooks: %v\n", err)
					return err
				}
			}
		}
	}

	if err := removeAgents(copilotAgentsEnv(env), copilotAgentsIntegration); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Warning: failed to update %s: %v\n", copilotInstructionsFile, err)
	}

	_, _ = fmt.Fprintln(env.stdout, "✓ GitHub Copilot hooks removed")
	return nil
}

// addCopilotHook adds a hook to the Copilot hooks config.
// Copilot uses a different format than Claude/Gemini:
//
//	{"type": "command", "bash": "<cmd>", "powershell": "<cmd>", "timeoutSec": 10}
//
// Returns true if the hook was added, false if already present.
func addCopilotHook(hooks map[string]interface{}, event, command string) bool {
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		eventHooks = []interface{}{}
	}

	// Check if already registered
	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			continue
		}
		if hookMap["bash"] == command {
			fmt.Printf("✓ Hook already registered: %s\n", event)
			return false
		}
	}

	newHook := map[string]interface{}{
		"type":       "command",
		"bash":       command,
		"powershell": command,
		"timeoutSec": float64(10),
	}

	eventHooks = append(eventHooks, newHook)
	hooks[event] = eventHooks
	return true
}

// removeCopilotHook removes a hook command from a Copilot hooks event.
func removeCopilotHook(hooks map[string]interface{}, event, command string) {
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		return
	}

	filtered := make([]interface{}, 0, len(eventHooks))
	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			filtered = append(filtered, hook)
			continue
		}
		if hookMap["bash"] == command {
			fmt.Printf("✓ Removed %s hook\n", event)
			continue
		}
		filtered = append(filtered, hook)
	}

	if len(filtered) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = filtered
	}
}

// hasCopilotBeadsHooks checks if the hooks file has bd prime hooks.
func hasCopilotBeadsHooks(hooksPath string) bool {
	data, err := os.ReadFile(hooksPath) // #nosec G304 -- hooksPath is constructed from known safe project directory
	if err != nil {
		return false
	}

	var hooksConfig map[string]interface{}
	if err := json.Unmarshal(data, &hooksConfig); err != nil {
		return false
	}

	hooks, ok := hooksConfig["hooks"].(map[string]interface{})
	if !ok {
		return false
	}

	eventHooks, ok := hooks["sessionStart"].([]interface{})
	if !ok {
		return false
	}

	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			continue
		}
		cmd, _ := hookMap["bash"].(string)
		if cmd == "bd prime" || cmd == "bd prime --stealth" {
			return true
		}
	}

	return false
}
