package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/compact"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
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
	compactAnalyze bool
	compactApply   bool
	compactAuto    bool
	compactSummary string
	compactActor   string
	compactLimit   int
	compactDolt    bool
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
  - Dolt: Run Dolt garbage collection (for Dolt-backend repositories)

Tiers:
  - Tier 1: Semantic compression (30 days closed, 70% reduction)
  - Tier 2: Ultra compression (90 days closed, 95% reduction)

Dolt Garbage Collection:
  With auto-commit per mutation, Dolt commit history grows over time. Use
  --dolt to run Dolt garbage collection and reclaim disk space.

  --dolt: Run Dolt GC on .beads/dolt directory to free disk space.
          This removes unreachable commits and compacts storage.

Examples:
  # Dolt garbage collection
  bd compact --dolt                        # Run Dolt GC
  bd compact --dolt --dry-run              # Preview without running GC

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
`,
	Run: func(_ *cobra.Command, _ []string) {
		// Compact modifies data unless --stats or --analyze or --dry-run or --dolt with --dry-run
		if !compactStats && !compactAnalyze && !compactDryRun && !(compactDolt && compactDryRun) {
			CheckReadonly("compact")
		}
		ctx := rootCtx

		// Handle compact stats first
		if compactStats {
			runCompactStats(ctx, store)
			return
		}

		// Handle dolt GC mode
		if compactDolt {
			runCompactDolt()
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
				fmt.Fprintf(os.Stderr, "Hint: Ensure a beads database is initialized (run 'bd init')\n")
				os.Exit(1)
			}
			runCompactAnalyze(ctx, store)
			return
		}

		// Handle apply mode (requires direct database access)
		if compactApply {
			if err := ensureDirectMode("compact --apply requires direct database access"); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				fmt.Fprintf(os.Stderr, "Hint: Ensure a beads database is initialized (run 'bd init')\n")
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
			runCompactApply(ctx, store)
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

			// Direct mode
			apiKey := os.Getenv("ANTHROPIC_API_KEY")
			if apiKey == "" && !compactDryRun {
				fmt.Fprintf(os.Stderr, "Error: --auto mode requires ANTHROPIC_API_KEY environment variable\n")
				os.Exit(1)
			}

			config := &compact.Config{
				APIKey:      apiKey,
				Concurrency: compactWorkers,
				DryRun:      compactDryRun,
			}

			compactor, err := compact.New(store, apiKey, config)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to create compactor: %v\n", err)
				os.Exit(1)
			}

			if compactID != "" {
				runCompactSingle(ctx, compactor, store, compactID)
				return
			}

			runCompactAll(ctx, compactor, store)
		}
	},
}

func runCompactSingle(ctx context.Context, compactor *compact.Compactor, store *dolt.DoltStore, issueID string) {
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
}

func runCompactAll(ctx context.Context, compactor *compact.Compactor, store *dolt.DoltStore) {
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
}

func runCompactStats(ctx context.Context, store *dolt.DoltStore) {
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

func runCompactAnalyze(ctx context.Context, store *dolt.DoltStore) {
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
		var tierCandidates []*types.CompactionCandidate
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

func runCompactApply(ctx context.Context, store *dolt.DoltStore) {
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

	elapsed := time.Since(start)

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
		outputJSON(output)
		return
	}

	fmt.Printf("✓ Compacted %s (Tier %d)\n", compactID, compactTier)
	fmt.Printf("  %d → %d bytes (saved %d, %.1f%%)\n", originalSize, compactedSize, savingBytes, reductionPct)
	fmt.Printf("  Time: %v\n", elapsed)
}

// runCompactDolt runs Dolt garbage collection on the .beads/dolt directory
func runCompactDolt() {
	start := time.Now()

	// Find beads directory
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		fmt.Fprintf(os.Stderr, "Error: could not find .beads directory\n")
		os.Exit(1)
	}

	// Check for dolt directory
	doltPath := filepath.Join(beadsDir, "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: Dolt directory not found at %s\n", doltPath)
		fmt.Fprintf(os.Stderr, "Hint: --dolt flag is only for repositories using the Dolt backend\n")
		os.Exit(1)
	}

	// Check if dolt command is available
	if _, err := exec.LookPath("dolt"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: dolt command not found in PATH\n")
		fmt.Fprintf(os.Stderr, "Hint: install Dolt from https://github.com/dolthub/dolt\n")
		os.Exit(1)
	}

	// Get size before GC
	sizeBefore, err := getDirSize(doltPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not calculate directory size: %v\n", err)
		sizeBefore = 0
	}

	if compactDryRun {
		if jsonOutput {
			output := map[string]interface{}{
				"dry_run":      true,
				"dolt_path":    doltPath,
				"size_before":  sizeBefore,
				"size_display": formatBytes(sizeBefore),
			}
			outputJSON(output)
			return
		}
		fmt.Printf("DRY RUN - Dolt garbage collection\n\n")
		fmt.Printf("Dolt directory: %s\n", doltPath)
		fmt.Printf("Current size: %s\n", formatBytes(sizeBefore))
		fmt.Printf("\nRun without --dry-run to perform garbage collection.\n")
		return
	}

	if !jsonOutput {
		fmt.Printf("Running Dolt garbage collection...\n")
	}

	// Run dolt gc
	cmd := exec.Command("dolt", "gc") // #nosec G204 -- fixed command, no user input
	cmd.Dir = doltPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: dolt gc failed: %v\n", err)
		if len(output) > 0 {
			fmt.Fprintf(os.Stderr, "Output: %s\n", string(output))
		}
		os.Exit(1)
	}

	// Get size after GC
	sizeAfter, err := getDirSize(doltPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not calculate directory size after GC: %v\n", err)
		sizeAfter = 0
	}

	elapsed := time.Since(start)
	freed := sizeBefore - sizeAfter
	if freed < 0 {
		freed = 0 // GC may not always reduce size
	}

	if jsonOutput {
		result := map[string]interface{}{
			"success":       true,
			"dolt_path":     doltPath,
			"size_before":   sizeBefore,
			"size_after":    sizeAfter,
			"freed_bytes":   freed,
			"freed_display": formatBytes(freed),
			"elapsed_ms":    elapsed.Milliseconds(),
		}
		outputJSON(result)
		return
	}

	fmt.Printf("✓ Dolt garbage collection complete\n")
	fmt.Printf("  %s → %s (freed %s)\n", formatBytes(sizeBefore), formatBytes(sizeAfter), formatBytes(freed))
	fmt.Printf("  Time: %v\n", elapsed)
}

// getDirSize calculates the total size of a directory recursively
func getDirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// formatBytes formats a byte count as a human-readable string
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// progressBar renders a text-based progress bar.
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
	compactCmd.Flags().BoolVar(&compactDolt, "dolt", false, "Dolt mode: run Dolt garbage collection on .beads/dolt")

	// Note: compactCmd is added to adminCmd in admin.go
}
