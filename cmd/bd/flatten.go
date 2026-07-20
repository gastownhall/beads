package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
)

var (
	flattenDryRun bool
	flattenForce  bool
)

var flattenCmd = &cobra.Command{
	Use:     "flatten",
	GroupID: "maint",
	Short:   "Squash all Dolt history into a single commit",
	Long: `Nuclear option: squash ALL Dolt commit history into a single commit.

This uses the Tim Sehn recipe:
  1. Create a new branch from the current state
  2. Soft-reset to the initial commit (preserving all data)
  3. Commit everything as a single snapshot
  4. Swap main branch to the new flattened branch
  5. Prune remote-tracking refs (they would keep the old history alive;
     the next push or fetch re-creates them at the new tip)
  6. Run Dolt GC to reclaim space from old history

This is irreversible — all commit history is lost. The resulting database
has exactly one commit containing all current data.

Use this when:
  - Your .beads/dolt directory has grown very large
  - You don't need commit-level history (time travel)
  - You want to start fresh with minimal storage

Examples:
  bd flatten --dry-run               # Preview: show commit count and disk usage
  bd flatten --force                 # Actually squash all history
  bd flatten --force --json          # JSON output`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		if usesProxiedServer() {
			return HandleErrorRespectJSON("flatten is not supported in proxied-server mode")
		}
		evt := metrics.NewCommandEvent("flatten")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if !flattenDryRun {
			CheckReadonly("flatten")
		}
		ctx := rootCtx
		start := time.Now()

		flattener, ok := storage.UnwrapStore(store).(storage.Flattener)
		if !ok {
			return HandleErrorRespectJSON("storage backend does not support flatten")
		}

		logEntries, logErr := store.Log(ctx, 0)
		if logErr != nil {
			return HandleErrorRespectJSON("failed to read commit log: %v", logErr)
		}
		commitCount := len(logEntries)

		var initialHash string
		if commitCount > 0 {
			initialHash = logEntries[commitCount-1].Hash
		}

		if flattenDryRun {
			remoteRefs, tags := listRemoteRefsAndTags(ctx)
			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"dry_run":           true,
					"commit_count":      commitCount,
					"initial_hash":      initialHash,
					"would_flatten":     commitCount > 1,
					"remote_refs":       remoteRefs,
					"tags":              tags,
					"size_before_bytes": storeSizeBytes(),
				})
			}
			fmt.Printf("DRY RUN — Flatten preview\n\n")
			fmt.Printf("  Commits:        %d\n", commitCount)
			fmt.Printf("  Initial commit: %s\n", initialHash)
			if len(remoteRefs) > 0 {
				fmt.Printf("  Remote refs:    %d (pruned before GC so old history is reclaimable)\n", len(remoteRefs))
			}
			if len(tags) > 0 {
				fmt.Printf("  Tags:           %d (anchor old history; GC cannot reclaim past them)\n", len(tags))
			}
			if commitCount <= 1 {
				fmt.Printf("\n  Already flat (1 commit). Nothing to do.\n")
			} else {
				fmt.Printf("\n  Would squash %d commits into 1.\n", commitCount)
				fmt.Printf("  Run with --force to proceed.\n")
			}
			return nil
		}

		if commitCount <= 1 {
			if jsonOutput {
				return outputJSON(map[string]interface{}{
					"success":      true,
					"message":      "already flat",
					"commit_count": commitCount,
				})
			}
			fmt.Println("Already flat (1 commit). Nothing to do.")
			return nil
		}

		if !flattenForce {
			return HandleErrorWithHintRespectJSON(
				fmt.Sprintf("would squash %d commits into 1 (irreversible)", commitCount),
				"Use --force to confirm or --dry-run to preview.")
		}

		if !jsonOutput {
			fmt.Printf("Flattening %d commits...\n", commitCount)
		}

		if err := flattener.Flatten(ctx); err != nil {
			return HandleErrorRespectJSON("flatten failed: %v", err)
		}

		// Prune remote-tracking refs before GC: they still anchor the entire
		// pre-flatten chain, and with them in place GC reclaims nothing on any
		// workspace that has ever pushed or fetched (bd-agctw).
		sizeBefore := storeSizeBytes()
		pruned, tags := pruneRemoteRefsForGC(ctx)
		if !jsonOutput {
			printPruneReport(pruned, tags)
		}

		if gc, ok := storage.UnwrapStore(store).(storage.GarbageCollector); ok {
			if err := gc.DoltGC(ctx); err != nil {
				WarnError("dolt gc after flatten failed: %v", err)
			}
		}
		sizeAfter := storeSizeBytes()

		elapsed := time.Since(start)

		if jsonOutput {
			result := map[string]interface{}{
				"success":            true,
				"commits_before":     commitCount,
				"commits_after":      1,
				"remote_refs_pruned": pruned,
				"tags_anchoring":     tags,
				"elapsed_ms":         elapsed.Milliseconds(),
			}
			addGCSizeJSON(result, sizeBefore, sizeAfter)
			return outputJSON(result)
		}
		fmt.Printf("✓ Flattened %d commits → 1\n", commitCount)
		if line := gcSizeLine(sizeBefore, sizeAfter); line != "" {
			fmt.Printf("  Store: %s\n", line)
		}
		fmt.Printf("  Time: %v\n", elapsed.Round(time.Millisecond))
		return nil
	},
}

func init() {
	flattenCmd.Flags().BoolVar(&flattenDryRun, "dry-run", false, "Preview without making changes")
	flattenCmd.Flags().BoolVarP(&flattenForce, "force", "f", false, "Confirm irreversible history squash")

	rootCmd.AddCommand(flattenCmd)
}
