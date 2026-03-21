package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
)

// runConventionsCheck runs a composite conventions check: lint, stale, and orphans.
// All findings are advisory (warning, never error) - conventions are a choice.
func runConventionsCheck(path string) {
	var checks []doctorCheck

	checks = append(checks, runConventionsLint()...)
	checks = append(checks, runConventionsStale()...)
	checks = append(checks, runConventionsOrphans(path)...)

	if jsonOutput {
		overallOK := true
		for _, c := range checks {
			if c.Status != statusOK {
				overallOK = false
				break
			}
		}
		outputJSON(struct {
			Path      string        `json:"path"`
			Checks    []doctorCheck `json:"checks"`
			OverallOK bool          `json:"overall_ok"`
		}{
			Path:      path,
			Checks:    checks,
			OverallOK: overallOK,
		})
		return
	}

	// Human-readable output
	fmt.Println()
	fmt.Println(ui.RenderCategory("Conventions"))

	var passCount, warnCount int
	for _, c := range checks {
		var statusIcon string
		switch c.Status {
		case statusOK:
			statusIcon = ui.RenderPassIcon()
			passCount++
		case statusWarning:
			statusIcon = ui.RenderWarnIcon()
			warnCount++
		}

		fmt.Printf("  %s  %s", statusIcon, c.Name)
		if c.Message != "" {
			fmt.Printf("%s", ui.RenderMuted(" "+c.Message))
		}
		fmt.Println()
		if c.Detail != "" {
			fmt.Printf("     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(c.Detail))
		}
		if c.Fix != "" {
			fmt.Printf("     %s\n", ui.RenderMuted("Fix: "+c.Fix))
		}
	}

	fmt.Println()
	fmt.Println(ui.RenderSeparator())
	fmt.Printf("%s %d passed  %s %d warnings\n",
		ui.RenderPassIcon(), passCount,
		ui.RenderWarnIcon(), warnCount,
	)

	if warnCount == 0 {
		fmt.Println()
		fmt.Printf("%s\n", ui.RenderPass("✓ All convention checks passed"))
	}
}

// runConventionsLint checks open issues for missing template sections.
func runConventionsLint() []doctorCheck {
	if store == nil {
		return []doctorCheck{{
			Name:     "conventions.lint",
			Status:   statusWarning,
			Message:  "database not available",
			Category: "Conventions",
		}}
	}

	ctx := rootCtx
	openStatus := types.StatusOpen
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{Status: &openStatus})
	if err != nil {
		return []doctorCheck{{
			Name:     "conventions.lint",
			Status:   statusWarning,
			Message:  fmt.Sprintf("error reading issues: %v", err),
			Category: "Conventions",
		}}
	}

	warningCount := 0
	for _, issue := range issues {
		if err := validation.LintIssue(issue); err != nil {
			warningCount++
		}
	}

	if warningCount == 0 {
		return []doctorCheck{{
			Name:     "conventions.lint",
			Status:   statusOK,
			Message:  fmt.Sprintf("all %d open issues pass template checks", len(issues)),
			Category: "Conventions",
		}}
	}

	return []doctorCheck{{
		Name:     "conventions.lint",
		Status:   statusWarning,
		Message:  fmt.Sprintf("%d of %d open issues missing recommended sections", warningCount, len(issues)),
		Fix:      "bd lint",
		Category: "Conventions",
	}}
}

// runConventionsStale checks for issues with no recent activity.
func runConventionsStale() []doctorCheck {
	if store == nil {
		return []doctorCheck{{
			Name:     "conventions.stale",
			Status:   statusWarning,
			Message:  "database not available",
			Category: "Conventions",
		}}
	}

	ctx := rootCtx
	filter := types.StaleFilter{Days: 14, Limit: 100}
	staleIssues, err := store.GetStaleIssues(ctx, filter)
	if err != nil {
		return []doctorCheck{{
			Name:     "conventions.stale",
			Status:   statusWarning,
			Message:  fmt.Sprintf("error checking stale issues: %v", err),
			Category: "Conventions",
		}}
	}

	if len(staleIssues) == 0 {
		return []doctorCheck{{
			Name:     "conventions.stale",
			Status:   statusOK,
			Message:  "no issues inactive for 14+ days",
			Category: "Conventions",
		}}
	}

	return []doctorCheck{{
		Name:     "conventions.stale",
		Status:   statusWarning,
		Message:  fmt.Sprintf("%d issues inactive for 14+ days", len(staleIssues)),
		Fix:      "bd stale",
		Category: "Conventions",
	}}
}

// runConventionsOrphans checks for issues referenced in commits but still open.
func runConventionsOrphans(path string) []doctorCheck {
	orphans, err := findOrphanedIssues(path)
	if err != nil {
		// Not an error - orphan detection may fail in non-git repos
		return []doctorCheck{{
			Name:     "conventions.orphans",
			Status:   statusOK,
			Message:  "orphan check skipped (no git history)",
			Category: "Conventions",
		}}
	}

	if len(orphans) == 0 {
		return []doctorCheck{{
			Name:     "conventions.orphans",
			Status:   statusOK,
			Message:  "no orphaned issues found",
			Category: "Conventions",
		}}
	}

	return []doctorCheck{{
		Name:     "conventions.orphans",
		Status:   statusWarning,
		Message:  fmt.Sprintf("%d issues referenced in commits but still open", len(orphans)),
		Fix:      "bd orphans",
		Category: "Conventions",
	}}
}
