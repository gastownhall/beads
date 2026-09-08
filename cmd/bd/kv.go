package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
)

// kvPrefix is prepended to all user keys to separate them from internal config
const kvPrefix = kvkeys.Prefix

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
	if strings.HasPrefix(key, kvPrefix) {
		return fmt.Errorf("key cannot start with 'kv.' (would create nested prefix)")
	}
	// Reserve the persistent-memory namespace: a generic memory.* key would
	// store to kv.memory.*, indistinguishable from a `bd remember` memory, and
	// the merge resolver auto-resolves kv.memory.* conflicts with --theirs
	// (GH#2474). Without this guard a user's deliberate kv value could be
	// silently overridden by a remote on pull. Keep the namespace owned by
	// bd remember / bd forget.
	if strings.HasPrefix(key, kvkeys.MemoryPrefix) {
		return fmt.Errorf("key cannot start with %q (reserved for persistent memories; use 'bd remember' / 'bd forget')", kvkeys.MemoryPrefix)
	}
	// Prevent keys that look like internal config
	if strings.HasPrefix(key, "sync.") || strings.HasPrefix(key, "conflict.") ||
		strings.HasPrefix(key, "federation.") || strings.HasPrefix(key, "jira.") ||
		strings.HasPrefix(key, "linear.") || strings.HasPrefix(key, "export.") ||
		strings.HasPrefix(key, "import.") {
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
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("kv set")

		evt := metrics.NewCommandEvent("kv-set")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if err := ensureDirectMode("kv set requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		key := args[0]
		if err := validateKVKey(key); err != nil {
			return HandleErrorRespectJSON("invalid key: %v", err)
		}
		value := args[1]
		storageKey := kvPrefix + key

		ctx := rootCtx
		if err := store.SetConfig(ctx, storageKey, value); err != nil {
			return HandleErrorRespectJSON("setting key: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]string{
				"key":   key,
				"value": value,
			})
		}
		fmt.Printf("Set %s = %s\n", key, value)
		return nil
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
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("kv-get")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if err := ensureDirectMode("kv get requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		key := args[0]
		storageKey := kvPrefix + key

		ctx := rootCtx
		value, err := store.GetConfig(ctx, storageKey)
		if err != nil {
			return HandleErrorRespectJSON("getting key: %v", err)
		}

		if jsonOutput {
			if jerr := outputJSON(map[string]interface{}{
				"key":   key,
				"value": value,
				"found": value != "",
			}); jerr != nil {
				return jerr
			}
			if value == "" {
				return SilentExit()
			}
			return nil
		}
		if value == "" {
			fmt.Fprintf(os.Stderr, "%s (not set)\n", key)
			return SilentExit()
		}
		fmt.Printf("%s\n", value)
		return nil
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
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("kv clear")

		evt := metrics.NewCommandEvent("kv-clear")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if err := ensureDirectMode("kv clear requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		key := args[0]
		if err := validateKVKey(key); err != nil {
			return HandleErrorRespectJSON("invalid key: %v", err)
		}
		storageKey := kvPrefix + key

		ctx := rootCtx
		if err := store.DeleteConfig(ctx, storageKey); err != nil {
			return HandleErrorRespectJSON("deleting key: %v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]string{
				"key":     key,
				"deleted": "true",
			})
		}
		fmt.Printf("Cleared %s\n", key)
		return nil
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("kv-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if err := ensureDirectMode("kv list requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx
		allConfig, err := store.GetAllConfig(ctx)
		if err != nil {
			return HandleErrorRespectJSON("listing keys: %v", err)
		}

		kvPairs := make(map[string]string)
		for k, v := range allConfig {
			if strings.HasPrefix(k, kvPrefix) {
				userKey := strings.TrimPrefix(k, kvPrefix)
				kvPairs[userKey] = v
			}
		}

		if jsonOutput {
			return outputJSON(kvPairs)
		}

		if len(kvPairs) == 0 {
			fmt.Println("No key-value pairs set")
			return nil
		}

		keys := make([]string, 0, len(kvPairs))
		for k := range kvPairs {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		fmt.Println("\nKey-Value Store:")
		for _, k := range keys {
			fmt.Printf("  %s = %s\n", k, kvPairs[k])
		}
		return nil
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
