package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"gopkg.in/yaml.v3"
)

//go:embed templates/*.yaml
var builtinTemplates embed.FS

// Template represents a simple YAML issue template (for --from-template)
type Template struct {
	Name               string   `yaml:"name" json:"name"`
	Description        string   `yaml:"description" json:"description"`
	Type               string   `yaml:"type" json:"type"`
	Priority           int      `yaml:"priority" json:"priority"`
	Labels             []string `yaml:"labels" json:"labels"`
	Design             string   `yaml:"design" json:"design"`
	AcceptanceCriteria string   `yaml:"acceptance_criteria" json:"acceptance_criteria"`
}

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

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage issue templates",
	Long: `Manage issue templates for streamlined issue creation.

There are two types of templates:

1. YAML Templates (for single issues):
   - Built-in: epic, bug, feature
   - Custom: stored in .beads/templates/
   - Used with: bd create --from-template=<name>

2. Beads Templates (for issue hierarchies):
   - Any epic with the "template" label
   - Can have child issues with {{variable}} placeholders
   - Used with: bd template instantiate <id> --var key=value`,
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available templates",
	Run: func(cmd *cobra.Command, args []string) {
		yamlOnly, _ := cmd.Flags().GetBool("yaml-only")
		beadsOnly, _ := cmd.Flags().GetBool("beads-only")

		type combinedOutput struct {
			YAMLTemplates  []Template      `json:"yaml_templates,omitempty"`
			BeadsTemplates []*types.Issue  `json:"beads_templates,omitempty"`
		}
		output := combinedOutput{}

		// Load YAML templates
		if !beadsOnly {
			templates, err := loadAllTemplates()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: error loading YAML templates: %v\n", err)
			} else {
				output.YAMLTemplates = templates
			}
		}

		// Load Beads templates
		if !yamlOnly {
			ctx := rootCtx
			var beadsTemplates []*types.Issue
			var err error

			if daemonClient != nil {
				resp, err := daemonClient.List(&rpc.ListArgs{})
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: error loading Beads templates: %v\n", err)
				} else {
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
				}
			} else if store != nil {
				beadsTemplates, err = store.GetIssuesByLabel(ctx, BeadsTemplateLabel)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: error loading Beads templates: %v\n", err)
				}
			}
			output.BeadsTemplates = beadsTemplates
		}

		if jsonOutput {
			outputJSON(output)
			return
		}

		// Human-readable output
		green := color.New(color.FgGreen).SprintFunc()
		blue := color.New(color.FgBlue).SprintFunc()
		cyan := color.New(color.FgCyan).SprintFunc()

		// Show YAML templates
		if !beadsOnly && len(output.YAMLTemplates) > 0 {
			// Group by source
			builtins := []Template{}
			customs := []Template{}
			for _, tmpl := range output.YAMLTemplates {
				if isBuiltinTemplate(tmpl.Name) {
					builtins = append(builtins, tmpl)
				} else {
					customs = append(customs, tmpl)
				}
			}

			if len(builtins) > 0 {
				fmt.Printf("%s\n", green("Built-in Templates (for --from-template):"))
				for _, tmpl := range builtins {
					fmt.Printf("  %s\n", blue(tmpl.Name))
					fmt.Printf("    Type: %s, Priority: P%d\n", tmpl.Type, tmpl.Priority)
				}
				fmt.Println()
			}

			if len(customs) > 0 {
				fmt.Printf("%s\n", green("Custom Templates (for --from-template):"))
				for _, tmpl := range customs {
					fmt.Printf("  %s\n", blue(tmpl.Name))
					fmt.Printf("    Type: %s, Priority: P%d\n", tmpl.Type, tmpl.Priority)
				}
				fmt.Println()
			}
		}

		// Show Beads templates
		if !yamlOnly && len(output.BeadsTemplates) > 0 {
			fmt.Printf("%s\n", green("Beads Templates (for bd template instantiate):"))
			for _, tmpl := range output.BeadsTemplates {
				vars := extractVariables(tmpl.Title + " " + tmpl.Description)
				varStr := ""
				if len(vars) > 0 {
					varStr = fmt.Sprintf(" (vars: %s)", strings.Join(vars, ", "))
				}
				fmt.Printf("  %s: %s%s\n", cyan(tmpl.ID), tmpl.Title, varStr)
			}
			fmt.Println()
		}

		if len(output.YAMLTemplates) == 0 && len(output.BeadsTemplates) == 0 {
			fmt.Println("No templates available.")
			fmt.Println("\nTo create a Beads template:")
			fmt.Println("  1. Create an epic with child issues")
			fmt.Println("  2. Add the 'template' label: bd label add <epic-id> template")
			fmt.Println("  3. Use {{variable}} placeholders in titles/descriptions")
		}
	},
}

var templateShowCmd = &cobra.Command{
	Use:   "show <template-name-or-id>",
	Short: "Show template details",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		arg := args[0]

		// Try loading as YAML template first
		yamlTmpl, yamlErr := loadTemplate(arg)
		if yamlErr == nil {
			showYAMLTemplate(yamlTmpl)
			return
		}

		// Try loading as Beads template
		ctx := rootCtx
		var templateID string

		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: arg}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				// Neither YAML nor Beads template found
				fmt.Fprintf(os.Stderr, "Error: template '%s' not found\n", arg)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &templateID); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		} else if store != nil {
			var err error
			templateID, err = utils.ResolvePartialID(ctx, store, arg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: template '%s' not found\n", arg)
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

func showYAMLTemplate(tmpl *Template) {
	if jsonOutput {
		outputJSON(tmpl)
		return
	}

	green := color.New(color.FgGreen).SprintFunc()
	blue := color.New(color.FgBlue).SprintFunc()

	fmt.Printf("%s %s (YAML template)\n", green("Template:"), blue(tmpl.Name))
	fmt.Printf("Type: %s\n", tmpl.Type)
	fmt.Printf("Priority: P%d\n", tmpl.Priority)
	if len(tmpl.Labels) > 0 {
		fmt.Printf("Labels: %s\n", strings.Join(tmpl.Labels, ", "))
	}
	fmt.Printf("\n%s\n%s\n", green("Description:"), tmpl.Description)
	if tmpl.Design != "" {
		fmt.Printf("\n%s\n%s\n", green("Design:"), tmpl.Design)
	}
	if tmpl.AcceptanceCriteria != "" {
		fmt.Printf("\n%s\n%s\n", green("Acceptance Criteria:"), tmpl.AcceptanceCriteria)
	}
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

	cyan := color.New(color.FgCyan).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	green := color.New(color.FgGreen).SprintFunc()

	fmt.Printf("\n%s Template: %s (Beads template)\n", cyan("📋"), subgraph.Root.Title)
	fmt.Printf("   ID: %s\n", subgraph.Root.ID)
	fmt.Printf("   Issues: %d\n", len(subgraph.Issues))

	// Show variables
	vars := extractAllVariables(subgraph)
	if len(vars) > 0 {
		fmt.Printf("\n%s Variables:\n", yellow("📝"))
		for _, v := range vars {
			fmt.Printf("   {{%s}}\n", v)
		}
	}

	// Show structure
	fmt.Printf("\n%s Structure:\n", green("🌲"))
	printTemplateTree(subgraph, subgraph.Root.ID, 0, true)
	fmt.Println()
}

var templateCreateCmd = &cobra.Command{
	Use:   "create <template-name>",
	Short: "Create a custom YAML template",
	Long: `Create a custom YAML template in .beads/templates/ directory.

This creates a simple template for pre-filling issue fields.
For workflow templates with hierarchies, create an epic and add the 'template' label.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		templateName := args[0]

		// Sanitize template name
		if err := sanitizeTemplateName(templateName); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Ensure .beads/templates directory exists
		templatesDir := filepath.Join(".beads", "templates")
		if err := os.MkdirAll(templatesDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating templates directory: %v\n", err)
			os.Exit(1)
		}

		// Create template file
		templatePath := filepath.Join(templatesDir, templateName+".yaml")
		if _, err := os.Stat(templatePath); err == nil {
			fmt.Fprintf(os.Stderr, "Error: template '%s' already exists\n", templateName)
			os.Exit(1)
		}

		// Default template structure
		tmpl := Template{
			Name:               templateName,
			Description:        "[Describe the issue]\n\n## Additional Context\n\n[Add relevant details]",
			Type:               "task",
			Priority:           2,
			Labels:             []string{},
			Design:             "[Design notes]",
			AcceptanceCriteria: "- [ ] Acceptance criterion 1\n- [ ] Acceptance criterion 2",
		}

		// Marshal to YAML
		data, err := yaml.Marshal(tmpl)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating template: %v\n", err)
			os.Exit(1)
		}

		// Write template file
		if err := os.WriteFile(templatePath, data, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing template: %v\n", err)
			os.Exit(1)
		}

		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Created template: %s\n", green("✓"), templatePath)
		fmt.Printf("Edit the file to customize your template.\n")
	},
}

var templateInstantiateCmd = &cobra.Command{
	Use:   "instantiate <template-id>",
	Short: "Create issues from a Beads template",
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

		// Clone the subgraph
		result, err := cloneSubgraph(ctx, store, subgraph, vars, assignee, actor)
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

		green := color.New(color.FgGreen).SprintFunc()
		fmt.Printf("%s Created %d issues from template\n", green("✓"), result.Created)
		fmt.Printf("  New epic: %s\n", result.NewEpicID)
	},
}

func init() {
	templateListCmd.Flags().Bool("yaml-only", false, "Show only YAML templates")
	templateListCmd.Flags().Bool("beads-only", false, "Show only Beads templates")

	templateInstantiateCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value)")
	templateInstantiateCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	templateInstantiateCmd.Flags().String("assignee", "", "Assign the root epic to this agent/user")

	templateCmd.AddCommand(templateListCmd)
	templateCmd.AddCommand(templateShowCmd)
	templateCmd.AddCommand(templateCreateCmd)
	templateCmd.AddCommand(templateInstantiateCmd)
	rootCmd.AddCommand(templateCmd)
}

// =============================================================================
// YAML Template Functions (for --from-template)
// =============================================================================

// loadAllTemplates loads both built-in and custom YAML templates
func loadAllTemplates() ([]Template, error) {
	templates := []Template{}

	// Load built-in templates
	builtins := []string{"epic", "bug", "feature"}
	for _, name := range builtins {
		tmpl, err := loadBuiltinTemplate(name)
		if err != nil {
			continue
		}
		templates = append(templates, *tmpl)
	}

	// Load custom templates from .beads/templates/
	templatesDir := filepath.Join(".beads", "templates")
	if _, err := os.Stat(templatesDir); err == nil {
		entries, err := os.ReadDir(templatesDir)
		if err != nil {
			return nil, fmt.Errorf("reading templates directory: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}

			name := strings.TrimSuffix(entry.Name(), ".yaml")
			tmpl, err := loadCustomTemplate(name)
			if err != nil {
				continue
			}
			templates = append(templates, *tmpl)
		}
	}

	return templates, nil
}

// sanitizeTemplateName validates template name to prevent path traversal
func sanitizeTemplateName(name string) error {
	if name != filepath.Base(name) {
		return fmt.Errorf("invalid template name '%s' (no path separators allowed)", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid template name '%s' (no .. allowed)", name)
	}
	return nil
}

// loadTemplate loads a YAML template by name (checks custom first, then built-in)
func loadTemplate(name string) (*Template, error) {
	if err := sanitizeTemplateName(name); err != nil {
		return nil, err
	}

	// Try custom templates first
	tmpl, err := loadCustomTemplate(name)
	if err == nil {
		return tmpl, nil
	}

	// Fall back to built-in templates
	return loadBuiltinTemplate(name)
}

// loadBuiltinTemplate loads a built-in YAML template
func loadBuiltinTemplate(name string) (*Template, error) {
	path := fmt.Sprintf("templates/%s.yaml", name)
	data, err := builtinTemplates.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("template '%s' not found", name)
	}

	var tmpl Template
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	return &tmpl, nil
}

// loadCustomTemplate loads a custom YAML template from .beads/templates/
func loadCustomTemplate(name string) (*Template, error) {
	path := filepath.Join(".beads", "templates", name+".yaml")
	// #nosec G304 - path is sanitized via sanitizeTemplateName before calling this function
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("template '%s' not found", name)
	}

	var tmpl Template
	if err := yaml.Unmarshal(data, &tmpl); err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	return &tmpl, nil
}

// isBuiltinTemplate checks if a template name is a built-in template
func isBuiltinTemplate(name string) bool {
	builtins := map[string]bool{
		"epic":    true,
		"bug":     true,
		"feature": true,
	}
	return builtins[name]
}

// =============================================================================
// Beads Template Functions (for bd template instantiate)
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

// cloneSubgraph creates new issues from the template with variable substitution
// If assignee is non-empty, it will be set on the root epic
func cloneSubgraph(ctx context.Context, s storage.Storage, subgraph *TemplateSubgraph, vars map[string]string, assignee string, actorName string) (*InstantiateResult, error) {
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
			if oldIssue.ID == subgraph.Root.ID && assignee != "" {
				issueAssignee = assignee
			}

			newIssue := &types.Issue{
				// Don't set ID - let the system generate it
				Title:              substituteVariables(oldIssue.Title, vars),
				Description:        substituteVariables(oldIssue.Description, vars),
				Design:             substituteVariables(oldIssue.Design, vars),
				AcceptanceCriteria: substituteVariables(oldIssue.AcceptanceCriteria, vars),
				Notes:              substituteVariables(oldIssue.Notes, vars),
				Status:             types.StatusOpen, // Always start fresh
				Priority:           oldIssue.Priority,
				IssueType:          oldIssue.IssueType,
				Assignee:           issueAssignee,
				EstimatedMinutes:   oldIssue.EstimatedMinutes,
				CreatedAt:          time.Now(),
				UpdatedAt:          time.Now(),
			}

			if err := tx.CreateIssue(ctx, newIssue, actorName); err != nil {
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
			if err := tx.AddDependency(ctx, newDep, actorName); err != nil {
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
