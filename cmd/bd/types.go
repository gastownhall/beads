package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
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
	{types.TypeDecision, "Architecture decision record (ADR)"},
	{types.TypeSpike, "Timeboxed investigation to reduce uncertainty before committing to a story"},
	{types.TypeStory, "User story describing a feature from the user's perspective"},
	{types.TypeMilestone, "Marks completion of a set of related issues (contains no work itself)"},
}

var typesCmd = &cobra.Command{
	Use:     "types",
	GroupID: "views",
	Short:   "List valid issue types",
	Long: `List all valid issue types that can be used with bd create --type.

Core work types (bug, task, feature, chore, epic, decision, spike, story, milestone) are always valid.
Additional types require configuration via types.custom in .beads/config.yaml.

Examples:
  bd types              # List all types with descriptions
  bd types --sections   # List required sections for each type
  bd types --json       # Output as JSON
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("types")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		showSections, _ := cmd.Flags().GetBool("sections")
		if showSections {
			return printSections(jsonOutput)
		}

		if usesProxiedServer() {
			return runTypesProxiedServer(rootCtx)
		}

		if err := ensureDirectMode("types command requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		var customTypes []string
		ctx := context.Background()
		if store != nil {
			if ct, err := store.GetCustomTypes(ctx); err == nil {
				customTypes = ct
			}
		}

		return renderTypes(customTypes)
	},
}

func renderTypes(customTypes []string) error {
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
		return outputJSON(result)
	}

	fmt.Println("Core work types (built-in):")
	for _, t := range coreWorkTypes {
		fmt.Printf("  %-14s %s\n", t.Type, t.Description)
	}

	if len(customTypes) > 0 {
		fmt.Println("\nConfigured custom types:")
		for _, t := range customTypes {
			fmt.Printf("  %s\n", t)
		}
	} else {
		fmt.Println("\nNo custom types configured.")
		fmt.Println("Configure with: bd config set types.custom \"type1,type2,...\"")
	}
	return nil
}

// typeSectionsInfo holds section data for JSON output.
type typeSectionsInfo struct {
	Name     string   `json:"name"`
	Sections []string `json:"sections,omitempty"`
	Hint     string   `json:"hint,omitempty"`
}

type typeInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// printSections prints the required sections for each type.
func printSections(jsonOut bool) error {
	if jsonOut {
		var results []typeSectionsInfo
		for _, t := range coreWorkTypes {
			sections := t.Type.RequiredSections()
			if len(sections) == 0 {
				results = append(results, typeSectionsInfo{
					Name: string(t.Type),
					Hint: "no required sections",
				})
			} else {
				var names []string
				for _, s := range sections {
					names = append(names, s.Heading)
				}
				results = append(results, typeSectionsInfo{
					Name:     string(t.Type),
					Sections: names,
				})
			}
		}
		return outputJSON(results)
	}

	fmt.Println("Required sections by type:")
	for _, t := range coreWorkTypes {
		sections := t.Type.RequiredSections()
		if len(sections) == 0 {
			fmt.Printf("  %-14s %s\n", t.Type, "(none)")
		} else {
			var names []string
			for _, s := range sections {
				names = append(names, s.Heading)
			}
			fmt.Printf("  %-14s %s\n", t.Type, strings.Join(names, ", "))
		}
	}
	return nil
}

func init() {
	rootCmd.AddCommand(typesCmd)
	typesCmd.Flags().Bool("sections", false, "Show required sections for each issue type")
}
