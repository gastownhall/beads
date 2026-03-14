package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/internal/beads"
)

var (
	compactDoltDryRun bool
	compactDoltForce  bool
	compactDoltDays   int
)

var compactDoltCmd = &cobra.Command{
	Use:     "compact",
	GroupID: "maint",
	Short:   "Squash old Dolt commits to reduce history size",
	Long: `Squash Dolt commits older than N days into a single commit.

Recent commits (within the retention window) are preserved via cherry-pick.
This reduces Dolt storage overhead from auto-commit history while keeping
recent change tracking intact.

For semantic issue compaction (summarizing closed issues), use 'bd admin compact'.
For full history squash, use 'bd flatten'.

How it works:
  1. Identifies commits older than --days threshold
  2. Creates a squashed base commit from all old history
  3. Cherry-picks recent commits on top
  4. Swaps main branch to the compacted version
  5. Runs Dolt GC to reclaim space

Examples:
  bd compact --dry-run               # Preview: show commit breakdown
  bd compact --force                 # Squash commits older than 30 days
  bd compact --days 7 --force        # Keep only last 7 days of history
  bd compact --days 90 --force       # Conservative: squash 90+ day old commits`,
	Run: func(_ *cobra.Command, _ []string) {
		if !compactDoltDryRun {
			CheckReadonly("compact")
		}
		ctx := rootCtx
		start := time.Now()

		if compactDoltDays < 0 {
			FatalError("--days must be non-negative")
		}

		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			FatalError("could not find .beads directory")
		}
		doltPath := filepath.Join(beadsDir, "dolt")

		if _, err := os.Stat(doltPath); os.IsNotExist(err) {
			FatalError("Dolt directory not found at %s", doltPath)
		}

		if _, err := exec.LookPath("dolt"); err != nil {
			FatalErrorWithHint("dolt command not found in PATH",
				"Install Dolt from https://github.com/dolthub/dolt")
		}

		// Get total commit count
		var totalCommits int
		if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&totalCommits); err != nil {
			FatalError("failed to count commits: %v", err)
		}

		if totalCommits <= 1 {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"success":       true,
					"message":       "nothing to compact",
					"total_commits": totalCommits,
				})
				return
			}
			fmt.Println("Only 1 commit. Nothing to compact.")
			return
		}

		// Find the cutoff date
		cutoff := time.Now().AddDate(0, 0, -compactDoltDays)

		// Count commits before and after cutoff
		var oldCommits int
		err := store.DB().QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_log WHERE date < ?", cutoff,
		).Scan(&oldCommits)
		if err != nil {
			FatalError("failed to count old commits: %v", err)
		}

		recentCommits := totalCommits - oldCommits

		// Get initial commit hash
		var initialHash string
		if err := store.DB().QueryRowContext(ctx,
			"SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1",
		).Scan(&initialHash); err != nil {
			FatalError("failed to find initial commit: %v", err)
		}

		// Find the boundary: most recent commit that is still "old"
		var boundaryHash string
		err = store.DB().QueryRowContext(ctx,
			"SELECT commit_hash FROM dolt_log WHERE date < ? ORDER BY date DESC LIMIT 1",
			cutoff,
		).Scan(&boundaryHash)
		if err == sql.ErrNoRows {
			boundaryHash = ""
		} else if err != nil {
			FatalError("failed to find boundary commit: %v", err)
		}

		sizeBefore, _ := getDirSize(doltPath)

		if compactDoltDryRun {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"dry_run":        true,
					"total_commits":  totalCommits,
					"old_commits":    oldCommits,
					"recent_commits": recentCommits,
					"cutoff_days":    compactDoltDays,
					"cutoff_date":    cutoff.Format("2006-01-02"),
					"initial_hash":   initialHash,
					"boundary_hash":  boundaryHash,
					"size_before":    sizeBefore,
					"size_display":   formatBytes(sizeBefore),
				})
				return
			}
			fmt.Printf("DRY RUN — Compact preview\n\n")
			fmt.Printf("  Dolt directory: %s\n", doltPath)
			fmt.Printf("  Current size:   %s\n", formatBytes(sizeBefore))
			fmt.Printf("  Total commits:  %d\n", totalCommits)
			fmt.Printf("  Old (>%d days): %d (would be squashed into 1)\n", compactDoltDays, oldCommits)
			fmt.Printf("  Recent:         %d (preserved)\n", recentCommits)
			fmt.Printf("  Cutoff date:    %s\n", cutoff.Format("2006-01-02"))
			if oldCommits <= 1 {
				fmt.Printf("\n  Nothing to compact (0-1 old commits).\n")
			} else {
				fmt.Printf("\n  Result: %d commits → %d commits\n", totalCommits, recentCommits+1)
				fmt.Printf("  Run with --force to proceed.\n")
			}
			return
		}

		if oldCommits <= 1 {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"success":       true,
					"message":       "nothing to compact",
					"total_commits": totalCommits,
					"old_commits":   oldCommits,
				})
				return
			}
			fmt.Printf("Only %d old commit(s). Nothing to compact.\n", oldCommits)
			return
		}

		if !compactDoltForce {
			FatalErrorWithHint(
				fmt.Sprintf("would squash %d old commits into 1, preserving %d recent commits",
					oldCommits, recentCommits),
				"Use --force to confirm or --dry-run to preview.")
		}

		// Collect recent commit hashes (in chronological order, oldest first)
		// These are commits we need to cherry-pick after squashing old history
		rows, err := store.DB().QueryContext(ctx,
			"SELECT commit_hash FROM dolt_log WHERE date >= ? ORDER BY date ASC",
			cutoff,
		)
		if err != nil {
			FatalError("failed to query recent commits: %v", err)
		}
		var recentHashes []string
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				_ = rows.Close()
				FatalError("failed to scan commit hash: %v", err)
			}
			recentHashes = append(recentHashes, h)
		}
		_ = rows.Close()

		if !jsonOutput {
			fmt.Printf("Compacting: %d old commits → 1, preserving %d recent\n",
				oldCommits, len(recentHashes))
		}

		// Close the store connection before CLI operations
		if store != nil {
			_ = store.Close()
		}

		// Compaction recipe:
		// 1. Create temp branch at boundary (last old commit)
		// 2. Checkout temp branch
		// 3. Soft-reset to initial commit (collapses all old history into working set)
		// 4. Stage and commit (single base commit)
		// 5. Cherry-pick each recent commit
		// 6. Checkout main, reset --hard to temp branch
		// 7. Delete temp branch
		// 8. GC

		runDolt := func(name string, args ...string) {
			cmd := exec.Command("dolt", args...) // #nosec G204 -- fixed commands
			cmd.Dir = doltPath
			output, err := cmd.CombinedOutput()
			if err != nil {
				FatalError("compact step '%s' failed: %v\nOutput: %s", name, err, string(output))
			}
		}

		runDolt("create temp branch", "branch", "compact-tmp", boundaryHash)
		runDolt("checkout temp", "checkout", "compact-tmp")
		runDolt("soft reset to initial", "reset", "--soft", initialHash)
		runDolt("stage all", "add", ".")
		runDolt("commit squashed base", "commit", "-Am",
			fmt.Sprintf("compact: squash %d commits into base snapshot", oldCommits))

		// Cherry-pick recent commits one by one
		for i, hash := range recentHashes {
			if !jsonOutput {
				fmt.Printf("  Cherry-picking %d/%d: %s\r", i+1, len(recentHashes), hash[:8])
			}
			runDolt(fmt.Sprintf("cherry-pick %s", hash[:8]), "cherry-pick", hash)
		}
		if !jsonOutput && len(recentHashes) > 0 {
			fmt.Println() // clear the \r line
		}

		runDolt("checkout main", "checkout", "main")
		runDolt("reset main to compacted", "reset", "--hard", "compact-tmp")
		runDolt("delete temp branch", "branch", "-D", "compact-tmp")
		runDolt("garbage collect", "gc")

		sizeAfter, _ := getDirSize(doltPath)
		freed := sizeBefore - sizeAfter
		if freed < 0 {
			freed = 0
		}
		elapsed := time.Since(start)
		resultCommits := len(recentHashes) + 1

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"success":        true,
				"commits_before": totalCommits,
				"commits_after":  resultCommits,
				"old_squashed":   oldCommits,
				"recent_kept":    len(recentHashes),
				"size_before":    sizeBefore,
				"size_after":     sizeAfter,
				"freed_bytes":    freed,
				"freed_display":  formatBytes(freed),
				"elapsed_ms":     elapsed.Milliseconds(),
			})
			return
		}

		fmt.Printf("✓ Compacted %d commits → %d\n", totalCommits, resultCommits)
		fmt.Printf("  Squashed: %d old commits → 1 base\n", oldCommits)
		fmt.Printf("  Preserved: %d recent commits\n", len(recentHashes))
		fmt.Printf("  %s → %s (freed %s)\n", formatBytes(sizeBefore), formatBytes(sizeAfter), formatBytes(freed))
		fmt.Printf("  Time: %v\n", elapsed.Round(time.Millisecond))
	},
}

func init() {
	compactDoltCmd.Flags().BoolVar(&compactDoltDryRun, "dry-run", false, "Preview without making changes")
	compactDoltCmd.Flags().BoolVarP(&compactDoltForce, "force", "f", false, "Confirm commit squash")
	compactDoltCmd.Flags().IntVar(&compactDoltDays, "days", 30, "Keep commits newer than N days")

	rootCmd.AddCommand(compactDoltCmd)
}
