package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	copilotInstructionsFile = ".github/copilot-instructions.md"
	copilotHooksFile        = ".github/hooks/beads-copilot.json"
	copilotMinVersion       = "1.0.5"
)

var (
	copilotLookPath = exec.LookPath
	copilotVersion  = func() ([]byte, error) {
		return exec.Command("copilot", "--version").Output()
	}
	copilotVersionPattern = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)
)

type copilotHookCommand struct {
	Type       string `json:"type"`
	Bash       string `json:"bash,omitempty"`
	PowerShell string `json:"powershell,omitempty"`
}

type copilotHooksConfig struct {
	Version int                             `json:"version"`
	Hooks   map[string][]copilotHookCommand `json:"hooks"`
}

func CheckCopilot(repoPath string) DoctorCheck {
	instructionsPath := filepath.Join(repoPath, copilotInstructionsFile)
	hooksPath := filepath.Join(repoPath, copilotHooksFile)

	hasInstructions := hasBeadsSection(instructionsPath)
	hasHooks := copilotFileExists(hooksPath)

	if !hasInstructions && !hasHooks {
		if _, err := copilotLookPath("copilot"); err == nil {
			return DoctorCheck{
				Name:    "Copilot CLI Integration",
				Status:  "ok",
				Message: "GitHub Copilot CLI available but not configured",
				Detail:  "Run 'bd setup copilot' to install repository instructions and hooks",
			}
		}
		return DoctorCheck{
			Name:    "Copilot CLI Integration",
			Status:  "ok",
			Message: "GitHub Copilot CLI integration not configured",
			Detail:  "Run 'bd setup copilot' if you want Copilot CLI to load bd prime hooks",
		}
	}

	if hasInstructions != hasHooks {
		missing := copilotInstructionsFile
		if hasInstructions {
			missing = copilotHooksFile
		}
		return DoctorCheck{
			Name:    "Copilot CLI Integration",
			Status:  "warning",
			Message: "GitHub Copilot CLI integration is partially installed",
			Detail:  fmt.Sprintf("Missing %s. Run 'bd setup copilot' to repair the integration.", missing),
		}
	}

	version, installed, err := detectCopilotVersion()
	switch {
	case err != nil:
		return DoctorCheck{
			Name:    "Copilot CLI Integration",
			Status:  "warning",
			Message: "GitHub Copilot CLI integration installed",
			Detail:  fmt.Sprintf("Could not determine installed Copilot CLI version: %v", err),
		}
	case !installed:
		return DoctorCheck{
			Name:    "Copilot CLI Integration",
			Status:  "warning",
			Message: "GitHub Copilot CLI integration installed",
			Detail:  fmt.Sprintf("Repository hooks are configured, but 'copilot' is not in PATH. Copilot CLI %s or newer is required.", copilotMinVersion),
		}
	case CompareVersions(version, copilotMinVersion) < 0:
		return DoctorCheck{
			Name:    "Copilot CLI Integration",
			Status:  "warning",
			Message: "GitHub Copilot CLI is too old for preCompact hooks",
			Detail:  fmt.Sprintf("Detected Copilot CLI %s, but %s or newer is required.", version, copilotMinVersion),
			Fix:     "Upgrade GitHub Copilot CLI, then restart your session.",
		}
	default:
		return DoctorCheck{
			Name:    "Copilot CLI Integration",
			Status:  "ok",
			Message: "GitHub Copilot CLI integration installed",
			Detail:  fmt.Sprintf("Instructions and hooks are installed; Copilot CLI %s supports preCompact.", version),
		}
	}
}

func CheckCopilotHooksHealth(repoPath string) DoctorCheck {
	path := filepath.Join(repoPath, copilotHooksFile)
	if !copilotFileExists(path) {
		return DoctorCheck{
			Name:    "Copilot Hooks Health",
			Status:  "ok",
			Message: "Copilot hook file not installed",
		}
	}

	data, err := os.ReadFile(path) // #nosec G304 - controlled path from repoPath
	if err != nil {
		return DoctorCheck{
			Name:    "Copilot Hooks Health",
			Status:  "error",
			Message: "Could not read Copilot hook file",
			Detail:  err.Error(),
		}
	}
	if _, err := parseCopilotHooks(data); err != nil {
		return DoctorCheck{
			Name:    "Copilot Hooks Health",
			Status:  "error",
			Message: "Copilot hook file contains invalid JSON",
			Detail:  err.Error(),
			Fix:     "Run 'bd setup copilot' to rewrite the hook file.",
		}
	}

	return DoctorCheck{
		Name:    "Copilot Hooks Health",
		Status:  "ok",
		Message: "Copilot hook file is valid JSON",
	}
}

func CheckCopilotHookCompleteness(repoPath string) DoctorCheck {
	path := filepath.Join(repoPath, copilotHooksFile)
	if !copilotFileExists(path) {
		return DoctorCheck{
			Name:    "Copilot Hooks",
			Status:  "ok",
			Message: "Copilot hook file not installed",
		}
	}

	data, err := os.ReadFile(path) // #nosec G304 - controlled path from repoPath
	if err != nil {
		return DoctorCheck{
			Name:    "Copilot Hooks",
			Status:  "error",
			Message: "Could not read Copilot hook file",
			Detail:  err.Error(),
		}
	}
	cfg, err := parseCopilotHooks(data)
	if err != nil {
		return DoctorCheck{
			Name:    "Copilot Hooks",
			Status:  "error",
			Message: "Could not parse Copilot hook file",
			Detail:  err.Error(),
		}
	}

	missing := make([]string, 0, 2)
	if !hasCopilotHookEvent(cfg, "sessionStart") {
		missing = append(missing, "sessionStart")
	}
	if !hasCopilotHookEvent(cfg, "preCompact") {
		missing = append(missing, "preCompact")
	}
	if len(missing) > 0 {
		return DoctorCheck{
			Name:    "Copilot Hooks",
			Status:  "warning",
			Message: "Copilot hooks are incomplete",
			Detail:  fmt.Sprintf("Missing bd prime hooks for: %s", strings.Join(missing, ", ")),
			Fix:     "Run 'bd setup copilot' to reinstall both hooks.",
		}
	}

	return DoctorCheck{
		Name:    "Copilot Hooks",
		Status:  "ok",
		Message: "Copilot hooks include sessionStart and preCompact",
	}
}

func detectCopilotVersion() (string, bool, error) {
	if _, err := copilotLookPath("copilot"); err != nil {
		return "", false, nil
	}
	out, err := copilotVersion()
	if err != nil {
		return "", true, err
	}
	version := extractCopilotVersion(string(out))
	if version == "" {
		return "", true, fmt.Errorf("could not parse version from output %q", strings.TrimSpace(string(out)))
	}
	return version, true, nil
}

func extractCopilotVersion(output string) string {
	match := copilotVersionPattern.FindStringSubmatch(output)
	if len(match) < 2 {
		return ""
	}
	return match[1]
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

func hasBeadsSection(path string) bool {
	data, err := os.ReadFile(path) // #nosec G304 - controlled path from repoPath
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "BEGIN BEADS INTEGRATION")
}

func copilotFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
