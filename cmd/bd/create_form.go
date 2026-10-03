package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
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
		ctx = storage.WithReservedChildCounter(ctx, fv.ParentID, childID)

		// Inherit parent labels (GH#2100), matching bd create --parent behavior
		inheritedLabels, _ = s.GetLabels(ctx, fv.ParentID)
	}

	var externalRefPtr *string
	if fv.ExternalRef != "" {
		externalRefPtr = &fv.ExternalRef
	}

	labels := mergeCreateLabels(fv.Labels, inheritedLabels)

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
		Labels:             labels,
	}

	if explicitID != "" {
		issue.ID = explicitID
	}

	// If a discovered-from dependency is present, inherit source_repo from
	// the referenced parent issue.
	if dfParent := discoveredFromParent(fv.Dependencies); dfParent != "" {
		parentIssue, err := s.GetIssue(ctx, dfParent)
		if err == nil && parentIssue != nil && parentIssue.SourceRepo != "" {
			issue.SourceRepo = parentIssue.SourceRepo
		}
	}

	// Parse dependency specs before creating anything. The form keeps its
	// historical lenient parsing (warn and skip malformed entries) for the
	// type itself, but once an entry's type is accepted it goes through the
	// same parseDepSpecs/dedupeDepSpecs pair `bd create --deps` uses, so
	// alias normalization (GH#5069) and the same-target-different-type
	// conflict (GH#4626, #4833) are handled identically on both paths.
	var validDepStrings []string
	for _, depSpec := range fv.Dependencies {
		depSpec = strings.TrimSpace(depSpec)
		if depSpec == "" {
			continue
		}

		var rawType types.DependencyType
		if strings.Contains(depSpec, ":") {
			parts := strings.SplitN(depSpec, ":", 2)
			rawType = types.DependencyType(strings.TrimSpace(parts[0]))
		} else {
			rawType = types.DepBlocks
		}

		if err := validateDependencyType(canonicalDependencyType(rawType)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid dependency type '%s' (valid: %s)\n", rawType, createDepsAcceptedTypeList())
			continue
		}

		validDepStrings = append(validDepStrings, depSpec)
	}

	// The form's `blocks:<id>` has always meant "the new issue depends on
	// <id>" - the OPPOSITE of --deps' explicit blocks: spelling, which
	// swaps direction. Parse each entry for alias canonicalization and
	// conflict detection, but pin the form's historical direction (every
	// form dep is an edge FROM the new issue) BEFORE deduping, so the
	// dedupe key sees the direction that will actually be stored.
	var depEntries []depSpecEntry
	for _, raw := range validDepStrings {
		spec, rawType, err := parseDepSpec(raw)
		if err != nil {
			return nil, err
		}
		spec.SwapDirection = false
		depEntries = append(depEntries, depSpecEntry{spec: spec, rawType: rawType})
	}
	depSpecs, err := dedupeDepSpecs(dropConflictingFormDeps(depEntries))
	if err != nil {
		return nil, err
	}

	// The issue and its edges (parent-child per GH#1983, plus form deps)
	// commit in one transaction; a failed edge rolls back the create instead
	// of leaving a dep-less issue behind (same contract as bd create).
	edges := createDepEdges{parentID: fv.ParentID, specs: depSpecs}
	if err := createIssueWithDeps(ctx, s, issue, actor, edges); err != nil {
		return nil, fmt.Errorf("failed to create issue: %w", err)
	}

	if edges.empty() {
		// Bare create: preserve the embedded-mode follow-up Dolt commit.
		// The deps path commits inside its transaction instead.
		shouldCommit, err := shouldCommitCreatePostWrites(issue, false)
		if err != nil {
			return nil, fmt.Errorf("dolt auto-commit: %w", err)
		}
		if shouldCommit {
			commitMsg := fmt.Sprintf("bd: create %s", issue.ID)
			if err := s.Commit(ctx, commitMsg); err != nil && !isDoltNothingToCommit(err) {
				WarnError("failed to commit post-create metadata: %v", err)
			}
		}
	}

	return issue, nil
}

// dropConflictingFormDeps applies the form's lenient posture to the
// same-target/different-type collision that dedupeDepSpecs rejects. On the
// `bd create --deps` path a hard failure is right: the caller re-runs with the
// flag fixed. In the interactive form the user has already typed every field,
// and an error at this point throws all of it away (review on #5648), so the
// form keeps the FIRST entry for each target, warns with the spellings as
// typed, and drops the rest. Identical repeats (e.g. blocked-by and depends-on
// both normalizing to blocks) are left for dedupeDepSpecs to collapse.
func dropConflictingFormDeps(entries []depSpecEntry) []depSpecEntry {
	seen := make(map[string]depSpecEntry, len(entries))
	kept := make([]depSpecEntry, 0, len(entries))
	for _, e := range entries {
		key := fmt.Sprintf("%t|%s", e.spec.SwapDirection, e.spec.TargetID)
		prev, ok := seen[key]
		switch {
		case !ok:
			seen[key] = e
			kept = append(kept, e)
		case prev.spec.Type != e.spec.Type:
			fmt.Fprintf(os.Stderr, "Warning: dropping %q on %s: it already carries %q and a target can only carry one dependency type at a time (GH#4626)\n",
				e.rawType, e.spec.TargetID, prev.rawType)
		default:
			kept = append(kept, e)
		}
	}
	return kept
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
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("create-form is not supported in proxied-server mode")
		}
		CheckReadonly("create-form")

		evt := metrics.NewCommandEvent("create-form")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		return runCreateForm(cmd)
	},
}

func runCreateForm(cmd *cobra.Command) error {
	parentID, _ := cmd.Flags().GetString("parent")

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
	).WithTheme(huh.ThemeFunc(huh.ThemeDracula))

	err := form.Run()
	if err != nil {
		if err == huh.ErrUserAborted {
			fmt.Fprintln(os.Stderr, "Issue creation canceled.")
			return nil
		}
		return HandleError("form error: %v", err)
	}

	fv := parseCreateFormInput(raw)
	fv.ParentID = parentID

	issue, err := CreateIssueFromFormValues(rootCtx, store, fv, actor)
	if err != nil {
		return HandleError("%v", err)
	}

	if jsonOutput {
		return outputJSON(issue)
	}
	printCreatedIssue(issue)
	return nil
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
