package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// kvPrefix is prepended to all user keys to separate them from internal config
const kvPrefix = "kv."

// validateKVKey checks if a key is valid for the KV store.
// Returns an error if the key is invalid.
func validateKVKey(key string) error {
	if key == "" {
		return fmt.Errorf("key cannot be empty")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("key cannot be only whitespace")
	}
	// Prevent keys that would create nested kv.kv.* prefixes
	if strings.HasPrefix(key, "kv.") {
		return fmt.Errorf("key cannot start with 'kv.' (would create nested prefix)")
	}
	// Prevent keys that look like internal config
	if strings.HasPrefix(key, "sync.") || strings.HasPrefix(key, "conflict.") ||
		strings.HasPrefix(key, "federation.") || strings.HasPrefix(key, "jira.") ||
		strings.HasPrefix(key, "linear.") || strings.HasPrefix(key, "export.") ||
		strings.HasPrefix(key, "dream.") {
		return fmt.Errorf("key cannot start with reserved prefix %q", strings.Split(key, ".")[0]+".")
	}
	return nil
}

// kvCmd is the parent command for kv subcommands
var kvCmd = &cobra.Command{
	Use:     "kv",
	GroupID: "setup",
	Short:   "Key-value store commands",
	Long: `Commands for working with the beads key-value store.

The key-value store is useful for storing flags, environment variables,
or other user-defined data that persists across sessions.

Examples:
  bd kv set mykey myvalue    # Set a value
  bd kv get mykey            # Get a value
  bd kv clear mykey          # Delete a key
  bd kv list                 # List all key-value pairs`,
}

// kvSetCmd sets a key-value pair
var kvSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a key-value pair",
	Long: `Set a key-value pair in the beads key-value store.

This is useful for storing flags, environment variables, or other
user-defined data that persists across sessions.

Examples:
  bd kv set feature_flag true
  bd kv set api_endpoint https://api.example.com
  bd kv set max_retries 3`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("kv set")

		if err := ensureDirectMode("kv set requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		key := args[0]
		if err := validateKVKey(key); err != nil {
			FatalErrorRespectJSON("invalid key: %v", err)
		}
		value := args[1]
		storageKey := kvPrefix + key

		ctx := rootCtx
		if err := store.SetConfig(ctx, storageKey, value); err != nil {
			FatalErrorRespectJSON("setting key: %v", err)
		}

		if jsonOutput {
			outputJSON(map[string]string{
				"key":   key,
				"value": value,
			})
		} else {
			fmt.Printf("Set %s = %s\n", key, value)
		}
	},
}

// kvGetCmd gets a value by key
var kvGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a value by key",
	Long: `Get a value from the beads key-value store.

Examples:
  bd kv get feature_flag
  bd kv get api_endpoint`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureDirectMode("kv get requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		key := args[0]
		storageKey := kvPrefix + key

		ctx := rootCtx
		value, err := store.GetConfig(ctx, storageKey)
		if err != nil {
			FatalErrorRespectJSON("getting key: %v", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"key":   key,
				"value": value,
				"found": value != "",
			}
			outputJSON(result)
			if value == "" {
				os.Exit(1)
			}
		} else {
			if value == "" {
				fmt.Fprintf(os.Stderr, "%s (not set)\n", key)
				os.Exit(1)
			} else {
				fmt.Printf("%s\n", value)
			}
		}
	},
}

// kvClearCmd deletes a key
var kvClearCmd = &cobra.Command{
	Use:   "clear <key>",
	Short: "Delete a key-value pair",
	Long: `Delete a key from the beads key-value store.

Examples:
  bd kv clear feature_flag
  bd kv clear api_endpoint`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("kv clear")

		if err := ensureDirectMode("kv clear requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		key := args[0]
		if err := validateKVKey(key); err != nil {
			FatalErrorRespectJSON("invalid key: %v", err)
		}
		storageKey := kvPrefix + key

		ctx := rootCtx
		if err := store.DeleteConfig(ctx, storageKey); err != nil {
			FatalErrorRespectJSON("deleting key: %v", err)
		}

		if jsonOutput {
			outputJSON(map[string]string{
				"key":     key,
				"deleted": "true",
			})
		} else {
			fmt.Printf("Cleared %s\n", key)
		}
	},
}

// kvListCmd lists all key-value pairs
var kvListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all key-value pairs",
	Long: `List all key-value pairs in the beads key-value store.

Examples:
  bd kv list
  bd kv list --json`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureDirectMode("kv list requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		ctx := rootCtx
		allConfig, err := store.GetAllConfig(ctx)
		if err != nil {
			FatalErrorRespectJSON("listing keys: %v", err)
		}

		// Filter for kv.* keys and strip prefix
		kvPairs := make(map[string]string)
		for k, v := range allConfig {
			if strings.HasPrefix(k, kvPrefix) {
				userKey := strings.TrimPrefix(k, kvPrefix)
				kvPairs[userKey] = v
			}
		}

		if jsonOutput {
			outputJSON(kvPairs)
			return
		}

		if len(kvPairs) == 0 {
			fmt.Println("No key-value pairs set")
			return
		}

		// Sort keys for consistent output
		keys := make([]string, 0, len(kvPairs))
		for k := range kvPairs {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		fmt.Println("\nKey-Value Store:")
		for _, k := range keys {
			fmt.Printf("  %s = %s\n", k, kvPairs[k])
		}
	},
}

func init() {
	// Register all kv subcommands under kvCmd
	kvCmd.AddCommand(kvSetCmd)
	kvCmd.AddCommand(kvGetCmd)
	kvCmd.AddCommand(kvClearCmd)
	kvCmd.AddCommand(kvListCmd)

	// Register kv command
	rootCmd.AddCommand(kvCmd)
}
