package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/versionedhistory"
)

// configSideEffect describes a hint or warning to show after a config change.
type configSideEffect struct {
	Message string `json:"message"`
	Command string `json:"command,omitempty"` // suggested command to run
}

// checkConfigSetSideEffects returns any hints/warnings for a config key being
// set. beadsDir is the active store; its Gas City ownership stamp selects the
// restart command, and an unreadable or invalid stamp is returned as an error.
func checkConfigSetSideEffects(beadsDir, key, value string) ([]configSideEffect, error) {
	var effects []configSideEffect

	switch {
	case key == "federation.remote":
		effects = append(effects, configSideEffect{
			Message: fmt.Sprintf("To activate, ensure a Dolt remote matches this URL: %s", remedy.PrintedValue(value)),
			Command: "bd dolt remote add origin " + commandArg(value, "<url>"),
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
		restart, err := doltserver.RestartHint(beadsDir)
		if err != nil {
			return nil, err
		}
		effects = append(effects, configSideEffect{
			Message: "Debug mode will apply on the next Dolt server start (loglevel=debug, --prof cpu).",
			Command: restart,
		})

	case key == "dolt.debug" && !strings.EqualFold(value, "true"):
		restart, err := doltserver.RestartHint(beadsDir)
		if err != nil {
			return nil, err
		}
		effects = append(effects, configSideEffect{
			Message: "Debug mode disabled. Restart the server to drop --prof and --loglevel=debug.",
			Command: restart,
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
			Command: "git remote -v | grep " + commandArg(value, "<remote>"),
		})

	case key == versionedHistorySettingKey && versionedhistory.ValueEnables(value):
		effects = append(effects, configSideEffect{
			Message: "Versioned history now records for this store. The setting replicates on 'bd dolt push' and 'bd dolt pull', so every clone that pulls it records too, and it is safe with a single writer only: keep one writer at a time per store (docs/reference/configuration.md#versioned-issue-history).",
		})
	}

	return effects, nil
}

// checkConfigUnsetSideEffects returns any hints/warnings for a config key being
// unset. beadsDir selects the restart command as in checkConfigSetSideEffects.
func checkConfigUnsetSideEffects(beadsDir, key string) ([]configSideEffect, error) {
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
		restart, err := doltserver.RestartHint(beadsDir)
		if err != nil {
			return nil, err
		}
		effects = append(effects, configSideEffect{
			Message: "Debug config removed. Restart the server to drop --prof and --loglevel=debug.",
			Command: restart,
		})

	case "backup.enabled":
		effects = append(effects, configSideEffect{
			Message: "Backup config removed. Automatic backups will no longer run.",
		})
	}

	return effects, nil
}

// reportConfigSideEffects prints the effects computed by
// checkConfigSetSideEffects/checkConfigUnsetSideEffects, or fails the command
// with their error.
func reportConfigSideEffects(effects []configSideEffect, err error) error {
	if err != nil {
		return HandleError("%v", err)
	}
	printConfigSideEffects(effects)
	return nil
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

// commandArg is value as one argument of a command printed for the user to
// paste on this platform, or placeholder with value named escaped after it
// when no quoting passes it on unchanged (remedy.CommandArg).
func commandArg(value, placeholder string) string {
	arg, note := remedy.CommandArg(runtime.GOOS, value, placeholder)
	return arg + note
}
