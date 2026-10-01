package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/issueops"
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

// systemTypeDescriptions describes the built-in types that are not core work
// types. bd's own commands create them (mail, molecules, gates, set-state), and
// like the core types they validate without any types.custom entry.
var systemTypeDescriptions = map[types.IssueType]string{
	types.TypeMessage:  "Message between agents or users",
	types.TypeMolecule: "Molecule root for swarm coordination (bd mol)",
	types.TypeGate:     "Async coordination gate (bd gate, formula gates)",
	types.TypeEvent:    "Audit-trail event (bd set-state)",
}

// systemWorkTypes lists every built-in type that is not a core work type, in
// the types package's declaration order. Together with coreWorkTypes it covers
// exactly the types IssueType.IsBuiltIn accepts.
func systemWorkTypes() []typeInfo {
	core := make(map[types.IssueType]bool, len(coreWorkTypes))
	for _, t := range coreWorkTypes {
		core[t.Type] = true
	}
	var out []typeInfo
	for _, t := range append(append([]types.IssueType{}, types.AllIssueTypes...), types.TypeEvent) {
		if core[t] {
			continue
		}
		out = append(out, typeInfo{Name: string(t), Description: systemTypeDescriptions[t]})
	}
	return out
}

var typesCmd = &cobra.Command{
	Use:     "types",
	GroupID: "views",
	Short:   "List valid issue types",
	Long: `List all valid issue types that can be used with bd create --type.

Core work types (bug, task, feature, chore, epic, decision, spike, story, milestone)
and system types (message, molecule, gate, event) are always valid.
Custom types are registered with 'bd config set types.custom' or declared under
types.custom in .beads/config.yaml; the list shown is exactly the set that
bd create --type accepts.

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

		if !usesProxiedServer() {
			if err := ensureDirectMode("types command requires direct database access"); err != nil {
				return HandleError("%v", err)
			}
		}

		customTypes, err := resolveWorkspaceCustomTypes(rootCtx)
		if err != nil {
			return HandleError("%v", err)
		}
		return renderTypes(customTypes)
	},
}

// resolveWorkspaceCustomTypes returns the workspace's registered custom issue
// types in either storage mode. Both routes end in issueops.ComposeCustomTypes,
// the rule issue create/update validation also resolves through, so what
// `bd types` lists is exactly what `bd create --type` accepts.
func resolveWorkspaceCustomTypes(ctx context.Context) ([]string, error) {
	if usesProxiedServer() {
		uw, err := openProxiedListUOW(ctx)
		if err != nil {
			return nil, err
		}
		defer uw.Close(ctx)
		customTypes, err := uw.ConfigUseCase().GetCustomTypes(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading custom types: %w", err)
		}
		return customTypes, nil
	}
	if store == nil {
		// No database is open (e.g. a --dry-run plan check): the config.yaml
		// layer is the only one available, composed by the same rule.
		return issueops.ComposeCustomTypes(nil, "", config.GetCustomTypesFromYAML()), nil
	}
	customTypes, err := store.GetCustomTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading custom types: %w", err)
	}
	return customTypes, nil
}

func renderTypes(customTypes []string) error {
	if jsonOutput {
		result := struct {
			CoreTypes   []typeInfo `json:"core_types"`
			SystemTypes []typeInfo `json:"system_types"`
			CustomTypes []string   `json:"custom_types,omitempty"`
		}{SystemTypes: systemWorkTypes()}

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

	fmt.Println("\nSystem types (built-in, created by bd commands):")
	for _, t := range systemWorkTypes() {
		fmt.Printf("  %-14s %s\n", t.Name, t.Description)
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
