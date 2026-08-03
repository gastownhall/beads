package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

// LintResult holds the validation result for a single issue.
type LintResult struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Type     string   `json:"type"`
	Missing  []string `json:"missing,omitempty"`
	Warnings int      `json:"warnings"`
}

var lintCmd = &cobra.Command{
	Use:     "lint [issue-id...]",
	GroupID: "views",
	Short:   "Check issues for missing template sections",
	Long: `Check issues for missing recommended sections based on issue type.

By default, lints all open issues. Specify issue IDs to lint specific issues.

Section requirements by type:
  bug:      Steps to Reproduce, Acceptance Criteria
  task:     Acceptance Criteria
  feature:  Acceptance Criteria
  epic:     Success Criteria (or Acceptance Criteria)
  chore:    (none)

Examples:
  bd lint                    # Lint all open issues
  bd lint bd-abc             # Lint specific issue
  bd lint bd-abc bd-def      # Lint multiple issues
  bd lint --type bug         # Lint only bugs
  bd lint --status all       # Lint all issues (including closed)
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("lint")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		typeFilter, _ := cmd.Flags().GetString("type")
		statusFilter, _ := cmd.Flags().GetString("status")

		if usesProxiedServer() {
			return runLintProxiedServer(rootCtx, args, typeFilter, statusFilter)
		}

		ctx := rootCtx
		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}

		var issues []*types.Issue
		if len(args) > 0 {
			issues = lintCollectByIDs(ctx, args, store.GetIssue)
		} else {
			// Lint all matching issues (env-only cap; designer §4 doctor family).
			filter := buildLintFilter(typeFilter, statusFilter)
			filter.MaxRows, filter.MaxRowsSource = resolveMaxRowsEnvOnly()

			var err error
			issues, err = store.SearchIssues(ctx, "", filter)
			if err != nil {
				if capErr := handleMaxRowsError(err); capErr != nil {
					return capErr
				}
				return HandleError("%v", err)
			}
		}

		return runLint(issues)
	},
}

func buildLintFilter(typeFilter, statusFilter string) types.IssueFilter {
	filter := types.IssueFilter{}

	if statusFilter == "" || statusFilter == "open" {
		s := types.StatusOpen
		filter.Status = &s
	} else if statusFilter != "all" {
		s := types.Status(statusFilter)
		filter.Status = &s
	}

	if typeFilter != "" {
		t := types.IssueType(typeFilter)
		filter.IssueType = &t
	}

	return filter
}

func lintCollectByIDs(ctx context.Context, ids []string, get func(context.Context, string) (*types.Issue, error)) []*types.Issue {
	var issues []*types.Issue
	for _, id := range ids {
		issue, err := get(ctx, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting %s: %v\n", id, err)
			continue
		}
		if issue == nil {
			fmt.Fprintf(os.Stderr, "Issue not found: %s\n", id)
			continue
		}
		issues = append(issues, issue)
	}
	return issues
}

func runLint(issues []*types.Issue) error {
	var results []LintResult
	totalWarnings := 0

	for _, issue := range issues {
		err := validation.LintIssue(issue)
		if err == nil {
			continue
		}

		templateErr, ok := err.(*validation.TemplateError)
		if !ok {
			continue
		}

		missing := make([]string, len(templateErr.Missing))
		for i, m := range templateErr.Missing {
			missing[i] = m.Heading
		}

		result := LintResult{
			ID:       issue.ID,
			Title:    issue.Title,
			Type:     string(issue.IssueType),
			Missing:  missing,
			Warnings: len(missing),
		}
		results = append(results, result)
		totalWarnings += len(missing)
	}

	if jsonOutput {
		output := struct {
			Total   int          `json:"total"`
			Issues  int          `json:"issues"`
			Results []LintResult `json:"results"`
		}{
			Total:   totalWarnings,
			Issues:  len(results),
			Results: results,
		}
		data, _ := json.MarshalIndent(output, "", "  ")
		fmt.Println(string(data))
		return nil
	}

	if len(results) == 0 {
		fmt.Printf("✓ No template warnings found (%d issues checked)\n", len(issues))
		return nil
	}

	fmt.Printf("Template warnings (%d issues, %d warnings):\n\n", len(results), totalWarnings)
	for _, r := range results {
		fmt.Printf("%s [%s]: %s\n", r.ID, r.Type, r.Title)
		for _, m := range r.Missing {
			fmt.Printf("  ⚠ Missing: %s\n", m)
		}
		fmt.Println()
	}

	return SilentExit()
}

func init() {
	lintCmd.Flags().StringP("type", "t", "", "Filter by issue type (bug, task, feature, epic, decision, spike, story, chore, milestone)")
	lintCmd.Flags().StringP("status", "s", "", "Filter by status (default: open, use 'all' for all)")

	rootCmd.AddCommand(lintCmd)
}
