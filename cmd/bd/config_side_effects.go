package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/config"
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

	case key == "dolt.debug" && strings.EqualFold(value, "true"):
		effects = append(effects, configSideEffect{
			Message: "Debug mode will apply on the next Dolt server start (loglevel=debug, --prof cpu).",
			Command: "bd dolt stop && bd dolt start",
		})

	case key == "dolt.debug" && !strings.EqualFold(value, "true"):
		effects = append(effects, configSideEffect{
			Message: "Debug mode disabled. Restart the server to drop --prof and --loglevel=debug.",
			Command: "bd dolt stop && bd dolt start",
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

	case key == syncRemoteRefKey && canonicalGitDataRef(value) == "":
		// The empty value and refs/dolt/data both mean the default: the same
		// outcome as unsetting the key.
		effects = append(effects, checkConfigUnsetSideEffects(key)...)

	case key == syncRemoteRefKey:
		effects = append(effects, configSideEffect{
			Message: fmt.Sprintf("Dolt data ref for origin set to %q. bd bootstrap and bd init read it; an existing origin remote keeps its ref until re-added.", value),
			Command: fmt.Sprintf("bd dolt remote add origin <url> --ref %s", value),
		})

	case key == "sync.remote":
		if hint, ok := syncRemoteRefIgnoredHint(value, config.GetString(syncRemoteRefKey)); ok {
			effects = append(effects, hint)
		}
	}

	return effects
}

// syncRemoteRefIgnoredHint warns when sync.remote is pointed at a URL that
// cannot carry the configured sync.remote-ref, so the stale key is named
// before a later bootstrap or init trips over it.
func syncRemoteRefIgnoredHint(remoteURL, configuredRef string) (configSideEffect, bool) {
	if configuredRef == "" || isGitBackedDoltRemoteURL(remoteURL) {
		return configSideEffect{}, false
	}
	return configSideEffect{
		Message: fmt.Sprintf("%s is %q, but %q is not a git-backed Dolt remote, so the ref cannot apply to it and is ignored. Clear the key, or use a git+ URL.", syncRemoteRefKey, configuredRef, remoteURL),
		Command: fmt.Sprintf("bd config set %s \"\"", syncRemoteRefKey),
	}, true
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

	case "dolt.debug":
		effects = append(effects, configSideEffect{
			Message: "Debug config removed. Restart the server to drop --prof and --loglevel=debug.",
			Command: "bd dolt stop && bd dolt start",
		})

	case "backup.enabled":
		effects = append(effects, configSideEffect{
			Message: "Backup config removed. Automatic backups will no longer run.",
		})

	case syncRemoteRefKey:
		effects = append(effects, configSideEffect{
			Message: "Dolt data ref removed from config; bootstrap and init look at refs/dolt/data again. An existing origin remote keeps its ref until re-added.",
			Command: "bd dolt remote add origin <url> --ref refs/dolt/data",
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
