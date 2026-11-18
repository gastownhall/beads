package main

import (
	"context"
	"fmt"
	"io"
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
		return HandleError("checking pollution candidates: %v", err)
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

	// Group by heuristic strength (JSON keeps the confidence bucket names).
	stronger := []doctor.PollutionMatch{}
	possible := []doctor.PollutionMatch{}

	for _, p := range polluted {
		if p.Strong() {
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

	writePollutionReport(os.Stdout, stronger, possible)

	if !clean {
		return nil
	}

	if !yes {
		fmt.Printf("\nDelete the %d reported issues? Each is re-checked and backed up first. [y/N] ", len(polluted))
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Canceled.")
			return nil
		}
	}

	cleanup := pollutionCleanup{
		store:      store,
		out:        os.Stdout,
		backupPath: ".beads/pollution-backup.jsonl",
	}
	if err := cleanup.run(ctx, polluted); err != nil {
		return HandleError("%v", err)
	}
	return nil
}

// writePollutionReport prints the human pollution report: the stronger then
// the possible heuristic matches, and how to review them.
func writePollutionReport(w io.Writer, stronger, possible []doctor.PollutionMatch) {
	fmt.Fprintf(w, "Found %d possible test leftovers (heuristic title and content matches):\n\n", len(stronger)+len(possible))
	writePollutionGroup(w, "Stronger heuristic matches (score ≥ 0.9)", stronger)
	writePollutionGroup(w, "Possible heuristic matches (score < 0.9)", possible)
	fmt.Fprintln(w, "Scores rank heuristic strength; they are not probabilities. Real work with a")
	fmt.Fprintln(w, "test-style title and no description can match too.")
	fmt.Fprintln(w, "Review each match with 'bd show <id>' before deleting anything.")
}

func writePollutionGroup(w io.Writer, heading string, matches []doctor.PollutionMatch) {
	if len(matches) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", heading)
	for _, p := range matches {
		fmt.Fprintf(w, "  %s: %q (score: %.2f)\n", p.Issue.ID, p.Issue.Title, p.Score)
		for _, reason := range p.Reasons {
			fmt.Fprintf(w, "    - %s\n", reason)
		}
	}
	fmt.Fprintf(w, "  (Total: %d issues)\n\n", len(matches))
}

// pollutionCleanupStore is the store surface --clean re-reads, re-checks and
// deletes through.
type pollutionCleanupStore interface {
	doctor.PollutionEvidence
	GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error)
	DeleteIssue(ctx context.Context, id string) error
}

// pollutionCleanup deletes confirmed pollution matches.
type pollutionCleanup struct {
	store      pollutionCleanupStore
	out        io.Writer
	backupPath string
}

// run re-reads the confirmed issues and classifies them again, so an issue
// edited, closed, linked, commented on or deleted while the prompt waited is
// skipped (and reported) rather than deleted; issues that started to match
// meanwhile are never added. It backs up and then deletes the issues still
// eligible. A refresh, evidence or backup error deletes nothing. The
// re-check narrows the race, it does not close it: a write after it is not
// seen.
func (c pollutionCleanup) run(ctx context.Context, confirmed []doctor.PollutionMatch) error {
	ids := make([]string, 0, len(confirmed))
	for _, m := range confirmed {
		ids = append(ids, m.Issue.ID)
	}

	refreshed, err := c.store.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("re-reading confirmed issues before deletion: %w", err)
	}
	byID := make(map[string]*types.Issue, len(refreshed))
	for _, issue := range refreshed {
		if issue != nil {
			byID[issue.ID] = issue
		}
	}
	current := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		if issue, ok := byID[id]; ok {
			current = append(current, issue)
		}
	}

	eligible, err := doctor.DetectTestPollution(ctx, c.store, current)
	if err != nil {
		return fmt.Errorf("re-checking confirmed issues before deletion: %w", err)
	}
	stillEligible := make(map[string]bool, len(eligible))
	for _, m := range eligible {
		stillEligible[m.Issue.ID] = true
	}
	for _, id := range ids {
		if !stillEligible[id] {
			fmt.Fprintf(c.out, "Skipping %s: changed, connected or removed since it was reported\n", id)
		}
	}
	if len(eligible) == 0 {
		fmt.Fprintln(c.out, "No reported issue still matches; nothing deleted.")
		return nil
	}

	if err := backupPollutedIssues(eligible, c.backupPath); err != nil {
		return fmt.Errorf("backing up issues: %w", err)
	}
	fmt.Fprintf(c.out, "Backed up %d issues to %s\n", len(eligible), c.backupPath)

	fmt.Fprintf(c.out, "\nDeleting %d issues...\n", len(eligible))
	deleted := 0
	for _, m := range eligible {
		if err := c.store.DeleteIssue(ctx, m.Issue.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting %s: %v\n", m.Issue.ID, err)
			continue
		}
		deleted++
	}

	fmt.Fprintf(c.out, "%s Deleted %d issues\n", ui.RenderPass("✓"), deleted)
	arg, note := remedy.CommandArg(runtime.GOOS, c.backupPath, "<backup>")
	fmt.Fprintf(c.out, "\nCleanup complete. To restore, run: bd init --from-jsonl %s%s\n", arg, note)
	return nil
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&perfMode, "perf", false, "Run performance diagnostics and generate CPU profile")
	doctorCmd.Flags().BoolVar(&checkHealthMode, "check-health", false, "Quick health check for git hooks (silent on success)")
	doctorCmd.Flags().StringVarP(&doctorOutput, "output", "o", "", "Export diagnostics to JSON file")
	doctorCmd.Flags().StringVar(&doctorCheckFlag, "check", "", "Run specific check in detail (e.g., 'pollution')")
	doctorCmd.Flags().BoolVar(&doctorClean, "clean", false, "For pollution check: delete the reported matches (with confirmation and backup)")
	doctorCmd.Flags().BoolVar(&doctorDeep, "deep", false, "Validate full graph integrity")
}
