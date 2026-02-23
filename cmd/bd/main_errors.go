package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// isFreshCloneError checks if the error is due to a fresh clone scenario
// where the database exists but is missing required config (like issue_prefix).
// This happens when someone clones a repo with beads but needs to initialize.
func isFreshCloneError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for the specific migration invariant error pattern
	return strings.Contains(errStr, "post-migration validation failed") &&
		strings.Contains(errStr, "required config key missing: issue_prefix")
}

// handleFreshCloneError displays a helpful message when a fresh clone is detected
// and returns true if the error was handled (so caller should exit).
// If not a fresh clone error, returns false and does nothing.
func handleFreshCloneError(err error, beadsDir string) bool {
	if !isFreshCloneError(err) {
		return false
	}

	fmt.Fprintf(os.Stderr, "Error: Database not initialized\n\n")
	fmt.Fprintf(os.Stderr, "This appears to be a fresh clone or the database needs initialization.\n")
	fmt.Fprintf(os.Stderr, "\nTo initialize a new database, run:\n")
	fmt.Fprintf(os.Stderr, "  bd init --prefix <your-prefix>\n\n")
	fmt.Fprintf(os.Stderr, "For more information: bd init --help\n")
	return true
}

// isWispOperation returns true if the command operates on ephemeral wisps.
// Wisp operations auto-bypass the daemon because wisps are local-only.
// Detects:
//   - mol wisp subcommands (create, list, gc, or direct proto invocation)
//   - mol burn (only operates on wisps)
//   - mol squash (condenses wisps to digests)
//   - Commands with ephemeral issue IDs in args (bd-*-wisp-*, wisp-*, or legacy eph-*)
func isWispOperation(cmd *cobra.Command, args []string) bool {
	cmdName := cmd.Name()

	// Check command hierarchy for wisp subcommands
	// bd mol wisp → parent is "mol", cmd is "wisp"
	// bd mol wisp create → parent is "wisp", cmd is "create"
	if cmd.Parent() != nil {
		parentName := cmd.Parent().Name()
		// Direct wisp command or subcommands under wisp
		if parentName == "wisp" || cmdName == "wisp" {
			return true
		}
		// mol burn and mol squash are wisp-only operations
		if parentName == "mol" && (cmdName == "burn" || cmdName == "squash") {
			return true
		}
	}

	// Check for ephemeral issue IDs in arguments
	// Ephemeral IDs have "wisp" segment: bd-wisp-xxx, gt-wisp-xxx, wisp-xxx
	// Also detect legacy "eph" prefix for backwards compatibility
	for _, arg := range args {
		// Skip flags
		if strings.HasPrefix(arg, "-") {
			continue
		}
		// Check for ephemeral prefix patterns (wisp-* or legacy eph-*)
		if strings.Contains(arg, "-wisp-") || strings.HasPrefix(arg, "wisp-") ||
			strings.Contains(arg, "-eph-") || strings.HasPrefix(arg, "eph-") {
			return true
		}
	}

	return false
}
