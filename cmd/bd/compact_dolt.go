package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
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
  5. Prunes remote-tracking refs (they would keep the old history alive;
     the next push or fetch re-creates them at the new tip)
  6. Runs Dolt GC to reclaim space

Examples:
  bd compact --dry-run               # Preview: show commit breakdown
  bd compact --force                 # Squash commits older than 30 days
  bd compact --days 7 --force        # Keep only last 7 days of history
  bd compact --days 90 --force       # Conservative: squash 90+ day old commits`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("compact is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("compact")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if !compactDoltDryRun {
			CheckReadonly("compact")
		}
		ctx := rootCtx
		start := time.Now()

		if compactDoltDays < 0 {
			return HandleError("--days must be non-negative")
		}

		logEntries, logErr := store.Log(ctx, 0)
		if logErr != nil {
			return HandleError("failed to read commit log: %v", logErr)
		}

		totalCommits := len(logEntries)
		if totalCommits <= 1 {
			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"success":       true,
					"message":       "nothing to compact",
					"total_commits": totalCommits,
				})
			}
			fmt.Printf("Only %d commit(s). Nothing to compact.\n", totalCommits)
			return nil
		}

		cutoff := time.Now().AddDate(0, 0, -compactDoltDays)

		var oldCommits int
		var recentHashes []string
		var initialHash, boundaryHash string

		for _, entry := range logEntries {
			if entry.Date.Before(cutoff) {
				oldCommits++
				boundaryHash = entry.Hash
			} else {
				recentHashes = append(recentHashes, entry.Hash)
			}
		}
		initialHash = logEntries[totalCommits-1].Hash
		boundaryHash = ""
		for _, entry := range logEntries {
			if entry.Date.Before(cutoff) {
				boundaryHash = entry.Hash
				break
			}
		}

		for i, j := 0, len(recentHashes)-1; i < j; i, j = i+1, j-1 {
			recentHashes[i], recentHashes[j] = recentHashes[j], recentHashes[i]
		}

		recentCommits := len(recentHashes)

		if compactDoltDryRun {
			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"dry_run":        true,
					"total_commits":  totalCommits,
					"old_commits":    oldCommits,
					"recent_commits": recentCommits,
					"cutoff_days":    compactDoltDays,
					"cutoff_date":    cutoff.Format("2006-01-02"),
					"initial_hash":   initialHash,
					"boundary_hash":  boundaryHash,
				})
			}
			fmt.Printf("DRY RUN — Compact preview\n\n")
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
			return nil
		}

		if oldCommits <= 1 {
			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"success":       true,
					"message":       "nothing to compact",
					"total_commits": totalCommits,
					"old_commits":   oldCommits,
				})
			}
			fmt.Printf("Only %d old commit(s). Nothing to compact.\n", oldCommits)
			return nil
		}

		if boundaryHash == "" {
			return HandleError("could not find boundary commit for compaction")
		}

		if !compactDoltForce {
			return HandleErrorWithHint(
				fmt.Sprintf("would squash %d old commits into 1, preserving %d recent commits",
					oldCommits, recentCommits),
				"Use --force to confirm or --dry-run to preview.")
		}

		if !jsonOutput {
			fmt.Printf("Compacting: %d old commits → 1, preserving %d recent\n",
				oldCommits, len(recentHashes))
		}

		compactor, ok := storage.UnwrapStore(store).(storage.Compactor)
		if !ok {
			return HandleError("storage backend does not support compact")
		}

		if err := compactor.Compact(ctx, initialHash, boundaryHash, oldCommits, recentHashes); err != nil {
			return HandleError("compact failed: %v", err)
		}

		// Prune remote-tracking refs before GC: they still anchor the
		// pre-compact chain, and with them in place GC reclaims nothing on any
		// workspace that has ever pushed or fetched (bd-agctw).
		sizeBefore := storeSizeBytes()
		pruned, tags := pruneRemoteRefsForGC(ctx)
		if !jsonOutput {
			printPruneReport(pruned, tags)
		}

		// Reclaim disk space from orphaned old history
		if gc, ok := storage.UnwrapStore(store).(storage.GarbageCollector); ok {
			if err := gc.DoltGC(ctx); err != nil {
				WarnError("dolt gc after compact failed: %v", err)
			}
		}
		sizeAfter := storeSizeBytes()

		elapsed := time.Since(start)
		resultCommits := len(recentHashes) + 1

		if jsonOutput {
			result := map[string]interface{}{
				"success":            true,
				"commits_before":     totalCommits,
				"commits_after":      resultCommits,
				"old_squashed":       oldCommits,
				"recent_kept":        len(recentHashes),
				"remote_refs_pruned": pruned,
				"tags_anchoring":     tags,
				"elapsed_ms":         elapsed.Milliseconds(),
			}
			addGCSizeJSON(result, sizeBefore, sizeAfter)
			return outputJSON(result)
		}
		fmt.Printf("✓ Compacted %d commits → %d\n", totalCommits, resultCommits)
		fmt.Printf("  Squashed: %d old commits → 1 base\n", oldCommits)
		fmt.Printf("  Preserved: %d recent commits\n", len(recentHashes))
		if line := gcSizeLine(sizeBefore, sizeAfter); line != "" {
			fmt.Printf("  Store: %s\n", line)
		}
		fmt.Printf("  Time: %v\n", elapsed.Round(time.Millisecond))
		return nil
	},
}

func init() {
	compactDoltCmd.Flags().BoolVar(&compactDoltDryRun, "dry-run", false, "Preview without making changes")
	compactDoltCmd.Flags().BoolVarP(&compactDoltForce, "force", "f", false, "Confirm commit squash")
	compactDoltCmd.Flags().IntVar(&compactDoltDays, "days", 30, "Keep commits newer than N days")

	rootCmd.AddCommand(compactDoltCmd)
}
