package doctor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DoctorCheck represents a single diagnostic check result
type DoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "ok", "warning", or "error"
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// CheckClaude returns Claude integration verification as a DoctorCheck
func CheckClaude() DoctorCheck {
	// Check what's installed
	hasPlugin := isBeadsPluginInstalled()
	hasMCP := isMCPServerInstalled()
	hasHooks := hasClaudeHooks()

	// Plugin provides slash commands and MCP server
	if hasPlugin && hasHooks {
		return DoctorCheck{
			Name:    "Claude Integration",
			Status:  "ok",
			Message: "Plugin and hooks installed",
			Detail:  "Slash commands and workflow reminders enabled",
		}
	} else if hasPlugin && !hasHooks {
		return DoctorCheck{
			Name:    "Claude Integration",
			Status:  "warning",
			Message: "Plugin installed but hooks missing",
			Fix:     "Run: bd setup claude",
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
			Detail: "Claude can use bd more effectively with hooks and optional plugin",
			Fix: "Set up Claude integration:\n" +
				"  1. Run 'bd setup claude' to add SessionStart/PreCompact hooks\n" +
				"  2. (Optional) Install beads plugin for slash commands\n" +
				"\n" +
				"Benefits:\n" +
				"  • Hooks: Auto-inject workflow context (~50-2k tokens)\n" +
				"  • Plugin: Convenient slash commands + MCP tools\n" +
				"  • CLI mode: Works without plugin (hooks + manual 'bd prime')\n" +
				"\n" +
				"See: bd setup claude --help",
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
