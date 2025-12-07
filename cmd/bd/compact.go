package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/compact"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/deletions"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

var (
	compactDryRun   bool
	compactTier     int
	compactAll      bool
	compactID       string
	compactForce    bool
	compactBatch    int
	compactWorkers  int
	compactStats    bool
	compactAnalyze  bool
	compactApply    bool
	compactAuto     bool
	compactSummary  string
	compactActor    string
	compactLimit    int
	compactRetention int
)

var compactCmd = &cobra.Command{
	Use:   "compact",
	Short: "Compact old closed issues to save space",
	Long: `Compact old closed issues using semantic summarization.

Compaction reduces database size by summarizing closed issues that are no longer
actively referenced. This is permanent graceful decay - original content is discarded.

Modes:
  - Analyze: Export candidates for agent review (no API key needed)
  - Apply: Accept agent-provided summary (no API key needed)
  - Auto: AI-powered compaction (requires ANTHROPIC_API_KEY, legacy)

Tiers:
  - Tier 1: Semantic compression (30 days closed, 70% reduction)
  - Tier 2: Ultra compression (90 days closed, 95% reduction)

Deletions Pruning:
  All modes also prune old deletion records from deletions.jsonl to prevent
  unbounded growth. Default retention is 3 days (configurable via --retention
  or deletions_retention_days in metadata.json).

Tombstone Pruning:
  All modes also prune expired tombstones from issues.jsonl. Tombstones are
  soft-delete markers that prevent resurrection of deleted issues. After the
  TTL expires (default 30 days), tombstones are removed to save space.

Examples:
  # Agent-driven workflow (recommended)
  bd compact --analyze --json              # Get candidates with full content
  bd compact --apply --id bd-42 --summary summary.txt
  bd compact --apply --id bd-42 --summary - < summary.txt

  # Legacy AI-powered workflow
  bd compact --auto --dry-run              # Preview candidates
  bd compact --auto --all                  # Compact all eligible issues
  bd compact --auto --id bd-42             # Compact specific issue

  # Statistics
  bd compact --stats                       # Show statistics

  # Override retention period
  bd compact --auto --all --retention=14   # Keep 14 days of deletions
`,
	Run: func(_ *cobra.Command, _ []string) {
		// Compact modifies data unless --stats or --analyze or --dry-run
		if !compactStats && !compactAnalyze && !compactDryRun {
			CheckReadonly("compact")
		}
		ctx := rootCtx

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

		// Count active modes
		activeModes := 0
		if compactAnalyze {
			activeModes++
		}
		if compactApply {
			activeModes++
		}
		if compactAuto {
			activeModes++
		}

		// Check for exactly one mode
		if activeModes == 0 {
			fmt.Fprintf(os.Stderr, "Error: must specify one mode: --analyze, --apply, or --auto\n")
			os.Exit(1)
		}
		if activeModes > 1 {
			fmt.Fprintf(os.Stderr, "Error: cannot use multiple modes together (--analyze, --apply, --auto are mutually exclusive)\n")
			os.Exit(1)
		}

		// Handle analyze mode (requires direct database access)
		if compactAnalyze {
			if err := ensureDirectMode("compact --analyze requires direct database access"); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				fmt.Fprintf(os.Stderr, "Hint: Use --no-daemon flag to bypass daemon and access database directly\n")
				os.Exit(1)
			}
			sqliteStore, ok := store.(*sqlite.SQLiteStorage)
			if !ok {
				fmt.Fprintf(os.Stderr, "Error: failed to open database in direct mode\n")
				fmt.Fprintf(os.Stderr, "Hint: Ensure .beads/beads.db exists and is readable\n")
				os.Exit(1)
			}
			runCompactAnalyze(ctx, sqliteStore)
			return
		}

		// Handle apply mode (requires direct database access)
		if compactApply {
			if err := ensureDirectMode("compact --apply requires direct database access"); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				fmt.Fprintf(os.Stderr, "Hint: Use --no-daemon flag to bypass daemon and access database directly\n")
				os.Exit(1)
			}
			if compactID == "" {
				fmt.Fprintf(os.Stderr, "Error: --apply requires --id\n")
				os.Exit(1)
			}
			if compactSummary == "" {
				fmt.Fprintf(os.Stderr, "Error: --apply requires --summary\n")
				os.Exit(1)
			}
			sqliteStore, ok := store.(*sqlite.SQLiteStorage)
			if !ok {
				fmt.Fprintf(os.Stderr, "Error: failed to open database in direct mode\n")
				fmt.Fprintf(os.Stderr, "Hint: Ensure .beads/beads.db exists and is readable\n")
				os.Exit(1)
			}
			runCompactApply(ctx, sqliteStore)
			return
		}

		// Handle auto mode (legacy)
		if compactAuto {
			// Validation checks
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

			// Use RPC if daemon available, otherwise direct mode
			if daemonClient != nil {
				runCompactRPC(ctx)
				return
			}

			// Fallback to direct mode
			apiKey := os.Getenv("ANTHROPIC_API_KEY")
			if apiKey == "" && !compactDryRun {
				fmt.Fprintf(os.Stderr, "Error: --auto mode requires ANTHROPIC_API_KEY environment variable\n")
				os.Exit(1)
			}

			sqliteStore, ok := store.(*sqlite.SQLiteStorage)
			if !ok {
				fmt.Fprintf(os.Stderr, "Error: compact requires SQLite storage\n")
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
		}
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

	// Prune old deletion records
	pruneDeletionsManifest()

	// Prune expired tombstones (bd-okh)
	if tombstonePruneResult, err := pruneExpiredTombstones(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to prune expired tombstones: %v\n", err)
	} else if tombstonePruneResult != nil && tombstonePruneResult.PrunedCount > 0 {
		fmt.Printf("\nTombstones pruned: %d expired (older than %d days)\n",
			tombstonePruneResult.PrunedCount, tombstonePruneResult.TTLDays)
	}

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

	// Prune old deletion records
	pruneDeletionsManifest()

	// Prune expired tombstones (bd-okh)
	if tombstonePruneResult, err := pruneExpiredTombstones(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to prune expired tombstones: %v\n", err)
	} else if tombstonePruneResult != nil && tombstonePruneResult.PrunedCount > 0 {
		fmt.Printf("\nTombstones pruned: %d expired (older than %d days)\n",
			tombstonePruneResult.PrunedCount, tombstonePruneResult.TTLDays)
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

func runCompactAnalyze(ctx context.Context, store *sqlite.SQLiteStorage) {
	type Candidate struct {
		ID                 string `json:"id"`
		Title              string `json:"title"`
		Description        string `json:"description"`
		Design             string `json:"design"`
		Notes              string `json:"notes"`
		AcceptanceCriteria string `json:"acceptance_criteria"`
		SizeBytes          int    `json:"size_bytes"`
		AgeDays            int    `json:"age_days"`
		Tier               int    `json:"tier"`
		Compacted          bool   `json:"compacted"`
	}

	var candidates []Candidate

	// Single issue mode
	if compactID != "" {
		issue, err := store.GetIssue(ctx, compactID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get issue: %v\n", err)
			os.Exit(1)
		}

		sizeBytes := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
		ageDays := 0
		if issue.ClosedAt != nil {
			ageDays = int(time.Since(*issue.ClosedAt).Hours() / 24)
		}

		candidates = append(candidates, Candidate{
			ID:                 issue.ID,
			Title:              issue.Title,
			Description:        issue.Description,
			Design:             issue.Design,
			Notes:              issue.Notes,
			AcceptanceCriteria: issue.AcceptanceCriteria,
			SizeBytes:          sizeBytes,
			AgeDays:            ageDays,
			Tier:               compactTier,
			Compacted:          issue.CompactionLevel > 0,
		})
	} else {
		// Get tier candidates
		var tierCandidates []*sqlite.CompactionCandidate
		var err error
		if compactTier == 1 {
			tierCandidates, err = store.GetTier1Candidates(ctx)
		} else {
			tierCandidates, err = store.GetTier2Candidates(ctx)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get candidates: %v\n", err)
			os.Exit(1)
		}

		// Apply limit if specified
		if compactLimit > 0 && len(tierCandidates) > compactLimit {
			tierCandidates = tierCandidates[:compactLimit]
		}

		// Fetch full details for each candidate
		for _, c := range tierCandidates {
			issue, err := store.GetIssue(ctx, c.IssueID)
			if err != nil {
				continue // Skip issues we can't fetch
			}

			ageDays := int(time.Since(c.ClosedAt).Hours() / 24)

			candidates = append(candidates, Candidate{
				ID:                 issue.ID,
				Title:              issue.Title,
				Description:        issue.Description,
				Design:             issue.Design,
				Notes:              issue.Notes,
				AcceptanceCriteria: issue.AcceptanceCriteria,
				SizeBytes:          c.OriginalSize,
				AgeDays:            ageDays,
				Tier:               compactTier,
				Compacted:          issue.CompactionLevel > 0,
			})
		}
	}

	if jsonOutput {
		outputJSON(candidates)
		return
	}

	// Human-readable output
	fmt.Printf("Compaction Candidates (Tier %d)\n\n", compactTier)
	for _, c := range candidates {
		compactStatus := ""
		if c.Compacted {
			compactStatus = " (already compacted)"
		}
		fmt.Printf("ID: %s%s\n", c.ID, compactStatus)
		fmt.Printf("  Title: %s\n", c.Title)
		fmt.Printf("  Size: %d bytes\n", c.SizeBytes)
		fmt.Printf("  Age: %d days\n\n", c.AgeDays)
	}
	fmt.Printf("Total: %d candidates\n", len(candidates))
}

func runCompactApply(ctx context.Context, store *sqlite.SQLiteStorage) {
	start := time.Now()

	// Read summary
	var summaryBytes []byte
	var err error
	if compactSummary == "-" {
		// Read from stdin
		summaryBytes, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to read summary from stdin: %v\n", err)
			os.Exit(1)
		}
	} else {
		// #nosec G304 -- summary file path provided explicitly by operator
		summaryBytes, err = os.ReadFile(compactSummary)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to read summary file: %v\n", err)
			os.Exit(1)
		}
	}
	summary := string(summaryBytes)

	// Get issue
	issue, err := store.GetIssue(ctx, compactID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to get issue: %v\n", err)
		os.Exit(1)
	}

	// Calculate sizes
	originalSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
	compactedSize := len(summary)

	// Check eligibility unless --force
	if !compactForce {
		eligible, reason, err := store.CheckEligibility(ctx, compactID, compactTier)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to check eligibility: %v\n", err)
			os.Exit(1)
		}
		if !eligible {
			fmt.Fprintf(os.Stderr, "Error: %s is not eligible for Tier %d compaction: %s\n", compactID, compactTier, reason)
			fmt.Fprintf(os.Stderr, "Hint: use --force to bypass eligibility checks\n")
			os.Exit(1)
		}

		// Enforce size reduction unless --force
		if compactedSize >= originalSize {
			fmt.Fprintf(os.Stderr, "Error: summary (%d bytes) is not shorter than original (%d bytes)\n", compactedSize, originalSize)
			fmt.Fprintf(os.Stderr, "Hint: use --force to bypass size validation\n")
			os.Exit(1)
		}
	}

	// Apply compaction
	actor := compactActor
	if actor == "" {
		actor = "agent"
	}

	updates := map[string]interface{}{
		"description":         summary,
		"design":              "",
		"notes":               "",
		"acceptance_criteria": "",
	}

	if err := store.UpdateIssue(ctx, compactID, updates, actor); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to update issue: %v\n", err)
		os.Exit(1)
	}

	commitHash := compact.GetCurrentCommitHash()
	if err := store.ApplyCompaction(ctx, compactID, compactTier, originalSize, compactedSize, commitHash); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to apply compaction: %v\n", err)
		os.Exit(1)
	}

	savingBytes := originalSize - compactedSize
	reductionPct := float64(savingBytes) / float64(originalSize) * 100
	eventData := fmt.Sprintf("Tier %d compaction: %d → %d bytes (saved %d, %.1f%%)", compactTier, originalSize, compactedSize, savingBytes, reductionPct)
	if err := store.AddComment(ctx, compactID, actor, eventData); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to record event: %v\n", err)
		os.Exit(1)
	}

	if err := store.MarkIssueDirty(ctx, compactID); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to mark dirty: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(start)

	// Prune old deletion records (do this before JSON output so we can include results)
	pruneResult, retentionDays := pruneDeletionsManifest()

	// Prune expired tombstones from issues.jsonl (bd-okh)
	tombstonePruneResult, tombstoneErr := pruneExpiredTombstones()
	if tombstoneErr != nil && !jsonOutput {
		fmt.Fprintf(os.Stderr, "Warning: failed to prune expired tombstones: %v\n", tombstoneErr)
	}

	if jsonOutput {
		output := map[string]interface{}{
			"success":        true,
			"issue_id":       compactID,
			"tier":           compactTier,
			"original_size":  originalSize,
			"compacted_size": compactedSize,
			"saved_bytes":    savingBytes,
			"reduction_pct":  reductionPct,
			"elapsed_ms":     elapsed.Milliseconds(),
		}
		// Include pruning results if any deletions were pruned (bd-v29)
		if pruneResult != nil && pruneResult.PrunedCount > 0 {
			output["deletions_pruned"] = map[string]interface{}{
				"count":          pruneResult.PrunedCount,
				"retention_days": retentionDays,
			}
		}
		// Include tombstone pruning results (bd-okh)
		if tombstonePruneResult != nil && tombstonePruneResult.PrunedCount > 0 {
			output["tombstones_pruned"] = map[string]interface{}{
				"count":    tombstonePruneResult.PrunedCount,
				"ttl_days": tombstonePruneResult.TTLDays,
			}
		}
		outputJSON(output)
		return
	}

	fmt.Printf("✓ Compacted %s (Tier %d)\n", compactID, compactTier)
	fmt.Printf("  %d → %d bytes (saved %d, %.1f%%)\n", originalSize, compactedSize, savingBytes, reductionPct)
	fmt.Printf("  Time: %v\n", elapsed)

	// Report pruning results for human-readable output
	if pruneResult != nil && pruneResult.PrunedCount > 0 {
		fmt.Printf("\nDeletions pruned: %d records older than %d days removed\n", pruneResult.PrunedCount, retentionDays)
	}

	// Report tombstone pruning results (bd-okh)
	if tombstonePruneResult != nil && tombstonePruneResult.PrunedCount > 0 {
		fmt.Printf("\nTombstones pruned: %d expired tombstones (older than %d days) removed\n",
			tombstonePruneResult.PrunedCount, tombstonePruneResult.TTLDays)
	}

	// Schedule auto-flush to export changes
	markDirtyAndScheduleFlush()
}

// pruneDeletionsManifest prunes old deletion records based on retention settings.
// Returns the prune result and retention days used, so callers can include in output.
// Uses the global dbPath to determine the .beads directory.
func pruneDeletionsManifest() (*deletions.PruneResult, int) {
	beadsDir := filepath.Dir(dbPath)
	// Determine retention days
	retentionDays := compactRetention
	if retentionDays <= 0 {
		// Load config for default
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			if !jsonOutput {
				fmt.Fprintf(os.Stderr, "Warning: could not load config for retention settings: %v\n", err)
			}
			retentionDays = configfile.DefaultDeletionsRetentionDays
		} else if cfg != nil {
			retentionDays = cfg.GetDeletionsRetentionDays()
		} else {
			retentionDays = configfile.DefaultDeletionsRetentionDays
		}
	}

	deletionsPath := deletions.DefaultPath(beadsDir)
	result, err := deletions.PruneDeletions(deletionsPath, retentionDays)
	if err != nil {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: failed to prune deletions: %v\n", err)
		}
		return nil, retentionDays
	}

	return result, retentionDays
}

// TombstonePruneResult contains the results of tombstone pruning
type TombstonePruneResult struct {
	PrunedCount int
	PrunedIDs   []string
	TTLDays     int
}

// pruneExpiredTombstones reads issues.jsonl, removes expired tombstones,
// and writes back the pruned file. Returns the prune result.
func pruneExpiredTombstones() (*TombstonePruneResult, error) {
	beadsDir := filepath.Dir(dbPath)
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")

	// Check if issues.jsonl exists
	if _, err := os.Stat(issuesPath); os.IsNotExist(err) {
		return &TombstonePruneResult{}, nil
	}

	// Read all issues
	// nolint:gosec // G304: issuesPath is controlled from beadsDir
	file, err := os.Open(issuesPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open issues.jsonl: %w", err)
	}

	var allIssues []*types.Issue
	decoder := json.NewDecoder(file)
	for {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			if err.Error() == "EOF" {
				break
			}
			// Skip corrupt lines
			continue
		}
		allIssues = append(allIssues, &issue)
	}
	file.Close()

	// Determine TTL
	ttl := types.DefaultTombstoneTTL
	ttlDays := int(ttl.Hours() / 24)

	// Filter out expired tombstones
	var kept []*types.Issue
	var prunedIDs []string
	for _, issue := range allIssues {
		if issue.IsExpired(ttl) {
			prunedIDs = append(prunedIDs, issue.ID)
		} else {
			kept = append(kept, issue)
		}
	}

	if len(prunedIDs) == 0 {
		return &TombstonePruneResult{TTLDays: ttlDays}, nil
	}

	// Write back the pruned file atomically
	dir := filepath.Dir(issuesPath)
	base := filepath.Base(issuesPath)
	tempFile, err := os.CreateTemp(dir, base+".prune.*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	encoder := json.NewEncoder(tempFile)
	for _, issue := range kept {
		if err := encoder.Encode(issue); err != nil {
			tempFile.Close()
			os.Remove(tempPath)
			return nil, fmt.Errorf("failed to write issue %s: %w", issue.ID, err)
		}
	}

	if err := tempFile.Close(); err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomically replace
	if err := os.Rename(tempPath, issuesPath); err != nil {
		os.Remove(tempPath)
		return nil, fmt.Errorf("failed to replace issues.jsonl: %w", err)
	}

	return &TombstonePruneResult{
		PrunedCount: len(prunedIDs),
		PrunedIDs:   prunedIDs,
		TTLDays:     ttlDays,
	}, nil
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
	compactCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON format")

	// New mode flags
	compactCmd.Flags().BoolVar(&compactAnalyze, "analyze", false, "Analyze mode: export candidates for agent review")
	compactCmd.Flags().BoolVar(&compactApply, "apply", false, "Apply mode: accept agent-provided summary")
	compactCmd.Flags().BoolVar(&compactAuto, "auto", false, "Auto mode: AI-powered compaction (legacy)")
	compactCmd.Flags().StringVar(&compactSummary, "summary", "", "Path to summary file (use '-' for stdin)")
	compactCmd.Flags().StringVar(&compactActor, "actor", "agent", "Actor name for audit trail")
	compactCmd.Flags().IntVar(&compactLimit, "limit", 0, "Limit number of candidates (0 = no limit)")

	// Deletions pruning flag
	compactCmd.Flags().IntVar(&compactRetention, "retention", 0, "Deletion retention days (0 = use config default)")

	rootCmd.AddCommand(compactCmd)
}
