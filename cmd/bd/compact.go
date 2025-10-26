package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/compact"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

var (
	compactDryRun  bool
	compactTier    int
	compactAll     bool
	compactID      string
	compactForce   bool
	compactBatch   int
	compactWorkers int
	compactStats   bool
)

var compactCmd = &cobra.Command{
	Use:   "compact",
	Short: "Compact old closed issues to save space",
	Long: `Compact old closed issues using semantic summarization.

Compaction reduces database size by summarizing closed issues that are no longer
actively referenced. This is permanent graceful decay - original content is discarded.

Tiers:
  - Tier 1: Semantic compression (30 days closed, 70% reduction)
  - Tier 2: Ultra compression (90 days closed, 95% reduction)

Examples:
  bd compact --dry-run                  # Preview candidates
  bd compact --all                      # Compact all eligible issues
  bd compact --id bd-42                 # Compact specific issue
  bd compact --id bd-42 --force         # Force compact (bypass checks)
  bd compact --stats                    # Show statistics
`,
	Run: func(_ *cobra.Command, _ []string) {
		ctx := context.Background()

		// Handle compact stats first
		if compactStats {
			if daemonClient != nil {
				runCompactStatsRPC()
			} else {
				sqliteStore, ok := store.(*sqlite.SQLiteStorage)
				if !ok {
					fmt.Fprintf(os.Stderr, "Error: compact requires SQLite storage\n")
					os.Exit(1)
				}
				runCompactStats(ctx, sqliteStore)
			}
			return
		}

		// If using daemon, delegate to RPC
		if daemonClient != nil {
			runCompactRPC(ctx)
			return
		}

		// Direct mode - original logic
		sqliteStore, ok := store.(*sqlite.SQLiteStorage)
		if !ok {
			fmt.Fprintf(os.Stderr, "Error: compact requires SQLite storage\n")
			os.Exit(1)
		}

		if compactID != "" && compactAll {
			fmt.Fprintf(os.Stderr, "Error: cannot use --id and --all together\n")
			os.Exit(1)
		}

		if compactForce && compactID == "" {
			fmt.Fprintf(os.Stderr, "Error: --force requires --id\n")
			os.Exit(1)
		}

		if compactID == "" && !compactAll && !compactDryRun {
			fmt.Fprintf(os.Stderr, "Error: must specify --all, --id, or --dry-run\n")
			os.Exit(1)
		}

		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" && !compactDryRun {
			fmt.Fprintf(os.Stderr, "Error: ANTHROPIC_API_KEY environment variable not set\n")
			os.Exit(1)
		}

		config := &compact.Config{
			APIKey:      apiKey,
			Concurrency: compactWorkers,
			DryRun:      compactDryRun,
		}

		compactor, err := compact.New(sqliteStore, apiKey, config)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create compactor: %v\n", err)
			os.Exit(1)
		}

		if compactID != "" {
			runCompactSingle(ctx, compactor, sqliteStore, compactID)
			return
		}

		runCompactAll(ctx, compactor, sqliteStore)
	},
}

func runCompactSingle(ctx context.Context, compactor *compact.Compactor, store *sqlite.SQLiteStorage, issueID string) {
	start := time.Now()

	if !compactForce {
		eligible, reason, err := store.CheckEligibility(ctx, issueID, compactTier)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to check eligibility: %v\n", err)
			os.Exit(1)
		}
		if !eligible {
			fmt.Fprintf(os.Stderr, "Error: %s is not eligible for Tier %d compaction: %s\n", issueID, compactTier, reason)
			os.Exit(1)
		}
	}

	issue, err := store.GetIssue(ctx, issueID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to get issue: %v\n", err)
		os.Exit(1)
	}

	originalSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)

	if compactDryRun {
		if jsonOutput {
			output := map[string]interface{}{
				"dry_run":             true,
				"tier":                compactTier,
				"issue_id":            issueID,
				"original_size":       originalSize,
				"estimated_reduction": "70-80%",
			}
			outputJSON(output)
			return
		}

		fmt.Printf("DRY RUN - Tier %d compaction\n\n", compactTier)
		fmt.Printf("Issue: %s\n", issueID)
		fmt.Printf("Original size: %d bytes\n", originalSize)
		fmt.Printf("Estimated reduction: 70-80%%\n")
		return
	}

	var compactErr error
	if compactTier == 1 {
		compactErr = compactor.CompactTier1(ctx, issueID)
	} else {
		fmt.Fprintf(os.Stderr, "Error: Tier 2 compaction not yet implemented\n")
		os.Exit(1)
	}

	if compactErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", compactErr)
		os.Exit(1)
	}

	issue, err = store.GetIssue(ctx, issueID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to get updated issue: %v\n", err)
		os.Exit(1)
	}

	compactedSize := len(issue.Description)
	savingBytes := originalSize - compactedSize
	elapsed := time.Since(start)

	if jsonOutput {
		output := map[string]interface{}{
			"success":        true,
			"tier":           compactTier,
			"issue_id":       issueID,
			"original_size":  originalSize,
			"compacted_size": compactedSize,
			"saved_bytes":    savingBytes,
			"reduction_pct":  float64(savingBytes) / float64(originalSize) * 100,
			"elapsed_ms":     elapsed.Milliseconds(),
		}
		outputJSON(output)
		return
	}

	fmt.Printf("✓ Compacted %s (Tier %d)\n", issueID, compactTier)
	fmt.Printf("  %d → %d bytes (saved %d, %.1f%%)\n",
		originalSize, compactedSize, savingBytes,
		float64(savingBytes)/float64(originalSize)*100)
	fmt.Printf("  Time: %v\n", elapsed)

	// Schedule auto-flush to export changes
	markDirtyAndScheduleFlush()
}

func runCompactAll(ctx context.Context, compactor *compact.Compactor, store *sqlite.SQLiteStorage) {
	start := time.Now()

	var candidates []string
	if compactTier == 1 {
		tier1, err := store.GetTier1Candidates(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get candidates: %v\n", err)
			os.Exit(1)
		}
		for _, c := range tier1 {
			candidates = append(candidates, c.IssueID)
		}
	} else {
		tier2, err := store.GetTier2Candidates(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get candidates: %v\n", err)
			os.Exit(1)
		}
		for _, c := range tier2 {
			candidates = append(candidates, c.IssueID)
		}
	}

	if len(candidates) == 0 {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"success": true,
				"count":   0,
				"message": "No eligible candidates",
			})
			return
		}
		fmt.Println("No eligible candidates for compaction")
		return
	}

	if compactDryRun {
		totalSize := 0
		for _, id := range candidates {
			issue, err := store.GetIssue(ctx, id)
			if err != nil {
				continue
			}
			totalSize += len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
		}

		if jsonOutput {
			output := map[string]interface{}{
				"dry_run":             true,
				"tier":                compactTier,
				"candidate_count":     len(candidates),
				"total_size_bytes":    totalSize,
				"estimated_reduction": "70-80%",
			}
			outputJSON(output)
			return
		}

		fmt.Printf("DRY RUN - Tier %d compaction\n\n", compactTier)
		fmt.Printf("Candidates: %d issues\n", len(candidates))
		fmt.Printf("Total size: %d bytes\n", totalSize)
		fmt.Printf("Estimated reduction: 70-80%%\n")
		return
	}

	if !jsonOutput {
		fmt.Printf("Compacting %d issues (Tier %d)...\n\n", len(candidates), compactTier)
	}

	results, err := compactor.CompactTier1Batch(ctx, candidates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: batch compaction failed: %v\n", err)
		os.Exit(1)
	}

	successCount := 0
	failCount := 0
	totalSaved := 0
	totalOriginal := 0

	for i, result := range results {
		if !jsonOutput {
			fmt.Printf("[%s] %d/%d\r", progressBar(i+1, len(results)), i+1, len(results))
		}

		if result.Err != nil {
			failCount++
		} else {
			successCount++
			totalOriginal += result.OriginalSize
			totalSaved += (result.OriginalSize - result.CompactedSize)
		}
	}

	elapsed := time.Since(start)

	if jsonOutput {
		output := map[string]interface{}{
			"success":       true,
			"tier":          compactTier,
			"total":         len(results),
			"succeeded":     successCount,
			"failed":        failCount,
			"saved_bytes":   totalSaved,
			"original_size": totalOriginal,
			"elapsed_ms":    elapsed.Milliseconds(),
		}
		outputJSON(output)
		return
	}

	fmt.Printf("\n\nCompleted in %v\n\n", elapsed)
	fmt.Printf("Summary:\n")
	fmt.Printf("  Succeeded: %d\n", successCount)
	fmt.Printf("  Failed: %d\n", failCount)
	if totalOriginal > 0 {
		fmt.Printf("  Saved: %d bytes (%.1f%%)\n", totalSaved, float64(totalSaved)/float64(totalOriginal)*100)
	}

	// Schedule auto-flush to export changes
	if successCount > 0 {
		markDirtyAndScheduleFlush()
	}
}

func runCompactStats(ctx context.Context, store *sqlite.SQLiteStorage) {
	tier1, err := store.GetTier1Candidates(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to get Tier 1 candidates: %v\n", err)
		os.Exit(1)
	}

	tier2, err := store.GetTier2Candidates(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to get Tier 2 candidates: %v\n", err)
		os.Exit(1)
	}

	tier1Size := 0
	for _, c := range tier1 {
		tier1Size += c.OriginalSize
	}

	tier2Size := 0
	for _, c := range tier2 {
		tier2Size += c.OriginalSize
	}

	if jsonOutput {
		output := map[string]interface{}{
			"tier1": map[string]interface{}{
				"candidates": len(tier1),
				"total_size": tier1Size,
			},
			"tier2": map[string]interface{}{
				"candidates": len(tier2),
				"total_size": tier2Size,
			},
		}
		outputJSON(output)
		return
	}

	fmt.Println("Compaction Statistics")
	fmt.Printf("Tier 1 (30+ days closed):\n")
	fmt.Printf("  Candidates: %d\n", len(tier1))
	fmt.Printf("  Total size: %d bytes\n", tier1Size)
	if tier1Size > 0 {
		fmt.Printf("  Estimated savings: %d bytes (70%%)\n\n", tier1Size*7/10)
	}

	fmt.Printf("Tier 2 (90+ days closed, Tier 1 compacted):\n")
	fmt.Printf("  Candidates: %d\n", len(tier2))
	fmt.Printf("  Total size: %d bytes\n", tier2Size)
	if tier2Size > 0 {
		fmt.Printf("  Estimated savings: %d bytes (95%%)\n", tier2Size*95/100)
	}
}

func progressBar(current, total int) string {
	const width = 40
	if total == 0 {
		return "[" + string(make([]byte, width)) + "]"
	}
	filled := (current * width) / total
	bar := ""
	for i := 0; i < width; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += " "
		}
	}
	return "[" + bar + "]"
}

//nolint:unparam // ctx may be used in future for cancellation
func runCompactRPC(_ context.Context) {
	if compactID != "" && compactAll {
		fmt.Fprintf(os.Stderr, "Error: cannot use --id and --all together\n")
		os.Exit(1)
	}

	if compactForce && compactID == "" {
		fmt.Fprintf(os.Stderr, "Error: --force requires --id\n")
		os.Exit(1)
	}

	if compactID == "" && !compactAll && !compactDryRun {
		fmt.Fprintf(os.Stderr, "Error: must specify --all, --id, or --dry-run\n")
		os.Exit(1)
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" && !compactDryRun {
		fmt.Fprintf(os.Stderr, "Error: ANTHROPIC_API_KEY environment variable not set\n")
		os.Exit(1)
	}

	args := map[string]interface{}{
		"tier":       compactTier,
		"dry_run":    compactDryRun,
		"force":      compactForce,
		"all":        compactAll,
		"api_key":    apiKey,
		"workers":    compactWorkers,
		"batch_size": compactBatch,
	}
	if compactID != "" {
		args["issue_id"] = compactID
	}

	resp, err := daemonClient.Execute("compact", args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	if jsonOutput {
		fmt.Println(string(resp.Data))
		return
	}

	var result struct {
		Success       bool   `json:"success"`
		IssueID       string `json:"issue_id,omitempty"`
		OriginalSize  int    `json:"original_size,omitempty"`
		CompactedSize int    `json:"compacted_size,omitempty"`
		Reduction     string `json:"reduction,omitempty"`
		Duration      string `json:"duration,omitempty"`
		DryRun        bool   `json:"dry_run,omitempty"`
		Results       []struct {
			IssueID       string `json:"issue_id"`
			Success       bool   `json:"success"`
			Error         string `json:"error,omitempty"`
			OriginalSize  int    `json:"original_size,omitempty"`
			CompactedSize int    `json:"compacted_size,omitempty"`
			Reduction     string `json:"reduction,omitempty"`
		} `json:"results,omitempty"`
	}

	if err := json.Unmarshal(resp.Data, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	if compactID != "" {
		if result.DryRun {
			fmt.Printf("DRY RUN - Tier %d compaction\n\n", compactTier)
			fmt.Printf("Issue: %s\n", compactID)
			fmt.Printf("Original size: %d bytes\n", result.OriginalSize)
			fmt.Printf("Estimated reduction: %s\n", result.Reduction)
		} else {
			fmt.Printf("Successfully compacted %s\n", result.IssueID)
			fmt.Printf("Original size: %d bytes\n", result.OriginalSize)
			fmt.Printf("Compacted size: %d bytes\n", result.CompactedSize)
			fmt.Printf("Reduction: %s\n", result.Reduction)
			fmt.Printf("Duration: %s\n", result.Duration)
		}
	} else if compactAll {
		if result.DryRun {
			fmt.Printf("DRY RUN - Found %d candidates for Tier %d compaction\n", len(result.Results), compactTier)
		} else {
			successCount := 0
			for _, r := range result.Results {
				if r.Success {
					successCount++
				}
			}
			fmt.Printf("Compacted %d/%d issues in %s\n", successCount, len(result.Results), result.Duration)
			for _, r := range result.Results {
				if r.Success {
					fmt.Printf("  ✓ %s: %d → %d bytes (%s)\n", r.IssueID, r.OriginalSize, r.CompactedSize, r.Reduction)
				} else {
					fmt.Printf("  ✗ %s: %s\n", r.IssueID, r.Error)
				}
			}
		}
	}
}

func runCompactStatsRPC() {
	args := map[string]interface{}{
		"tier": compactTier,
	}

	resp, err := daemonClient.Execute("compact_stats", args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	if jsonOutput {
		fmt.Println(string(resp.Data))
		return
	}

	var result struct {
		Success bool `json:"success"`
		Stats   struct {
			Tier1Candidates  int    `json:"tier1_candidates"`
			Tier2Candidates  int    `json:"tier2_candidates"`
			TotalClosed      int    `json:"total_closed"`
			Tier1MinAge      string `json:"tier1_min_age"`
			Tier2MinAge      string `json:"tier2_min_age"`
			EstimatedSavings string `json:"estimated_savings,omitempty"`
		} `json:"stats"`
	}

	if err := json.Unmarshal(resp.Data, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nCompaction Statistics\n")
	fmt.Printf("=====================\n\n")
	fmt.Printf("Total closed issues: %d\n\n", result.Stats.TotalClosed)
	fmt.Printf("Tier 1 (30+ days closed, not compacted):\n")
	fmt.Printf("  Candidates: %d\n", result.Stats.Tier1Candidates)
	fmt.Printf("  Min age: %s\n\n", result.Stats.Tier1MinAge)
	fmt.Printf("Tier 2 (90+ days closed, Tier 1 compacted):\n")
	fmt.Printf("  Candidates: %d\n", result.Stats.Tier2Candidates)
	fmt.Printf("  Min age: %s\n", result.Stats.Tier2MinAge)
}

func init() {
	compactCmd.Flags().BoolVar(&compactDryRun, "dry-run", false, "Preview without compacting")
	compactCmd.Flags().IntVar(&compactTier, "tier", 1, "Compaction tier (1 or 2)")
	compactCmd.Flags().BoolVar(&compactAll, "all", false, "Process all candidates")
	compactCmd.Flags().StringVar(&compactID, "id", "", "Compact specific issue")
	compactCmd.Flags().BoolVar(&compactForce, "force", false, "Force compact (bypass checks, requires --id)")
	compactCmd.Flags().IntVar(&compactBatch, "batch-size", 10, "Issues per batch")
	compactCmd.Flags().IntVar(&compactWorkers, "workers", 5, "Parallel workers")
	compactCmd.Flags().BoolVar(&compactStats, "stats", false, "Show compaction statistics")

	rootCmd.AddCommand(compactCmd)
}
