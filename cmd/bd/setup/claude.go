package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// InstallClaude installs Claude Code hooks
func InstallClaude(project bool, stealth bool) {
	var settingsPath string

	if project {
		settingsPath = ".claude/settings.local.json"
		fmt.Println("Installing Claude hooks for this project...")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get home directory: %v\n", err)
			os.Exit(1)
		}
		settingsPath = filepath.Join(home, ".claude/settings.json")
		fmt.Println("Installing Claude hooks globally...")
	}

	// Ensure parent directory exists
	if err := EnsureDir(filepath.Dir(settingsPath), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Load or create settings
	var settings map[string]interface{}
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- settingsPath is constructed from user home dir, not user input
	if err != nil {
		settings = make(map[string]interface{})
	} else {
		if err := json.Unmarshal(data, &settings); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to parse settings.json: %v\n", err)
			os.Exit(1)
		}
	}

	// Get or create hooks section
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
		settings["hooks"] = hooks
	}

	// Determine which command to use
	command := "bd prime"
	if stealth {
		command = "bd prime --stealth"
	}

	// Add SessionStart hook
	if addHookCommand(hooks, "SessionStart", command) {
		fmt.Println("✓ Registered SessionStart hook")
	}

	// Add PreCompact hook
	if addHookCommand(hooks, "PreCompact", command) {
		fmt.Println("✓ Registered PreCompact hook")
	}

	// Write back to file
	data, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: marshal settings: %v\n", err)
		os.Exit(1)
	}

	if err := atomicWriteFile(settingsPath, data); err != nil {
		fmt.Fprintf(os.Stderr, "Error: write settings: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n✓ Claude Code integration installed\n")
	fmt.Printf("  Settings: %s\n", settingsPath)
	fmt.Println("\nRestart Claude Code for changes to take effect.")
}

// CheckClaude checks if Claude integration is installed
func CheckClaude() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to get home directory: %v\n", err)
		os.Exit(1)
	}

	globalSettings := filepath.Join(home, ".claude/settings.json")
	projectSettings := ".claude/settings.local.json"

	globalHooks := hasBeadsHooks(globalSettings)
	projectHooks := hasBeadsHooks(projectSettings)

	if globalHooks {
		fmt.Println("✓ Global hooks installed:", globalSettings)
	} else if projectHooks {
		fmt.Println("✓ Project hooks installed:", projectSettings)
	} else {
		fmt.Println("✗ No hooks installed")
		fmt.Println("  Run: bd setup claude")
		os.Exit(1)
	}
}

// RemoveClaude removes Claude Code hooks
func RemoveClaude(project bool) {
	var settingsPath string

	if project {
		settingsPath = ".claude/settings.local.json"
		fmt.Println("Removing Claude hooks from project...")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get home directory: %v\n", err)
			os.Exit(1)
		}
		settingsPath = filepath.Join(home, ".claude/settings.json")
		fmt.Println("Removing Claude hooks globally...")
	}

	// Load settings
	data, err := os.ReadFile(settingsPath) // #nosec G304 -- settingsPath is constructed from user home dir, not user input
	if err != nil {
		fmt.Println("No settings file found")
		return
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to parse settings.json: %v\n", err)
		os.Exit(1)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		fmt.Println("No hooks found")
		return
	}

	// Remove bd prime hooks (both variants for backwards compatibility)
	removeHookCommand(hooks, "SessionStart", "bd prime")
	removeHookCommand(hooks, "PreCompact", "bd prime")
	removeHookCommand(hooks, "SessionStart", "bd prime --stealth")
	removeHookCommand(hooks, "PreCompact", "bd prime --stealth")

	// Write back
	data, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: marshal settings: %v\n", err)
		os.Exit(1)
	}

	if err := atomicWriteFile(settingsPath, data); err != nil {
		fmt.Fprintf(os.Stderr, "Error: write settings: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✓ Claude hooks removed")
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
	var filtered []interface{}
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

	hooks[event] = filtered
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
