package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/templates/agents"
	"github.com/steveyegge/beads/internal/ui"
)

// addAgentsInstructions creates or updates AGENTS.md with embedded template content.
// If templatePath is non-empty, the custom template file is used instead of the embedded default.
func addAgentsInstructions(verbose bool, templatePath string) {
	agentFile := "AGENTS.md"

	if err := updateAgentFile(agentFile, verbose, templatePath); err != nil {
		// Non-fatal - continue with other files
		if verbose {
			fmt.Fprintf(os.Stderr, "Warning: failed to update %s: %v\n", agentFile, err)
		}
	}
}

// updateAgentFile creates or updates an agent instructions file with embedded template content.
func updateAgentFile(filename string, verbose bool, templatePath string) error {
	// Check if file exists
	//nolint:gosec // G304: filename comes from hardcoded list in addAgentsInstructions
	content, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		// File doesn't exist - create from template
		var newContent string
		if templatePath != "" {
			//nolint:gosec // G304: templatePath comes from --agents-template flag
			data, readErr := os.ReadFile(templatePath)
			if readErr != nil {
				return fmt.Errorf("failed to read template %s: %w", templatePath, readErr)
			}
			newContent = string(data)
		} else {
			newContent = agents.EmbeddedDefault()
		}

		// #nosec G306 - markdown needs to be readable
		if err := os.WriteFile(filename, []byte(newContent), 0644); err != nil {
			return fmt.Errorf("failed to create %s: %w", filename, err)
		}
		if verbose {
			fmt.Printf("  %s Created %s with agent instructions\n", ui.RenderPass("✓"), filename)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to read %s: %w", filename, err)
	}

	// File exists - check if it already has our sections
	contentStr := string(content)
	hasBeads := strings.Contains(contentStr, "BEGIN BEADS INTEGRATION")

	if hasBeads {
		if verbose {
			fmt.Printf("  %s already has agent instructions\n", filename)
		}
		return nil
	}

	// Append beads section with profile metadata (includes landing-the-plane)
	newContent := contentStr
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}

	newContent += "\n" + agents.RenderSection(agents.ProfileFull)

	// #nosec G306 - markdown needs to be readable
	if err := os.WriteFile(filename, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to update %s: %w", filename, err)
	}
	if verbose {
		fmt.Printf("  %s Added agent instructions to %s\n", ui.RenderPass("✓"), filename)
	}
	return nil
}

// setupClaudeSettings creates or updates .claude/settings.local.json with onboard instruction
func setupClaudeSettings(verbose bool) error {
	claudeDir := ".claude"
	settingsPath := filepath.Join(claudeDir, "settings.local.json")

	// Create .claude directory if it doesn't exist
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	// Check if settings.local.json already exists
	var existingSettings map[string]interface{}
	// #nosec G304 - user config path
	if content, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(content, &existingSettings); err != nil {
			// Don't silently overwrite - the user has a file with invalid JSON
			// that likely contains important settings they don't want to lose
			return fmt.Errorf("existing %s contains invalid JSON: %w\nPlease fix the JSON syntax manually before running bd init", settingsPath, err)
		}
	} else if !os.IsNotExist(err) {
		// File exists but couldn't be read (permissions issue, etc.)
		return fmt.Errorf("failed to read existing %s: %w", settingsPath, err)
	} else {
		// File doesn't exist - create new empty settings
		existingSettings = make(map[string]interface{})
	}

	// Add or update the prompt with onboard instruction
	onboardPrompt := "Before starting any work, run 'bd onboard' to understand the current project state and available issues."

	// Check if prompt already contains onboard instruction
	if promptValue, exists := existingSettings["prompt"]; exists {
		if promptStr, ok := promptValue.(string); ok {
			if strings.Contains(promptStr, "bd onboard") {
				if verbose {
					fmt.Printf("Claude settings already configured with bd onboard instruction\n")
				}
				return nil
			}
			// Update existing prompt to include onboard instruction
			existingSettings["prompt"] = promptStr + "\n\n" + onboardPrompt
		} else {
			// Existing prompt is not a string, replace it
			existingSettings["prompt"] = onboardPrompt
		}
	} else {
		// Add new prompt with onboard instruction
		existingSettings["prompt"] = onboardPrompt
	}

	// Write updated settings
	updatedContent, err := json.MarshalIndent(existingSettings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings JSON: %w", err)
	}

	// #nosec G306 - config file needs 0644
	if err := os.WriteFile(settingsPath, updatedContent, 0644); err != nil {
		return fmt.Errorf("failed to write claude settings: %w", err)
	}

	if verbose {
		fmt.Printf("Configured Claude settings with bd onboard instruction\n")
	}

	return nil
}
