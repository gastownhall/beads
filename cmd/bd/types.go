package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

// AllIssueTypes returns all valid built-in issue types with descriptions.
// Ordered by typical usage frequency: work types first, then system types.
var allIssueTypes = []struct {
	Type        types.IssueType
	Description string
}{
	// Work types (common user-facing types)
	{types.TypeTask, "General work item (default)"},
	{types.TypeBug, "Bug report or defect"},
	{types.TypeFeature, "New feature or enhancement"},
	{types.TypeChore, "Maintenance or housekeeping"},
	{types.TypeEpic, "Large body of work spanning multiple issues"},

	// System types (used by tooling)
	{types.TypeMolecule, "Template for issue hierarchies"},
	{types.TypeGate, "Async coordination gate"},
	{types.TypeConvoy, "Cross-project tracking with reactive completion"},
	{types.TypeMergeRequest, "Merge queue entry for refinery processing"},
	{types.TypeSlot, "Exclusive access slot (merge-slot gate)"},

	// Agent types (Gas Town infrastructure)
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

Types are organized into categories:
- Work types: Common types for tracking work (task, bug, feature, etc.)
- System types: Used by beads tooling (molecule, gate, convoy, etc.)
- Agent types: Used by Gas Town agent infrastructure

Examples:
  bd types              # List all types with descriptions
  bd types --json       # Output as JSON
`,
	Run: func(cmd *cobra.Command, args []string) {
		if jsonOutput {
			result := struct {
				Types []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"types"`
			}{}

			for _, t := range allIssueTypes {
				result.Types = append(result.Types, struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				}{
					Name:        string(t.Type),
					Description: t.Description,
				})
			}
			outputJSON(result)
			return
		}

		// Text output with categories
		fmt.Println("Work types:")
		for _, t := range allIssueTypes[:5] {
			fmt.Printf("  %-14s %s\n", t.Type, t.Description)
		}

		fmt.Println("\nSystem types:")
		for _, t := range allIssueTypes[5:10] {
			fmt.Printf("  %-14s %s\n", t.Type, t.Description)
		}

		fmt.Println("\nAgent types:")
		for _, t := range allIssueTypes[10:] {
			fmt.Printf("  %-14s %s\n", t.Type, t.Description)
		}
	},
}

func init() {
	rootCmd.AddCommand(typesCmd)
}
