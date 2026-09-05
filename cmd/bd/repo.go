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
  bd repo add ../other-repo          # Relative path, stored as absolute
  bd repo list                       # Show all configured repos
  bd repo remove ~/beads-planning    # Remove by path
  bd repo sync                       # Sync from all configured repos`,
}

var repoAddCmd = &cobra.Command{
	Use:   "add <path>",
	Short: "Add an additional repository to sync",
	Long: `Add a repository path to the repos.additional list in config.yaml.

The path should point to a directory containing a .beads folder.

A relative path is resolved against the current directory and stored as an
absolute path, so later commands run from anywhere resolve the same repository.
Absolute paths and "~/"-prefixed paths are stored verbatim.

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

			// The existence check above proves the path resolves *now*, from
			// this cwd. Canonicalize before persisting so it still resolves
			// later, from anywhere (be-d0t).
			normalized, err := normalizeRepoPathForStorage(repoPath)
			if err != nil {
				return HandleError("failed to resolve repository path %s: %v", repoPath, err)
			}
			repoPath = normalized
		}

		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return HandleError("failed to find config.yaml: %v", err)
		}

		if err := config.AddRepo(configPath, repoPath); err != nil {
			return HandleError("failed to add repository: %v", err)
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

The path is matched against the configured entries verbatim first, then in the
same canonical form 'bd repo add' stores (a relative path resolved against the
current directory). So "~/foo" removes the entry added as "~/foo", and
"../other-repo" removes the absolute entry it was stored as, provided you run
both commands from the same directory.

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

		repoPath := args[0]

		if err := ensureDirectMode("repo remove requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		// Resolve the configured entry before deleting anything: issues and the
		// mtime cache are keyed by the string stored in config.yaml, which is
		// the canonical form 'bd repo add' persists rather than the argument as
		// typed. Looking the config up first also means a missing config.yaml
		// fails before the destructive delete rather than after it.
		configPath, err := config.FindConfigYAMLPath()
		if err != nil {
			return HandleError("failed to find config.yaml: %v", err)
		}
		repoPath = resolveConfiguredRepoPath(configPath, repoPath)

		ctx := rootCtx

		deletedCount, err := store.DeleteIssuesBySourceRepo(ctx, repoPath)
		if err != nil {
			return HandleError("failed to delete issues from repo: %v", err)
		}

		if err := store.ClearRepoMtime(ctx, repoPath); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear mtime cache: %v\n", err)
		}

		if err := config.RemoveRepo(configPath, repoPath); err != nil {
			return HandleError("failed to remove repository: %v", err)
		}

		// Evict remote cache if applicable
		if remotecache.IsRemoteURL(repoPath) {
			if cache, err := remotecache.DefaultCache(); err == nil {
				_ = cache.Evict(repoPath)
			}
		}

		commandDidWrite.Store(true)

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

// expandRepoTilde expands a leading '~' in a `bd repo` path to the user's home
// directory. It is the expansion every consumer of repos.additional already
// performs (repo sync here, ResolveBeadsDirForRepo in the doctor checks), kept
// in one place so add-time and use-time agree on what a stored entry means.
//
// A path that does not begin with '~', or a home directory that cannot be
// determined, is returned unchanged.
func expandRepoTilde(repoPath string) string {
	if len(repoPath) == 0 || repoPath[0] != '~' {
		return repoPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return repoPath
	}
	return filepath.Join(home, repoPath[1:])
}

// normalizeRepoPathForStorage canonicalizes a local repository path for
// persistence in .beads/config.yaml.
//
// Remote URLs and '~'-prefixed paths are returned verbatim: the first is not a
// filesystem path, and the second is anchored to the user's home directory
// rather than to a working directory, so it stays portable across clones of the
// shared config. Anything else that is not already absolute is cwd-relative and
// is resolved against the current directory.
//
// Storing a relative path as typed is the be-d0t defect: the entry names a
// directory relative to the cwd of whoever ran `bd repo add`, so every later
// resolution — `bd repo sync` from a subdirectory, `bd doctor` from anywhere —
// points somewhere else, or nowhere. The existence check performed at add time
// says nothing about resolution at use time, because the two happen from
// different directories.
func normalizeRepoPathForStorage(repoPath string) (string, error) {
	if remotecache.IsRemoteURL(repoPath) {
		return repoPath, nil
	}
	if repoPath == "" || repoPath[0] == '~' || filepath.IsAbs(repoPath) {
		return repoPath, nil
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// resolveConfiguredRepoPath maps a user-supplied `bd repo remove` argument onto
// the entry actually stored in config.yaml.
//
// A verbatim match wins, so an entry added before paths were canonicalized (or
// added as "~/foo") is still removable by the string it is stored under. Failing
// that, the argument is canonicalized the way `bd repo add` would store it, so
// `bd repo remove ../other-repo` matches the absolute entry that
// `bd repo add ../other-repo` wrote from the same directory.
//
// The argument is returned unchanged when neither form is configured; the caller
// reports the resulting "repository not found" against what the user typed.
func resolveConfiguredRepoPath(configPath, repoPath string) string {
	repos, err := config.ListRepos(configPath)
	if err != nil || repos == nil {
		return repoPath
	}

	for _, existing := range repos.Additional {
		if existing == repoPath {
			return repoPath
		}
	}

	normalized, err := normalizeRepoPathForStorage(repoPath)
	if err != nil || normalized == repoPath {
		return repoPath
	}
	for _, existing := range repos.Additional {
		if existing == normalized {
			return normalized
		}
	}
	return repoPath
}
