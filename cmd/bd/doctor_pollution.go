package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// pollutionEdgeReader is the store seam the pollution cleanup needs: the two
// batched dependency lookups that decide whether a hit is safe to delete.
type pollutionEdgeReader interface {
	GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error)
	GetDependentRecordsForIssues(ctx context.Context, targetIDs []string) (map[string][]*types.Dependency, error)
}

// selectDeletablePollution filters high-confidence hits down to the ones
// --clean may delete: those with no dependency edges at all, in input order.
// A hit with any edge — outbound or inbound, any type, the ephemeral wisp
// tables included — is skipped: its links cannot be restored from the
// issue-object backup, so the operator removes them by hand first. A lookup
// error returns no selection at all: the caller must abort before any prompt,
// backup or delete rather than guess which hits are linked.
func selectDeletablePollution(ctx context.Context, reader pollutionEdgeReader, hits []doctor.PollutionResult) (deletable, linked []doctor.PollutionResult, err error) {
	if len(hits) == 0 {
		return nil, nil, nil
	}
	ids := make([]string, 0, len(hits))
	for _, p := range hits {
		ids = append(ids, p.Issue.ID)
	}
	outbound, err := reader.GetDependencyRecordsForIssues(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching outbound dependencies: %w", err)
	}
	inbound, err := reader.GetDependentRecordsForIssues(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching inbound dependencies: %w", err)
	}
	for _, p := range hits {
		id := p.Issue.ID
		if len(outbound[id]) > 0 || len(inbound[id]) > 0 {
			linked = append(linked, p)
			continue
		}
		deletable = append(deletable, p)
	}
	return deletable, linked, nil
}

// runPollutionCheck runs detailed test pollution detection
// This integrates the detect-pollution command functionality into doctor.
//
//nolint:unparam // path reserved for future use
func runPollutionCheck(_ string, clean bool, yes bool) error {
	if err := ensureDirectMode("pollution check requires direct mode"); err != nil {
		return HandleError("%v", err)
	}

	ctx := rootCtx

	// Get the pollution candidates (env-only cap via BEADS_MAX_ROWS; designer
	// §4: doctor family is env opt-in, no operator-facing flag). The load
	// filter matches the validation gate's; the scorer re-checks eligibility.
	maxRows, maxRowsSource := resolveMaxRowsEnvOnly()
	filter := doctor.PollutionIssueFilter()
	filter.MaxRows = maxRows
	filter.MaxRowsSource = maxRowsSource
	allIssues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		if capErr := handleMaxRowsError(err); capErr != nil {
			return capErr
		}
		return HandleError("fetching issues: %v", err)
	}

	polluted := doctor.DetectTestPollution(allIssues)

	if len(polluted) == 0 {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"polluted_count": 0,
				"issues":         []interface{}{},
			})
		}
		fmt.Println("No test pollution detected!")
		return nil
	}

	// Categorize by confidence
	highConfidence := []doctor.PollutionResult{}
	mediumConfidence := []doctor.PollutionResult{}

	for _, p := range polluted {
		if p.HighConfidence() {
			highConfidence = append(highConfidence, p)
		} else {
			mediumConfidence = append(mediumConfidence, p)
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"polluted_count":    len(polluted),
			"high_confidence":   len(highConfidence),
			"medium_confidence": len(mediumConfidence),
			"issues":            []map[string]interface{}{},
		}

		for _, p := range polluted {
			result["issues"] = append(result["issues"].([]map[string]interface{}), map[string]interface{}{
				"id":         p.Issue.ID,
				"title":      p.Issue.Title,
				"score":      p.Score,
				"reasons":    p.Reasons,
				"created_at": p.Issue.CreatedAt,
			})
		}

		return outputJSON(result)
	}

	// Human-readable output
	fmt.Printf("Found %d potential test issues:\n\n", len(polluted))

	if len(highConfidence) > 0 {
		fmt.Printf("High Confidence (score ≥ 0.9):\n")
		for _, p := range highConfidence {
			fmt.Printf("  %s: %q (score: %.2f)\n", p.Issue.ID, p.Issue.Title, p.Score)
			for _, reason := range p.Reasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
		fmt.Printf("  (Total: %d issues)\n\n", len(highConfidence))
	}

	if len(mediumConfidence) > 0 {
		fmt.Printf("Medium Confidence (score 0.7-0.9, manual review required — not eligible for --clean):\n")
		for _, p := range mediumConfidence {
			fmt.Printf("  %s: %q (score: %.2f)\n", p.Issue.ID, p.Issue.Title, p.Score)
			for _, reason := range p.Reasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
		fmt.Printf("  (Total: %d issues)\n\n", len(mediumConfidence))
	}

	if !clean {
		fmt.Printf("Run 'bd doctor --check=pollution --clean' to delete the high-confidence issues (with confirmation).\n")
		return nil
	}

	// --clean deletes only high-confidence hits whose dependency graph is
	// empty; medium hits stay for manual review (GH#5025: bulk deletion of
	// weak matches destroyed real work), and linked hits keep their edges
	// (the issue-object backup cannot restore them).
	deletable, linked, err := selectDeletablePollution(ctx, store, highConfidence)
	if err != nil {
		return HandleError("%v", err)
	}
	if len(linked) > 0 {
		fmt.Printf("Skipped %d high-confidence issue(s) with a dependency link — their edges cannot be restored from the backup; remove the links first:\n", len(linked))
		for _, p := range linked {
			fmt.Printf("  %s: %q (score: %.2f)\n", p.Issue.ID, p.Issue.Title, p.Score)
		}
		fmt.Println()
	}
	if len(deletable) == 0 {
		fmt.Printf("No deletable issues: every hit needs manual review. Nothing to clean up.\n")
		return nil
	}

	if !yes {
		fmt.Printf("\nDelete %d high-confidence test issues? [y/N] ", len(deletable))
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Canceled.")
			return nil
		}
	}

	backupPath := ".beads/pollution-backup.jsonl"
	if err := backupPollutedIssues(deletable, backupPath); err != nil {
		return HandleError("backing up issues: %v", err)
	}
	fmt.Printf("Backed up %d issues to %s\n", len(deletable), backupPath)

	fmt.Printf("\nDeleting %d issues...\n", len(deletable))
	deleted := 0
	for _, p := range deletable {
		if err := deleteIssue(ctx, p.Issue.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting %s: %v\n", p.Issue.ID, err)
			continue
		}
		deleted++
	}

	fmt.Printf("%s Deleted %d test issues\n", ui.RenderPass("✓"), deleted)
	arg, note := remedy.CommandArg(runtime.GOOS, backupPath, "<backup>")
	fmt.Printf("\nCleanup complete. To restore, run: bd init --from-jsonl %s%s\n", arg, note)
	return nil
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&perfMode, "perf", false, "Run performance diagnostics and generate CPU profile")
	doctorCmd.Flags().BoolVar(&checkHealthMode, "check-health", false, "Quick health check for git hooks (silent on success)")
	doctorCmd.Flags().StringVarP(&doctorOutput, "output", "o", "", "Export diagnostics to JSON file")
	doctorCmd.Flags().StringVar(&doctorCheckFlag, "check", "", "Run specific check in detail (e.g., 'pollution')")
	doctorCmd.Flags().BoolVar(&doctorClean, "clean", false, "For pollution check: delete high-confidence test issues (medium hits need manual review)")
	doctorCmd.Flags().BoolVar(&doctorDeep, "deep", false, "Validate full graph integrity")
}
