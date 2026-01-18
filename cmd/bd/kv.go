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

// setCmd sets a key-value pair (top-level: bd set key value)
var setCmd = &cobra.Command{
	Use:     "set <key> <value>",
	GroupID: "setup",
	Short:   "Set a key-value pair",
	Long: `Set a key-value pair in the beads key-value store.

This is useful for storing flags, environment variables, or other
user-defined data that persists across sessions.

Examples:
  bd set feature_flag true
  bd set api_endpoint https://api.example.com
  bd set max_retries 3`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("set")

		if err := ensureDirectMode("set requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		key := args[0]
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

// getCmd gets a value by key (top-level: bd get key)
var getCmd = &cobra.Command{
	Use:     "get <key>",
	GroupID: "setup",
	Short:   "Get a value by key",
	Long: `Get a value from the beads key-value store.

Examples:
  bd get feature_flag
  bd get api_endpoint`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureDirectMode("get requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		key := args[0]
		storageKey := kvPrefix + key

		ctx := rootCtx
		value, err := store.GetConfig(ctx, storageKey)
		if err != nil {
			FatalErrorRespectJSON("getting key: %v", err)
		}

		if jsonOutput {
			outputJSON(map[string]string{
				"key":   key,
				"value": value,
			})
		} else {
			if value == "" {
				fmt.Printf("%s (not set)\n", key)
			} else {
				fmt.Printf("%s\n", value)
			}
		}
	},
}

// clearCmd deletes a key (top-level: bd clear key)
var clearCmd = &cobra.Command{
	Use:     "clear <key>",
	GroupID: "setup",
	Short:   "Delete a key-value pair",
	Long: `Delete a key from the beads key-value store.

Examples:
  bd clear feature_flag
  bd clear api_endpoint`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("clear")

		if err := ensureDirectMode("clear requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		key := args[0]
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

// kvCmd is the parent command for kv subcommands
var kvCmd = &cobra.Command{
	Use:     "kv",
	GroupID: "setup",
	Short:   "Key-value store commands",
	Long: `Commands for working with the beads key-value store.

The key-value store is useful for storing flags, environment variables,
or other user-defined data that persists across sessions.

Examples:
  bd kv list              # List all key-value pairs
  bd set mykey myvalue    # Set a value (top-level alias)
  bd get mykey            # Get a value (top-level alias)
  bd clear mykey          # Delete a key (top-level alias)`,
}

// kvListCmd lists all key-value pairs (bd kv list)
var kvListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all key-value pairs",
	Long: `List all key-value pairs in the beads key-value store.

Examples:
  bd kv list
  bd kv list --json`,
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureDirectMode("kv list requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
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
	// Register top-level commands
	rootCmd.AddCommand(setCmd)
	rootCmd.AddCommand(getCmd)
	rootCmd.AddCommand(clearCmd)

	// Register kv subcommand with list
	kvCmd.AddCommand(kvListCmd)
	rootCmd.AddCommand(kvCmd)
}
