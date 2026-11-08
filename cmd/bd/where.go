package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/utils"
)

// WhereResult contains information about the active beads location
type WhereResult struct {
	Path           string `json:"path"`                      // Active .beads directory path
	RedirectedFrom string `json:"redirected_from,omitempty"` // Original path if redirected
	Prefix         string `json:"prefix,omitempty"`          // Issue prefix (if detectable)
	DatabasePath   string `json:"database_path,omitempty"`   // Full path to database file
}

var whereCmd = &cobra.Command{
	Use:     "where",
	GroupID: "setup",
	Short:   "Show active beads location",
	Long: `Show the active beads database location, including redirect information.

	This command is useful for debugging when using redirects, to understand
	which beads workspace is actually being used.

Examples:
  bd where           # Show active beads location
  bd where --json    # Output in JSON format
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("where")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		result := WhereResult{}

		if selected := selectedNoDBBeadsDir(cmd); selected != "" {
			prepareSelectedNoDBContext(selected)
		}

		beadsDir := resolveWhereBeadsDir(cmd)
		if beadsDir == "" {
			hint := whereDiagHint()
			if note := skippedUserGlobalBeadsNote(); note != "" {
				// GH#4402: name the user-level ~/.beads discovery passed
				// over, in the hint both for text and --json output.
				hint += "\n" + note
			}
			if jsonOutput {
				if jerr := outputJSON(map[string]string{
					"error":   "no_beads_directory",
					"message": activeWorkspaceNotFoundMessage(),
					"hint":    hint,
				}); jerr != nil {
					return jerr
				}
				return SilentExit()
			}
			return HandleErrorWithHint(activeWorkspaceNotFoundMessage(), hint)
		}

		result.Path = beadsDir
		result.RedirectedFrom = whereRedirectedFrom(beadsDir)

		// Find the database path
		dbPath := resolveWhereDatabasePath()
		if dbPath != "" {
			result.DatabasePath = dbPath
		}

		// Prefer the active workspace YAML when available. Avoid process-global
		// config here because `bd where` may be reporting a workspace selected
		// by BEADS_DB/BEADS_DIR rather than the caller's current repository.
		if prefix := config.GetStringFromDir(beadsDir, "issue-prefix"); prefix != "" {
			result.Prefix = prefix
		} else if prefix := config.GetStringFromDir(beadsDir, "issue_prefix"); prefix != "" {
			result.Prefix = prefix
		} else if dbPath != "" && shouldReadWherePrefixFromStore(beadsDir) {
			_ = withStorage(getRootContext(), nil, dbPath, func(currentStore storage.DoltStorage) error {
				prefix, err := currentStore.GetConfig(getRootContext(), "issue_prefix")
				if err == nil && prefix != "" {
					result.Prefix = prefix
				}
				return nil
			})
		}

		if jsonOutput {
			return outputJSON(result)
		}
		fmt.Println(result.Path)
		if result.RedirectedFrom != "" {
			fmt.Printf("  (via redirect from %s)\n", result.RedirectedFrom)
		}
		if result.Prefix != "" {
			fmt.Printf("  prefix: %s\n", result.Prefix)
		}
		if result.DatabasePath != "" {
			fmt.Printf("  database: %s\n", result.DatabasePath)
		}
		return nil
	},
}

func resolveWhereBeadsDir(cmd *cobra.Command) string {
	if selected := selectedNoDBBeadsDir(cmd); selected != "" {
		return selected
	}

	return beads.FindBeadsDir()
}

func resolveWhereDatabasePath() string {
	return beads.FindDatabasePath()
}

func shouldReadWherePrefixFromStore(beadsDir string) bool {
	if beadsDir == "" {
		return false
	}

	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil || cfg == nil {
		return true
	}

	// `bd where` should be able to report selected metadata without requiring
	// a live Dolt server (or spawning the proxied-server daemon) just to
	// recover issue_prefix.
	return !cfg.IsDoltServerMode() && !cfg.IsDoltProxiedServerMode()
}

// whereRedirectedFrom returns the redirecting .beads directory that led to
// beadsDir, or "". The source is the one startup recorded for the selected
// workspace (selectedRedirectSourceDir), reported only while its redirect
// still names beadsDir: a redirect in the caller's cwd repo is not the
// provenance of a workspace selected with -C or BEADS_DIR (GH#5756).
func whereRedirectedFrom(beadsDir string) string {
	source := selectedRedirectSource
	if source == "" || sameDirPath(source, beadsDir) {
		return ""
	}
	if info := beads.ResolveRedirect(source); !info.WasRedirected || !sameDirPath(info.TargetDir, beadsDir) {
		return ""
	}
	return utils.CanonicalizePath(source)
}

func init() {
	rootCmd.AddCommand(whereCmd)
}
