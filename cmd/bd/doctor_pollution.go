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

	// Get all issues (env-only cap via BEADS_MAX_ROWS; designer §4: doctor
	// family is env opt-in, no operator-facing flag).
	maxRows, maxRowsSource := resolveMaxRowsEnvOnly()
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{
		MaxRows:       maxRows,
		MaxRowsSource: maxRowsSource,
	})
	if err != nil {
		if capErr := handleMaxRowsError(err); capErr != nil {
			return capErr
		}
		return HandleError("fetching issues: %v", err)
	}

	polluted, err := doctor.DetectTestPollution(ctx, store, allIssues)
	if err != nil {
		return HandleError("detecting test pollution: %v", err)
	}

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

	// Categorize for presentation. The buckets are heuristic strengths,
	// not probabilities, and confer no deletion privilege: cleanup treats
	// both buckets identically.
	stronger := []doctor.PollutionMatch{}
	possible := []doctor.PollutionMatch{}

	for _, p := range polluted {
		if doctor.PollutionScoreHighBucket(p) {
			stronger = append(stronger, p)
		} else {
			possible = append(possible, p)
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"polluted_count":    len(polluted),
			"high_confidence":   len(stronger),
			"medium_confidence": len(possible),
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

	// Human-readable output. These are heuristic matches on title shape and
	// thin content, not verdicts: real work about the test suite can still
	// carry a matching title, so review comes before any deletion.
	fmt.Printf("Found %d possible test leftovers:\n\n", len(polluted))
	fmt.Printf("Heuristic matches on test-style titles and thin content; title-only work\n")
	fmt.Printf("can still match. Review each issue with 'bd show <id>' before deleting anything.\n\n")

	if len(stronger) > 0 {
		fmt.Printf("Stronger heuristic matches:\n")
		for _, p := range stronger {
			fmt.Printf("  %s: %q\n", p.Issue.ID, p.Issue.Title)
			for _, reason := range p.Reasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
		fmt.Printf("\n  (Total: %d issues)\n\n", len(stronger))
	}

	if len(possible) > 0 {
		fmt.Printf("Possible heuristic matches:\n")
		for _, p := range possible {
			fmt.Printf("  %s: %q\n", p.Issue.ID, p.Issue.Title)
			for _, reason := range p.Reasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
		fmt.Printf("\n  (Total: %d issues)\n\n", len(possible))
	}

	if !clean {
		fmt.Printf("'bd doctor --check=pollution --clean' deletes confirmed leftovers after a\n")
		fmt.Printf("confirmation prompt; it is never run automatically.\n")
		return nil
	}

	if !yes {
		fmt.Printf("\nDelete %d test issues? [y/N] ", len(polluted))
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Canceled.")
			return nil
		}
	}

	backupPath := ".beads/pollution-backup.jsonl"

	// Post-confirmation refresh (GH#5025): the reported set was computed
	// before the prompt. Reload exactly the originally reported IDs and
	// rerun the classifier, so a row that became protected, connected or
	// commented — or was deleted or edited away — between report and
	// confirmation is skipped and reported rather than deleted, and no
	// newly discovered ID joins an already confirmed set. This is not an
	// atomic compare-and-delete against writes that land after the
	// refresh.
	reported := make([]string, len(polluted))
	for i, p := range polluted {
		reported[i] = p.Issue.ID
	}
	reloaded, err := store.SearchIssues(ctx, "", types.IssueFilter{IDs: reported})
	if err != nil {
		return HandleError("reloading reported issues before cleanup: %v", err)
	}
	fresh, err := doctor.DetectTestPollution(ctx, store, reloaded)
	if err != nil {
		return HandleError("re-checking reported issues before cleanup: %v", err)
	}
	freshByID := make(map[string]doctor.PollutionMatch, len(fresh))
	for _, m := range fresh {
		freshByID[m.Issue.ID] = m
	}
	var eligible []doctor.PollutionMatch
	var skipped []string
	for _, id := range reported {
		if m, ok := freshByID[id]; ok {
			eligible = append(eligible, m)
		} else {
			skipped = append(skipped, id)
		}
	}
	if len(skipped) > 0 {
		fmt.Printf("Skipping %d issue(s) that no longer qualify (closed, protected, connected, commented, edited, or deleted): %s\n",
			len(skipped), strings.Join(skipped, ", "))
	}
	if len(eligible) == 0 {
		fmt.Printf("No remaining eligible issues; nothing deleted.\n")
		return nil
	}

	if err := backupPollutedIssues(eligible, backupPath); err != nil {
		return HandleError("backing up issues: %v", err)
	}
	fmt.Printf("Backed up %d issues to %s\n", len(eligible), backupPath)

	fmt.Printf("\nDeleting %d issues...\n", len(eligible))
	deleted := 0
	for _, p := range eligible {
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
	doctorCmd.Flags().BoolVar(&doctorClean, "clean", false, "For pollution check: delete detected test issues")
	doctorCmd.Flags().BoolVar(&doctorDeep, "deep", false, "Validate full graph integrity")
}
