package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

func runCompactProxiedServer(ctx context.Context) error {
	evt := metrics.NewCommandEvent("compact")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	if !compactDoltDryRun {
		CheckReadonly("compact")
	}
	start := time.Now()

	if compactDoltDays < 0 {
		return HandleError("--days must be non-negative")
	}

	var logEntries []storage.CommitInfo
	if err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var err error
		logEntries, err = versioncontrolops.Log(ctx, conn, 0)
		return err
	}); err != nil {
		return HandleError("failed to read commit log: %v", err)
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
	for _, entry := range logEntries {
		if entry.Date.Before(cutoff) {
			oldCommits++
		} else {
			recentHashes = append(recentHashes, entry.Hash)
		}
	}
	initialHash := logEntries[totalCommits-1].Hash
	boundaryHash := ""
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
			oldCommits, recentCommits)
	}

	var pruned, tags []string
	if err := runProxiedNonTx(ctx, func(ctx context.Context, conn *sql.Conn) error {
		if err := versioncontrolops.Compact(ctx, conn, initialHash, boundaryHash, oldCommits, recentHashes); err != nil {
			return err
		}
		var perr error
		if pruned, perr = versioncontrolops.PruneRemoteRefs(ctx, conn); perr != nil {
			WarnError("pruning remote-tracking refs before GC: %v (GC may reclaim little)", perr)
		}
		if tags, perr = versioncontrolops.ListTags(ctx, conn); perr != nil {
			WarnError("listing tags before GC: %v", perr)
		}
		if perr := versioncontrolops.DoltGC(ctx, conn); perr != nil {
			WarnError("dolt gc after compact failed: %v", perr)
		}
		return nil
	}); err != nil {
		return HandleError("compact failed: %v", err)
	}

	elapsed := time.Since(start)
	resultCommits := recentCommits + 1

	if !jsonOutput {
		printPruneReport(pruned, tags)
	}

	if jsonOutput {
		return outputJSON(map[string]interface{}{
			"success":            true,
			"commits_before":     totalCommits,
			"commits_after":      resultCommits,
			"old_squashed":       oldCommits,
			"recent_kept":        recentCommits,
			"remote_refs_pruned": pruned,
			"tags_anchoring":     tags,
			"elapsed_ms":         elapsed.Milliseconds(),
		})
	}

	fmt.Printf("✓ Compacted %d commits → %d\n", totalCommits, resultCommits)
	fmt.Printf("  Squashed: %d old commits → 1 base\n", oldCommits)
	fmt.Printf("  Preserved: %d recent commits\n", recentCommits)
	fmt.Printf("  Time: %v\n", elapsed.Round(time.Millisecond))
	return nil
}
