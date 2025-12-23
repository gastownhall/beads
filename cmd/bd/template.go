package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// BeadsTemplateLabel is the label used to identify Beads-based templates
const BeadsTemplateLabel = "template"

// variablePattern matches {{variable}} placeholders
var variablePattern = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

// TemplateSubgraph holds a template epic and all its descendants
type TemplateSubgraph struct {
	Root         *types.Issue            // The template epic
	Issues       []*types.Issue          // All issues in the subgraph (including root)
	Dependencies []*types.Dependency     // All dependencies within the subgraph
	IssueMap     map[string]*types.Issue // ID -> Issue for quick lookup
}

// InstantiateResult holds the result of template instantiation
type InstantiateResult struct {
	NewEpicID string            `json:"new_epic_id"`
	IDMapping map[string]string `json:"id_mapping"` // old ID -> new ID
	Created   int               `json:"created"`    // number of issues created
}

// CloneOptions controls how the subgraph is cloned during spawn/bond
type CloneOptions struct {
	Vars     map[string]string // Variable substitutions for {{key}} placeholders
	Assignee string            // Assign the root epic to this agent/user
	Actor    string            // Actor performing the operation
	Wisp     bool              // If true, spawned issues are marked for bulk deletion

	// Dynamic bonding fields (for Christmas Ornament pattern)
	ParentID string // Parent molecule ID to bond under (e.g., "patrol-x7k")
	ChildRef string // Child reference with variables (e.g., "arm-{{polecat_name}}")
}

// bondedIDPattern validates bonded IDs (alphanumeric, dash, underscore, dot)
var bondedIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

var templateCmd = &cobra.Command{
	Use:        "template",
	GroupID:    "setup",
	Short:      "Manage issue templates",
	Deprecated: "use 'bd mol' instead (mol catalog, mol show, mol bond)",
	Long: `Manage Beads templates for creating issue hierarchies.

Templates are epics with the "template" label. They can have child issues
with {{variable}} placeholders that get substituted during instantiation.

To create a template:
  1. Create an epic with child issues
  2. Add the 'template' label: bd label add <epic-id> template
  3. Use {{variable}} placeholders in titles/descriptions

To use a template:
  bd template instantiate <id> --var key=value`,
}

var templateListCmd = &cobra.Command{
	Use:        "list",
	Short:      "List available templates",
	Deprecated: "use 'bd mol catalog' instead",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		var beadsTemplates []*types.Issue

		if daemonClient != nil {
			resp, err := daemonClient.List(&rpc.ListArgs{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading templates: %v\n", err)
				os.Exit(1)
			}
			var allIssues []*types.Issue
			if err := json.Unmarshal(resp.Data, &allIssues); err == nil {
				for _, issue := range allIssues {
					for _, label := range issue.Labels {
						if label == BeadsTemplateLabel {
							beadsTemplates = append(beadsTemplates, issue)
							break
						}
					}
				}
			}
		} else if store != nil {
			var err error
			beadsTemplates, err = store.GetIssuesByLabel(ctx, BeadsTemplateLabel)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading templates: %v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(beadsTemplates)
			return
		}

		// Human-readable output
		if len(beadsTemplates) == 0 {
			fmt.Println("No templates available.")
			fmt.Println("\nTo create a template:")
			fmt.Println("  1. Create an epic with child issues")
			fmt.Println("  2. Add the 'template' label: bd label add <epic-id> template")
			fmt.Println("  3. Use {{variable}} placeholders in titles/descriptions")
			return
		}

		fmt.Printf("%s\n", ui.RenderPass("Templates (for bd template instantiate):"))
		for _, tmpl := range beadsTemplates {
			vars := extractVariables(tmpl.Title + " " + tmpl.Description)
			varStr := ""
			if len(vars) > 0 {
				varStr = fmt.Sprintf(" (vars: %s)", strings.Join(vars, ", "))
			}
			fmt.Printf("  %s: %s%s\n", ui.RenderAccent(tmpl.ID), tmpl.Title, varStr)
		}
		fmt.Println()
	},
}

var templateShowCmd = &cobra.Command{
	Use:        "show <template-id>",
	Short:      "Show template details",
	Deprecated: "use 'bd mol show' instead",
	Args:       cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		var templateID string

		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: template '%s' not found\n", args[0])
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &templateID); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else if store != nil {
			var err error
			templateID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: template '%s' not found\n", args[0])
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			os.Exit(1)
		}

		// Load and show Beads template
		subgraph, err := loadTemplateSubgraph(ctx, store, templateID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading template: %v\n", err)
			os.Exit(1)
		}

		showBeadsTemplate(subgraph)
	},
}

func showBeadsTemplate(subgraph *TemplateSubgraph) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"root":         subgraph.Root,
			"issues":       subgraph.Issues,
			"dependencies": subgraph.Dependencies,
			"variables":    extractAllVariables(subgraph),
		})
		return
	}

	fmt.Printf("\n%s Template: %s\n", ui.RenderAccent("📋"), subgraph.Root.Title)
	fmt.Printf("   ID: %s\n", subgraph.Root.ID)
	fmt.Printf("   Issues: %d\n", len(subgraph.Issues))

	// Show variables
	vars := extractAllVariables(subgraph)
	if len(vars) > 0 {
		fmt.Printf("\n%s Variables:\n", ui.RenderWarn("📝"))
		for _, v := range vars {
			fmt.Printf("   {{%s}}\n", v)
		}
	}

	// Show structure
	fmt.Printf("\n%s Structure:\n", ui.RenderPass("🌲"))
	printTemplateTree(subgraph, subgraph.Root.ID, 0, true)
	fmt.Println()
}

var templateInstantiateCmd = &cobra.Command{
	Use:        "instantiate <template-id>",
	Short:      "Create issues from a Beads template",
	Deprecated: "use 'bd mol bond' instead",
	Long: `Instantiate a Beads template by cloning its subgraph and substituting variables.

Variables are specified with --var key=value flags. The template's {{key}}
placeholders will be replaced with the corresponding values.

Example:
  bd template instantiate bd-abc123 --var version=1.2.0 --var date=2024-01-15`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("template instantiate")

		ctx := rootCtx
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		varFlags, _ := cmd.Flags().GetStringSlice("var")
		assignee, _ := cmd.Flags().GetString("assignee")

		// Parse variables
		vars := make(map[string]string)
		for _, v := range varFlags {
			parts := strings.SplitN(v, "=", 2)
			if len(parts) != 2 {
				fmt.Fprintf(os.Stderr, "Error: invalid variable format '%s', expected 'key=value'\n", v)
				os.Exit(1)
			}
			vars[parts[0]] = parts[1]
		}

		// Resolve template ID
		var templateID string
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving template ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &templateID); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else if store != nil {
			var err error
			templateID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving template ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			os.Exit(1)
		}

		// Load the template subgraph
		subgraph, err := loadTemplateSubgraph(ctx, store, templateID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading template: %v\n", err)
			os.Exit(1)
		}

		// Check for missing variables
		requiredVars := extractAllVariables(subgraph)
		var missingVars []string
		for _, v := range requiredVars {
			if _, ok := vars[v]; !ok {
				missingVars = append(missingVars, v)
			}
		}
		if len(missingVars) > 0 {
			fmt.Fprintf(os.Stderr, "Error: missing required variables: %s\n", strings.Join(missingVars, ", "))
			fmt.Fprintf(os.Stderr, "Provide them with: --var %s=<value>\n", missingVars[0])
			os.Exit(1)
		}

		if dryRun {
			// Preview what would be created
			fmt.Printf("\nDry run: would create %d issues from template %s\n\n", len(subgraph.Issues), templateID)
			for _, issue := range subgraph.Issues {
				newTitle := substituteVariables(issue.Title, vars)
				suffix := ""
				if issue.ID == subgraph.Root.ID && assignee != "" {
					suffix = fmt.Sprintf(" (assignee: %s)", assignee)
				}
				fmt.Printf("  - %s (from %s)%s\n", newTitle, issue.ID, suffix)
			}
			if len(vars) > 0 {
				fmt.Printf("\nVariables:\n")
				for k, v := range vars {
					fmt.Printf("  {{%s}} = %s\n", k, v)
				}
			}
			return
		}

		// Clone the subgraph (deprecated command, non-wisp for backwards compatibility)
		opts := CloneOptions{
			Vars:     vars,
			Assignee: assignee,
			Actor:    actor,
			Wisp:     false,
		}
		result, err := cloneSubgraph(ctx, store, subgraph, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error instantiating template: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(result)
			return
		}

		fmt.Printf("%s Created %d issues from template\n", ui.RenderPass("✓"), result.Created)
		fmt.Printf("  New epic: %s\n", result.NewEpicID)
	},
}

func init() {
	templateInstantiateCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value)")
	templateInstantiateCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	templateInstantiateCmd.Flags().String("assignee", "", "Assign the root epic to this agent/user")

	templateCmd.AddCommand(templateListCmd)
	templateCmd.AddCommand(templateShowCmd)
	templateCmd.AddCommand(templateInstantiateCmd)
	rootCmd.AddCommand(templateCmd)
}

// =============================================================================
// Beads Template Functions
// =============================================================================

// loadTemplateSubgraph loads a template epic and all its descendants
func loadTemplateSubgraph(ctx context.Context, s storage.Storage, templateID string) (*TemplateSubgraph, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Get the root issue
	root, err := s.GetIssue(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("template %s not found", templateID)
	}

	subgraph := &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}

	// Recursively load all children
	if err := loadDescendants(ctx, s, subgraph, root.ID); err != nil {
		return nil, err
	}

	// Load all dependencies within the subgraph
	for _, issue := range subgraph.Issues {
		deps, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get dependencies for %s: %w", issue.ID, err)
		}
		for _, dep := range deps {
			// Only include dependencies where both ends are in the subgraph
			if _, ok := subgraph.IssueMap[dep.DependsOnID]; ok {
				subgraph.Dependencies = append(subgraph.Dependencies, dep)
			}
		}
	}

	return subgraph, nil
}

// loadDescendants recursively loads all child issues
func loadDescendants(ctx context.Context, s storage.Storage, subgraph *TemplateSubgraph, parentID string) error {
	// GetDependents returns issues that depend on parentID
	dependents, err := s.GetDependents(ctx, parentID)
	if err != nil {
		return fmt.Errorf("failed to get dependents of %s: %w", parentID, err)
	}

	// Check each dependent to see if it's a child (has parent-child relationship)
	for _, dependent := range dependents {
		if _, exists := subgraph.IssueMap[dependent.ID]; exists {
			continue // Already in subgraph
		}

		// Check if this dependent has a parent-child relationship with parentID
		depRecs, err := s.GetDependencyRecords(ctx, dependent.ID)
		if err != nil {
			continue
		}

		isChild := false
		for _, depRec := range depRecs {
			if depRec.DependsOnID == parentID && depRec.Type == types.DepParentChild {
				isChild = true
				break
			}
		}

		if !isChild {
			continue
		}

		// Add to subgraph
		subgraph.Issues = append(subgraph.Issues, dependent)
		subgraph.IssueMap[dependent.ID] = dependent

		// Recurse to get children of this child
		if err := loadDescendants(ctx, s, subgraph, dependent.ID); err != nil {
			return err
		}
	}

	return nil
}

// extractVariables finds all {{variable}} patterns in text
func extractVariables(text string) []string {
	matches := variablePattern.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var vars []string
	for _, match := range matches {
		if len(match) >= 2 && !seen[match[1]] {
			vars = append(vars, match[1])
			seen[match[1]] = true
		}
	}
	return vars
}

// extractAllVariables finds all variables across the entire subgraph
func extractAllVariables(subgraph *TemplateSubgraph) []string {
	allText := ""
	for _, issue := range subgraph.Issues {
		allText += issue.Title + " " + issue.Description + " "
		allText += issue.Design + " " + issue.AcceptanceCriteria + " " + issue.Notes + " "
	}
	return extractVariables(allText)
}

// substituteVariables replaces {{variable}} with values
func substituteVariables(text string, vars map[string]string) string {
	return variablePattern.ReplaceAllStringFunc(text, func(match string) string {
		// Extract variable name from {{name}}
		name := match[2 : len(match)-2]
		if val, ok := vars[name]; ok {
			return val
		}
		return match // Leave unchanged if not found
	})
}

// generateBondedID creates a custom ID for dynamically bonded molecules.
// When bonding a proto to a parent molecule, this generates IDs like:
//   - Root: parent.childref (e.g., "patrol-x7k.arm-ace")
//   - Children: parent.childref.step (e.g., "patrol-x7k.arm-ace.capture")
//
// The childRef is variable-substituted before use.
// Returns empty string if not a bonded operation (opts.ParentID empty).
func generateBondedID(oldID string, rootID string, opts CloneOptions) (string, error) {
	if opts.ParentID == "" {
		return "", nil // Not a bonded operation
	}

	// Substitute variables in childRef
	childRef := substituteVariables(opts.ChildRef, opts.Vars)

	// Validate childRef after substitution
	if childRef == "" {
		return "", fmt.Errorf("childRef is empty after variable substitution")
	}
	if !bondedIDPattern.MatchString(childRef) {
		return "", fmt.Errorf("invalid childRef '%s': must be alphanumeric, dash, underscore, or dot only", childRef)
	}

	if oldID == rootID {
		// Root issue: parent.childref
		newID := fmt.Sprintf("%s.%s", opts.ParentID, childRef)
		return newID, nil
	}

	// Child issue: parent.childref.relative
	// Extract the relative portion of the old ID (part after root)
	relativeID := getRelativeID(oldID, rootID)
	if relativeID == "" {
		// No hierarchical relationship - use a suffix from the old ID to ensure uniqueness.
		// Extract the last part of the old ID (after any prefix or dash)
		suffix := extractIDSuffix(oldID)
		newID := fmt.Sprintf("%s.%s.%s", opts.ParentID, childRef, suffix)
		return newID, nil
	}

	newID := fmt.Sprintf("%s.%s.%s", opts.ParentID, childRef, relativeID)
	return newID, nil
}

// extractIDSuffix extracts a suffix from an ID for use when IDs aren't hierarchical.
// For "patrol-abc123", returns "abc123".
// For "bd-xyz.1", returns "1".
// This ensures child IDs remain unique when bonding.
func extractIDSuffix(id string) string {
	// First try to get the part after the last dot (for hierarchical IDs)
	if lastDot := strings.LastIndex(id, "."); lastDot >= 0 {
		return id[lastDot+1:]
	}
	// Otherwise, get the part after the last dash (for prefix-hash IDs)
	if lastDash := strings.LastIndex(id, "-"); lastDash >= 0 {
		return id[lastDash+1:]
	}
	// Fallback: use the whole ID
	return id
}

// getRelativeID extracts the relative portion of a child ID from its parent.
// For example: getRelativeID("bd-abc.step1.sub", "bd-abc") returns "step1.sub"
// Returns empty string if oldID equals rootID or doesn't start with rootID.
func getRelativeID(oldID, rootID string) string {
	if oldID == rootID {
		return ""
	}
	// Check if oldID starts with rootID followed by a dot
	prefix := rootID + "."
	if strings.HasPrefix(oldID, prefix) {
		return oldID[len(prefix):]
	}
	return ""
}

// cloneSubgraph creates new issues from the template with variable substitution.
// Uses CloneOptions to control all spawn/bond behavior including dynamic bonding.
func cloneSubgraph(ctx context.Context, s storage.Storage, subgraph *TemplateSubgraph, opts CloneOptions) (*InstantiateResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Generate new IDs and create mapping
	idMapping := make(map[string]string)

	// Use transaction for atomicity
	err := s.RunInTransaction(ctx, func(tx storage.Transaction) error {
		// First pass: create all issues with new IDs
		for _, oldIssue := range subgraph.Issues {
			// Determine assignee: use override for root epic, otherwise keep template's
			issueAssignee := oldIssue.Assignee
			if oldIssue.ID == subgraph.Root.ID && opts.Assignee != "" {
				issueAssignee = opts.Assignee
			}

			newIssue := &types.Issue{
				// ID will be set below based on bonding options
				Title:              substituteVariables(oldIssue.Title, opts.Vars),
				Description:        substituteVariables(oldIssue.Description, opts.Vars),
				Design:             substituteVariables(oldIssue.Design, opts.Vars),
				AcceptanceCriteria: substituteVariables(oldIssue.AcceptanceCriteria, opts.Vars),
				Notes:              substituteVariables(oldIssue.Notes, opts.Vars),
				Status:             types.StatusOpen, // Always start fresh
				Priority:           oldIssue.Priority,
				IssueType:          oldIssue.IssueType,
				Assignee:           issueAssignee,
				EstimatedMinutes:   oldIssue.EstimatedMinutes,
				Wisp:               opts.Wisp, // bd-2vh3: mark for cleanup when closed
				CreatedAt:          time.Now(),
				UpdatedAt:          time.Now(),
			}

			// Generate custom ID for dynamic bonding if ParentID is set
			if opts.ParentID != "" {
				bondedID, err := generateBondedID(oldIssue.ID, subgraph.Root.ID, opts)
				if err != nil {
					return fmt.Errorf("failed to generate bonded ID for %s: %w", oldIssue.ID, err)
				}
				newIssue.ID = bondedID
			}

			if err := tx.CreateIssue(ctx, newIssue, opts.Actor); err != nil {
				return fmt.Errorf("failed to create issue from %s: %w", oldIssue.ID, err)
			}

			idMapping[oldIssue.ID] = newIssue.ID
		}

		// Second pass: recreate dependencies with new IDs
		for _, dep := range subgraph.Dependencies {
			newFromID, ok1 := idMapping[dep.IssueID]
			newToID, ok2 := idMapping[dep.DependsOnID]
			if !ok1 || !ok2 {
				continue // Skip if either end is outside the subgraph
			}

			newDep := &types.Dependency{
				IssueID:     newFromID,
				DependsOnID: newToID,
				Type:        dep.Type,
			}
			if err := tx.AddDependency(ctx, newDep, opts.Actor); err != nil {
				return fmt.Errorf("failed to create dependency: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &InstantiateResult{
		NewEpicID: idMapping[subgraph.Root.ID],
		IDMapping: idMapping,
		Created:   len(subgraph.Issues),
	}, nil
}

// printTemplateTree prints the template structure as a tree
func printTemplateTree(subgraph *TemplateSubgraph, parentID string, depth int, isRoot bool) {
	indent := strings.Repeat("  ", depth)

	// Print root
	if isRoot {
		fmt.Printf("%s   %s (root)\n", indent, subgraph.Root.Title)
	}

	// Find children of this parent
	var children []*types.Issue
	for _, dep := range subgraph.Dependencies {
		if dep.DependsOnID == parentID && dep.Type == types.DepParentChild {
			if child, ok := subgraph.IssueMap[dep.IssueID]; ok {
				children = append(children, child)
			}
		}
	}

	// Print children
	for i, child := range children {
		connector := "├──"
		if i == len(children)-1 {
			connector = "└──"
		}
		vars := extractVariables(child.Title)
		varStr := ""
		if len(vars) > 0 {
			varStr = fmt.Sprintf(" [%s]", strings.Join(vars, ", "))
		}
		fmt.Printf("%s   %s %s%s\n", indent, connector, child.Title, varStr)
		printTemplateTree(subgraph, child.ID, depth+1, false)
	}
}
