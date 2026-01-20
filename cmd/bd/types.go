package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

// coreWorkTypes are the built-in types that beads validates without configuration.
var coreWorkTypes = []struct {
	Type        types.IssueType
	Description string
}{
	{types.TypeTask, "General work item (default)"},
	{types.TypeBug, "Bug report or defect"},
	{types.TypeFeature, "New feature or enhancement"},
	{types.TypeChore, "Maintenance or housekeeping"},
	{types.TypeEpic, "Large body of work spanning multiple issues"},
}

// wellKnownCustomTypes are commonly used types that require types.custom configuration.
// These are used by Gas Town and other infrastructure that extends beads.
var wellKnownCustomTypes = []struct {
	Type        types.IssueType
	Description string
}{
	{types.TypeMolecule, "Template for issue hierarchies"},
	{types.TypeGate, "Async coordination gate"},
	{types.TypeConvoy, "Cross-project tracking with reactive completion"},
	{types.TypeMergeRequest, "Merge queue entry for refinery processing"},
	{types.TypeSlot, "Exclusive access slot (merge-slot gate)"},
	{types.TypeAgent, "Agent identity bead"},
	{types.TypeRole, "Agent role definition"},
	{types.TypeRig, "Rig identity bead (multi-repo workspace)"},
	{types.TypeEvent, "Operational state change record"},
	{types.TypeMessage, "Ephemeral communication between workers"},
}

var typesCmd = &cobra.Command{
	Use:     "types",
	GroupID: "views",
	Short:   "List valid issue types",
	Long: `List all valid issue types that can be used with bd create --type.

Core work types (bug, task, feature, chore, epic) are always valid.
Additional types require configuration via types.custom in .beads/config.yaml.

Examples:
  bd types              # List all types with descriptions
  bd types --json       # Output as JSON
`,
	Run: func(cmd *cobra.Command, args []string) {
		// Ensure direct mode for database access (types command needs to read config).
		// In daemon mode, store is nil so custom types would never be fetched.
		if err := ensureDirectMode("types command requires direct database access"); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}

		// Get custom types from config
		var customTypes []string
		ctx := context.Background()
		if store != nil {
			if ct, err := store.GetCustomTypes(ctx); err == nil {
				customTypes = ct
			}
		}

		if jsonOutput {
			result := struct {
				CoreTypes   []typeInfo `json:"core_types"`
				CustomTypes []string   `json:"custom_types,omitempty"`
			}{}

			for _, t := range coreWorkTypes {
				result.CoreTypes = append(result.CoreTypes, typeInfo{
					Name:        string(t.Type),
					Description: t.Description,
				})
			}
			result.CustomTypes = customTypes
			outputJSON(result)
			return
		}

		// Text output
		fmt.Println("Core work types (built-in):")
		for _, t := range coreWorkTypes {
			fmt.Printf("  %-14s %s\n", t.Type, t.Description)
		}

		if len(customTypes) > 0 {
			fmt.Println("\nConfigured custom types:")
			for _, t := range customTypes {
				// Check if it's a well-known type and show description
				desc := ""
				for _, wk := range wellKnownCustomTypes {
					if string(wk.Type) == t {
						desc = wk.Description
						break
					}
				}
				if desc != "" {
					fmt.Printf("  %-14s %s\n", t, desc)
				} else {
					fmt.Printf("  %s\n", t)
				}
			}
		} else {
			fmt.Println("\nNo custom types configured.")
			fmt.Println("Configure with: bd config set types.custom \"type1,type2,...\"")
		}

		// Show hint about well-known types if none are configured
		if len(customTypes) == 0 {
			fmt.Println("\nWell-known custom types (used by Gas Town):")
			var typeNames []string
			for _, t := range wellKnownCustomTypes {
				typeNames = append(typeNames, string(t.Type))
			}
			fmt.Printf("  %s\n", strings.Join(typeNames, ", "))
		}
	},
}

type typeInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func init() {
	rootCmd.AddCommand(typesCmd)
}
