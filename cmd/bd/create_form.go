package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
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
	ParentID           string // Parent issue ID for hierarchical child creation
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
// This function handles parent-child relationships, labels, dependencies,
// and source_repo inheritance.
func CreateIssueFromFormValues(ctx context.Context, s storage.DoltStorage, fv *createFormValues, actor string) (*types.Issue, error) {
	// If parent is specified, validate it exists and generate child ID
	var explicitID string
	var inheritedLabels []string
	if fv.ParentID != "" {
		_, err := s.GetIssue(ctx, fv.ParentID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, fmt.Errorf("parent issue %s not found", fv.ParentID)
			}
			return nil, fmt.Errorf("failed to check parent issue: %w", err)
		}
		childID, err := s.GetNextChildID(ctx, fv.ParentID)
		if err != nil {
			return nil, fmt.Errorf("failed to generate child ID: %w", err)
		}
		explicitID = childID

		// Inherit parent labels (GH#2100), matching bd create --parent behavior
		inheritedLabels, _ = s.GetLabels(ctx, fv.ParentID)
	}

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
		IssueType:          types.IssueType(fv.IssueType).Normalize(),
		Assignee:           fv.Assignee,
		ExternalRef:        externalRefPtr,
		CreatedBy:          getActorWithGit(), // GH#748: track who created the issue
	}

	if explicitID != "" {
		issue.ID = explicitID
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

	// Track whether any post-create writes occurred. CreateIssue commits
	// the issue to Dolt internally, but subsequent AddDependency/AddLabel
	// calls only write to the working set. A follow-up Dolt commit is
	// needed to persist them (GH#2009).
	postCreateWrites := false

	// If parent was specified, add parent-child dependency (GH#1983)
	if fv.ParentID != "" {
		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: fv.ParentID,
			Type:        types.DepParentChild,
		}
		if err := s.AddDependency(ctx, dep, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add parent-child dependency %s -> %s: %v\n", issue.ID, fv.ParentID, err)
		} else {
			postCreateWrites = true
		}
	}

	// Merge inherited parent labels with user-specified labels (GH#2100)
	if len(inheritedLabels) > 0 {
		seen := make(map[string]bool)
		for _, l := range fv.Labels {
			seen[l] = true
		}
		for _, l := range inheritedLabels {
			if !seen[l] {
				fv.Labels = append(fv.Labels, l)
			}
		}
	}

	// Add labels if specified
	for _, label := range fv.Labels {
		if err := s.AddLabel(ctx, issue.ID, label, actor); err != nil {
			// Log warning but don't fail the entire operation
			fmt.Fprintf(os.Stderr, "Warning: failed to add label %s: %v\n", label, err)
		} else {
			postCreateWrites = true
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
		} else {
			postCreateWrites = true
		}
	}

	// Commit post-create metadata (deps, labels) to Dolt. CreateIssue's
	// internal DOLT_COMMIT only covers the issue row; AddDependency and
	// AddLabel write to the SQL working set without a Dolt commit. Without
	// this, the metadata is visible but not durable — it can be lost on
	// push, sync, or server restart (GH#2009).
	if postCreateWrites {
		commitMsg := fmt.Sprintf("bd: create %s (metadata)", issue.ID)
		if err := s.Commit(ctx, commitMsg); err != nil && !isDoltNothingToCommit(err) {
			WarnError("failed to commit post-create metadata: %v", err)
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

Use --parent to create a sub-issue under an existing parent issue.
The child will get an auto-generated hierarchical ID (e.g., parent-id.1).

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
	parentID, _ := cmd.Flags().GetString("parent")

	sc := bufio.NewScanner(os.Stdin)
	prompt := func(label, fallback string) string {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, fallback)
		if !sc.Scan() {
			fmt.Fprintln(os.Stderr, "\nAborted.")
			os.Exit(0)
		}
		v := strings.TrimSpace(sc.Text())
		if v == "" {
			return fallback
		}
		return v
	}
	promptRequired := func(label string) string {
		for {
			fmt.Fprintf(os.Stderr, "%s: ", label)
			if !sc.Scan() {
				fmt.Fprintln(os.Stderr, "\nAborted.")
				os.Exit(0)
			}
			v := strings.TrimSpace(sc.Text())
			if v != "" {
				return v
			}
			fmt.Fprintln(os.Stderr, "  (required, try again)")
		}
	}
	promptOpt := func(label string) string {
		fmt.Fprintf(os.Stderr, "%s: ", label)
		if !sc.Scan() {
			return ""
		}
		return strings.TrimSpace(sc.Text())
	}

	raw := &createFormRawInput{}
	raw.Title = promptRequired("Title")
	raw.Description = promptOpt("Description")
	raw.IssueType = prompt("Type (task/bug/feature/epic/chore)", "task")
	raw.Priority = prompt("Priority (0-4)", "2")
	raw.Assignee = promptOpt("Assignee")
	raw.Labels = promptOpt("Labels (comma-separated)")
	raw.ExternalRef = promptOpt("External reference")
	raw.Design = promptOpt("Design notes")
	raw.Acceptance = promptOpt("Acceptance criteria")
	raw.Deps = promptOpt("Dependencies (type:id, comma-separated)")

	confirm := prompt("Create this issue? (y/n)", "y")
	if !strings.HasPrefix(strings.ToLower(confirm), "y") {
		fmt.Fprintln(os.Stderr, "Issue creation canceled.")
		os.Exit(0)
	}

	fv := parseCreateFormInput(raw)
	fv.ParentID = parentID

	issue, err := CreateIssueFromFormValues(rootCtx, store, fv, actor)
	if err != nil {
		FatalError("%v", err)
	}

	if jsonOutput {
		outputJSON(issue)
	} else {
		printCreatedIssue(issue)
	}
}

func printCreatedIssue(issue *types.Issue) {
	fmt.Printf("\n%s Created issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(issue.ID, issue.Title))
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
	createFormCmd.Flags().String("parent", "", "Parent issue ID for creating a hierarchical child (e.g., 'bd-a3f8e9')")
	rootCmd.AddCommand(createFormCmd)
}
