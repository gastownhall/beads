package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
)

var repoCmd = &cobra.Command{
	Use:   "repo",
	Short: "Manage multiple repository configuration",
	Long: `Configure and manage multiple repository support for multi-clone sync.

Examples:
  bd repo add ~/.beads-planning      # Add planning repo
  bd repo add ../other-repo "notes"  # Add with alias
  bd repo list                       # Show all configured repos
  bd repo remove notes               # Remove by alias
  bd repo remove ~/.beads-planning   # Remove by path`,
}

var repoAddCmd = &cobra.Command{
	Use:   "add <path> [alias]",
	Short: "Add an additional repository to sync",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := ensureDirectMode("repo add requires direct database access"); err != nil {
			return err
		}

		ctx := rootCtx
		path := args[0]
		var alias string
		if len(args) > 1 {
			alias = args[1]
		}

		// Use path as key if no alias provided
		key := alias
		if key == "" {
			key = path
		}

		// Get existing repos
		existing, err := getRepoConfig(ctx, store)
		if err != nil {
			return fmt.Errorf("failed to get existing repos: %w", err)
		}

		existing[key] = path

		// Save back
		if err := setRepoConfig(ctx, store, existing); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"added": true,
				"key":   key,
				"path":  path,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Printf("Added repository: %s → %s\n", key, path)
		return nil
	},
}

var repoRemoveCmd = &cobra.Command{
	Use:   "remove <key>",
	Short: "Remove a repository from sync configuration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := ensureDirectMode("repo remove requires direct database access"); err != nil {
			return err
		}

		ctx := rootCtx
		key := args[0]

		// Get existing repos
		existing, err := getRepoConfig(ctx, store)
		if err != nil {
			return fmt.Errorf("failed to get existing repos: %w", err)
		}

		path, exists := existing[key]
		if !exists {
			return fmt.Errorf("repository not found: %s", key)
		}

		delete(existing, key)

		// Save back
		if err := setRepoConfig(ctx, store, existing); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"removed": true,
				"key":     key,
				"path":    path,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Printf("Removed repository: %s → %s\n", key, path)
		return nil
	},
}

var repoListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured repositories",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := ensureDirectMode("repo list requires direct database access"); err != nil {
			return err
		}

		ctx := rootCtx
		repos, err := getRepoConfig(ctx, store)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"primary":    ".",
				"additional": repos,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Println("Primary repository: .")
		if len(repos) == 0 {
			fmt.Println("No additional repositories configured")
		} else {
			fmt.Println("\nAdditional repositories:")
			for key, path := range repos {
				fmt.Printf("  %s → %s\n", key, path)
			}
		}
		return nil
	},
}

var repoSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Manually trigger multi-repo sync",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := ensureDirectMode("repo sync requires direct database access"); err != nil {
			return err
		}

		ctx := rootCtx

		// Import from all repos
		jsonlPath := findJSONLPath()
		if err := importToJSONLWithStore(ctx, store, jsonlPath); err != nil {
			return fmt.Errorf("import failed: %w", err)
		}

		// Export to all repos
		if err := exportToJSONLWithStore(ctx, store, jsonlPath); err != nil {
			return fmt.Errorf("export failed: %w", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"synced": true,
			}
			return json.NewEncoder(os.Stdout).Encode(result)
		}

		fmt.Println("Multi-repo sync complete")
		return nil
	},
}

// Helper functions for repo config management
func getRepoConfig(ctx context.Context, store storage.Storage) (map[string]string, error) {
	value, err := store.GetConfig(ctx, "repos.additional")
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return make(map[string]string), nil
		}
		return nil, err
	}

	// Parse JSON map
	repos := make(map[string]string)
	if err := json.Unmarshal([]byte(value), &repos); err != nil {
		return nil, fmt.Errorf("failed to parse repos config: %w", err)
	}

	return repos, nil
}

func setRepoConfig(ctx context.Context, store storage.Storage, repos map[string]string) error {
	data, err := json.Marshal(repos)
	if err != nil {
		return fmt.Errorf("failed to serialize repos: %w", err)
	}

	return store.SetConfig(ctx, "repos.additional", string(data))
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

	rootCmd.AddCommand(repoCmd)
}
