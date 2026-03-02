package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// memoryPrefix is prepended (after kvPrefix) to all memory keys.
const memoryPrefix = "memory."

// memoryKeyFlag allows explicit key override for bd remember.
var memoryKeyFlag string

// slugify converts a string to a URL-friendly slug for use as a memory key.
// Takes the first ~8 words, lowercases, replaces non-alphanumeric with hyphens.
func slugify(s string) string {
	s = strings.ToLower(s)
	// Replace non-alphanumeric chars with hyphens
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	// Limit to first ~8 "words" (hyphen-separated segments)
	parts := strings.SplitN(s, "-", 10)
	if len(parts) > 8 {
		parts = parts[:8]
	}
	slug := strings.Join(parts, "-")

	// Cap total length
	if len(slug) > 60 {
		slug = slug[:60]
		// Don't end on a hyphen
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}

// rememberCmd stores a memory.
var rememberCmd = &cobra.Command{
	Use:   `remember "<insight>"`,
	Short: "Store a persistent memory",
	Long: `Store a memory that persists across sessions and account rotations.

Memories are injected at prime time (bd prime / gt prime) so you have them
in every session without manual loading.

Examples:
  bd remember "always run tests with -race flag"
  bd remember "Dolt phantom DBs hide in three places" --key dolt-phantoms
  bd remember "auth module uses JWT not sessions" --key auth-jwt`,
	GroupID: "setup",
	Args:    cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("remember")

		if err := ensureDirectMode("remember requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		insight := args[0]
		if strings.TrimSpace(insight) == "" {
			FatalErrorRespectJSON("memory content cannot be empty")
		}

		// Generate or use provided key
		key := memoryKeyFlag
		if key == "" {
			key = slugify(insight)
		}
		if key == "" {
			FatalErrorRespectJSON("could not generate key from content; use --key to specify one")
		}

		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx

		// Check if updating an existing memory
		existing, _ := store.GetConfig(ctx, storageKey)
		verb := "Remembered"
		if existing != "" {
			verb = "Updated"
		}

		if err := store.SetConfig(ctx, storageKey, insight); err != nil {
			FatalErrorRespectJSON("storing memory: %v", err)
		}

		if jsonOutput {
			outputJSON(map[string]string{
				"key":    key,
				"value":  insight,
				"action": strings.ToLower(verb),
			})
		} else {
			fmt.Printf("%s [%s]: %s\n", verb, key, truncateMemory(insight, 80))
		}
	},
}

// memoriesCmd lists and searches memories.
var memoriesCmd = &cobra.Command{
	Use:   "memories [search]",
	Short: "List or search persistent memories",
	Long: `List all memories, or search by keyword.

Examples:
  bd memories              # list all memories
  bd memories dolt         # search for memories about dolt
  bd memories "race flag"  # search for a phrase`,
	GroupID: "setup",
	Args:    cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureDirectMode("memories requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		ctx := rootCtx
		allConfig, err := store.GetAllConfig(ctx)
		if err != nil {
			FatalErrorRespectJSON("listing memories: %v", err)
		}

		// Filter for kv.memory.* keys
		fullPrefix := kvPrefix + memoryPrefix
		memories := make(map[string]string)
		for k, v := range allConfig {
			if strings.HasPrefix(k, fullPrefix) {
				userKey := strings.TrimPrefix(k, fullPrefix)
				memories[userKey] = v
			}
		}

		// Apply search filter if provided
		var search string
		if len(args) > 0 {
			search = strings.ToLower(args[0])
		}
		if search != "" {
			filtered := make(map[string]string)
			for k, v := range memories {
				if strings.Contains(strings.ToLower(k), search) ||
					strings.Contains(strings.ToLower(v), search) {
					filtered[k] = v
				}
			}
			memories = filtered
		}

		if jsonOutput {
			outputJSON(memories)
			return
		}

		if len(memories) == 0 {
			if search != "" {
				fmt.Printf("No memories matching %q\n", search)
			} else {
				fmt.Println("No memories stored. Use 'bd remember \"insight\"' to add one.")
			}
			return
		}

		// Sort keys for consistent output
		keys := make([]string, 0, len(memories))
		for k := range memories {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		if search != "" {
			fmt.Printf("Memories matching %q:\n\n", search)
		} else {
			fmt.Printf("Memories (%d):\n\n", len(memories))
		}
		for _, k := range keys {
			v := memories[k]
			fmt.Printf("  %s\n", k)
			// Indent the value, wrapping long lines
			fmt.Printf("    %s\n\n", truncateMemory(v, 120))
		}
	},
}

// forgetCmd removes a memory.
var forgetCmd = &cobra.Command{
	Use:   "forget <key>",
	Short: "Remove a persistent memory",
	Long: `Remove a memory by its key.

Use 'bd memories' to see available keys.

Examples:
  bd forget dolt-phantoms
  bd forget auth-jwt`,
	GroupID: "setup",
	Args:    cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("forget")

		if err := ensureDirectMode("forget requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		key := args[0]
		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx

		// Check if it exists first
		existing, _ := store.GetConfig(ctx, storageKey)
		if existing == "" {
			if jsonOutput {
				outputJSON(map[string]string{
					"key":   key,
					"found": "false",
				})
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "No memory with key %q\n", key)
			os.Exit(1)
		}

		if err := store.DeleteConfig(ctx, storageKey); err != nil {
			FatalErrorRespectJSON("forgetting memory: %v", err)
		}

		if jsonOutput {
			outputJSON(map[string]string{
				"key":     key,
				"deleted": "true",
			})
		} else {
			fmt.Printf("Forgot [%s]: %s\n", key, truncateMemory(existing, 80))
		}
	},
}

// recallCmd retrieves a specific memory by key.
var recallCmd = &cobra.Command{
	Use:   "recall <key>",
	Short: "Retrieve a specific memory",
	Long: `Retrieve the full content of a memory by its key.

Examples:
  bd recall dolt-phantoms
  bd recall auth-jwt`,
	GroupID: "setup",
	Args:    cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureDirectMode("recall requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		key := args[0]
		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx
		value, err := store.GetConfig(ctx, storageKey)
		if err != nil {
			FatalErrorRespectJSON("recalling memory: %v", err)
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
				fmt.Fprintf(os.Stderr, "No memory with key %q\n", key)
				os.Exit(1)
			}
			fmt.Printf("%s\n", value)
		}
	},
}

// truncateMemory shortens a string to maxLen for display.
func truncateMemory(s string, maxLen int) string {
	// Replace newlines with spaces for single-line display
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func init() {
	rememberCmd.Flags().StringVar(&memoryKeyFlag, "key", "", "Explicit key for the memory (auto-generated from content if not set)")

	rootCmd.AddCommand(rememberCmd)
	rootCmd.AddCommand(memoriesCmd)
	rootCmd.AddCommand(forgetCmd)
	rootCmd.AddCommand(recallCmd)
}
