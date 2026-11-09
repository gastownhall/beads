package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// runPollutionCheck runs detailed test pollution detection
// This integrates the detect-pollution command functionality into doctor.
//
//nolint:unparam // path reserved for future use
func runPollutionCheck(_ string, clean bool, yes bool) error {
	if err := ensureDirectMode("pollution check requires direct mode"); err != nil {
		return HandleError("%v", err)
	}

	ctx := rootCtx

	// Get the pollution candidates the validate gate also scores (env-only
	// cap via BEADS_MAX_ROWS; designer §4: doctor family is env opt-in, no
	// operator-facing flag).
	filter := doctor.PollutionCandidateFilter()
	filter.MaxRows, filter.MaxRowsSource = resolveMaxRowsEnvOnly()
	allIssues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		if capErr := handleMaxRowsError(err); capErr != nil {
			return capErr
		}
		return HandleError("fetching issues: %v", err)
	}

	polluted := detectTestPollution(allIssues)

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
	highConfidence := []pollutionResult{}
	mediumConfidence := []pollutionResult{}

	for _, p := range polluted {
		if p.highConfidence() {
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
				"id":         p.issue.ID,
				"title":      p.issue.Title,
				"score":      p.score,
				"reasons":    p.reasons,
				"created_at": p.issue.CreatedAt,
			})
		}

		return outputJSON(result)
	}

	// Human-readable output
	fmt.Printf("Found %d potential test issues:\n\n", len(polluted))

	if len(highConfidence) > 0 {
		fmt.Printf("High Confidence (score ≥ 0.9):\n")
		for _, p := range highConfidence {
			fmt.Printf("  %s: %q (score: %.2f)\n", p.issue.ID, p.issue.Title, p.score)
			for _, reason := range p.reasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
		fmt.Printf("  (Total: %d issues)\n\n", len(highConfidence))
	}

	if len(mediumConfidence) > 0 {
		fmt.Printf("Medium Confidence (score 0.7-0.9):\n")
		for _, p := range mediumConfidence {
			fmt.Printf("  %s: %q (score: %.2f)\n", p.issue.ID, p.issue.Title, p.score)
			for _, reason := range p.reasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
		fmt.Printf("  (Total: %d issues)\n\n", len(mediumConfidence))
	}

	if !clean {
		fmt.Printf("Run 'bd doctor --check=pollution --clean' to delete the high-confidence issues (with confirmation); medium-confidence issues are for manual review.\n")
		return nil
	}

	deletable, skipped := selectPollutionCleanup(polluted)
	for _, skip := range skipped {
		fmt.Printf("Skipping %s: %s\n", skip.issue.ID, skip.reason)
	}
	if len(deletable) == 0 {
		fmt.Println("No issues eligible for cleanup.")
		return nil
	}

	if !yes {
		fmt.Printf("\nDelete %d test issues? [y/N] ", len(deletable))
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
		if err := deleteIssue(ctx, p.issue.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting %s: %v\n", p.issue.ID, err)
			continue
		}
		deleted++
	}

	fmt.Printf("%s Deleted %d test issues\n", ui.RenderPass("✓"), deleted)
	arg, note := remedy.CommandArg(runtime.GOOS, backupPath, "<backup>")
	fmt.Printf("\nCleanup complete. To restore, run: bd init --from-jsonl %s%s\n", arg, note)
	return nil
}

// pollutionSkip is a reported hit cleanup leaves in place, and why.
type pollutionSkip struct {
	issue  *types.Issue
	reason string
}

// selectPollutionCleanup splits reported hits into those cleanup may back up
// and delete and those it leaves for review. Only high-confidence hits are
// deletable: a medium-confidence hit rests on a single title signal, which
// real work can carry (GH#5025).
func selectPollutionCleanup(polluted []pollutionResult) (deletable []pollutionResult, skipped []pollutionSkip) {
	for _, p := range polluted {
		if !p.highConfidence() {
			skipped = append(skipped, pollutionSkip{issue: p.issue, reason: "medium confidence; left for manual review"})
			continue
		}
		deletable = append(deletable, p)
	}
	return deletable, skipped
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&perfMode, "perf", false, "Run performance diagnostics and generate CPU profile")
	doctorCmd.Flags().BoolVar(&checkHealthMode, "check-health", false, "Quick health check for git hooks (silent on success)")
	doctorCmd.Flags().StringVarP(&doctorOutput, "output", "o", "", "Export diagnostics to JSON file")
	doctorCmd.Flags().StringVar(&doctorCheckFlag, "check", "", "Run specific check in detail (e.g., 'pollution')")
	doctorCmd.Flags().BoolVar(&doctorClean, "clean", false, "For pollution check: delete high-confidence test issues")
	doctorCmd.Flags().BoolVar(&doctorDeep, "deep", false, "Validate full graph integrity")
}
