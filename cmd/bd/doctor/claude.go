package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CheckClaude returns Claude integration verification as a DoctorCheck
func CheckClaude() DoctorCheck {
	// Check what's installed
	hasPlugin := isBeadsPluginInstalled()
	hasMCP := isMCPServerInstalled()
	hasHooks := hasClaudeHooks()

	// Plugin now provides hooks directly via plugin.json, so if plugin is installed
	// we consider hooks to be available (plugin hooks + any user-configured hooks)
	if hasPlugin {
		return DoctorCheck{
			Name:    "Claude Integration",
			Status:  "ok",
			Message: "Plugin installed",
			Detail:  "Slash commands and workflow hooks enabled via plugin",
		}
	} else if hasMCP && hasHooks {
		return DoctorCheck{
			Name:    "Claude Integration",
			Status:  "ok",
			Message: "MCP server and hooks installed",
			Detail:  "Workflow reminders enabled (legacy MCP mode)",
		}
	} else if !hasMCP && !hasPlugin && hasHooks {
		return DoctorCheck{
			Name:    "Claude Integration",
			Status:  "ok",
			Message: "Hooks installed (CLI mode)",
			Detail:  "Plugin not detected - install for slash commands",
		}
	} else if hasMCP && !hasHooks {
		return DoctorCheck{
			Name:    "Claude Integration",
			Status:  "warning",
			Message: "MCP server installed but hooks missing",
			Detail: "MCP-only mode: relies on tools for every query (~10.5k tokens)\n" +
				"  bd prime hooks provide much better token efficiency",
			Fix: "Add bd prime hooks for better token efficiency:\n" +
				"  1. Run 'bd setup claude' to add SessionStart/PreCompact hooks\n" +
				"\n" +
				"Benefits:\n" +
				"  • MCP mode: ~50 tokens vs ~10.5k for full tool scan (99% reduction)\n" +
				"  • Automatic context refresh on session start and compaction\n" +
				"  • Works alongside MCP tools for when you need them\n" +
				"\n" +
				"See: bd setup claude --help",
		}
	} else {
		return DoctorCheck{
			Name:    "Claude Integration",
			Status:  "warning",
			Message: "Not configured",
			Detail:  "Claude can use bd more effectively with the beads plugin",
			Fix: "Set up Claude integration:\n" +
				"  Option 1: Install the beads plugin (recommended)\n" +
				"    • Provides hooks, slash commands, and MCP tools automatically\n" +
				"    • See: https://github.com/steveyegge/beads/blob/main/docs/PLUGIN.md\n" +
				"\n" +
				"  Option 2: CLI-only mode\n" +
				"    • Run 'bd setup claude' to add SessionStart/PreCompact hooks\n" +
				"    • No slash commands, but hooks provide workflow context\n" +
				"\n" +
				"Benefits:\n" +
				"  • Auto-inject workflow context on session start (~50-2k tokens)\n" +
				"  • Automatic context recovery before compaction",
		}
	}
}

// isBeadsPluginInstalled checks if beads plugin is enabled in Claude Code
func isBeadsPluginInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	settingsPath := filepath.Join(home, ".claude/settings.json")
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- settingsPath is constructed from user home dir, not user input
	if err != nil {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	// Check enabledPlugins section for beads
	enabledPlugins, ok := settings["enabledPlugins"].(map[string]interface{})
	if !ok {
		return false
	}

	// Look for beads@beads-marketplace plugin
	for key, value := range enabledPlugins {
		if strings.Contains(strings.ToLower(key), "beads") {
			// Check if it's enabled (value should be true)
			if enabled, ok := value.(bool); ok && enabled {
				return true
			}
		}
	}

	return false
}

// isMCPServerInstalled checks if MCP server is configured
func isMCPServerInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	settingsPath := filepath.Join(home, ".claude/settings.json")
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- settingsPath is constructed from user home dir, not user input
	if err != nil {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	// Check mcpServers section for beads
	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		return false
	}

	// Look for beads server (any key containing "beads")
	for key := range mcpServers {
		if strings.Contains(strings.ToLower(key), "beads") {
			return true
		}
	}

	return false
}

// hasClaudeHooks checks if Claude hooks are installed
func hasClaudeHooks() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}

	globalSettings := filepath.Join(home, ".claude/settings.json")
	projectSettings := ".claude/settings.local.json"

	return hasBeadsHooks(globalSettings) || hasBeadsHooks(projectSettings)
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
				if cmdMap["command"] == "bd prime" {
					return true
				}
			}
		}
	}

	return false
}

// verifyPrimeOutput checks if bd prime command works and adapts correctly
// Returns a check result
func VerifyPrimeOutput() DoctorCheck {
	cmd := exec.Command("bd", "prime")
	output, err := cmd.CombinedOutput()

	if err != nil {
		return DoctorCheck{
			Name:    "bd prime Command",
			Status:  "error",
			Message: "Command failed to execute",
			Fix:     "Ensure bd is installed and in PATH",
		}
	}

	if len(output) == 0 {
		return DoctorCheck{
			Name:    "bd prime Command",
			Status:  "error",
			Message: "No output produced",
			Detail:  "Expected workflow context markdown",
		}
	}

	// Check if output adapts to MCP mode
	hasMCP := isMCPServerInstalled()
	outputStr := string(output)

	if hasMCP && strings.Contains(outputStr, "mcp__plugin_beads_beads__") {
		return DoctorCheck{
			Name:    "bd prime Output",
			Status:  "ok",
			Message: "MCP mode detected",
			Detail:  "Outputting workflow reminders",
		}
	} else if !hasMCP && strings.Contains(outputStr, "bd ready") {
		return DoctorCheck{
			Name:    "bd prime Output",
			Status:  "ok",
			Message: "CLI mode detected",
			Detail:  "Outputting full command reference",
		}
	} else {
		return DoctorCheck{
			Name:    "bd prime Output",
			Status:  "warning",
			Message: "Output may not be adapting to environment",
		}
	}
}

// CheckBdInPath verifies that 'bd' command is available in PATH.
// This is important because Claude hooks rely on executing 'bd prime'.
func CheckBdInPath() DoctorCheck {
	_, err := exec.LookPath("bd")
	if err != nil {
		return DoctorCheck{
			Name:    "CLI Availability",
			Status:  "warning",
			Message: "'bd' command not found in PATH",
			Detail:  "Claude hooks execute 'bd prime' and won't work without bd in PATH",
			Fix: "Install bd globally:\n" +
				"  • Homebrew: brew install steveyegge/tap/bd\n" +
				"  • Script: curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash\n" +
				"  • Or add bd to your PATH",
		}
	}

	return DoctorCheck{
		Name:    "CLI Availability",
		Status:  "ok",
		Message: "'bd' command available in PATH",
	}
}

// CheckDocumentationBdPrimeReference checks if AGENTS.md or CLAUDE.md reference 'bd prime'
// and verifies the command exists. This helps catch version mismatches where docs
// reference features not available in the installed version.
// Also supports local-only variants (claude.local.md) that are gitignored.
func CheckDocumentationBdPrimeReference(repoPath string) DoctorCheck {
	docFiles := []string{
		filepath.Join(repoPath, "AGENTS.md"),
		filepath.Join(repoPath, "CLAUDE.md"),
		filepath.Join(repoPath, ".claude", "CLAUDE.md"),
		// Local-only variants (not committed to repo)
		filepath.Join(repoPath, "claude.local.md"),
		filepath.Join(repoPath, ".claude", "claude.local.md"),
	}

	var filesWithBdPrime []string
	for _, docFile := range docFiles {
		content, err := os.ReadFile(docFile) // #nosec G304 - controlled paths from repoPath
		if err != nil {
			continue
		}

		if strings.Contains(string(content), "bd prime") {
			filesWithBdPrime = append(filesWithBdPrime, filepath.Base(docFile))
		}
	}

	// If no docs reference bd prime, that's fine - not everyone uses it
	if len(filesWithBdPrime) == 0 {
		return DoctorCheck{
			Name:    "Prime Documentation",
			Status:  "ok",
			Message: "No bd prime references in documentation",
		}
	}

	// Docs reference bd prime - verify the command works
	cmd := exec.Command("bd", "prime", "--help")
	if err := cmd.Run(); err != nil {
		return DoctorCheck{
			Name:    "Prime Documentation",
			Status:  "warning",
			Message: "Documentation references 'bd prime' but command not found",
			Detail:  "Files: " + strings.Join(filesWithBdPrime, ", "),
			Fix: "Upgrade bd to get the 'bd prime' command:\n" +
				"  • Homebrew: brew upgrade bd\n" +
				"  • Script: curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash\n" +
				"  Or remove 'bd prime' references from documentation if using older version",
		}
	}

	return DoctorCheck{
		Name:    "Prime Documentation",
		Status:  "ok",
		Message: "Documentation references match installed features",
		Detail:  "Files: " + strings.Join(filesWithBdPrime, ", "),
	}
}

// CheckClaudePlugin checks if the beads Claude Code plugin is installed and up to date.
func CheckClaudePlugin() DoctorCheck {
	// Check if running in Claude Code
	if os.Getenv("CLAUDECODE") != "1" {
		return DoctorCheck{
			Name:    "Claude Plugin",
			Status:  StatusOK,
			Message: "N/A (not running in Claude Code)",
		}
	}

	// Get plugin version from installed_plugins.json
	pluginVersion, pluginInstalled, err := GetClaudePluginVersion()
	if err != nil {
		return DoctorCheck{
			Name:    "Claude Plugin",
			Status:  StatusWarning,
			Message: "Unable to check plugin version",
			Detail:  err.Error(),
		}
	}

	if !pluginInstalled {
		return DoctorCheck{
			Name:    "Claude Plugin",
			Status:  StatusWarning,
			Message: "beads plugin not installed",
			Fix:     "Install plugin: /plugin install beads@beads-marketplace",
		}
	}

	// Query PyPI for latest MCP version
	latestMCPVersion, err := fetchLatestPyPIVersion("beads-mcp")
	if err != nil {
		// Network error - don't fail
		return DoctorCheck{
			Name:    "Claude Plugin",
			Status:  StatusOK,
			Message: fmt.Sprintf("version %s (unable to check for updates)", pluginVersion),
		}
	}

	// Compare versions
	if latestMCPVersion == "" || pluginVersion == latestMCPVersion {
		return DoctorCheck{
			Name:    "Claude Plugin",
			Status:  StatusOK,
			Message: fmt.Sprintf("version %s (latest)", pluginVersion),
		}
	}

	if CompareVersions(latestMCPVersion, pluginVersion) > 0 {
		return DoctorCheck{
			Name:    "Claude Plugin",
			Status:  StatusWarning,
			Message: fmt.Sprintf("version %s (latest: %s)", pluginVersion, latestMCPVersion),
			Fix:     "Update plugin: /plugin update beads@beads-marketplace\nRestart Claude Code after update",
		}
	}

	return DoctorCheck{
		Name:    "Claude Plugin",
		Status:  StatusOK,
		Message: fmt.Sprintf("version %s", pluginVersion),
	}
}

// GetClaudePluginVersion returns the installed beads Claude plugin version.
func GetClaudePluginVersion() (version string, installed bool, err error) {
	// Get user home directory (cross-platform)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("unable to determine home directory: %w", err)
	}

	// Path to installed_plugins.json
	pluginPath := filepath.Join(homeDir, ".claude", "plugins", "installed_plugins.json")

	// Read plugin file
	data, err := os.ReadFile(pluginPath) // #nosec G304 - path is controlled
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("unable to read plugin file: %w", err)
	}

	// First, determine the format version
	var versionCheck struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &versionCheck); err != nil {
		return "", false, fmt.Errorf("unable to parse plugin file: %w", err)
	}

	// Handle version 2 format (GH#741): plugins map contains arrays
	if versionCheck.Version == 2 {
		var pluginDataV2 struct {
			Plugins map[string][]struct {
				Version string `json:"version"`
				Scope   string `json:"scope"`
			} `json:"plugins"`
		}
		if err := json.Unmarshal(data, &pluginDataV2); err != nil {
			return "", false, fmt.Errorf("unable to parse plugin file v2: %w", err)
		}

		// Look for beads plugin - take first entry from the array
		if entries, ok := pluginDataV2.Plugins["beads@beads-marketplace"]; ok && len(entries) > 0 {
			return entries[0].Version, true, nil
		}
		return "", false, nil
	}

	// Handle version 1 format (original): plugins map contains structs directly
	var pluginDataV1 struct {
		Plugins map[string]struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}

	if err := json.Unmarshal(data, &pluginDataV1); err != nil {
		return "", false, fmt.Errorf("unable to parse plugin file: %w", err)
	}

	// Look for beads plugin
	if plugin, ok := pluginDataV1.Plugins["beads@beads-marketplace"]; ok {
		return plugin.Version, true, nil
	}

	return "", false, nil
}

func fetchLatestPyPIVersion(packageName string) (string, error) {
	url := fmt.Sprintf("https://pypi.org/pypi/%s/json", packageName)

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	// Set User-Agent
	req.Header.Set("User-Agent", "beads-cli-doctor")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pypi api returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	return data.Info.Version, nil
}
