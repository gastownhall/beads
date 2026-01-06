package fix

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// ExternalHookManager represents a detected external hook management tool.
type ExternalHookManager struct {
	Name       string // e.g., "lefthook", "husky", "pre-commit"
	ConfigFile string // Path to the config file that was detected
}

// hookManagerConfig pairs a manager name with its possible config files.
type hookManagerConfig struct {
	name        string
	configFiles []string
}

// hookManagerConfigs defines external hook managers in priority order.
// See https://lefthook.dev/configuration/ for lefthook config options.
var hookManagerConfigs = []hookManagerConfig{
	{"lefthook", []string{
		// YAML variants
		"lefthook.yml", ".lefthook.yml", ".config/lefthook.yml",
		"lefthook.yaml", ".lefthook.yaml", ".config/lefthook.yaml",
		// TOML variants
		"lefthook.toml", ".lefthook.toml", ".config/lefthook.toml",
		// JSON variants
		"lefthook.json", ".lefthook.json", ".config/lefthook.json",
	}},
	{"husky", []string{".husky"}},
	{"pre-commit", []string{".pre-commit-config.yaml", ".pre-commit-config.yml"}},
	{"overcommit", []string{".overcommit.yml"}},
	{"yorkie", []string{".yorkie"}},
	{"simple-git-hooks", []string{
		".simple-git-hooks.cjs", ".simple-git-hooks.js",
		"simple-git-hooks.cjs", "simple-git-hooks.js",
	}},
}

// DetectExternalHookManagers checks for presence of external hook management tools.
// Returns a list of detected managers along with their config file paths.
func DetectExternalHookManagers(path string) []ExternalHookManager {
	var managers []ExternalHookManager

	for _, mgr := range hookManagerConfigs {
		for _, configFile := range mgr.configFiles {
			configPath := filepath.Join(path, configFile)
			if info, err := os.Stat(configPath); err == nil {
				// For directories like .husky, check if it exists
				// For files, check if it's a regular file
				if info.IsDir() || info.Mode().IsRegular() {
					managers = append(managers, ExternalHookManager{
						Name:       mgr.name,
						ConfigFile: configFile,
					})
					break // Only report each manager once
				}
			}
		}
	}

	return managers
}

// HookIntegrationStatus represents the status of bd integration in an external hook manager.
type HookIntegrationStatus struct {
	Manager          string   // Hook manager name
	HooksWithBd      []string // Hooks that have bd integration (bd hooks run)
	HooksWithoutBd   []string // Hooks configured but without bd integration
	HooksNotInConfig []string // Recommended hooks not in config at all
	Configured       bool     // Whether any bd integration was found
	DetectionOnly    bool     // True if we detected the manager but can't verify its config
}

// bdHookPattern matches the recommended bd hooks run pattern with word boundaries
var bdHookPattern = regexp.MustCompile(`\bbd\s+hooks\s+run\b`)

// hookManagerPattern pairs a manager name with its detection pattern.
type hookManagerPattern struct {
	name    string
	pattern *regexp.Regexp
}

// hookManagerPatterns identifies which hook manager installed a git hook (in priority order).
var hookManagerPatterns = []hookManagerPattern{
	{"lefthook", regexp.MustCompile(`(?i)lefthook`)},
	{"husky", regexp.MustCompile(`(?i)(\.husky|husky\.sh)`)},
	{"pre-commit", regexp.MustCompile(`(?i)(pre-commit\s+run|\.pre-commit-config|INSTALL_PYTHON|PRE_COMMIT)`)},
	{"simple-git-hooks", regexp.MustCompile(`(?i)simple-git-hooks`)},
}

// DetectActiveHookManager reads the git hooks to determine which manager installed them.
// This is more reliable than just checking for config files when multiple managers exist.
func DetectActiveHookManager(path string) string {
	// Get git dir
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(path, gitDir)
	}

	// Check for custom hooks path (core.hooksPath)
	hooksDir := filepath.Join(gitDir, "hooks")
	hooksPathCmd := exec.Command("git", "config", "--get", "core.hooksPath")
	hooksPathCmd.Dir = path
	if hooksPathOutput, err := hooksPathCmd.Output(); err == nil {
		customPath := strings.TrimSpace(string(hooksPathOutput))
		if customPath != "" {
			if !filepath.IsAbs(customPath) {
				customPath = filepath.Join(path, customPath)
			}
			hooksDir = customPath
		}
	}

	// Check common hooks for manager signatures
	for _, hookName := range []string{"pre-commit", "pre-push", "post-merge"} {
		hookPath := filepath.Join(hooksDir, hookName)
		content, err := os.ReadFile(hookPath) // #nosec G304 - path is validated
		if err != nil {
			continue
		}
		contentStr := string(content)

		// Check each manager pattern (deterministic order)
		for _, mp := range hookManagerPatterns {
			if mp.pattern.MatchString(contentStr) {
				return mp.name
			}
		}
	}

	return ""
}

// recommendedBdHooks are the hooks that should have bd integration
var recommendedBdHooks = []string{"pre-commit", "post-merge", "pre-push"}

// lefthookConfigFiles lists lefthook config files (derived from hookManagerConfigs).
// Format is inferred from extension.
var lefthookConfigFiles = hookManagerConfigs[0].configFiles // lefthook is first

// CheckLefthookBdIntegration parses lefthook config (YAML, TOML, or JSON) and checks if bd hooks are integrated.
// See https://lefthook.dev/configuration/ for supported config file locations.
func CheckLefthookBdIntegration(path string) *HookIntegrationStatus {
	// Find first existing config file
	var configPath string
	for _, name := range lefthookConfigFiles {
		p := filepath.Join(path, name)
		if _, err := os.Stat(p); err == nil {
			configPath = p
			break
		}
	}
	if configPath == "" {
		return nil
	}

	content, err := os.ReadFile(configPath) // #nosec G304 - path is validated
	if err != nil {
		return nil
	}

	// Parse config based on extension
	var config map[string]interface{}
	ext := filepath.Ext(configPath)
	switch ext {
	case ".toml":
		if _, err := toml.Decode(string(content), &config); err != nil {
			return nil
		}
	case ".json":
		if err := json.Unmarshal(content, &config); err != nil {
			return nil
		}
	default: // .yml, .yaml
		if err := yaml.Unmarshal(content, &config); err != nil {
			return nil
		}
	}

	status := &HookIntegrationStatus{
		Manager:    "lefthook",
		Configured: false,
	}

	// Check each recommended hook
	for _, hookName := range recommendedBdHooks {
		hookSection, ok := config[hookName]
		if !ok {
			// Hook not configured at all in lefthook
			status.HooksNotInConfig = append(status.HooksNotInConfig, hookName)
			continue
		}

		// Walk to commands.*.run to check for bd hooks run
		if hasBdInCommands(hookSection) {
			status.HooksWithBd = append(status.HooksWithBd, hookName)
			status.Configured = true
		} else {
			// Hook is in config but has no bd integration
			status.HooksWithoutBd = append(status.HooksWithoutBd, hookName)
		}
	}

	return status
}

// hasBdInCommands checks if any command's "run" field contains bd hooks run.
// Walks the lefthook structure: hookSection.commands.*.run
func hasBdInCommands(hookSection interface{}) bool {
	sectionMap, ok := hookSection.(map[string]interface{})
	if !ok {
		return false
	}

	commands, ok := sectionMap["commands"]
	if !ok {
		return false
	}

	commandsMap, ok := commands.(map[string]interface{})
	if !ok {
		return false
	}

	for _, cmdConfig := range commandsMap {
		cmdMap, ok := cmdConfig.(map[string]interface{})
		if !ok {
			continue
		}

		runVal, ok := cmdMap["run"]
		if !ok {
			continue
		}

		runStr, ok := runVal.(string)
		if !ok {
			continue
		}

		if bdHookPattern.MatchString(runStr) {
			return true
		}
	}

	return false
}

// CheckHuskyBdIntegration checks .husky/ scripts for bd integration.
func CheckHuskyBdIntegration(path string) *HookIntegrationStatus {
	huskyDir := filepath.Join(path, ".husky")
	if _, err := os.Stat(huskyDir); os.IsNotExist(err) {
		return nil
	}

	status := &HookIntegrationStatus{
		Manager:    "husky",
		Configured: false,
	}

	for _, hookName := range recommendedBdHooks {
		hookPath := filepath.Join(huskyDir, hookName)
		content, err := os.ReadFile(hookPath) // #nosec G304 - path is validated
		if err != nil {
			// Hook script doesn't exist in .husky/
			status.HooksNotInConfig = append(status.HooksNotInConfig, hookName)
			continue
		}

		contentStr := string(content)

		// Check for bd hooks run pattern
		if bdHookPattern.MatchString(contentStr) {
			status.HooksWithBd = append(status.HooksWithBd, hookName)
			status.Configured = true
		} else {
			status.HooksWithoutBd = append(status.HooksWithoutBd, hookName)
		}
	}

	return status
}

// checkManagerBdIntegration checks a specific manager for bd integration.
func checkManagerBdIntegration(name, path string) *HookIntegrationStatus {
	switch name {
	case "lefthook":
		return CheckLefthookBdIntegration(path)
	case "husky":
		return CheckHuskyBdIntegration(path)
	default:
		return nil
	}
}

// CheckExternalHookManagerIntegration checks if detected hook managers have bd integration.
func CheckExternalHookManagerIntegration(path string) *HookIntegrationStatus {
	managers := DetectExternalHookManagers(path)
	if len(managers) == 0 {
		return nil
	}

	// First, try to detect which manager is actually active from git hooks
	if activeManager := DetectActiveHookManager(path); activeManager != "" {
		if status := checkManagerBdIntegration(activeManager, path); status != nil {
			return status
		}
	}

	// Fall back to checking detected managers in order
	for _, m := range managers {
		if status := checkManagerBdIntegration(m.Name, path); status != nil {
			return status
		}
	}

	// Return basic status for unsupported managers (detection only, can't verify config)
	return &HookIntegrationStatus{
		Manager:       ManagerNames(managers),
		Configured:    false,
		DetectionOnly: true,
	}
}

// ManagerNames extracts names from a slice of ExternalHookManager as comma-separated string.
func ManagerNames(managers []ExternalHookManager) string {
	names := make([]string, len(managers))
	for i, m := range managers {
		names[i] = m.Name
	}
	return strings.Join(names, ", ")
}

// GitHooks fixes missing or broken git hooks by calling bd hooks install.
// If external hook managers are detected (lefthook, husky, etc.), it uses
// --chain to preserve existing hooks instead of overwriting them.
func GitHooks(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	// Check if we're in a git repository using git rev-parse
	// This handles worktrees where .git is a file, not a directory
	checkCmd := exec.Command("git", "rev-parse", "--git-dir")
	checkCmd.Dir = path
	if err := checkCmd.Run(); err != nil {
		return fmt.Errorf("not a git repository")
	}

	// Detect external hook managers
	externalManagers := DetectExternalHookManagers(path)

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Build command arguments
	args := []string{"hooks", "install"}

	// If external hook managers detected, use --chain to preserve them
	if len(externalManagers) > 0 {
		args = append(args, "--chain")
	}

	// Run bd hooks install
	cmd := newBdCmd(bdBinary, args...)
	cmd.Dir = path // Set working directory without changing process dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install hooks: %w", err)
	}

	return nil
}
