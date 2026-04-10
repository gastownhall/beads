package main

import (
	"fmt"
	"os"
	"strings"
)

// configSideEffect describes a hint or warning to show after a config change.
type configSideEffect struct {
	Message string `json:"message"`
	Command string `json:"command,omitempty"` // suggested command to run
}

// checkConfigSetSideEffects returns any hints/warnings for a config key being set.
func checkConfigSetSideEffects(key, value string) []configSideEffect {
	var effects []configSideEffect

	switch {
	case key == "federation.remote":
		effects = append(effects, configSideEffect{
			Message: fmt.Sprintf("To activate, ensure a Dolt remote matches this URL: %s", value),
			Command: fmt.Sprintf("bd dolt remote add origin %s", value),
		})

	case key == "dolt.shared-server" && strings.EqualFold(value, "true"):
		effects = append(effects, configSideEffect{
			Message: "Shared server mode enabled. Start the server to activate.",
			Command: "bd dolt server start",
		})

	case key == "dolt.shared-server" && !strings.EqualFold(value, "true"):
		effects = append(effects, configSideEffect{
			Message: "Shared server mode disabled. Stop any running server if no longer needed.",
			Command: "bd dolt server stop",
		})

	case key == "routing.mode":
		validModes := map[string]bool{"maintainer": true, "contributor": true, "auto": true, "explicit": true}
		if !validModes[value] {
			effects = append(effects, configSideEffect{
				Message: fmt.Sprintf("Unknown routing mode %q. Valid values: auto, maintainer, contributor, explicit", value),
			})
		}

	case key == "backup.enabled" && strings.EqualFold(value, "true"):
		effects = append(effects, configSideEffect{
			Message: "Backups enabled. Backups run automatically on issue writes.",
		})

	case key == "sync.git-remote":
		effects = append(effects, configSideEffect{
			Message: fmt.Sprintf("Git sync remote set to %q. Ensure this git remote exists.", value),
			Command: fmt.Sprintf("git remote -v | grep %s", value),
		})
	}

	return effects
}

// checkConfigUnsetSideEffects returns any hints/warnings for a config key being unset.
func checkConfigUnsetSideEffects(key string) []configSideEffect {
	var effects []configSideEffect

	switch key {
	case "federation.remote":
		effects = append(effects, configSideEffect{
			Message: "Federation remote removed from config. The Dolt remote still exists and can be removed manually.",
			Command: "bd dolt remote remove origin",
		})

	case "dolt.shared-server":
		effects = append(effects, configSideEffect{
			Message: "Shared server config removed. Stop any running server if no longer needed.",
			Command: "bd dolt server stop",
		})

	case "backup.enabled":
		effects = append(effects, configSideEffect{
			Message: "Backup config removed. Automatic backups will no longer run.",
		})
	}

	return effects
}

// printConfigSideEffects displays side-effect hints to stderr (so they don't
// interfere with --json stdout output).
func printConfigSideEffects(effects []configSideEffect) {
	if len(effects) == 0 {
		return
	}

	for _, e := range effects {
		fmt.Fprintf(os.Stderr, "\nHint: %s\n", e.Message)
		if e.Command != "" {
			fmt.Fprintf(os.Stderr, "  → %s\n", e.Command)
		}
	}
}
