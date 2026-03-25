package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var (
	gcDryRun    bool
	gcForce     bool
	gcOlderThan int
	gcSkipDecay bool
	gcSkipDolt  bool
)

var gcCmd = &cobra.Command{
	Use:     "gc",
	GroupID: "maint",
	Short:   "Garbage collect: decay old issues, compact Dolt commits, run Dolt GC",
	Long: `Full lifecycle garbage collection for standalone Beads databases.

Runs three phases in sequence:
  1. DECAY   — Delete closed issues older than N days (default 90)
  2. COMPACT — Squash old Dolt commits into fewer commits (bd compact)
  3. GC      — Run Dolt garbage collection to reclaim disk space

Each phase can be skipped individually. Use --dry-run to preview all phases
without making changes.

Examples:
  bd gc                              # Full GC with defaults (90 day decay)
  bd gc --dry-run                    # Preview what would happen
  bd gc --older-than 30              # Decay issues closed 30+ days ago
  bd gc --skip-decay                 # Skip issue deletion, just compact+GC
  bd gc --skip-dolt                  # Skip Dolt GC, just decay+compact
  bd gc --force                      # Skip confirmation prompt`,
	Run: func(cmd *cobra.Command, _ []string) {
		if !gcDryRun {
			CheckReadonly("gc")
		}
		ctx := rootCtx
		start := time.Now()

		if gcOlderThan < 0 {
			FatalError("--older-than must be non-negative")
		}

		// Phase tracking for summary
		type phaseResult struct {
			name    string
			skipped bool
			detail  string
		}
		var results []phaseResult

		// ── Phase 1: DECAY ──
		if gcSkipDecay {
			results = append(results, phaseResult{name: "Decay", skipped: true})
		} else {
			if !jsonOutput {
				fmt.Println("Phase 1/3: Decay (delete old closed issues)")
			}

			cutoffDays := gcOlderThan
			cutoffTime := time.Now().AddDate(0, 0, -cutoffDays)
			statusClosed := types.StatusClosed
			filter := types.IssueFilter{
				Status:       &statusClosed,
				ClosedBefore: &cutoffTime,
			}

			closedIssues, err := store.SearchIssues(ctx, "", filter)
			if err != nil {
				FatalError("searching closed issues: %v", err)
			}

			// Filter out pinned issues
			filtered := make([]*types.Issue, 0, len(closedIssues))
			for _, issue := range closedIssues {
				if !issue.Pinned {
					filtered = append(filtered, issue)
				}
			}
			closedIssues = filtered

			if len(closedIssues) == 0 {
				detail := fmt.Sprintf("  No closed issues older than %d days", cutoffDays)
				if !jsonOutput {
					fmt.Println(detail)
				}
				results = append(results, phaseResult{name: "Decay", detail: "0 issues deleted"})
			} else {
				if gcDryRun {
					detail := fmt.Sprintf("  Would delete %d closed issue(s)", len(closedIssues))
					if !jsonOutput {
						fmt.Println(detail)
					}
					results = append(results, phaseResult{name: "Decay", detail: fmt.Sprintf("%d issues (dry-run)", len(closedIssues))})
				} else {
					if !gcForce {
						FatalErrorWithHint(
							fmt.Sprintf("would delete %d closed issue(s) older than %d days", len(closedIssues), cutoffDays),
							"Use --force to confirm or --dry-run to preview.")
					}

					deleted := 0
					for _, issue := range closedIssues {
						if err := store.DeleteIssue(ctx, issue.ID); err != nil {
							WarnError("failed to delete %s: %v", issue.ID, err)
						} else {
							deleted++
						}
					}
					commandDidWrite.Store(true)
					detail := fmt.Sprintf("  Deleted %d issue(s)", deleted)
					if !jsonOutput {
						fmt.Println(detail)
					}
					results = append(results, phaseResult{name: "Decay", detail: fmt.Sprintf("%d issues deleted", deleted)})

					// Embedded mode: flush Dolt commit after deletes.
					if isEmbeddedDolt && deleted > 0 && store != nil {
						if _, err := store.CommitPending(ctx, actor); err != nil {
							WarnError("failed to commit after decay: %v", err)
						}
					}
				}
			}
			if !jsonOutput {
				fmt.Println()
			}
		}

		// ── Phase 2: COMPACT (report only — actual squashing is bd flatten) ──
		if !jsonOutput {
			fmt.Println("Phase 2/3: Compact (Dolt commit history info)")
		}

		commitCount := 0
		logEntries, logErr := store.Log(ctx, 0)
		if logErr == nil {
			commitCount = len(logEntries)
		}

		if commitCount <= 1 {
			if !jsonOutput {
				fmt.Printf("  Only %d commit(s), nothing to compact\n\n", commitCount)
			}
			results = append(results, phaseResult{name: "Compact", detail: "nothing to compact"})
		} else {
			if gcDryRun {
				if !jsonOutput {
					fmt.Printf("  %d commits in history (use bd flatten to squash)\n\n", commitCount)
				}
				results = append(results, phaseResult{name: "Compact", detail: fmt.Sprintf("%d commits (dry-run)", commitCount)})
			} else {
				if !jsonOutput {
					fmt.Printf("  %d commits in history\n", commitCount)
					fmt.Printf("  Tip: use 'bd flatten' to squash all history to one commit\n\n")
				}
				results = append(results, phaseResult{name: "Compact", detail: fmt.Sprintf("%d commits", commitCount)})
			}
		}

		// ── Phase 3: Dolt GC ──
		if gcSkipDolt {
			results = append(results, phaseResult{name: "Dolt GC", skipped: true})
		} else {
			if !jsonOutput {
				fmt.Println("Phase 3/3: Dolt GC (reclaim disk space)")
			}

			gc, ok := store.(storage.GarbageCollector)
			if !ok {
				if !jsonOutput {
					fmt.Println("  Storage backend does not support GC, skipping")
				}
				results = append(results, phaseResult{name: "Dolt GC", detail: "not supported"})
			} else if gcDryRun {
				if !jsonOutput {
					fmt.Println("  Would run DOLT_GC()")
				}
				results = append(results, phaseResult{name: "Dolt GC", detail: "dry-run"})
			} else {
				if err := gc.DoltGC(ctx); err != nil {
					WarnError("dolt gc failed: %v", err)
					results = append(results, phaseResult{name: "Dolt GC", detail: "failed"})
				} else {
					if !jsonOutput {
						fmt.Println("  Done")
					}
					results = append(results, phaseResult{name: "Dolt GC", detail: "complete"})
				}
			}
			if !jsonOutput {
				fmt.Println()
			}
		}

		elapsed := time.Since(start)

		// ── Summary ──
		if jsonOutput {
			summaryMap := make(map[string]interface{})
			summaryMap["dry_run"] = gcDryRun
			summaryMap["elapsed_ms"] = elapsed.Milliseconds()
			phases := make([]map[string]interface{}, 0, len(results))
			for _, r := range results {
				p := map[string]interface{}{
					"name":    r.name,
					"skipped": r.skipped,
				}
				if r.detail != "" {
					p["detail"] = r.detail
				}
				phases = append(phases, p)
			}
			summaryMap["phases"] = phases
			outputJSON(summaryMap)
			return
		}

		mode := "✓ GC complete"
		if gcDryRun {
			mode = "DRY RUN complete"
		}
		fmt.Printf("%s (%v)\n", mode, elapsed.Round(time.Millisecond))
		for _, r := range results {
			if r.skipped {
				fmt.Printf("  %s: skipped\n", r.name)
			} else {
				fmt.Printf("  %s: %s\n", r.name, r.detail)
			}
		}
	},
}

func init() {
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", false, "Preview without making changes")
	gcCmd.Flags().BoolVarP(&gcForce, "force", "f", false, "Skip confirmation prompts")
	gcCmd.Flags().IntVar(&gcOlderThan, "older-than", 90, "Delete closed issues older than N days")
	gcCmd.Flags().BoolVar(&gcSkipDecay, "skip-decay", false, "Skip issue deletion phase")
	gcCmd.Flags().BoolVar(&gcSkipDolt, "skip-dolt", false, "Skip Dolt garbage collection phase")

	rootCmd.AddCommand(gcCmd)
}
