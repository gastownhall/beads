package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// runPollutionCheck runs detailed test pollution detection
// This integrates the detect-pollution command functionality into doctor.
//
//nolint:unparam // path reserved for future use
func runPollutionCheck(_ string, clean bool, yes bool) {
	// Ensure we have a store initialized (uses direct mode, no daemon support yet)
	if err := ensureDirectMode("pollution check requires direct mode"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx := rootCtx

	// Get all issues
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching issues: %v\n", err)
		os.Exit(1)
	}

	// Detect pollution (reuse detectTestPollution from detect_pollution.go)
	polluted := detectTestPollution(allIssues)

	if len(polluted) == 0 {
		if !jsonOutput {
			fmt.Println("No test pollution detected!")
		} else {
			outputJSON(map[string]interface{}{
				"polluted_count": 0,
				"issues":         []interface{}{},
			})
		}
		return
	}

	// Categorize by confidence
	highConfidence := []pollutionResult{}
	mediumConfidence := []pollutionResult{}

	for _, p := range polluted {
		if p.score >= 0.9 {
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

		outputJSON(result)
		return
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
		fmt.Printf("Run 'bd doctor --check=pollution --clean' to delete these issues (with confirmation).\n")
		return
	}

	// Confirmation prompt
	if !yes {
		fmt.Printf("\nDelete %d test issues? [y/N] ", len(polluted))
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Canceled.")
			return
		}
	}

	// Backup to JSONL before deleting
	backupPath := ".beads/pollution-backup.jsonl"
	if err := backupPollutedIssues(polluted, backupPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error backing up issues: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Backed up %d issues to %s\n", len(polluted), backupPath)

	// Delete issues
	fmt.Printf("\nDeleting %d issues...\n", len(polluted))
	deleted := 0
	for _, p := range polluted {
		if err := deleteIssue(ctx, p.issue.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting %s: %v\n", p.issue.ID, err)
			continue
		}
		deleted++
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	fmt.Printf("%s Deleted %d test issues\n", ui.RenderPass("✓"), deleted)
	fmt.Printf("\nCleanup complete. To restore, run: bd import %s\n", backupPath)
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
