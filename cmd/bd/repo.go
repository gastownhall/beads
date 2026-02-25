package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var repoCmd = &cobra.Command{
	Use:     "repo",
	GroupID: "advanced",
	Short:   "Manage multiple repository configuration",
	Long: `Configure and manage multiple repository support for multi-repo hydration.

Multi-repo support allows hydrating issues from multiple beads repositories
into a single database for unified cross-repo issue tracking.

Configuration is stored in .beads/config.yaml under the 'repos' section:

  repos:
    primary: "."
    additional:
      - ~/beads-planning
      - ~/work-repo

Examples:
  bd repo add ~/beads-planning       # Add planning repo
  bd repo add ../other-repo          # Add relative path repo
  bd repo list                       # Show all configured repos
  bd repo remove ~/beads-planning    # Remove by path
  bd repo sync                       # Sync from all configured repos`,
}

var repoAddCmd = &cobra.Command{
	Use:   "add <path>",
	Short: "Add an additional repository to sync",
	Long: `Add a repository path to the repos.additional list in config.yaml.

The path should point to a directory containing a .beads folder.
Paths can be absolute or relative (they are stored as-is).

This modifies .beads/config.yaml, which is version-controlled and
shared across all clones of this repository.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoPath := args[0]

		// Expand ~ to home directory for validation and display
		expandedPath := repoPath
		if len(repoPath) > 0 && repoPath[0] == '~' {
			home, err := os.UserHomeDir()
			if err == nil {
				expandedPath = filepath.Join(home, repoPath[1:])
			}
		}

		// Validate the repo path exists and has .beads
		beadsDir := filepath.Join(expandedPath, ".beads")
		if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
			return fmt.Errorf("no .beads directory found at %s - is this a beads repository?", expandedPath)
		}

		// Find config.yaml
		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return fmt.Errorf("failed to find config.yaml: %w", err)
		}

		// Add the repo (use original path to preserve ~ etc.)
		if err := config.AddRepo(configPath, repoPath); err != nil {
			return fmt.Errorf("failed to add repository: %w", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"added": true,
				"path":  repoPath,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Printf("Added repository: %s\n", repoPath)
		fmt.Printf("Run 'bd repo sync' to hydrate issues from this repository.\n")
		return nil
	},
}

var repoRemoveCmd = &cobra.Command{
	Use:   "remove <path>",
	Short: "Remove a repository from sync configuration",
	Long: `Remove a repository path from the repos.additional list in config.yaml.

The path must exactly match what was added (e.g., if you added "~/foo",
you must remove "~/foo", not "/home/user/foo").

This command also removes any previously-hydrated issues from the database
that came from the removed repository.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoPath := args[0]

		// Ensure we have direct database access for cleanup
		if err := ensureDirectMode("repo remove requires direct database access"); err != nil {
			return err
		}

		ctx := rootCtx

		// Delete issues from the removed repo before removing from config
		// The source_repo field uses the original path (e.g., "~/foo")
		deletedCount, err := store.DeleteIssuesBySourceRepo(ctx, repoPath)
		if err != nil {
			return fmt.Errorf("failed to delete issues from repo: %w", err)
		}

		// Also clear the mtime cache entry
		if err := store.ClearRepoMtime(ctx, repoPath); err != nil {
			// Non-fatal: just log a warning
			fmt.Fprintf(os.Stderr, "Warning: failed to clear mtime cache: %v\n", err)
		}

		// Find config.yaml
		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return fmt.Errorf("failed to find config.yaml: %w", err)
		}

		// Remove the repo from config
		if err := config.RemoveRepo(configPath, repoPath); err != nil {
			return fmt.Errorf("failed to remove repository: %w", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"removed":        true,
				"path":           repoPath,
				"issues_deleted": deletedCount,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Printf("Removed repository: %s\n", repoPath)
		if deletedCount > 0 {
			fmt.Printf("Deleted %d issue(s) from the database\n", deletedCount)
		}
		return nil
	},
}

var repoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured repositories",
	Long: `List all repositories configured in .beads/config.yaml.

Shows the primary repository (always ".") and any additional
repositories configured for hydration.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Find config.yaml
		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return fmt.Errorf("failed to find config.yaml: %w", err)
		}

		// Get repos from YAML
		repos, err := config.ListRepos(configPath)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if jsonOutput {
			primary := repos.Primary
			if primary == "" {
				primary = "."
			}
			result := map[string]interface{}{
				"primary":    primary,
				"additional": repos.Additional,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		primary := repos.Primary
		if primary == "" {
			primary = "."
		}
		fmt.Printf("Primary repository: %s\n", primary)
		if len(repos.Additional) == 0 {
			fmt.Println("No additional repositories configured")
		} else {
			fmt.Println("\nAdditional repositories:")
			for _, path := range repos.Additional {
				fmt.Printf("  - %s\n", path)
			}
		}
		return nil
	},
}

var repoSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manually trigger multi-repo sync",
	Long: `Synchronize issues from all configured additional repositories.

Reads issues.jsonl from each additional repository and imports them into
the primary database with their original prefixes and source_repo set.
Uses mtime caching to skip repos whose JSONL hasn't changed.

Also triggers Dolt push/pull if a remote is configured.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := ensureDirectMode("repo sync requires direct database access"); err != nil {
			return err
		}

		ctx := rootCtx
		verbose, _ := cmd.Flags().GetBool("verbose")

		// Find config.yaml and get additional repos
		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return fmt.Errorf("failed to find config.yaml: %w", err)
		}

		repos, err := config.ListRepos(configPath)
		if err != nil {
			return fmt.Errorf("failed to load repo config: %w", err)
		}

		totalImported := 0
		totalSkipped := 0

		// Hydrate issues from each additional repository
		for _, repoPath := range repos.Additional {
			// Expand tilde
			expandedPath := repoPath
			if len(repoPath) > 0 && repoPath[0] == '~' {
				home, err := os.UserHomeDir()
				if err == nil {
					expandedPath = filepath.Join(home, repoPath[1:])
				}
			}

			// Resolve to absolute path for consistent mtime caching
			absPath, err := filepath.Abs(expandedPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to resolve path %s: %v\n", repoPath, err)
				continue
			}

			jsonlPath := filepath.Join(absPath, ".beads", "issues.jsonl")
			info, err := os.Stat(jsonlPath)
			if err != nil {
				if verbose {
					fmt.Fprintf(os.Stderr, "Skipping %s: no issues.jsonl found\n", repoPath)
				}
				continue
			}

			// Check mtime cache — skip if JSONL hasn't changed
			currentMtime := info.ModTime().UnixNano()
			cachedMtime, _ := store.GetRepoMtime(ctx, absPath)
			if cachedMtime == currentMtime {
				if verbose {
					fmt.Fprintf(os.Stderr, "Skipping %s: JSONL unchanged\n", repoPath)
				}
				totalSkipped++
				continue
			}

			// Parse issues from JSONL
			issues, err := parseIssuesFromJSONL(jsonlPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to parse %s: %v\n", jsonlPath, err)
				continue
			}

			if len(issues) == 0 {
				if verbose {
					fmt.Fprintf(os.Stderr, "Skipping %s: no issues in JSONL\n", repoPath)
				}
				continue
			}

			// Set source_repo on all imported issues
			for _, issue := range issues {
				issue.SourceRepo = repoPath
			}

			// Import with prefix validation skipped (cross-prefix hydration)
			if err := store.CreateIssuesWithFullOptions(ctx, issues, "repo-sync", storage.BatchCreateOptions{
				OrphanHandling:       storage.OrphanAllow,
				SkipPrefixValidation: true,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to import from %s: %v\n", repoPath, err)
				continue
			}

			// Update mtime cache
			if err := store.SetRepoMtime(ctx, absPath, jsonlPath, currentMtime); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to update mtime cache for %s: %v\n", repoPath, err)
			}

			totalImported += len(issues)
			if verbose {
				fmt.Fprintf(os.Stderr, "Imported %d issue(s) from %s\n", len(issues), repoPath)
			}
		}

		// Push is handled by daemon periodic task, not per-operation.
		// Manual push available via: bd dolt push

		if jsonOutput {
			result := map[string]interface{}{
				"synced":          true,
				"repos_synced":    len(repos.Additional) - totalSkipped,
				"repos_skipped":   totalSkipped,
				"issues_imported": totalImported,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		if totalImported > 0 {
			fmt.Printf("Multi-repo sync complete: imported %d issue(s) from %d repo(s)\n",
				totalImported, len(repos.Additional)-totalSkipped)
		} else if totalSkipped == len(repos.Additional) {
			fmt.Println("Multi-repo sync complete: all repos up to date")
		} else {
			fmt.Println("Multi-repo sync complete")
		}
		return nil
	},
}

// parseIssuesFromJSONL reads and parses issues from a JSONL file.
func parseIssuesFromJSONL(path string) ([]*types.Issue, error) {
	// #nosec G304 -- path comes from user-configured repos.additional in config.yaml
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer f.Close()

	var issues []*types.Issue
	scanner := bufio.NewScanner(f)
	// Allow up to 10MB per line (large issues with embedded content)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal(line, &issue); err != nil {
			return nil, fmt.Errorf("failed to parse issue at line %d: %w", lineNum, err)
		}
		if issue.ID == "" {
			continue // Skip malformed entries
		}
		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read JSONL: %w", err)
	}

	return issues, nil
}

func init() {
	repoCmd.AddCommand(repoAddCmd)
	repoCmd.AddCommand(repoRemoveCmd)
	repoCmd.AddCommand(repoListCmd)
	repoCmd.AddCommand(repoSyncCmd)

	repoAddCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON")
	repoRemoveCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON")
	repoListCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON")
	repoSyncCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON")
	repoSyncCmd.Flags().Bool("verbose", false, "Show detailed sync progress")

	rootCmd.AddCommand(repoCmd)
}
