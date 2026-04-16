package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/steveyegge/beads/internal/templates/agents"
)

var codexIntegration = agentsIntegration{
	name:         "Codex CLI",
	setupCommand: "bd setup codex --agents",
	readHint:     "Codex reads AGENTS.md at the start of each run or session. Restart Codex if it is already running.",
	profile:      agents.ProfileFull,
}

var (
	codexEnvProvider     = defaultCodexEnv
	errCodexHooksMissing = errors.New("codex hooks not installed")
)

const (
	codexSessionStartHookCommand              = "bd prime --codex"
	codexSessionStartHookStealthCommand       = "bd prime --codex --stealth"
	codexUserPromptSubmitHookCommand          = "bd run-hook --codex"
	codexUserPromptSubmitHookStealthCommand   = "bd run-hook --codex --stealth"
	legacyCodexSessionStartHookCommand        = "bd prime --full"
	legacyCodexSessionStartHookStealthCommand = "bd prime --full --stealth"
	codexRunHookStateFile                     = "bd-run-hook-state.json"
)

type codexEnv struct {
	stdout     io.Writer
	stderr     io.Writer
	projectDir string
	ensureDir  func(string, os.FileMode) error
	readFile   func(string) ([]byte, error)
	writeFile  func(string, []byte) error
	removeFile func(string) error
}

func defaultCodexEnv() (codexEnv, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return codexEnv{}, fmt.Errorf("working directory: %w", err)
	}
	return codexEnv{
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		projectDir: workDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
		removeFile: os.Remove,
	}, nil
}

func codexAgentsEnv(env codexEnv) agentsEnv {
	return agentsEnv{
		agentsPath: filepath.Join(env.projectDir, "AGENTS.md"),
		stdout:     env.stdout,
		stderr:     env.stderr,
	}
}

func codexHooksPath(base string) string {
	return filepath.Join(base, ".codex", "hooks.json")
}

func codexConfigPath(base string) string {
	return filepath.Join(base, ".codex", "config.toml")
}

func codexGitignorePath(base string) string {
	return filepath.Join(base, ".codex", ".gitignore")
}

// InstallCodex installs Codex integration.
func InstallCodex(withHooks bool, stealth bool, withAgents bool) {
	env, err := codexEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := installCodex(env, withHooks, stealth, withAgents); err != nil {
		setupExit(1)
	}
}

func installCodex(env codexEnv, withHooks bool, stealth bool, withAgents bool) error {
	if err := installAgentSkill(agentSkillEnvFromCodex(env)); err != nil {
		return err
	}
	if withAgents {
		if err := installAgents(codexAgentsEnv(env), codexIntegration); err != nil {
			return err
		}
	}
	if withHooks {
		return installCodexProjectHooks(env, stealth)
	}
	return nil
}

// InstallCodexProjectHooks installs project-local Codex hooks, returning an
// error instead of exiting. Used by bd init after AGENTS.md is generated.
func InstallCodexProjectHooks(stealth bool) error {
	return InstallCodexProjectHooksWithOutput(stealth, os.Stdout, os.Stderr)
}

// InstallCodexProjectHooksWithOutput installs project-local Codex hooks using
// explicit output streams. Used by bd init to honor --quiet.
func InstallCodexProjectHooksWithOutput(stealth bool, stdout, stderr io.Writer) error {
	env, err := codexEnvProvider()
	if err != nil {
		return err
	}
	env.stdout = stdout
	env.stderr = stderr
	return installCodexProjectHooks(env, stealth)
}

func installCodexProjectHooks(env codexEnv, stealth bool) error {
	_, _ = fmt.Fprintln(env.stdout, "Installing Codex hooks for this project...")

	hooksDir := filepath.Dir(codexHooksPath(env.projectDir))
	if err := env.ensureDir(hooksDir, 0o755); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}

	sessionStartCommand := codexSessionStartHookCommand
	userPromptSubmitCommand := codexUserPromptSubmitHookCommand
	if stealth {
		sessionStartCommand = codexSessionStartHookStealthCommand
		userPromptSubmitCommand = codexUserPromptSubmitHookStealthCommand
	}

	hooksSettings := make(map[string]interface{})
	hooksPath := codexHooksPath(env.projectDir)
	if data, err := env.readFile(hooksPath); err == nil {
		if err := json.Unmarshal(data, &hooksSettings); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: failed to parse hooks.json: %v\n", err)
			return err
		}
	}

	hooks, ok := hooksSettings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		hooksSettings["hooks"] = hooks
	}

	removeManagedCodexSessionStartHooks(hooks, sessionStartCommand)
	removeManagedCodexUserPromptSubmitHooks(hooks, userPromptSubmitCommand)

	if addHookCommand(hooks, "SessionStart", sessionStartCommand) {
		_, _ = fmt.Fprintln(env.stdout, "✓ Registered SessionStart hook")
	}
	if addHookCommand(hooks, "UserPromptSubmit", userPromptSubmitCommand) {
		_, _ = fmt.Fprintln(env.stdout, "✓ Registered UserPromptSubmit hook")
	}

	hooksData, err := json.MarshalIndent(hooksSettings, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: marshal hooks.json: %v\n", err)
		return err
	}
	if err := env.writeFile(hooksPath, hooksData); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: write hooks.json: %v\n", err)
		return err
	}

	configPath := codexConfigPath(env.projectDir)
	configData, err := loadCodexConfig(env, configPath)
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}
	features, err := ensureCodexConfigTable(configData, "features")
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}
	features["codex_hooks"] = true
	if err := writeCodexConfig(env, configPath, configData); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}
	_, _ = fmt.Fprintln(env.stdout, "✓ Enabled codex_hooks feature")

	if err := ensureCodexGitignore(env); err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
		return err
	}

	_, _ = fmt.Fprintln(env.stdout, "\n✓ Codex CLI hook integration installed")
	_, _ = fmt.Fprintf(env.stdout, "  Hooks: %s\n", hooksPath)
	_, _ = fmt.Fprintf(env.stdout, "  Config: %s\n", configPath)
	_, _ = fmt.Fprintln(env.stdout, "\nRestart Codex for changes to take effect.")
	return nil
}

// CheckCodex checks if Codex integration is installed.
func CheckCodex(withHooks bool, withAgents bool) {
	env, err := codexEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := checkCodex(env, withHooks, withAgents); err != nil {
		setupExit(1)
	}
}

func checkCodex(env codexEnv, withHooks bool, withAgents bool) error {
	if err := checkAgentSkill(agentSkillEnvFromCodex(env), "bd setup codex"); err != nil {
		return err
	}
	if withHooks {
		hooksPath := codexHooksPath(env.projectDir)
		configPath := codexConfigPath(env.projectDir)
		switch {
		case hasCodexBeadsHooks(hooksPath):
			_, _ = fmt.Fprintf(env.stdout, "✓ Project hooks installed: %s\n", hooksPath)
		default:
			_, _ = fmt.Fprintln(env.stdout, "✗ No hooks installed")
			_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup codex --hooks")
			return errCodexHooksMissing
		}
		if codexHooksFeatureEnabled(configPath) {
			_, _ = fmt.Fprintf(env.stdout, "✓ codex_hooks enabled: %s\n", configPath)
		} else {
			_, _ = fmt.Fprintln(env.stdout, "✗ codex_hooks feature not enabled")
			_, _ = fmt.Fprintln(env.stdout, "  Run: bd setup codex --hooks")
			return errCodexHooksMissing
		}
	}
	if withAgents {
		return checkAgents(codexAgentsEnv(env), codexIntegration)
	}
	return nil
}

// RemoveCodex removes Codex integration.
func RemoveCodex(withHooks bool, withAgents bool) {
	env, err := codexEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := removeCodex(env, withHooks, withAgents); err != nil {
		setupExit(1)
	}
}

func removeCodex(env codexEnv, withHooks bool, withAgents bool) error {
	if err := removeAgentSkill(agentSkillEnvFromCodex(env)); err != nil {
		return err
	}
	if withHooks {
		_, _ = fmt.Fprintln(env.stdout, "Removing Codex hooks from project...")
		hooksPath := codexHooksPath(env.projectDir)
		if data, err := env.readFile(hooksPath); err == nil {
			var hooksSettings map[string]interface{}
			if err := json.Unmarshal(data, &hooksSettings); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: failed to parse hooks.json: %v\n", err)
				return err
			}
			if hooks, ok := hooksSettings["hooks"].(map[string]interface{}); ok {
				removeManagedCodexSessionStartHooks(hooks, "")
				removeManagedCodexUserPromptSubmitHooks(hooks, "")
				hooksData, err := json.MarshalIndent(hooksSettings, "", "  ")
				if err != nil {
					_, _ = fmt.Fprintf(env.stderr, "Error: marshal hooks.json: %v\n", err)
					return err
				}
				if err := env.writeFile(hooksPath, hooksData); err != nil {
					_, _ = fmt.Fprintf(env.stderr, "Error: write hooks.json: %v\n", err)
					return err
				}
			}
		} else {
			_, _ = fmt.Fprintln(env.stdout, "No hooks file found")
		}

		configPath := codexConfigPath(env.projectDir)
		configData, err := loadCodexConfig(env, configPath)
		if err == nil && removeCodexConfigBool(configData, "features", "codex_hooks") {
			if len(configData) == 0 {
				if err := env.removeFile(configPath); err != nil && !os.IsNotExist(err) {
					_, _ = fmt.Fprintf(env.stderr, "Error: remove config.toml: %v\n", err)
					return err
				}
			} else if err := writeCodexConfig(env, configPath, configData); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: %v\n", err)
				return err
			}
		}

		_, _ = fmt.Fprintln(env.stdout, "✓ Codex hooks removed")
	}

	if withAgents {
		return removeAgents(codexAgentsEnv(env), codexIntegration)
	}
	return nil
}

func removeManagedCodexSessionStartHooks(hooks map[string]interface{}, keepCommand string) {
	removeCodexHookCommands(hooks, "SessionStart", keepCommand, isManagedCodexSessionStartCommand)
}

func removeManagedCodexUserPromptSubmitHooks(hooks map[string]interface{}, keepCommand string) {
	removeCodexHookCommands(hooks, "UserPromptSubmit", keepCommand, isManagedCodexUserPromptSubmitCommand)
}

func isManagedCodexSessionStartCommand(command string) bool {
	switch command {
	case codexSessionStartHookCommand,
		codexSessionStartHookStealthCommand,
		legacyCodexSessionStartHookCommand,
		legacyCodexSessionStartHookStealthCommand:
		return true
	default:
		return false
	}
}

func isManagedCodexUserPromptSubmitCommand(command string) bool {
	switch command {
	case codexUserPromptSubmitHookCommand,
		codexUserPromptSubmitHookStealthCommand:
		return true
	default:
		return false
	}
}

func removeCodexHookCommands(hooks map[string]interface{}, event, keepCommand string, isManaged func(string) bool) {
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		return
	}

	filteredHooks := make([]interface{}, 0, len(eventHooks))
	for _, hook := range eventHooks {
		hookMap, ok := hook.(map[string]interface{})
		if !ok {
			filteredHooks = append(filteredHooks, hook)
			continue
		}
		commands, ok := hookMap["hooks"].([]interface{})
		if !ok {
			filteredHooks = append(filteredHooks, hook)
			continue
		}

		filteredCommands := make([]interface{}, 0, len(commands))
		for _, command := range commands {
			cmdMap, ok := command.(map[string]interface{})
			if !ok {
				filteredCommands = append(filteredCommands, command)
				continue
			}
			cmdString, ok := cmdMap["command"].(string)
			if !ok {
				filteredCommands = append(filteredCommands, command)
				continue
			}
			if isManaged(cmdString) && cmdString != keepCommand {
				continue
			}
			filteredCommands = append(filteredCommands, command)
		}

		if len(filteredCommands) == 0 {
			continue
		}
		hookMap["hooks"] = filteredCommands
		filteredHooks = append(filteredHooks, hookMap)
	}

	if len(filteredHooks) == 0 {
		delete(hooks, event)
		return
	}
	hooks[event] = filteredHooks
}

func loadCodexConfig(env codexEnv, path string) (map[string]interface{}, error) {
	config := make(map[string]interface{})
	data, err := env.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil
		}
		return nil, fmt.Errorf("read config.toml: %w", err)
	}
	if _, err := toml.Decode(string(data), &config); err != nil {
		return nil, fmt.Errorf("parse config.toml: %w", err)
	}
	return config, nil
}

func ensureCodexConfigTable(config map[string]interface{}, key string) (map[string]interface{}, error) {
	if existing, ok := config[key]; ok {
		if table, ok := existing.(map[string]interface{}); ok {
			return table, nil
		}
		return nil, fmt.Errorf("%s must be a TOML table", key)
	}
	table := make(map[string]interface{})
	config[key] = table
	return table, nil
}

func writeCodexConfig(env codexEnv, path string, config map[string]interface{}) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(config); err != nil {
		return fmt.Errorf("encode config.toml: %w", err)
	}
	if err := env.writeFile(path, buf.Bytes()); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	return nil
}

func removeCodexConfigBool(config map[string]interface{}, tableKey, valueKey string) bool {
	table, ok := config[tableKey].(map[string]interface{})
	if !ok {
		return false
	}
	if _, ok := table[valueKey]; !ok {
		return false
	}
	delete(table, valueKey)
	if len(table) == 0 {
		delete(config, tableKey)
	}
	return true
}

func ensureCodexGitignore(env codexEnv) error {
	path := codexGitignorePath(env.projectDir)
	var existing string
	data, err := env.readFile(path)
	switch {
	case err == nil:
		existing = string(data)
	case os.IsNotExist(err):
		// Create a small tool-local ignore file instead of mutating the
		// project root .gitignore from a tool-specific setup command.
	default:
		return fmt.Errorf("read .codex/.gitignore: %w", err)
	}
	if containsCodexGitignorePattern(existing, codexRunHookStateFile) {
		return nil
	}
	if existing != "" && existing[len(existing)-1] != '\n' {
		existing += "\n"
	}
	existing += codexRunHookStateFile + "\n"
	if err := env.writeFile(path, []byte(existing)); err != nil {
		return fmt.Errorf("write .codex/.gitignore: %w", err)
	}
	return nil
}

func containsCodexGitignorePattern(content, pattern string) bool {
	for _, line := range bytes.Split([]byte(content), []byte{'\n'}) {
		if string(bytes.TrimSpace(line)) == pattern {
			return true
		}
	}
	return false
}

func hasCodexBeadsHooks(hooksPath string) bool {
	data, err := os.ReadFile(hooksPath) // #nosec G304 -- hooksPath is constructed from project directory
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
	return hasCodexHookCommand(hooks, "SessionStart",
		codexSessionStartHookCommand,
		codexSessionStartHookStealthCommand,
		legacyCodexSessionStartHookCommand,
		legacyCodexSessionStartHookStealthCommand,
	) && hasCodexHookCommand(hooks, "UserPromptSubmit",
		codexUserPromptSubmitHookCommand,
		codexUserPromptSubmitHookStealthCommand,
	)
}

func hasCodexHookCommand(hooks map[string]interface{}, event string, commands ...string) bool {
	eventHooks, ok := hooks[event].([]interface{})
	if !ok {
		return false
	}
	wanted := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		wanted[command] = struct{}{}
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
		for _, command := range commands {
			cmdMap, ok := command.(map[string]interface{})
			if !ok {
				continue
			}
			command, ok := cmdMap["command"].(string)
			if !ok {
				continue
			}
			if _, ok := wanted[command]; ok {
				return true
			}
		}
	}
	return false
}

func codexHooksFeatureEnabled(configPath string) bool {
	data, err := os.ReadFile(configPath) // #nosec G304 -- configPath is constructed from project directory
	if err != nil {
		return false
	}
	var config map[string]interface{}
	if _, err := toml.Decode(string(data), &config); err != nil {
		return false
	}
	features, ok := config["features"].(map[string]interface{})
	if !ok {
		return false
	}
	value, ok := features["codex_hooks"]
	if !ok {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}
