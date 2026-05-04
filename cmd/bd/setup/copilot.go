package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/steveyegge/beads/internal/templates/agents"
)

var (
	copilotEnvProvider     = defaultCopilotEnv
	errCopilotHooksMissing = errors.New("copilot hooks not installed")
	errCopilotVersionOld   = errors.New("copilot cli version too old for preCompact hooks")
)

const (
	copilotProjectInstructionsFile = ".github/copilot-instructions.md"
	copilotGlobalInstructionsFile  = ".copilot/copilot-instructions.md"
	copilotGlobalHooksFile         = ".copilot/hooks/beads-copilot.json"
	copilotHooksFile               = ".github/hooks/beads-copilot.json"
	copilotMinVersion              = "1.0.5"
	copilotHookComment             = "Installed by bd setup copilot"
)

var (
	copilotProjectIntegration = agentsIntegration{
		name:         "GitHub Copilot CLI (project)",
		setupCommand: "bd setup copilot --project",
		readHint:     "Copilot CLI reads .github/copilot-instructions.md and loads repository hooks from .github/hooks/. Restart Copilot CLI if it is already running.",
		profile:      agents.ProfileMinimal,
	}

	copilotGlobalIntegration = agentsIntegration{
		name:         "GitHub Copilot CLI (global)",
		setupCommand: "bd setup copilot",
		readHint:     "Copilot CLI also reads $HOME/.copilot/copilot-instructions.md and $HOME/.copilot/hooks/ for global defaults.",
		profile:      agents.ProfileMinimal,
	}
)

type copilotEnv struct {
	stdout     io.Writer
	stderr     io.Writer
	projectDir string
	homeDir    string
	ensureDir  func(string, os.FileMode) error
	readFile   func(string) ([]byte, error)
	writeFile  func(string, []byte) error
	lookPath   func(string) (string, error)
	runCommand func(string, ...string) ([]byte, error)
}

type copilotHookCommand struct {
	Type       string `json:"type"`
	Bash       string `json:"bash,omitempty"`
	PowerShell string `json:"powershell,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
	Comment    string `json:"comment,omitempty"`
}

type copilotHooksConfig struct {
	Version int                             `json:"version"`
	Hooks   map[string][]copilotHookCommand `json:"hooks"`
}

func defaultCopilotEnv() (copilotEnv, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return copilotEnv{}, fmt.Errorf("working directory: %w", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return copilotEnv{}, fmt.Errorf("home directory: %w", err)
	}
	return copilotEnv{
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		projectDir: workDir,
		homeDir:    homeDir,
		ensureDir:  EnsureDir,
		readFile:   os.ReadFile,
		writeFile: func(path string, data []byte) error {
			return atomicWriteFile(path, data)
		},
		lookPath: exec.LookPath,
		runCommand: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		},
	}, nil
}

func copilotProjectInstructionsPath(base string) string {
	return filepath.Join(base, filepath.FromSlash(copilotProjectInstructionsFile))
}

func copilotGlobalInstructionsPath(homeDir string) string {
	return filepath.Join(homeDir, filepath.FromSlash(copilotGlobalInstructionsFile))
}

func copilotGlobalHooksPath(homeDir string) string {
	return filepath.Join(homeDir, filepath.FromSlash(copilotGlobalHooksFile))
}

func copilotHooksPath(base string) string {
	return filepath.Join(base, filepath.FromSlash(copilotHooksFile))
}

func copilotAgentsEnv(path string, env copilotEnv) agentsEnv {
	return agentsEnv{
		agentsPath: path,
		stdout:     env.stdout,
		stderr:     env.stderr,
	}
}

// InstallCopilot installs GitHub Copilot CLI integration.
func InstallCopilot(project bool, global bool, stealth bool) {
	env, err := copilotEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := installCopilot(env, project, global, stealth); err != nil {
		setupExit(1)
	}
}

func installCopilot(env copilotEnv, project bool, global bool, stealth bool) error {
	project, global = resolveCopilotScopes(project, global)

	if project || global {
		if err := validateCopilotVersion(env, true); err != nil {
			return err
		}
	}

	if global {
		if err := ensureCopilotInstructionsScaffold(env, copilotGlobalInstructionsPath(env.homeDir), false); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: prepare global instructions file: %v\n", err)
			return err
		}
		if err := installAgents(copilotAgentsEnv(copilotGlobalInstructionsPath(env.homeDir), env), copilotGlobalIntegration); err != nil {
			return err
		}
		if err := installCopilotHooksAtPath(env, copilotGlobalHooksPath(env.homeDir), stealth); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: install global hooks: %v\n", err)
			return err
		}
		_, _ = fmt.Fprintln(env.stdout, "\n✓ GitHub Copilot CLI global instructions installed")
		_, _ = fmt.Fprintf(env.stdout, "  Instructions: %s\n", copilotGlobalInstructionsPath(env.homeDir))
		_, _ = fmt.Fprintf(env.stdout, "  Hooks: %s\n", copilotGlobalHooksPath(env.homeDir))
	}

	if project {
		if err := ensureCopilotInstructionsScaffold(env, copilotProjectInstructionsPath(env.projectDir), true); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: prepare project instructions file: %v\n", err)
			return err
		}
		if err := installAgents(copilotAgentsEnv(copilotProjectInstructionsPath(env.projectDir), env), copilotProjectIntegration); err != nil {
			return err
		}
		if err := installCopilotHooksAtPath(env, copilotHooksPath(env.projectDir), stealth); err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: install hooks: %v\n", err)
			return err
		}
		_, _ = fmt.Fprintln(env.stdout, "\n✓ GitHub Copilot CLI project integration installed")
		_, _ = fmt.Fprintf(env.stdout, "  Instructions: %s\n", copilotProjectInstructionsPath(env.projectDir))
		_, _ = fmt.Fprintf(env.stdout, "  Hooks: %s\n", copilotHooksPath(env.projectDir))
		_, _ = fmt.Fprintf(env.stdout, "  Requires Copilot CLI %s or newer for preCompact hooks.\n", copilotMinVersion)
	}

	_, _ = fmt.Fprintln(env.stdout, "\nRestart Copilot CLI for changes to take effect.")
	return nil
}

func resolveCopilotScopes(project bool, global bool) (bool, bool) {
	if !project && !global {
		return false, true
	}
	return project, global
}

func ensureCopilotInstructionsScaffold(env copilotEnv, path string, project bool) error {
	if _, err := env.readFile(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := env.ensureDir(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := `# GitHub Copilot Instructions

`
	if project {
		content += "This file provides project-specific instructions for GitHub Copilot CLI.\n"
	} else {
		content += "This file provides default instructions for GitHub Copilot CLI across repositories.\n"
	}
	return env.writeFile(path, []byte(content))
}

func installCopilotHooksAtPath(env copilotEnv, path string, stealth bool) error {
	if err := env.ensureDir(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	if data, err := env.readFile(path); err == nil {
		cfg, err := parseCopilotHooks(data)
		if err != nil {
			return fmt.Errorf("parse existing hooks file: %w", err)
		}
		if !isManagedCopilotHooks(cfg) {
			return fmt.Errorf("%s exists and is not managed by bd setup copilot", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	command := "bd prime"
	if stealth {
		command = "bd prime --stealth"
	}
	cfg := generateCopilotHooks(command)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return env.writeFile(path, data)
}

// CheckCopilot checks if GitHub Copilot CLI integration is installed.
func CheckCopilot(project bool, global bool) {
	env, err := copilotEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := checkCopilot(env, project, global); err != nil {
		setupExit(1)
	}
}

func checkCopilot(env copilotEnv, project bool, global bool) error {
	project, global = resolveCopilotScopes(project, global)

	var checkErr error
	if global {
		if err := checkCopilotHooksAtPath(env, copilotGlobalHooksPath(env.homeDir), "bd setup copilot"); err != nil {
			checkErr = err
		}
		if err := checkAgents(copilotAgentsEnv(copilotGlobalInstructionsPath(env.homeDir), env), copilotGlobalIntegration); err != nil {
			checkErr = err
		}
	}
	if project {
		if err := checkCopilotHooksAtPath(env, copilotHooksPath(env.projectDir), "bd setup copilot --project"); err != nil {
			checkErr = err
		}
		if err := validateCopilotVersion(env, false); err != nil {
			checkErr = err
		}
		if err := checkAgents(copilotAgentsEnv(copilotProjectInstructionsPath(env.projectDir), env), copilotProjectIntegration); err != nil {
			checkErr = err
		}
	}
	return checkErr
}

func checkCopilotHooksAtPath(env copilotEnv, path string, setupCommand string) error {
	data, err := env.readFile(path)
	if os.IsNotExist(err) {
		_, _ = fmt.Fprintln(env.stdout, "✗ No hooks installed")
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", setupCommand)
		return errCopilotHooksMissing
	}
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: read hooks file: %v\n", err)
		return err
	}
	cfg, err := parseCopilotHooks(data)
	if err != nil {
		_, _ = fmt.Fprintf(env.stderr, "Error: parse hooks file: %v\n", err)
		return err
	}
	if !hasCopilotHookEvent(cfg, "sessionStart") || !hasCopilotHookEvent(cfg, "preCompact") {
		_, _ = fmt.Fprintf(env.stdout, "⚠ Incomplete hooks installed: %s\n", path)
		_, _ = fmt.Fprintf(env.stdout, "  Run: %s\n", setupCommand)
		return errCopilotHooksMissing
	}
	_, _ = fmt.Fprintf(env.stdout, "✓ Hooks installed: %s\n", path)
	return nil
}

func validateCopilotVersion(env copilotEnv, warnIfMissing bool) error {
	version, installed, err := detectCopilotVersion(env)
	switch {
	case err != nil:
		_, _ = fmt.Fprintf(env.stderr, "Warning: unable to determine Copilot CLI version: %v\n", err)
		return nil
	case !installed:
		if warnIfMissing {
			_, _ = fmt.Fprintf(env.stdout, "  ℹ Copilot CLI not found in PATH; project hooks require Copilot CLI %s or newer.\n", copilotMinVersion)
		}
		return nil
	case compareSetupVersions(version, copilotMinVersion) < 0:
		_, _ = fmt.Fprintf(env.stderr, "Error: Copilot CLI %s detected, but %s or newer is required for preCompact hooks.\n", version, copilotMinVersion)
		return errCopilotVersionOld
	default:
		_, _ = fmt.Fprintf(env.stdout, "✓ Copilot CLI version %s supports preCompact hooks\n", version)
		return nil
	}
}

func detectCopilotVersion(env copilotEnv) (string, bool, error) {
	if _, err := env.lookPath("copilot"); err != nil {
		return "", false, nil
	}
	out, err := env.runCommand("copilot", "--version")
	if err != nil {
		return "", true, err
	}
	version := extractVersion(string(out))
	if version == "" {
		return "", true, fmt.Errorf("could not parse version from output: %q", strings.TrimSpace(string(out)))
	}
	return version, true, nil
}

var semverPattern = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

func extractVersion(output string) string {
	match := semverPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func compareSetupVersions(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}
	for i := 0; i < maxLen; i++ {
		var p1, p2 int
		if i < len(parts1) {
			_, _ = fmt.Sscanf(parts1[i], "%d", &p1)
		}
		if i < len(parts2) {
			_, _ = fmt.Sscanf(parts2[i], "%d", &p2)
		}
		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}
	return 0
}

func generateCopilotHooks(command string) copilotHooksConfig {
	hook := copilotHookCommand{
		Type:       "command",
		Bash:       command,
		PowerShell: command,
		Cwd:        ".",
		TimeoutSec: 30,
		Comment:    copilotHookComment,
	}
	return copilotHooksConfig{
		Version: 1,
		Hooks: map[string][]copilotHookCommand{
			"sessionStart": {hook},
			"preCompact":   {hook},
		},
	}
}

func parseCopilotHooks(data []byte) (copilotHooksConfig, error) {
	var cfg copilotHooksConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return copilotHooksConfig{}, err
	}
	return cfg, nil
}

func hasCopilotHookEvent(cfg copilotHooksConfig, event string) bool {
	commands, ok := cfg.Hooks[event]
	if !ok {
		return false
	}
	for _, cmd := range commands {
		if cmd.Type != "command" {
			continue
		}
		if cmd.Bash == "bd prime" || cmd.Bash == "bd prime --stealth" {
			return true
		}
	}
	return false
}

func isManagedCopilotHooks(cfg copilotHooksConfig) bool {
	return cfg.Version == 1 && hasCopilotHookEvent(cfg, "sessionStart") && hasCopilotHookEvent(cfg, "preCompact")
}

// RemoveCopilot removes GitHub Copilot CLI integration.
func RemoveCopilot(project bool, global bool) {
	env, err := copilotEnvProvider()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		setupExit(1)
		return
	}
	if err := removeCopilot(env, project, global); err != nil {
		setupExit(1)
	}
}

func removeCopilot(env copilotEnv, project bool, global bool) error {
	project, global = resolveCopilotScopes(project, global)

	var removeErr error
	if project {
		if err := removeCopilotHooksAtPath(env, copilotHooksPath(env.projectDir)); err != nil {
			removeErr = err
		}
		if err := removeAgents(copilotAgentsEnv(copilotProjectInstructionsPath(env.projectDir), env), copilotProjectIntegration); err != nil {
			removeErr = err
		}
	}
	if global {
		if err := removeCopilotHooksAtPath(env, copilotGlobalHooksPath(env.homeDir)); err != nil {
			removeErr = err
		}
		if err := removeAgents(copilotAgentsEnv(copilotGlobalInstructionsPath(env.homeDir), env), copilotGlobalIntegration); err != nil {
			removeErr = err
		}
	}
	return removeErr
}

func removeCopilotHooksAtPath(env copilotEnv, path string) error {
	data, err := env.readFile(path)
	switch {
	case os.IsNotExist(err):
		_, _ = fmt.Fprintln(env.stdout, "No Copilot hook file found")
		return nil
	case err != nil:
		_, _ = fmt.Fprintf(env.stderr, "Error: read hooks file: %v\n", err)
		return err
	default:
		cfg, err := parseCopilotHooks(data)
		if err != nil {
			_, _ = fmt.Fprintf(env.stderr, "Error: parse hooks file: %v\n", err)
			return err
		}
		if isManagedCopilotHooks(cfg) {
			if err := os.Remove(path); err != nil {
				_, _ = fmt.Fprintf(env.stderr, "Error: remove hooks file: %v\n", err)
				return err
			}
			_, _ = fmt.Fprintf(env.stdout, "✓ Removed Copilot hooks: %s\n", path)
		} else {
			_, _ = fmt.Fprintf(env.stdout, "ℹ Kept existing custom Copilot hooks file: %s\n", path)
		}
		return nil
	}
}
