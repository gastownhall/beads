package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/remotecache"
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
A relative path is resolved to an absolute path at add time, so the
stored entry names the same workspace no matter which directory later
commands run from.

Absolute paths, remote URLs and ~/-prefixed paths are stored verbatim;
~/ stays home-relative rather than baking one machine's home directory
into the shared config.

This modifies .beads/config.yaml, which is version-controlled and
shared across all clones of this repository.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("repo add is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("repo-add")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		repoPath := args[0]

		if remotecache.IsRemoteURL(repoPath) {
			fmt.Fprintf(os.Stderr, "Adding remote repository: %s\n", repoPath)
		} else {
			expandedPath := expandRepoTilde(repoPath)

			beadsDir := filepath.Join(expandedPath, ".beads")
			if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
				return HandleError("no beads workspace found at %s", expandedPath)
			}
		}

		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return HandleError("failed to find config.yaml: %v", err)
		}

		// Resolve the target once, at add time: every consumer re-resolves a
		// stored relative string against its own cwd, so it would name a
		// different repository per directory (bd-59llyl).
		storedPath, err := normalizeRepoPathForStorage(repoPath)
		if err != nil {
			return HandleError("failed to resolve repository path %s: %v", repoPath, err)
		}

		if err := config.AddRepo(configPath, storedPath); err != nil {
			return HandleError("failed to add repository: %v", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"added": true,
				"path":  storedPath,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Printf("Added repository: %s\n", storedPath)
		fmt.Printf("Run 'bd repo sync' to hydrate issues from this repository.\n")
		return nil
	},
}

var repoRemoveCmd = &cobra.Command{
	Use:   "remove <path>",
	Short: "Remove a repository from sync configuration",
	Long: `Remove a repository path from the repos.additional list in config.yaml.

An argument that exactly matches a configured entry removes that entry,
so entries written by older versions — including relative ones — stay
removable from any directory. Otherwise a relative argument is resolved
against the current directory the way bd repo add resolves it, and the
absolute entry it names is removed. Nothing is deleted unless a
configured entry matches.

This command also removes any previously-hydrated issues from the database
that came from the removed repository.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("repo remove is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("repo-remove")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		repoArg := args[0]

		if err := ensureDirectMode("repo remove requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		// An empty argument would otherwise match an empty configured entry
		// and delete every local issue (source_repo defaults to "").
		if repoArg == "" {
			return HandleError("repository path must not be empty")
		}

		// Find the configured entry before touching the database or the
		// config: an argument that matches nothing must delete no issue and
		// clear no mtime row (bd-59llyl).
		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return HandleError("failed to find config.yaml: %v", err)
		}

		repos, err := config.ListRepos(configPath)
		if err != nil {
			return HandleError("failed to load repo config: %v", err)
		}

		storedPath := findConfiguredRepoEntry(repos.Additional, repoArg)
		if storedPath == "" {
			return HandleError("repository not found: %s", repoArg)
		}

		ctx := rootCtx

		// Every consumer — deletion, mtime cache, config, remote eviction
		// and output — works on the stored entry, so the source key remove
		// deletes is the key bd repo sync recorded.
		deletedCount, err := store.DeleteIssuesBySourceRepo(ctx, storedPath)
		if err != nil {
			return HandleError("failed to delete issues from repo: %v", err)
		}

		if err := store.ClearRepoMtime(ctx, storedPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear mtime cache: %v\n", err)
		}

		if err := config.RemoveRepo(configPath, storedPath); err != nil {
			return HandleError("failed to remove repository: %v", err)
		}

		// Evict remote cache if applicable
		if remotecache.IsRemoteURL(storedPath) {
			if cache, err := remotecache.DefaultCache(); err == nil {
				_ = cache.Evict(storedPath)
			}
		}

		commandDidWrite.Store(true)

		if jsonOutput {
			result := map[string]interface{}{
				"removed":        true,
				"path":           storedPath,
				"issues_deleted": deletedCount,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Printf("Removed repository: %s\n", storedPath)
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("repo list is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("repo-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return HandleError("failed to find config.yaml: %v", err)
		}

		repos, err := config.ListRepos(configPath)
		if err != nil {
			return HandleError("failed to load config: %v", err)
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("repo sync is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("repo-sync")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if err := ensureDirectMode("repo sync requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx
		verbose, _ := cmd.Flags().GetBool("verbose")

		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return HandleError("failed to find config.yaml: %v", err)
		}

		repos, err := config.ListRepos(configPath)
		if err != nil {
			return HandleError("failed to load repo config: %v", err)
		}

		totalImported := 0
		totalSkipped := 0

		// Hydrate issues from each additional repository
		for _, repoPath := range repos.Additional {
			// Remote URL: pull into cache, read issues from SQL store
			if remotecache.IsRemoteURL(repoPath) {
				cache, err := remotecache.DefaultCache()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to init cache for %s: %v\n", repoPath, err)
					continue
				}
				if _, err = cache.Ensure(ctx, repoPath); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to sync remote %s: %v\n", repoPath, err)
					continue
				}
				remoteStore, err := cache.OpenStore(ctx, repoPath, newDoltStoreFromConfig)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to open remote store %s: %v\n", repoPath, err)
					continue
				}

				issues, err := remoteStore.SearchIssues(ctx, "", types.IssueFilter{})
				_ = remoteStore.Close() // close eagerly — defer in a loop would leak connections
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to read issues from %s: %v\n", repoPath, err)
					continue
				}

				for _, issue := range issues {
					issue.SourceRepo = repoPath
				}
				if len(issues) > 0 {
					if importErr := store.CreateIssuesWithFullOptions(ctx, issues, "repo-sync", storage.BatchCreateOptions{
						SkipPrefixValidation: true,
					}); importErr != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to import from %s: %v\n", repoPath, importErr)
						continue
					}
					totalImported += len(issues)
					if verbose {
						fmt.Fprintf(os.Stderr, "Imported %d issue(s) from remote %s\n", len(issues), repoPath)
					}
				}
				continue
			}

			// Local path: expand tilde
			expandedPath := expandRepoTilde(repoPath)

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

		// Push is handled by periodic sync, not per-operation.
		// Manual push available via: bd dolt push

		if totalImported > 0 {
			commandDidWrite.Store(true)
		}

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

// expandRepoTilde expands a leading `~` to the user's home directory.
// `bd repo add` (validating the target) and `bd repo sync` (resolving a
// configured entry) both read a path through this helper, so add-time
// validation and use-time resolution cannot drift apart. A home-directory
// lookup that fails leaves the path unchanged.
func expandRepoTilde(path string) string {
	if len(path) > 0 && path[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

// normalizeRepoPathForStorage returns the entry `bd repo add` persists for
// arg. A relative target is resolved against the working directory once, at
// add time: every consumer re-resolves a stored relative string against its
// own cwd, so it would name a different repository per directory (bd-59llyl).
// Remote URLs, `~`-prefixed paths and absolute paths are stored verbatim: a
// `~/` entry is already cwd-independent, and absolutizing it would bake one
// machine's home directory into a config.yaml shared across clones.
func normalizeRepoPathForStorage(arg string) (string, error) {
	if remotecache.IsRemoteURL(arg) {
		return arg, nil
	}
	if arg == "" || arg[0] == '~' || filepath.IsAbs(arg) {
		return arg, nil
	}
	return filepath.Abs(arg)
}

// findConfiguredRepoEntry returns the configured repos.additional entry that
// repoArg removes, or "" when none matches. An exact string match wins, so
// entries written by older versions — including relative ones — stay
// removable from any directory; otherwise repoArg is resolved the way
// `bd repo add` resolves it and matched against the stored entries, so a
// relative argument still finds the absolute entry add wrote from that
// directory.
func findConfiguredRepoEntry(entries []string, repoArg string) string {
	for _, entry := range entries {
		if entry == repoArg {
			return entry
		}
	}
	normalized, err := normalizeRepoPathForStorage(repoArg)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry == normalized {
			return entry
		}
	}
	return ""
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

	repoSyncCmd.Flags().Bool("verbose", false, "Show detailed sync progress")

	rootCmd.AddCommand(repoCmd)
}
