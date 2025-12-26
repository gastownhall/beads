package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// createFormRawInput holds the raw string values from the form UI.
// This struct encapsulates all form fields before parsing/conversion.
type createFormRawInput struct {
	Title       string
	Description string
	IssueType   string
	Priority    string // String from select, e.g., "0", "1", "2"
	Assignee    string
	Labels      string // Comma-separated
	Design      string
	Acceptance  string
	ExternalRef string
	Deps        string // Comma-separated, format: "type:id" or "id"
}

// createFormValues holds the parsed values from the create-form input.
// This struct is used to pass form data to the issue creation logic,
// allowing the creation logic to be tested independently of the form UI.
type createFormValues struct {
	Title              string
	Description        string
	IssueType          string
	Priority           int
	Assignee           string
	Labels             []string
	Design             string
	AcceptanceCriteria string
	ExternalRef        string
	Dependencies       []string
}

// parseCreateFormInput parses raw form input into a createFormValues struct.
// It handles comma-separated labels and dependencies, and converts priority strings.
func parseCreateFormInput(raw *createFormRawInput) *createFormValues {
	// Parse priority
	priority, err := strconv.Atoi(raw.Priority)
	if err != nil {
		priority = 2 // Default to medium if parsing fails
	}

	// Parse labels
	var labels []string
	if raw.Labels != "" {
		for _, l := range strings.Split(raw.Labels, ",") {
			l = strings.TrimSpace(l)
			if l != "" {
				labels = append(labels, l)
			}
		}
	}

	// Parse dependencies
	var deps []string
	if raw.Deps != "" {
		for _, d := range strings.Split(raw.Deps, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				deps = append(deps, d)
			}
		}
	}

	return &createFormValues{
		Title:              raw.Title,
		Description:        raw.Description,
		IssueType:          raw.IssueType,
		Priority:           priority,
		Assignee:           raw.Assignee,
		Labels:             labels,
		Design:             raw.Design,
		AcceptanceCriteria: raw.Acceptance,
		ExternalRef:        raw.ExternalRef,
		Dependencies:       deps,
	}
}

// CreateIssueFromFormValues creates an issue from the given form values.
// It returns the created issue and any error that occurred.
// This function handles labels, dependencies, and source_repo inheritance.
func CreateIssueFromFormValues(ctx context.Context, s storage.Storage, fv *createFormValues, actor string) (*types.Issue, error) {
	var externalRefPtr *string
	if fv.ExternalRef != "" {
		externalRefPtr = &fv.ExternalRef
	}

	issue := &types.Issue{
		Title:              fv.Title,
		Description:        fv.Description,
		Design:             fv.Design,
		AcceptanceCriteria: fv.AcceptanceCriteria,
		Status:             types.StatusOpen,
		Priority:           fv.Priority,
		IssueType:          types.IssueType(fv.IssueType),
		Assignee:           fv.Assignee,
		ExternalRef:        externalRefPtr,
		CreatedBy:          getActorWithGit(), // GH#748: track who created the issue
	}

	// Check if any dependencies are discovered-from type
	// If so, inherit source_repo from the parent issue
	var discoveredFromParentID string
	for _, depSpec := range fv.Dependencies {
		depSpec = strings.TrimSpace(depSpec)
		if depSpec == "" {
			continue
		}

		if strings.Contains(depSpec, ":") {
			parts := strings.SplitN(depSpec, ":", 2)
			if len(parts) == 2 {
				depType := types.DependencyType(strings.TrimSpace(parts[0]))
				dependsOnID := strings.TrimSpace(parts[1])

				if depType == types.DepDiscoveredFrom && dependsOnID != "" {
					discoveredFromParentID = dependsOnID
					break
				}
			}
		}
	}

	// If we found a discovered-from dependency, inherit source_repo from parent
	if discoveredFromParentID != "" {
		parentIssue, err := s.GetIssue(ctx, discoveredFromParentID)
		if err == nil && parentIssue != nil && parentIssue.SourceRepo != "" {
			issue.SourceRepo = parentIssue.SourceRepo
		}
	}

	if err := s.CreateIssue(ctx, issue, actor); err != nil {
		return nil, fmt.Errorf("failed to create issue: %w", err)
	}

	// Add labels if specified
	for _, label := range fv.Labels {
		if err := s.AddLabel(ctx, issue.ID, label, actor); err != nil {
			// Log warning but don't fail the entire operation
			fmt.Fprintf(os.Stderr, "Warning: failed to add label %s: %v\n", label, err)
		}
	}

	// Add dependencies if specified
	for _, depSpec := range fv.Dependencies {
		depSpec = strings.TrimSpace(depSpec)
		if depSpec == "" {
			continue
		}

		var depType types.DependencyType
		var dependsOnID string

		if strings.Contains(depSpec, ":") {
			parts := strings.SplitN(depSpec, ":", 2)
			if len(parts) != 2 {
				fmt.Fprintf(os.Stderr, "Warning: invalid dependency format '%s', expected 'type:id' or 'id'\n", depSpec)
				continue
			}
			depType = types.DependencyType(strings.TrimSpace(parts[0]))
			dependsOnID = strings.TrimSpace(parts[1])
		} else {
			depType = types.DepBlocks
			dependsOnID = depSpec
		}

		if !depType.IsValid() {
			fmt.Fprintf(os.Stderr, "Warning: invalid dependency type '%s' (valid: blocks, related, parent-child, discovered-from)\n", depType)
			continue
		}

		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: dependsOnID,
			Type:        depType,
		}
		if err := s.AddDependency(ctx, dep, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add dependency %s -> %s: %v\n", issue.ID, dependsOnID, err)
		}
	}

	return issue, nil
}

var createFormCmd = &cobra.Command{
	Use:     "create-form",
	GroupID: "issues",
	Short:   "Create a new issue using an interactive form",
	Long: `Create a new issue using an interactive terminal form.

This command provides a user-friendly form interface for creating issues,
with fields for title, description, type, priority, labels, and more.

The form uses keyboard navigation:
  - Tab/Shift+Tab: Move between fields
  - Enter: Submit the form (on the last field or submit button)
  - Ctrl+C: Cancel and exit
  - Arrow keys: Navigate within select fields`,
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("create-form")
		runCreateForm(cmd)
	},
}

func runCreateForm(cmd *cobra.Command) {
	_ = cmd // cmd parameter required by cobra.Command.Run signature
	// Raw form input - will be populated by the form
	raw := &createFormRawInput{}

	// Issue type options
	typeOptions := []huh.Option[string]{
		huh.NewOption("Task", "task"),
		huh.NewOption("Bug", "bug"),
		huh.NewOption("Feature", "feature"),
		huh.NewOption("Epic", "epic"),
		huh.NewOption("Chore", "chore"),
	}

	// Priority options
	priorityOptions := []huh.Option[string]{
		huh.NewOption("P0 - Critical", "0"),
		huh.NewOption("P1 - High", "1"),
		huh.NewOption("P2 - Medium (default)", "2"),
		huh.NewOption("P3 - Low", "3"),
		huh.NewOption("P4 - Backlog", "4"),
	}

	// Build the form
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Title").
				Description("Brief summary of the issue (required)").
				Placeholder("e.g., Fix authentication bug in login handler").
				Value(&raw.Title).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("title is required")
					}
					if len(s) > 500 {
						return fmt.Errorf("title must be 500 characters or less")
					}
					return nil
				}),

			huh.NewText().
				Title("Description").
				Description("Detailed context about the issue").
				Placeholder("Explain why this issue exists and what needs to be done...").
				CharLimit(5000).
				Value(&raw.Description),

			huh.NewSelect[string]().
				Title("Type").
				Description("Categorize the kind of work").
				Options(typeOptions...).
				Value(&raw.IssueType),

			huh.NewSelect[string]().
				Title("Priority").
				Description("Set urgency level").
				Options(priorityOptions...).
				Value(&raw.Priority),
		),

		huh.NewGroup(
			huh.NewInput().
				Title("Assignee").
				Description("Who should work on this? (optional)").
				Placeholder("username or email").
				Value(&raw.Assignee),

			huh.NewInput().
				Title("Labels").
				Description("Comma-separated tags (optional)").
				Placeholder("e.g., urgent, backend, needs-review").
				Value(&raw.Labels),

			huh.NewInput().
				Title("External Reference").
				Description("Link to external tracker (optional)").
				Placeholder("e.g., gh-123, jira-ABC-456").
				Value(&raw.ExternalRef),
		),

		huh.NewGroup(
			huh.NewText().
				Title("Design Notes").
				Description("Technical approach or design details (optional)").
				Placeholder("Describe the implementation approach...").
				CharLimit(5000).
				Value(&raw.Design),

			huh.NewText().
				Title("Acceptance Criteria").
				Description("How do we know this is done? (optional)").
				Placeholder("List the criteria for completion...").
				CharLimit(5000).
				Value(&raw.Acceptance),
		),

		huh.NewGroup(
			huh.NewInput().
				Title("Dependencies").
				Description("Format: type:id or just id (optional)").
				Placeholder("e.g., discovered-from:bd-20, blocks:bd-15").
				Value(&raw.Deps),

			huh.NewConfirm().
				Title("Create this issue?").
				Affirmative("Create").
				Negative("Cancel"),
		),
	).WithTheme(huh.ThemeDracula())

	err := form.Run()
	if err != nil {
		if err == huh.ErrUserAborted {
			fmt.Fprintln(os.Stderr, "Issue creation canceled.")
			os.Exit(0)
		}
		FatalError("form error: %v", err)
	}

	// Parse the form input
	fv := parseCreateFormInput(raw)

	// If daemon is running, use RPC
	if daemonClient != nil {
		createArgs := &rpc.CreateArgs{
			Title:              fv.Title,
			Description:        fv.Description,
			IssueType:          fv.IssueType,
			Priority:           fv.Priority,
			Design:             fv.Design,
			AcceptanceCriteria: fv.AcceptanceCriteria,
			Assignee:           fv.Assignee,
			ExternalRef:        fv.ExternalRef,
			Labels:             fv.Labels,
			Dependencies:       fv.Dependencies,
		}

		resp, err := daemonClient.Create(createArgs)
		if err != nil {
			FatalError("%v", err)
		}

		if jsonOutput {
			fmt.Println(string(resp.Data))
		} else {
			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err != nil {
				FatalError("parsing response: %v", err)
			}
			printCreatedIssue(&issue)
		}
		return
	}

	// Direct mode - use the extracted creation function
	issue, err := CreateIssueFromFormValues(rootCtx, store, fv, actor)
	if err != nil {
		FatalError("%v", err)
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	if jsonOutput {
		outputJSON(issue)
	} else {
		printCreatedIssue(issue)
	}
}

func printCreatedIssue(issue *types.Issue) {
	fmt.Printf("\n%s Created issue: %s\n", ui.RenderPass("✓"), issue.ID)
	fmt.Printf("  Title:    %s\n", issue.Title)
	fmt.Printf("  Type:     %s\n", issue.IssueType)
	fmt.Printf("  Priority: P%d\n", issue.Priority)
	fmt.Printf("  Status:   %s\n", issue.Status)
	if issue.Assignee != "" {
		fmt.Printf("  Assignee: %s\n", issue.Assignee)
	}
	if issue.Description != "" {
		desc := issue.Description
		if len(desc) > 100 {
			desc = desc[:97] + "..."
		}
		fmt.Printf("  Description: %s\n", desc)
	}
}

func init() {
	// Note: --json flag is defined as a persistent flag in main.go
	rootCmd.AddCommand(createFormCmd)
}
