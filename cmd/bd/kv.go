package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
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

// printKVSetResult renders the `bd kv set` success output. Shared by the
// classic and proxied-server paths so the output shape cannot drift.
func printKVSetResult(key, value string) error {
	if jsonOutput {
		return outputJSON(map[string]string{
			"key":   key,
			"value": value,
		})
	}
	fmt.Printf("Set %s = %s\n", key, value)
	return nil
}

// printKVGetResult renders the `bd kv get` output (including the not-found
// SilentExit contract). Shared by the classic and proxied-server paths.
func printKVGetResult(key, value string) error {
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
}

// printKVClearResult renders the `bd kv clear` success output. Shared by the
// classic and proxied-server paths.
func printKVClearResult(key string) error {
	if jsonOutput {
		return outputJSON(map[string]string{
			"key":     key,
			"deleted": "true",
		})
	}
	fmt.Printf("Cleared %s\n", key)
	return nil
}

// kvPairsFromConfig filters a full config map down to the kv.* namespace,
// stripping the prefix. Shared by the classic and proxied-server paths.
func kvPairsFromConfig(allConfig map[string]string) map[string]string {
	kvPairs := make(map[string]string)
	for k, v := range allConfig {
		if strings.HasPrefix(k, kvPrefix) {
			userKey := strings.TrimPrefix(k, kvPrefix)
			kvPairs[userKey] = v
		}
	}
	return kvPairs
}

// configPrefixReader mirrors domain.ConfigPrefixReader structurally: the
// optional store fast path that reads only the keys under a prefix. Both
// list paths discover it by assertion and fall back to the full read plus
// kvPairsWithPrefix when the store predates it.
type configPrefixReader interface {
	GetConfigByPrefix(ctx context.Context, prefix string) (map[string]string, error)
}

// The mirror above is structural, so this assignment is what ties it to the
// domain interface the stores are pinned to. The check is one-way: it stops
// compiling once the mirror no longer covers domain's method set, but a method
// added only to the mirror still compiles — and would silently fail discovery
// against the stores. TestKVPrefixReaderUnwrapsStorageDecorators covers that
// direction: its stub implements only DoltStorage plus the domain method.
var _ domain.ConfigPrefixReader = configPrefixReader(nil)

// configPrefixReaderFor discovers the optional prefix-read capability on a
// store, peeling the decorator chain first.
//
// The peel is load-bearing, not defensive: cmd/bd's store is always decorated
// (wireStorageDecorators interposes externaldeps.Store and, by default,
// HookFiringStore), and those wrappers embed the storage.DoltStorage
// *interface* — which deliberately does not declare GetConfigByPrefix. A bare
// assertion on the outermost layer therefore can never succeed, and because
// the fallback returns identical rows it would silently full-scan forever.
// storage.UnwrapStore is the repo's documented idiom for reaching the concrete
// store behind optional-interface assertions; every wrapper implements
// Unwrap(), so it lands on the raw store in every chain configuration.
func configPrefixReaderFor(s storage.DoltStorage) (configPrefixReader, bool) {
	pr, ok := storage.UnwrapStore(s).(configPrefixReader)
	return pr, ok
}

// readKVListConfig is the direct-mode store read behind `bd kv list`: only the
// rows under kvPrefix+userPrefix when the store offers the prefix fast path,
// the whole config table otherwise. kvPairsWithPrefix narrows the result
// either way, so the two arms differ only in cost — which is why
// TestKVPrefixReaderUnwrapsStorageDecorators drives this function rather than
// configPrefixReaderFor alone.
func readKVListConfig(ctx context.Context, s storage.DoltStorage, userPrefix string) (map[string]string, error) {
	if pr, ok := configPrefixReaderFor(s); ok && userPrefix != "" {
		return pr.GetConfigByPrefix(ctx, kvPrefix+userPrefix)
	}
	return s.GetAllConfig(ctx)
}

// kvPairsWithPrefix is kvPairsFromConfig narrowed to user keys starting with
// userPrefix. It runs on every listing: after a SQL-side prefix read it is a
// no-op re-check, and on the GetAllConfig fallback it IS the filter — so the
// output is identical whichever path served the read.
func kvPairsWithPrefix(allConfig map[string]string, userPrefix string) map[string]string {
	kvPairs := kvPairsFromConfig(allConfig)
	if userPrefix == "" {
		return kvPairs
	}
	out := make(map[string]string)
	for k, v := range kvPairs {
		if strings.HasPrefix(k, userPrefix) {
			out[k] = v
		}
	}
	return out
}

// printKVListResult renders the `bd kv list` output. Shared by the classic
// and proxied-server paths (a mail client parses the --json shape; keep it
// byte-identical across modes).
func printKVListResult(kvPairs map[string]string) error {
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

		key := args[0]
		if err := validateKVKey(key); err != nil {
			return HandleErrorRespectJSON("invalid key: %v", err)
		}
		value := args[1]

		if usesProxiedServer() {
			return runKVSetProxiedServer(rootCtx, key, value)
		}

		if err := ensureDirectMode("kv set requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		storageKey := kvPrefix + key

		ctx := rootCtx
		if err := store.SetConfig(ctx, storageKey, value); err != nil {
			return HandleErrorRespectJSON("setting key: %v", err)
		}

		return printKVSetResult(key, value)
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

		key := args[0]

		if usesProxiedServer() {
			return runKVGetProxiedServer(rootCtx, key)
		}

		if err := ensureDirectMode("kv get requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		storageKey := kvPrefix + key

		ctx := rootCtx
		value, err := store.GetConfig(ctx, storageKey)
		if err != nil {
			return HandleErrorRespectJSON("getting key: %v", err)
		}

		return printKVGetResult(key, value)
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

		key := args[0]
		if err := validateKVKey(key); err != nil {
			return HandleErrorRespectJSON("invalid key: %v", err)
		}

		if usesProxiedServer() {
			return runKVClearProxiedServer(rootCtx, key)
		}

		if err := ensureDirectMode("kv clear requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		storageKey := kvPrefix + key

		ctx := rootCtx
		if err := store.DeleteConfig(ctx, storageKey); err != nil {
			return HandleErrorRespectJSON("deleting key: %v", err)
		}

		return printKVClearResult(key)
	},
}

// kvListPrefix is the --prefix flag: list only keys starting with it.
var kvListPrefix string

// kvListCmd lists all key-value pairs
var kvListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all key-value pairs",
	Long: `List all key-value pairs in the beads key-value store.

With --prefix, list only the keys starting with that prefix. The filter is
pushed into SQL on stores that support it, so a scoped read of a large kv
store does not serialize the whole table.

Examples:
  bd kv list
  bd kv list --json
  bd kv list --prefix mail.dog. --json`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("kv-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runKVListProxiedServer(rootCtx, kvListPrefix)
		}

		if err := ensureDirectMode("kv list requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx
		allConfig, err := readKVListConfig(ctx, store, kvListPrefix)
		if err != nil {
			return HandleErrorRespectJSON("listing keys: %v", err)
		}

		return printKVListResult(kvPairsWithPrefix(allConfig, kvListPrefix))
	},
}

func init() {
	kvListCmd.Flags().StringVar(&kvListPrefix, "prefix", "", "list only keys starting with this prefix (filtered in SQL where supported)")

	// Register all kv subcommands under kvCmd
	kvCmd.AddCommand(kvSetCmd)
	kvCmd.AddCommand(kvGetCmd)
	kvCmd.AddCommand(kvClearCmd)
	kvCmd.AddCommand(kvListCmd)

	// Register kv command
	rootCmd.AddCommand(kvCmd)
}
